package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"twitter_read_play/internal/source"
)

// InspectAccount 桌面内置浏览器识别：读取目标账号的公开资料。
// 失败不直接报网络错误：先区分未登录 / 账号不存在 / 页面结构变化，供上层降级。
func (a *FeedAdapter) InspectAccount(ctx context.Context, ch source.Channel, t source.Target) (source.AccountProfile, error) {
	if !a.mgr.Available() {
		return source.AccountProfile{}, &source.InspectError{
			Kind: source.InspectUnsupported, ChannelID: ch.ID, AdapterID: a.info.ID,
			Message: "内置浏览器仅在桌面模式可用",
		}
	}

	var (
		p  source.AccountProfile
		ok bool
	)
	switch a.provider {
	case ProviderX:
		p, ok = a.inspectX(ctx, t)
	case ProviderWeibo:
		p, ok = a.inspectWeibo(ctx, t)
	case ProviderXHS:
		p, ok = a.inspectXHS(ctx, t)
	case ProviderInstagram:
		p, ok = a.inspectInstagram(ctx, t)
	case ProviderTruth:
		p, ok = a.inspectTruth(ctx, t)
	default:
		return source.AccountProfile{}, &source.InspectError{
			Kind: source.InspectUnsupported, ChannelID: ch.ID, AdapterID: a.info.ID,
			Message: fmt.Sprintf("不支持的平台 %q", a.provider),
		}
	}
	if ctx.Err() != nil {
		return source.AccountProfile{}, &source.InspectError{
			Kind: source.InspectTimeout, ChannelID: ch.ID, AdapterID: a.info.ID,
			Message: "识别超时", Err: ctx.Err(),
		}
	}
	if !ok {
		kind := source.InspectNotFound
		msg := "未读取到账号资料：可能账号不存在或页面结构发生变化"
		cfg, hasCfg := configFor(a.provider)
		if st, err := a.mgr.Probe(ctx, a.provider); err != nil {
			kind = source.InspectUpstream
			msg = "识别失败：" + err.Error()
		} else if !st.Connected {
			if hasCfg && cfg.LoginOptional {
				// 游客态就能读的平台（Truth Social）：读不到是账号/页面问题，
				// 不是登录问题，别把用户引去登录。
				msg = "未读取到账号资料：可能账号不存在或该账号没有公开内容"
			} else {
				kind = source.InspectNeedLogin
				msg = "未登录 " + string(a.provider) + "，请先登录或配置其他渠道"
			}
		}
		return source.AccountProfile{}, &source.InspectError{
			Kind: kind, ChannelID: ch.ID, AdapterID: a.info.ID, Message: msg,
		}
	}
	if p.CanonicalURL == "" {
		p.CanonicalURL, _ = t.ViewURL()
	}
	p.Source = a.info.ID
	if p.FetchedAt.IsZero() {
		p.FetchedAt = time.Now()
	}
	return p, nil
}

func (a *FeedAdapter) inspectX(ctx context.Context, t source.Target) (source.AccountProfile, bool) {
	id := strings.TrimPrefix(strings.TrimSpace(t.ID), "@")
	if id == "" {
		return source.AccountProfile{}, false
	}
	caps, err := a.mgr.Capture(ctx, ProviderX, CaptureRequest{
		Navigate: "https://x.com/" + url.PathEscape(id),
		URLRegex: `/i/api/graphql/.*/(UserByScreenName)`,
		Scroll:   1, Wait: 1500 * time.Millisecond,
	})
	if err == nil {
		if p, ok := ParseXUser(caps); ok {
			return p, true
		}
	}
	// 远程浏览器（chrome-driverless）是「导航加载完成才返回」，捕获钩子只能在之后注入，
	// 加载期的 UserByScreenName 永远抓不到 → capture 为空时读已渲染的 DOM 兜底。
	if p, ok := a.inspectXFromDOM(ctx, id); ok {
		return p, true
	}
	return source.AccountProfile{}, false
}

// inspectXDOMJS 从已渲染的 X 主页读取资料。用 textContent 而不是 innerText：
// 桌面端工作窗是隐藏的，innerText 依赖布局，可能读成空串。
const inspectXDOMJS = `(() => {
  const text = (el) => el ? (el.textContent || '').trim() : '';
  const flat = (el) => el ? (el.textContent || '').replace(/\s+/g, ' ').trim() : '';
  const un = document.querySelector('[data-testid="UserName"]');
  const spans = un ? [...un.querySelectorAll('span')].map(text).filter(Boolean) : [];
  const handle = (spans.find((s) => s.startsWith('@')) || '').replace(/^@/, '');
  const name = spans.find((s) => s && !s.startsWith('@')) || '';
  const stat = (re) => {
    for (const a of document.querySelectorAll('a[href]')) {
      if (re.test(a.getAttribute('href') || '')) return text(a);
    }
    return '';
  };
  const img = document.querySelector('[data-testid^="UserAvatar"] img');
  // 只取主页栏正文：整页 textContent 头部是一大段内联 error 页样式，会把帖子数挤出截断窗口
  const col = document.querySelector('[data-testid="primaryColumn"]');
  const body = flat(col) || flat(document.body);
  return JSON.stringify({
    href: location.href,
    handle: handle,
    name: name,
    bio: text(document.querySelector('[data-testid="UserDescription"]')),
    avatar: img ? img.src : '',
    verified: !!document.querySelector('[data-testid="icon-verified"]'),
    followers: stat(/\/verified_followers$|\/followers$/),
    following: stat(/\/following$/),
    body: body.slice(0, 1200)
  });
})()`

