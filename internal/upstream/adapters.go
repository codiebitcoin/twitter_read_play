package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/source"
	"twitter_read_play/internal/twitter"
)

// feedAdapter 把现有 twitter.Provider 包装成 source.FeedAdapter。
// 渠道只提供凭据，目标平台/账号由 Role 的 Target 决定。
type feedAdapter struct {
	info        source.Info
	fallbackKey func() string
}

func (a *feedAdapter) Info() source.Info { return a.info }

func (a *feedAdapter) Match(t source.Target) source.Match {
	return source.BaseMatch(a.info, t)
}

func (a *feedAdapter) OpenFeed(ch source.Channel, t source.Target) (source.FeedWorker, error) {
	return a.provider(ch, t)
}

func (a *feedAdapter) provider(ch source.Channel, t source.Target) (twitter.Provider, error) {
	u := &model.Upstream{
		ID:       ch.ID,
		Name:     ch.Name,
		API:      ch.AdapterID,
		Platform: string(t.Platform),
		APIKey:   ch.Credentials.APIKey,
		BaseURL:  ch.Credentials.BaseURL,
		Cookie:   ch.Credentials.Cookie,
		Extra:    ch.Credentials.BaseURL,
	}
	// 兼容新凭据字段：xtoken 的 auth_token / ct0。
	if strings.EqualFold(ch.AdapterID, "xtoken") {
		if u.APIKey == "" {
			u.APIKey = ch.Credentials.AuthToken
		}
		if u.Cookie == "" {
			u.Cookie = ch.Credentials.CT0
		}
	}
	key := ""
	if a.fallbackKey != nil {
		key = a.fallbackKey()
	}
	return ProviderFor(u, key)
}

// InspectAccount 可选能力：读取账号公开资料。
func (a *feedAdapter) InspectAccount(ctx context.Context, ch source.Channel, t source.Target) (source.AccountProfile, error) {
	p, err := a.provider(ch, t)
	if err != nil {
		return source.AccountProfile{}, &source.InspectError{
			Kind: source.InspectUnsupported, ChannelID: ch.ID, AdapterID: a.info.ID,
			Message: err.Error(), Err: err,
		}
	}
	pp, ok := p.(twitter.ProfileProvider)
	if !ok {
		return source.AccountProfile{}, &source.InspectError{
			Kind: source.InspectUnsupported, ChannelID: ch.ID, AdapterID: a.info.ID,
			Message: "适配器不支持账号详情识别",
		}
	}
	up, err := pp.Profile(ctx, t.ID)
	if err != nil {
		return source.AccountProfile{}, inspectError(ch, a.info.ID, err)
	}
	return mapAccountProfile(a.info.ID, t, up), nil
}

func (a *feedAdapter) TestFeed(ctx context.Context, ch source.Channel, t source.Target) (source.Probe, error) {
	w, err := a.OpenFeed(ch, t)
	if err != nil {
		return source.Probe{OK: false, Detail: err.Error()}, err
	}
	handle := strings.TrimSpace(t.ID)
	if handle == "" {
		handle = sampleHandle(t.Platform)
	}
	start := time.Now()
	tweets, err := w.FetchLatest(ctx, handle, 1)
	ms := time.Since(start).Milliseconds()
	if err != nil {
		return source.Probe{OK: false, Detail: err.Error(), Milliseconds: ms}, err
	}
	if len(tweets) == 0 {
		return source.Probe{OK: false, Detail: "连通但返回 0 条（确认账号/URL 是否存在）", Milliseconds: ms}, nil
	}
	return source.Probe{OK: true, Count: len(tweets), Milliseconds: ms, Detail: fmt.Sprintf("%d 条 · %dms", len(tweets), ms)}, nil
}

