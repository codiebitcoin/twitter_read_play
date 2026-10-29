package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/source"
)

// FeedAdapter 内置浏览器 Feed 适配器（X / 微博 / 小红书）。
type FeedAdapter struct {
	info     source.Info
	mgr      *Manager
	provider Provider
}

func (a *FeedAdapter) Info() source.Info {
	// 远程浏览器（Web 模式）复用同一套 adapter，模式按 driver 动态算。
	info := a.info
	info.Modes = a.mgr.Modes()
	return info
}

func (a *FeedAdapter) Match(t source.Target) source.Match {
	return source.BaseMatch(a.info, t)
}

func (a *FeedAdapter) OpenFeed(ch source.Channel, t source.Target) (source.FeedWorker, error) {
	if !a.mgr.Available() {
		return nil, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	// 小红书主页链接带 xsec_token（有时还带 xsec_source），必须原样打开；
	// 只靠 target.ID 重建 URL 会丢掉 query，接口直接返回空笔记。
	raw := strings.TrimSpace(t.Raw)
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = ""
	}
	return &browserFeedWorker{mgr: a.mgr, provider: a.provider, rawURL: raw}, nil
}

func (a *FeedAdapter) TestFeed(ctx context.Context, ch source.Channel, t source.Target) (source.Probe, error) {
	w, err := a.OpenFeed(ch, t)
	if err != nil {
		return source.Probe{OK: false, Detail: err.Error()}, err
	}
	start := time.Now()
	tweets, err := w.FetchLatest(ctx, t.ID, 1)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return source.Probe{OK: false, Detail: err.Error(), Milliseconds: ms}, err
	}
	if len(tweets) == 0 {
		return source.Probe{OK: false, Detail: "连通但返回 0 条", Milliseconds: ms}, nil
	}
	return source.Probe{OK: true, Count: len(tweets), Milliseconds: ms, Detail: fmt.Sprintf("%d 条 · %dms", len(tweets), ms)}, nil
}

type browserFeedWorker struct {
	mgr      *Manager
	provider Provider
	// rawURL 目标原始链接（含 query）。带签名 token 的平台（小红书）必须用它导航。
	rawURL string
}

func (w *browserFeedWorker) Name() string { return string(w.provider) + "_browser" }

func (w *browserFeedWorker) FetchLatest(ctx context.Context, id string, count int) ([]model.Tweet, error) {
	switch w.provider {
	case ProviderX:
		return w.fetchX(ctx, id, count)
	case ProviderWeibo:
		return w.fetchWeibo(ctx, id, count)
	case ProviderXHS:
		return w.fetchXHS(ctx, id, count)
	case ProviderInstagram:
		return w.fetchInstagram(ctx, id, count)
	case ProviderTruth:
		return w.fetchTruth(ctx, id, count)
	}
	return nil, fmt.Errorf("不支持的平台 %q", w.provider)
}

func (w *browserFeedWorker) fetchX(ctx context.Context, id string, count int) ([]model.Tweet, error) {
	id = strings.TrimPrefix(strings.TrimSpace(id), "@")
	if id == "" {
		return nil, fmt.Errorf("X 目标为空")
	}
	req := CaptureRequest{
		Navigate: "https://x.com/" + id,
		URLRegex: `/i/api/graphql/.*/(UserByScreenName|UserTweets|UserOriginalsTimeline|UserWithRepliesTimeline|UserMediaTimeline)`,
		Scroll:   3, Wait: 1500 * time.Millisecond,
	}
	caps, err := w.mgr.Capture(ctx, ProviderX, req)
	if err != nil {
		return nil, err
	}
	tweets := ParseX(caps)
	if len(tweets) == 0 {
		// 页面加载慢：再滚几次
		req.Scroll = 8
		caps, err = w.mgr.Capture(ctx, ProviderX, req)
		if err != nil {
			return nil, err
		}
		tweets = ParseX(caps)
	}
	if len(tweets) == 0 {
		return nil, fmt.Errorf("未捕获到 X 推文：可能未登录、账号不存在或被限流")
	}
	if count > 0 && len(tweets) > count {
		tweets = tweets[:count]
	}
	return tweets, nil
}

