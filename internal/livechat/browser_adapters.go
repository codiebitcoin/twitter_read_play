package livechat

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"twitter_read_play/internal/browser"
	"twitter_read_play/internal/source"
)

// ---- source.LiveWorker <-> livechat.Streamer ----

type liveWorkerStreamer struct{ w source.LiveWorker }

func (s *liveWorkerStreamer) Name() string { return s.w.Name() }

func (s *liveWorkerStreamer) Stream(ctx context.Context, on func(Message)) error {
	return s.w.Stream(ctx, func(m source.LiveMessage) {
		on(Message{
			ID: m.ID, Channel: m.Channel, User: m.User, Text: m.Text,
			At: m.At, Kind: m.Kind, Amount: m.Amount, Source: m.Source,
			UserLevel: m.UserLevel,
		})
	})
}

// WrapLiveWorker 把 source.LiveWorker 适配成 livechat.Streamer。
func WrapLiveWorker(w source.LiveWorker) Streamer { return &liveWorkerStreamer{w: w} }

type streamerLiveWorker struct{ s Streamer }

func (w *streamerLiveWorker) Name() string { return w.s.Name() }

func (w *streamerLiveWorker) Stream(ctx context.Context, on func(source.LiveMessage)) error {
	return w.s.Stream(ctx, func(m Message) {
		on(source.LiveMessage{
			ID: m.ID, Channel: m.Channel, User: m.User, Text: m.Text,
			At: m.At, Kind: m.Kind, Amount: m.Amount, Source: m.Source,
			UserLevel: m.UserLevel,
		})
	})
}

// ---- 浏览器 Live 适配器 ----

// BrowserLiveAdapter 登录态 Live 源。
type BrowserLiveAdapter struct {
	info     source.Info
	mgr      *browser.Manager
	provider browser.Provider
}

func (a *BrowserLiveAdapter) Info() source.Info {
	// 远程浏览器（Web 模式）复用同一套 adapter，模式按 driver 动态算。
	info := a.info
	info.Modes = a.mgr.Modes()
	return info
}

func (a *BrowserLiveAdapter) Match(t source.Target) source.Match {
	return source.BaseMatch(a.info, t)
}

func (a *BrowserLiveAdapter) OpenLive(ch source.Channel, t source.Target) (source.LiveWorker, error) {
	if !a.mgr.Available() {
		return nil, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	switch a.provider {
	case browser.ProviderYouTube:
		return &youtubeBrowserWorker{mgr: a.mgr, target: t}, nil
	case browser.ProviderTwitch:
		return &twitchBrowserWorker{mgr: a.mgr, target: t}, nil
	}
	return nil, fmt.Errorf("不支持的平台 %q", a.provider)
}

func (a *BrowserLiveAdapter) TestLive(ctx context.Context, ch source.Channel, t source.Target) (source.Probe, error) {
	st, err := a.mgr.Probe(ctx, a.provider)
	if err != nil {
		return source.Probe{OK: false, Detail: err.Error()}, err
	}
	if !st.Connected {
		return source.Probe{OK: false, Detail: "未登录，请先点「登录」"}, nil
	}
	return source.Probe{OK: true, Detail: "已登录 " + st.Account}, nil
}

// youtubeChatDOMJS 读取渲染后的聊天行。
//
// 网络捕获在远程浏览器里拿不到 YouTube 的流式响应：钩子只能在导航完成后注入，
// 首屏那批聊天请求已经发完，之后的 get_live_chat 是长连接流，res.text() 不结束。
// 桌面模式靠文档开始前的注入能拿到，Web 模式只能读 DOM 兜底。
const youtubeChatDOMJS = `(() => {
  const sel = 'yt-live-chat-text-message-renderer, yt-live-chat-paid-message-renderer,'
    + 'yt-live-chat-paid-sticker-renderer, yt-live-chat-membership-item-renderer';
  const rows = Array.from(document.querySelectorAll(sel)).slice(-100);
  const out = rows.map((r) => {
    const q = (s) => { const n = r.querySelector(s); return n ? n.textContent.trim() : ''; };
    return {
      id: r.id || '',
      kind: r.tagName.toLowerCase(),
      author: q('#author-name'),
      text: q('#message'),
      amount: q('#purchase-amount') || q('#purchase-amount-chip'),
    };
  });
  return JSON.stringify(out);
})()`

type youtubeDOMRow struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Author string `json:"author"`
	Text   string `json:"text"`
	Amount string `json:"amount"`
}