// RegisterFeedAdapters 注册现有的直接 Feed 上游。
func RegisterFeedAdapters(reg *source.Registry, fallbackKey func() string) error {
	both := []source.Mode{source.ModeDesktop, source.ModeWeb}
	infos := []source.Info{
		{
			ID: "socialdata", Name: "socialdata.tools",
			Domains:    []source.Domain{source.DomainFeed},
			Platforms:  []source.Platform{source.PlatformX},
			Kinds:      map[source.Platform][]source.TargetKind{source.PlatformX: {source.KindProfile}},
			Needs:      map[source.Platform][]source.CredentialKey{source.PlatformX: {source.CredAPIKey}},
			Transports: []source.Transport{source.TransportDirect},
			Modes:      both, Priority: 20, Visible: true,
			Hint: "按量计费 $0.2/1k 条；也可指向自建兼容服务",
		},
		{
			ID: "socialread", Name: "twitter-read（微博/小红书/B站）",
			Domains:   []source.Domain{source.DomainFeed},
			Platforms: []source.Platform{source.PlatformWeibo, source.PlatformXHS, source.PlatformBilibili},
			Kinds: map[source.Platform][]source.TargetKind{
				source.PlatformWeibo:    {source.KindProfile},
				source.PlatformXHS:      {source.KindProfile},
				source.PlatformBilibili: {source.KindProfile},
			},
			Needs: map[source.Platform][]source.CredentialKey{
				source.PlatformWeibo:    {source.CredAPIKey},
				source.PlatformXHS:      {source.CredAPIKey},
				source.PlatformBilibili: {source.CredAPIKey},
			},
			Transports: []source.Transport{source.TransportDirect},
			Modes:      both, Priority: 10, Visible: true,
			Hint: "自建 twitter-read；base_url 空 = 默认服务",
		},
		{
			ID: "xtoken", Name: "X 登录 token",
			Domains:    []source.Domain{source.DomainFeed},
			Platforms:  []source.Platform{source.PlatformX},
			Kinds:      map[source.Platform][]source.TargetKind{source.PlatformX: {source.KindProfile}},
			Needs:      map[source.Platform][]source.CredentialKey{source.PlatformX: {source.CredAPIKey}},
			Transports: []source.Transport{source.TransportDirect},
			Modes:      both, Priority: 30, Visible: true,
			Hint: "填 auth_token；ct0 可写在 Cookie 栏",
		},
		{
			ID: "twitter_official", Name: "X 官方 API v2",
			Domains:    []source.Domain{source.DomainFeed},
			Platforms:  []source.Platform{source.PlatformX},
			Kinds:      map[source.Platform][]source.TargetKind{source.PlatformX: {source.KindProfile}},
			Needs:      map[source.Platform][]source.CredentialKey{source.PlatformX: {source.CredAPIKey}},
			Transports: []source.Transport{source.TransportDirect},
			Modes:      both, Priority: 40, Visible: true,
			Hint: "免费档无读取权限，Basic $200/月起",
		},
		{
			ID: "rss", Name: "RSS / Atom",
			Domains:    []source.Domain{source.DomainFeed},
			Platforms:  []source.Platform{source.PlatformRSS},
			Kinds:      map[source.Platform][]source.TargetKind{source.PlatformRSS: {source.KindFeedURL}},
			Transports: []source.Transport{source.TransportDirect},
			Modes:      both, Priority: 10, Visible: true,
			Hint: "任意 RSS/Atom 源；推荐自建 RSSHub",
		},
		{
			ID: "weibo", Name: "微博（Cookie）",
			Domains:    []source.Domain{source.DomainFeed},
			Platforms:  []source.Platform{source.PlatformWeibo},
			Kinds:      map[source.Platform][]source.TargetKind{source.PlatformWeibo: {source.KindProfile}},
			Needs:      map[source.Platform][]source.CredentialKey{source.PlatformWeibo: {source.CredCookie}},
			Transports: []source.Transport{source.TransportDirect},
			Modes:      both, Priority: 30, Visible: true,
			Hint: "m.weibo.cn container 接口；需登录 Cookie",
		},
		{
			ID: "truth", Name: "Truth Social",
			Domains:    []source.Domain{source.DomainFeed},
			Platforms:  []source.Platform{source.PlatformTruth},
			Kinds:      map[source.Platform][]source.TargetKind{source.PlatformTruth: {source.KindProfile}},
			Needs:      map[source.Platform][]source.CredentialKey{source.PlatformTruth: {source.CredBaseURL}},
			Transports: []source.Transport{source.TransportDirect},
			Modes:      both, Priority: 5, Visible: true,
			Hint: "Mastodon 风格 /api/v1；国内直连需反代 base_url（桌面端可用内置浏览器渠道）",
		},
		{
			ID: "bluesky", Name: "Bluesky（公开 API）",
			Domains:    []source.Domain{source.DomainFeed},
			Platforms:  []source.Platform{source.PlatformBluesky},
			Kinds:      map[source.Platform][]source.TargetKind{source.PlatformBluesky: {source.KindProfile}},
			Transports: []source.Transport{source.TransportDirect},
			Modes:      both, Priority: 10, Visible: true,
			Hint: "public.api.bsky.app 公开 AppView，无需登录/Key",
		},
	}
	for _, info := range infos {
		if err := reg.Register(&feedAdapter{info: info, fallbackKey: fallbackKey}); err != nil {
			return err
		}
	}
	return nil
}