// inspectXFromDOM 读取当前工作页的 DOM 资料；页面必须确实落在该账号主页上
// （导航静默失败时读到的会是上一页，宁可失败也不返回别人的资料）。
func (a *FeedAdapter) inspectXFromDOM(ctx context.Context, id string) (source.AccountProfile, bool) {
	v, err := a.mgr.Eval(ctx, ProviderX, inspectXDOMJS)
	if err != nil {
		return source.AccountProfile{}, false
	}
	return ParseXUserDOM(v, id)
}

func (a *FeedAdapter) inspectWeibo(ctx context.Context, t source.Target) (source.AccountProfile, bool) {
	id := strings.TrimPrefix(strings.TrimSpace(t.ID), "@")
	if id == "" {
		return source.AccountProfile{}, false
	}
	nav := "https://m.weibo.cn/n/" + url.PathEscape(id)
	if _, err := strconv.Atoi(id); err == nil {
		nav = "https://m.weibo.cn/u/" + url.PathEscape(id)
	}
	caps, err := a.mgr.Capture(ctx, ProviderWeibo, CaptureRequest{
		Navigate: nav, URLRegex: `/api/container/getIndex`,
		Scroll: 1, Wait: 1200 * time.Millisecond,
	})
	if err == nil {
		if p, ok := ParseWeiboUser(caps); ok {
			return p, true
		}
	}
	// 微博 API URL 会直接把 JSON 打在页面上，作为兜底。
	if text, terr := a.mgr.Text(ctx, ProviderWeibo); terr == nil {
		if p, ok := ParseWeiboUser([]Captured{{JSON: json.RawMessage(text)}}); ok {
			return p, true
		}
	}
	return source.AccountProfile{}, false
}

// inspectInstagram 用 web_profile_info 读账号资料；它同时返回最近 12 条帖子，
// 失败时再退回主页 capture。
func (a *FeedAdapter) inspectInstagram(ctx context.Context, t source.Target) (source.AccountProfile, bool) {
	id := strings.TrimPrefix(strings.TrimSpace(t.ID), "@")
	if id == "" {
		return source.AccountProfile{}, false
	}
	if resp, err := a.mgr.Fetch(ctx, ProviderInstagram, Request{
		URL:     "https://www.instagram.com/api/v1/users/web_profile_info/?username=" + url.QueryEscape(id),
		Headers: instagramHeaders,
	}); err == nil && resp.Status == 200 {
		if p, ok := ParseInstagramUserJSON(resp.Body); ok {
			return p, true
		}
	}
	caps, err := a.mgr.Capture(ctx, ProviderInstagram, CaptureRequest{
		Navigate: "https://www.instagram.com/" + url.PathEscape(id) + "/",
		URLRegex: `(/api/v1/users/web_profile_info/|/graphql/query)`,
		Scroll:   1, Wait: 1500 * time.Millisecond,
	})
	if err != nil {
		return source.AccountProfile{}, false
	}
	return ParseInstagramUser(caps)
}

// inspectTruth 用 accounts/lookup：它游客可读，且返回的资料和 accounts/<id> 一样全
// （后者需要登录，401 USER_UNAUTHENTICATED）。
func (a *FeedAdapter) inspectTruth(ctx context.Context, t source.Target) (source.AccountProfile, bool) {
	id := strings.TrimPrefix(strings.TrimSpace(t.ID), "@")
	if id == "" {
		return source.AccountProfile{}, false
	}
	resp, err := a.mgr.Fetch(ctx, ProviderTruth, Request{
		URL:     "https://truthsocial.com/api/v1/accounts/lookup?acct=" + url.QueryEscape(id),
		Headers: map[string]string{"Accept": "application/json"},
	})
	if err != nil || resp.Status != 200 {
		return source.AccountProfile{}, false
	}
	return ParseTruthUserJSON(resp.Body)
}

func (a *FeedAdapter) inspectXHS(ctx context.Context, t source.Target) (source.AccountProfile, bool) {
	id := strings.TrimPrefix(strings.TrimSpace(t.ID), "@")
	if id == "" {
		return source.AccountProfile{}, false
	}
	caps, err := a.mgr.Capture(ctx, ProviderXHS, CaptureRequest{
		Navigate: "https://www.xiaohongshu.com/user/profile/" + url.PathEscape(id),
		URLRegex: `/api/sns/web/v\d/(user/otherinfo|user_posted)`,
		Scroll:   1, Wait: 1500 * time.Millisecond,
	})
	if err == nil {
		if p, ok := ParseXHSUser(caps); ok {
			return p, true
		}
	}
	// SSR 兜底：__INITIAL_STATE__.user.userPageData。
	v, e := a.mgr.Eval(ctx, ProviderXHS, `(() => {
	  const s = window.__INITIAL_STATE__ || {};
	  const u = s.user || {};
	  return JSON.stringify(u.userPageData || {});
	})()`)
	if e == nil {
		if p, ok := ParseXHSPageState(v); ok {
			return p, true
		}
	}
	return source.AccountProfile{}, false
}