// parseYouTubeChatDOM 把 DOM 行转成 LiveMessage（与网络解析出的 id 同源，可去重）。
func parseYouTubeChatDOM(v any) []source.LiveMessage {
	var raw string
	switch x := v.(type) {
	case string:
		raw = x
	default:
		b, _ := json.Marshal(x)
		raw = string(b)
	}
	var rows []youtubeDOMRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil
	}
	out := make([]source.LiveMessage, 0, len(rows))
	for _, r := range rows {
		if strings.TrimSpace(r.Text) == "" && strings.TrimSpace(r.Amount) == "" {
			continue
		}
		m := source.LiveMessage{
			ID: r.ID, User: r.Author, Text: r.Text,
			At: time.Now(), Kind: "text", Source: "youtube_browser",
		}
		if m.User == "" {
			m.User = "anonymous"
		}
		switch r.Kind {
		case "yt-live-chat-paid-message-renderer", "yt-live-chat-paid-sticker-renderer":
			m.Kind, m.Amount = "superchat", r.Amount
			if m.Text == "" {
				m.Text = r.Amount
			}
		case "yt-live-chat-membership-item-renderer":
			m.Kind = "sub"
		}
		out = append(out, m)
	}
	return out
}

// youtubeBrowserWorker 打开聊天页，轮询页面自身发出的 live_chat 响应。
//
// 不能直接抓直播页：聊天在 iframe 里，而 PreloadScript 只注入主 frame。
// live_chat 独立页把聊天放在主 frame，且页面自己会持续轮询 get_live_chat。
type youtubeBrowserWorker struct {
	mgr    *browser.Manager
	target source.Target
}

func (w *youtubeBrowserWorker) Name() string { return "youtube_browser" }

func (w *youtubeBrowserWorker) Stream(ctx context.Context, on func(source.LiveMessage)) error {
	videoID, err := w.resolveVideoID(ctx)
	if err != nil {
		return err
	}
	chatURL := "https://www.youtube.com/live_chat?v=" + videoID + "&is_popout=1"
	if _, err := w.mgr.Capture(ctx, browser.ProviderYouTube, browser.CaptureRequest{
		Navigate: chatURL, URLRegex: `live_chat/get_live_chat`, Scroll: 0, Wait: 3 * time.Second,
	}); err != nil {
		return err
	}
	seen := map[string]bool{}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			caps, err := w.mgr.Snapshot(ctx, browser.ProviderYouTube, `live_chat/get_live_chat`)
			if err != nil {
				return err
			}
			msgs := browser.ParseYouTubeChat(caps)
			// DOM 兜底：Web 模式下流式响应抓不到，直接读渲染后的聊天行。
			if v, derr := w.mgr.Eval(ctx, browser.ProviderYouTube, youtubeChatDOMJS); derr == nil {
				msgs = append(msgs, parseYouTubeChatDOM(v)...)
			}
			for _, m := range msgs {
				key := m.ID
				if key == "" {
					// DOM 里个别行没有 id：用内容指纹去重，避免每 2 秒重复上一条。
					key = "dom:" + m.User + "\x00" + m.Text + "\x00" + m.Kind
				}
				if seen[key] {
					continue
				}
				seen[key] = true
				on(m)
			}
		}
	}
}