func mapAccountProfile(adapterID string, t source.Target, p twitter.UserProfile) source.AccountProfile {
	canonical, _ := t.ViewURL()
	if p.CanonicalURL != "" {
		canonical = p.CanonicalURL
	}
	handle := firstNonEmptyString(p.Handle, t.ID)
	if t.Platform == source.PlatformX && handle != "" && !strings.HasPrefix(handle, "@") {
		handle = "@" + handle
	}
	return source.AccountProfile{
		Platform:     t.Platform,
		Kind:         t.Kind,
		ID:           firstNonEmptyString(p.ID, t.ID),
		Handle:       handle,
		DisplayName:  firstNonEmptyString(p.DisplayName, handle),
		AvatarURL:    p.AvatarURL,
		Bio:          p.Bio,
		Followers:    int64(p.Followers),
		Following:    int64(p.Following),
		Posts:        int64(p.Posts),
		Verified:     p.Verified,
		VerifiedNote: p.VerifiedNote,
		CanonicalURL: canonical,
		Source:       adapterID,
		FetchedAt:    time.Now(),
		Partial:      p.Partial,
	}
}

func inspectError(ch source.Channel, adapterID string, err error) error {
	kind := source.InspectUpstream
	var qe *twitter.QuotaError
	if errors.As(err, &qe) {
		switch qe.Code {
		case http.StatusPaymentRequired, http.StatusTooManyRequests:
			kind = source.InspectRateLimited
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		kind = source.InspectTimeout
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "need_login"), strings.Contains(msg, "未登录"),
		strings.Contains(lower, "auth_token"), strings.Contains(lower, "cookie"):
		kind = source.InspectNeedLogin
	case strings.Contains(msg, "反爬"), strings.Contains(lower, "http 429"), strings.Contains(msg, "限流"):
		kind = source.InspectRateLimited
	case strings.Contains(msg, "不存在"), strings.Contains(lower, "not found"):
		kind = source.InspectNotFound
	}
	return &source.InspectError{Kind: kind, ChannelID: ch.ID, AdapterID: adapterID, Message: msg, Err: err}
}

func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func sampleHandle(p source.Platform) string {
	switch p {
	case source.PlatformWeibo:
		return "人民日报"
	case source.PlatformXHS:
		return "butterzhu"
	case source.PlatformBilibili:
		return "2"
	case source.PlatformRSS:
		return "https://hnrss.org/frontpage"
	case source.PlatformTruth:
		return "truthsocial"
	case source.PlatformBluesky:
		return "bsky.app"
	}
	return "X"
}