func (w *browserFeedWorker) fetchWeibo(ctx context.Context, id string, count int) ([]model.Tweet, error) {
	id = strings.TrimSpace(strings.TrimPrefix(id, "@"))
	if id == "" {
		return nil, fmt.Errorf("微博目标为空")
	}
	nav := "https://m.weibo.cn/n/" + id
	if _, err := strconv.Atoi(id); err == nil {
		nav = "https://m.weibo.cn/u/" + id
	}
	caps, err := w.mgr.Capture(ctx, ProviderWeibo, CaptureRequest{
		Navigate: nav, URLRegex: `/api/container/getIndex`,
		Scroll: 3, Wait: 1200 * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}
	var tweets []model.Tweet
	for _, c := range caps {
		if len(c.JSON) > 0 {
			tweets = append(tweets, ParseWeibo(c.JSON)...)
		}
	}
	if len(tweets) == 0 {
		// 微博 API URL 会直接把 JSON 打在页面上
		if text, terr := w.mgr.Text(ctx, ProviderWeibo); terr == nil {
			tweets = ParseWeibo([]byte(text))
		}
	}
	if len(tweets) == 0 {
		return nil, fmt.Errorf("未捕获到微博：可能页面结构变化或账号不存在")
	}
	if count > 0 && len(tweets) > count {
		tweets = tweets[:count]
	}
	return tweets, nil
}

// xhsStateNotesJS 读取主页 SSR 状态里的笔记列表。
// 远程浏览器里小红书主页常常整页 SSR、不再发 user_posted 请求，网络捕获为空。
const xhsStateNotesJS = `(() => { try {
  const s = window.__INITIAL_STATE__ || {};
  let notes = (s.user || {}).notes;
  if (notes && notes._value !== undefined) notes = notes._value;
  const flat = [];
  for (const k of Object.keys(notes || {})) {
    const v = notes[k];
    (Array.isArray(v) ? v : [v]).forEach((x) => { if (x) flat.push(x); });
  }
  const up = (s.user || {}).userPageData;
  const info = ((up && (up.basicInfo || up)) || (s.user || {}).userInfo || {});
  const nick = info.nickname || info.nickName || '';
  return JSON.stringify(flat.map((it) => {
    const c = it.noteCard || it.note_card || it;
    const u = c.user || {};
    return {
      id: c.noteId || it.noteId || '',
      time: c.time || 0,
      title: c.displayTitle || c.title || '',
      desc: c.desc || '',
      liked: ((c.interactInfo || {}).likedCount) || '',
      type: c.type || '',
      user: u.nickname || u.nickName || nick,
    };
  }));
} catch (e) { return '[]'; } })()`

func (w *browserFeedWorker) fetchXHS(ctx context.Context, id string, count int) ([]model.Tweet, error) {
	id = strings.TrimSpace(strings.TrimPrefix(id, "@"))
	if id == "" && w.rawURL == "" {
		return nil, fmt.Errorf("小红书目标为空")
	}
	nav := "https://www.xiaohongshu.com/user/profile/" + id
	if w.rawURL != "" {
		nav = w.rawURL
	}
	caps, err := w.mgr.Capture(ctx, ProviderXHS, CaptureRequest{
		Navigate: nav,
		URLRegex: `/api/sns/web/v\d/(user_posted|user/otherinfo|search/notes|search/users)`,
		Scroll:   3, Wait: 1500 * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}
	var tweets []model.Tweet
	for _, c := range caps {
		if len(c.JSON) > 0 {
			tweets = append(tweets, ParseXHS(c.JSON)...)
		}
	}
	// SSR 兜底：整页服务端渲染时页面不发 user_posted，只能读 __INITIAL_STATE__。
	if len(tweets) == 0 {
		if v, eerr := w.mgr.Eval(ctx, ProviderXHS, xhsStateNotesJS); eerr == nil {
			tweets = ParseXHSStateNotes(v)
		}
	}
	if len(tweets) == 0 {
		if w.rawURL == "" {
			return nil, fmt.Errorf("未捕获到小红书笔记：需要登录，或主页链接缺少 xsec_token（请直接粘贴 App 里分享出来的主页链接）")
		}
		return nil, fmt.Errorf("未捕获到小红书笔记：需要登录，或链接已过期（请重新从 App 复制主页链接）")
	}
	if count > 0 && len(tweets) > count {
		tweets = tweets[:count]
	}
	return tweets, nil
}

// instagramHeaders Instagram 页面自身请求都带这几个头：
// 只给 X-IG-App-ID 时缺 ASBD/claim，容易被直接判 401。
var instagramHeaders = map[string]string{
	"X-IG-App-ID":    "936619743392459",
	"X-ASBD-ID":      "129477",
	"X-IG-WWW-Claim": "0",
}

// fetchInstagram 先走同源 REST（web_profile_info 一次返回最近 12 条），
// 失败再退回分享页 + 拦截页面自身 GraphQL。
func (w *browserFeedWorker) fetchInstagram(ctx context.Context, id string, count int) ([]model.Tweet, error) {
	id = strings.TrimSpace(strings.TrimPrefix(id, "@"))
	if id == "" && w.rawURL == "" {
		return nil, fmt.Errorf("Instagram 目标为空")
	}
	fetchErr := ""
	if id != "" {
		resp, err := w.mgr.Fetch(ctx, ProviderInstagram, Request{
			URL:     "https://www.instagram.com/api/v1/users/web_profile_info/?username=" + url.QueryEscape(id),
			Headers: instagramHeaders,
		})
		if err != nil {
			fetchErr = err.Error()
		} else if resp.Status == 200 {
			if tweets := ParseInstagram(resp.Body); len(tweets) > 0 {
				if count > 0 && len(tweets) > count {
					tweets = tweets[:count]
				}
				return tweets, nil
			}
			fetchErr = "web_profile_info 未返回帖子（账号可能私密或未登录）"
		} else {
			fetchErr = fmt.Sprintf("web_profile_info HTTP %d", resp.Status)
		}
	}

	nav := "https://www.instagram.com/" + url.PathEscape(id) + "/"
	if w.rawURL != "" {
		nav = w.rawURL
	}
	caps, err := w.mgr.Capture(ctx, ProviderInstagram, CaptureRequest{
		Navigate: nav, URLRegex: `(/api/v1/(feed|users)/|/graphql/query)`,
		Scroll: 3, Wait: 1500 * time.Millisecond,
	})
	if err != nil {
		return nil, err
	}
	var tweets []model.Tweet
	for _, c := range caps {
		if len(c.JSON) > 0 {
			tweets = append(tweets, ParseInstagram(c.JSON)...)
		}
	}
	if len(tweets) == 0 {
		if fetchErr != "" {
			return nil, fmt.Errorf("未捕获到 Instagram 帖子：%s", fetchErr)
		}
		return nil, fmt.Errorf("未捕获到 Instagram 帖子：需要登录，或账号不存在/私密")
	}
	if count > 0 && len(tweets) > count {
		tweets = tweets[:count]
	}
	return tweets, nil
}

// fetchTruth 走 Mastodon 风格 /api/v1：先 lookup 数字账号，再拉 statuses。
// 请求在 truthsocial.com 页面内发出，Cloudflare 的 cf_clearance 由浏览器带上。
func (w *browserFeedWorker) fetchTruth(ctx context.Context, id string, count int) ([]model.Tweet, error) {
	acct := strings.TrimSpace(strings.TrimPrefix(id, "@"))
	if acct == "" && w.rawURL != "" {
		if u, err := url.Parse(w.rawURL); err == nil {
			segs := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(segs) > 0 && segs[len(segs)-1] != "" {
				acct = strings.TrimPrefix(segs[len(segs)-1], "@")
			}
		}
	}
	if acct == "" {
		return nil, fmt.Errorf("Truth Social 目标为空")
	}
	if count <= 0 || count > 40 {
		count = 20
	}
	uid, err := w.truthAccountID(ctx, acct)
	if err != nil {
		return nil, err
	}
	resp, err := w.mgr.Fetch(ctx, ProviderTruth, Request{
		URL:     fmt.Sprintf("https://truthsocial.com/api/v1/accounts/%s/statuses?limit=%d&exclude_replies=true", uid, count),
		Headers: map[string]string{"Accept": "application/json"},
	})
	if err != nil {
		return nil, err
	}
	if resp.Status != 200 {
		return nil, fmt.Errorf("Truth Social 拉取失败：HTTP %d", resp.Status)
	}
	tweets := ParseTruth(resp.Body)
	if len(tweets) == 0 {
		return nil, fmt.Errorf("未捕获到 Truth Social 帖子：账号可能不存在或没有公开内容")
	}
	if len(tweets) > count {
		tweets = tweets[:count]
	}
	return tweets, nil
}

func (w *browserFeedWorker) truthAccountID(ctx context.Context, acct string) (string, error) {
	resp, err := w.mgr.Fetch(ctx, ProviderTruth, Request{
		URL:     "https://truthsocial.com/api/v1/accounts/lookup?acct=" + url.QueryEscape(acct),
		Headers: map[string]string{"Accept": "application/json"},
	})
	if err != nil {
		return "", err
	}
	if resp.Status == 404 {
		return "", fmt.Errorf("Truth Social 账号不存在：%s", acct)
	}
	if resp.Status != 200 {
		return "", fmt.Errorf("Truth Social 用户解析失败：HTTP %d", resp.Status)
	}
	var acc struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp.Body, &acc); err != nil || acc.ID == "" {
		return "", fmt.Errorf("Truth Social 用户解析失败")
	}
	return acc.ID, nil
}

// RegisterFeedAdapters 注册内置浏览器 Feed 适配器。
func RegisterFeedAdapters(reg *source.Registry, mgr *Manager) error {
	specs := []struct {
		info     source.Info
		provider Provider
	}{
		{
			info: source.Info{
				ID: "x_browser", Name: "X（内置浏览器）",
				Domains:    []source.Domain{source.DomainFeed},
				Platforms:  []source.Platform{source.PlatformX},
				Kinds:      map[source.Platform][]source.TargetKind{source.PlatformX: {source.KindProfile}},
				Needs:      map[source.Platform][]source.CredentialKey{source.PlatformX: {source.CredDesktopSession}},
				Transports: []source.Transport{source.TransportCapture},
				Modes:      []source.Mode{source.ModeDesktop},
				Priority:   10, Visible: true,
				Hint: "桌面登录 X 后，拦截页面自身 GraphQL（无需 API Key）",
			},
			provider: ProviderX,
		},
		{
			info: source.Info{
				ID: "weibo_browser", Name: "微博（内置浏览器）",
				Domains:    []source.Domain{source.DomainFeed},
				Platforms:  []source.Platform{source.PlatformWeibo},
				Kinds:      map[source.Platform][]source.TargetKind{source.PlatformWeibo: {source.KindProfile}},
				Needs:      map[source.Platform][]source.CredentialKey{source.PlatformWeibo: {source.CredDesktopSession}},
				Transports: []source.Transport{source.TransportCapture},
				Modes:      []source.Mode{source.ModeDesktop},
				Priority:   10, Visible: true,
				Hint: "游客态也可抓公开数据；登录后更全",
			},
			provider: ProviderWeibo,
		},
		{
			info: source.Info{
				ID: "xiaohongshu_browser", Name: "小红书（内置浏览器）",
				Domains:    []source.Domain{source.DomainFeed},
				Platforms:  []source.Platform{source.PlatformXHS},
				Kinds:      map[source.Platform][]source.TargetKind{source.PlatformXHS: {source.KindProfile}},
				Needs:      map[source.Platform][]source.CredentialKey{source.PlatformXHS: {source.CredDesktopSession}},
				Transports: []source.Transport{source.TransportCapture},
				Modes:      []source.Mode{source.ModeDesktop},
				Priority:   10, Visible: true,
				Hint: "必须登录；签名由页面自己计算",
			},
			provider: ProviderXHS,
		},
		{
			info: source.Info{
				ID: "instagram_browser", Name: "Instagram（内置浏览器）",
				Domains:    []source.Domain{source.DomainFeed},
				Platforms:  []source.Platform{source.PlatformInstagram},
				Kinds:      map[source.Platform][]source.TargetKind{source.PlatformInstagram: {source.KindProfile}},
				Needs:      map[source.Platform][]source.CredentialKey{source.PlatformInstagram: {source.CredDesktopSession}},
				Transports: []source.Transport{source.TransportCapture},
				Modes:      []source.Mode{source.ModeDesktop},
				Priority:   10, Visible: true,
				Hint: "必须登录；用页面自身的 web_profile_info / GraphQL 拉帖子",
			},
			provider: ProviderInstagram,
		},
		{
			info: source.Info{
				ID: "truth_browser", Name: "Truth Social（内置浏览器）",
				Domains:    []source.Domain{source.DomainFeed},
				Platforms:  []source.Platform{source.PlatformTruth},
				Kinds:      map[source.Platform][]source.TargetKind{source.PlatformTruth: {source.KindProfile}},
				Needs:      map[source.Platform][]source.CredentialKey{source.PlatformTruth: {source.CredDesktopSession}},
				Transports: []source.Transport{source.TransportPageFetch},
				Modes:      []source.Mode{source.ModeDesktop},
				Priority:   20, Visible: true,
				Hint: "桌面内置浏览器直连（走系统代理），游客也能读公开账号",
			},
			provider: ProviderTruth,
		},
	}
	for _, s := range specs {
		if err := reg.Register(&FeedAdapter{info: s.info, mgr: mgr, provider: s.provider}); err != nil {
			return err
		}
	}
	return nil
}