// resolveVideoID 频道目标先解析成当前直播的 video id；已给 video id 直接用。
func (w *youtubeBrowserWorker) resolveVideoID(ctx context.Context) (string, error) {
	id := strings.TrimPrefix(strings.TrimSpace(w.target.ID), "@")
	if w.target.Kind == source.KindLiveVideo && id != "" {
		return id, nil
	}
	if id == "" {
		return "", fmt.Errorf("YouTube 直播目标为空")
	}
	liveURL := "https://www.youtube.com/@" + id + "/live"
	if raw := strings.TrimSpace(w.target.Raw); strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		liveURL = raw
	}
	if _, err := w.mgr.Capture(ctx, browser.ProviderYouTube, browser.CaptureRequest{
		Navigate: liveURL, URLRegex: `youtubei/v1/(player|next)`, Scroll: 0, Wait: 4 * time.Second,
	}); err != nil {
		return "", err
	}
	v, err := w.mgr.Eval(ctx, browser.ProviderYouTube, `(() => {
  const r = window.ytInitialPlayerResponse;
  if (r && r.videoDetails && r.videoDetails.videoId) return r.videoDetails.videoId;
  const m = location.href.match(/[?&]v=([\w-]{11})/);
  if (m) return m[1];
  const l = document.querySelector('link[rel="canonical"]');
  if (l) { const mm = l.href.match(/[?&]v=([\w-]{11})/); if (mm) return mm[1]; }
  return '';
})()`)
	if err != nil {
		return "", err
	}
	videoID, _ := v.(string)
	if videoID == "" {
		return "", fmt.Errorf("未能从 %s 解析出正在直播的视频（可能未开播）", liveURL)
	}
	return videoID, nil
}

// twitchBrowserWorker 打开 Twitch popout 聊天页，轮询 DOM 聊天行。
//
// 不走 IRC：irc.chat.twitch.tv:6697 被常见代理节点黑洞（irc read i/o timeout）。
// 页面方案登录态由内置浏览器会话自动带上，未登录也能读（降级）；未开播聊天室仍可用。
// DOM 没有稳定消息 id，用「作者\x00文本」指纹 + FIFO 上限去重；同文本连发会被合并，
// 这是 DOM 方案的已知取舍。
type twitchBrowserWorker struct {
	mgr    *browser.Manager
	target source.Target
}

func (w *twitchBrowserWorker) Name() string { return "twitch_browser" }

// twitchChatJS 取末尾 80 条聊天行（作者/文本），限制单次 Eval 体积。
// 选择器取 Twitch 多年稳定的 chat-line__message 系；文本优先 data-a-target，
// 兜底 text-fragment 拼接。
const twitchChatJS = `(() => {
  const lines = document.querySelectorAll('.chat-line__message');
  const out = [];
  const from = Math.max(0, lines.length - 80);
  for (let i = from; i < lines.length; i++) {
    const el = lines[i];
    const au = el.querySelector('.chat-author__display-name');
    let u = au ? au.textContent : '';
    if (!u) {
      const l = el.querySelector('[data-a-user]');
      u = l ? (l.getAttribute('data-a-user') || '') : '';
    }
    const tEl = el.querySelector('[data-a-target="chat-message-text"]');
    let t = tEl ? tEl.textContent : '';
    if (!t) {
      t = Array.from(el.querySelectorAll('.text-fragment')).map((n) => n.textContent).join('');
    }
    u = (u || '').trim();
    t = (t || '').trim();
    if (u || t) out.push({ u: u, t: t });
  }
  return out;
})()`

func (w *twitchBrowserWorker) Stream(ctx context.Context, on func(source.LiveMessage)) error {
	channel := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(w.target.ID), "@"))
	if channel == "" {
		channel = twitchChannelFromURL(w.target.Raw)
	}
	if channel == "" {
		return fmt.Errorf("Twitch 直播目标为空")
	}
	chatURL := "https://www.twitch.tv/popout/" + channel + "/chat"
	if _, err := w.mgr.Capture(ctx, browser.ProviderTwitch, browser.CaptureRequest{
		Navigate: chatURL, Wait: 4 * time.Second,
	}); err != nil {
		return err
	}
	seen := map[string]bool{}
	var queue []string
	push := func(fp string) {
		if seen[fp] {
			return
		}
		seen[fp] = true
		queue = append(queue, fp)
		if len(queue) > 2000 {
			delete(seen, queue[0])
			queue = queue[1:]
		}
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			raw, err := w.mgr.Eval(ctx, browser.ProviderTwitch, twitchChatJS)
			if err != nil {
				return err
			}
			items, ok := raw.([]any)
			if !ok {
				continue
			}
			for _, it := range items {
				m, ok := it.(map[string]any)
				if !ok {
					continue
				}
				user, text := anyToString(m["u"]), anyToString(m["t"])
				if text == "" {
					continue
				}
				fp := user + "\x00" + text
				if seen[fp] {
					continue
				}
				push(fp)
				on(source.LiveMessage{
					ID: fp, Channel: channel, User: user, Text: text,
					At: time.Now(), Kind: "text", Source: "twitch_browser",
				})
			}
		}
	}
}

// twitchChannelFromURL 从 https://www.twitch.tv/{channel} 形式的 URL 取频道名。
func twitchChannelFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	if !strings.HasSuffix(strings.ToLower(u.Hostname()), "twitch.tv") {
		return ""
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) > 0 && segs[0] != "" {
		return strings.ToLower(strings.TrimPrefix(segs[0], "@"))
	}
	return ""
}

func anyToString(v any) string {
	s, _ := v.(string)
	return s
}

// ---- 直接 Live 适配器（官方 / chatdl_go）----

type directLiveAdapter struct {
	info    source.Info
	open    func(ch source.Channel, t source.Target) (Streamer, error)
	inspect func(ctx context.Context, ch source.Channel, t source.Target) (source.AccountProfile, error)
}

func (a *directLiveAdapter) Info() source.Info { return a.info }

func (a *directLiveAdapter) Match(t source.Target) source.Match {
	return source.BaseMatch(a.info, t)
}

// InspectAccount 可选：Live 目标也能做详情识别（B 站房间 / YouTube 视频）。
func (a *directLiveAdapter) InspectAccount(ctx context.Context, ch source.Channel, t source.Target) (source.AccountProfile, error) {
	if a.inspect == nil {
		return source.AccountProfile{}, &source.InspectError{
			Kind: source.InspectUnsupported, ChannelID: ch.ID, AdapterID: a.info.ID,
			Message: "适配器不支持账号详情识别",
		}
	}
	return a.inspect(ctx, ch, t)
}

func (a *directLiveAdapter) OpenLive(ch source.Channel, t source.Target) (source.LiveWorker, error) {
	s, err := a.open(ch, t)
	if err != nil {
		return nil, err
	}
	return &streamerLiveWorker{s: s}, nil
}

func (a *directLiveAdapter) TestLive(ctx context.Context, ch source.Channel, t source.Target) (source.Probe, error) {
	if _, err := a.open(ch, t); err != nil {
		return source.Probe{OK: false, Detail: err.Error()}, err
	}
	return source.Probe{OK: true, Detail: "配置已加载（点击开始后拉流）"}, nil
}

// RegisterBilibiliLiveAdapter B 站弹幕（blivedm）。
//
// B 站没有官方弹幕 API，blivedm-go 走公开 WebSocket，游客态即可收到弹幕
// （实测 room 5050 游客 15s 内 11 条）；Cookie 可选，填了昵称/礼物信息更完整。
// 这是 Bilibili 唯一的 Live 入口（原 bilibili_browser 底下就是同一个 blivedm，已合并）。
// bm 用于频道没填 Cookie 时复用浏览器里已登录的 B 站会话（游客 getDanmuInfo 会 -352）。
func RegisterBilibiliLiveAdapter(reg *source.Registry, bm *browser.Manager) error {
	return reg.Register(&directLiveAdapter{
		info: source.Info{
			ID: "bilibili_blivedm", Name: "Bilibili 弹幕（blivedm）",
			Domains:   []source.Domain{source.DomainLive},
			Platforms: []source.Platform{source.PlatformBilibili},
			Kinds:     map[source.Platform][]source.TargetKind{source.PlatformBilibili: {source.KindLiveRoom}},
			// 游客态可读；Cookie（SESSDATA + bili_jct）可选，不是 Needs。
			Transports: []source.Transport{source.TransportDirect},
			Modes:      []source.Mode{source.ModeDesktop, source.ModeWeb}, Priority: 10, Visible: true,
			Hint: "免登录公开 WebSocket；填 Cookie 后昵称/礼物信息更完整",
		},
		open: func(ch source.Channel, t source.Target) (Streamer, error) {
			cookie := strings.TrimSpace(ch.Credentials.Cookie)
			if cookie == "" {
				cookie = bilibiliCookieFromBrowser(bm)
			}
			return biliLibStreamer(SourceConfig{
				Kind: KindBilibili, Channel: liveTargetURL(t),
				BilibiliCookie: cookie,
			}), nil
		},
		inspect: inspectBilibiliRoom,
	})
}

// bilibiliCookieFromBrowser 用浏览器里已登录的 B 站会话补 Cookie。
func bilibiliCookieFromBrowser(bm *browser.Manager) string {
	if bm == nil || !bm.Available() {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return BilibiliCookieFromBrowser(ctx, bm)
}

// BilibiliCookieFromBrowser 从浏览器已登录的 B 站会话拼出 blivedm 需要的
// Cookie（SESSDATA + bili_jct）。游客 getDanmuInfo 会 -352：房间能连上但
// 一条弹幕都收不到，所以这一步决定弹幕是否为空。拿不到就返回空串。
func BilibiliCookieFromBrowser(ctx context.Context, bm *browser.Manager) string {
	if bm == nil || !bm.Available() {
		return ""
	}
	ck := map[string]string{}
	// 远程浏览器能导出 HttpOnly 的 SESSDATA；桌面模式只能读 document.cookie。
	if rc, err := bm.RemoteCookies(ctx, browser.ProviderBilibili); err == nil {
		for k, v := range rc {
			ck[k] = v
		}
	}
	if ts, err := bm.Tokens(ctx, browser.ProviderBilibili); err == nil {
		for k, v := range ts.Cookies {
			if ck[k] == "" {
				ck[k] = v
			}
		}
	}
	if ck["SESSDATA"] == "" || ck["bili_jct"] == "" {
		log.Printf("[livechat/bilibili] 浏览器里没有可用的 B 站 Cookie（游客 getDanmuInfo 会 -352，弹幕为空）")
		return ""
	}
	return "SESSDATA=" + ck["SESSDATA"] + "; bili_jct=" + ck["bili_jct"]
}

// RegisterBrowserLiveAdapters 桌面登录 Live 源。
func RegisterBrowserLiveAdapters(reg *source.Registry, mgr *browser.Manager) error {
	specs := []struct {
		info     source.Info
		provider browser.Provider
	}{
		{
			info: source.Info{
				ID: "youtube_browser", Name: "YouTube（内置浏览器）",
				Domains:    []source.Domain{source.DomainLive},
				Platforms:  []source.Platform{source.PlatformYouTube},
				Kinds:      map[source.Platform][]source.TargetKind{source.PlatformYouTube: {source.KindLiveVideo, source.KindChannel}},
				Needs:      map[source.Platform][]source.CredentialKey{source.PlatformYouTube: {source.CredDesktopSession}},
				Transports: []source.Transport{source.TransportPageFetch, source.TransportCapture},
				Modes:      []source.Mode{source.ModeDesktop},
				Priority:   15, Visible: true,
				Hint: "登录态走内嵌 YouTube 页面，无需 API Key",
			},
			provider: browser.ProviderYouTube,
		},
		{
			info: source.Info{
				ID: "twitch_browser", Name: "Twitch（内置浏览器）",
				Domains:    []source.Domain{source.DomainLive},
				Platforms:  []source.Platform{source.PlatformTwitch},
				Kinds:      map[source.Platform][]source.TargetKind{source.PlatformTwitch: {source.KindChannel}},
				Needs:      map[source.Platform][]source.CredentialKey{source.PlatformTwitch: {source.CredDesktopSession}},
				Transports: []source.Transport{source.TransportPageFetch},
				Modes:      []source.Mode{source.ModeDesktop},
				Priority:   15, Visible: true,
				Hint: "登录态 popout 聊天页轮询（不走 IRC）",
			},
			provider: browser.ProviderTwitch,
		},
	}
	for _, s := range specs {
		if err := reg.Register(&BrowserLiveAdapter{info: s.info, mgr: mgr, provider: s.provider}); err != nil {
			return err
		}
	}
	return nil
}

func liveTargetURL(t source.Target) string {
	raw := strings.TrimSpace(t.Raw)
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	id := strings.TrimPrefix(strings.TrimSpace(t.ID), "@")
	switch t.Platform {
	case source.PlatformYouTube:
		if t.Kind == source.KindChannel {
			if strings.HasPrefix(id, "UC") {
				return "https://www.youtube.com/channel/" + id + "/live"
			}
			return "https://www.youtube.com/@" + id + "/live"
		}
		return "https://www.youtube.com/watch?v=" + id
	case source.PlatformBilibili:
		return "https://live.bilibili.com/" + id
	case source.PlatformTwitch:
		return "https://www.twitch.tv/" + id
	}
	return raw
}
