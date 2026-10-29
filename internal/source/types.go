// Package source 上游领域模型：渠道（Channel）、目标（Target）、适配器（Adapter）与
// 双向匹配（目标→能力 ∧ 渠道→凭据）。
//
// 设计要点：
//   - Channel 只保存「怎么连」（适配器 + 凭据），不保存「连谁」；
//   - Target 只保存「连谁」（Role 里解析出来的平台/类型/标识）；
//   - Adapter 只声明能力（平台、target kind、需要的凭据），由 Resolver 选源。
package source

import (
	"context"
	"strings"
	"time"

	"twitter_read_play/internal/model"
)

// Mode 运行模式：桌面（内置 WebView）或 Web/Docker（远程 chrome-driverless）。
type Mode string

const (
	ModeDesktop Mode = "desktop"
	ModeWeb     Mode = "web"
)

// Domain 数据域。
type Domain string

const (
	DomainFeed Domain = "feed"
	DomainLive Domain = "live"
)

// Platform 平台。
type Platform string

const (
	PlatformX         Platform = "x"
	PlatformWeibo     Platform = "weibo"
	PlatformXHS       Platform = "xiaohongshu"
	PlatformInstagram Platform = "instagram"
	PlatformBluesky   Platform = "bluesky"
	PlatformBilibili  Platform = "bilibili"
	PlatformYouTube   Platform = "youtube"
	PlatformTwitch    Platform = "twitch"
	PlatformRSS       Platform = "rss"
	PlatformTruth     Platform = "truth"
)

// NormalizePlatform 归一化平台名（x/twitter 等价，wb/xhs 等别名）。
func NormalizePlatform(s string) Platform {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "twitter", "x":
		return PlatformX
	case "weibo", "wb":
		return PlatformWeibo
	case "xiaohongshu", "xhs", "redbook":
		return PlatformXHS
	case "instagram", "ig", "ins":
		return PlatformInstagram
	case "bluesky", "bsky":
		return PlatformBluesky
	case "bilibili", "bili":
		return PlatformBilibili
	case "youtube", "yt":
		return PlatformYouTube
	case "twitch":
		return PlatformTwitch
	case "rss":
		return PlatformRSS
	case "truth", "truthsocial":
		return PlatformTruth
	}
	return Platform(strings.ToLower(strings.TrimSpace(s)))
}

// TargetKind 目标类型。
type TargetKind string

const (
	KindProfile   TargetKind = "profile"    // @handle / 用户主页
	KindFeedURL   TargetKind = "feed_url"   // RSS / Atom
	KindChannel   TargetKind = "channel"    // YouTube/Twitch 频道
	KindLiveRoom  TargetKind = "live_room"  // B 站直播间
	KindLiveVideo TargetKind = "live_video" // YouTube 直播/首播
	KindVideo     TargetKind = "video"      // 点播/VOD/回放
)

// Target Role 侧解析结果。
type Target struct {
	Domain   Domain     `json:"domain"`
	Platform Platform   `json:"platform"`
	Kind     TargetKind `json:"kind"`
	Raw      string     `json:"raw"`
	ID       string     `json:"id"`
	Display  string     `json:"display,omitempty"`
}

// Intent 本次解析的用途。fetch = 拉时间线/弹幕；inspect = 读取账号公开资料。
type Intent string

const (
	IntentFetch   Intent = "fetch"
	IntentInspect Intent = "inspect"
)

// AccountProfile 目标账号的公开资料（不含 token / Cookie / 私信）。
// 由 AccountInspector 从渠道读取，只用于「确认是不是对的人」。
type AccountProfile struct {
	Platform     Platform   `json:"platform"`
	Kind         TargetKind `json:"kind"`
	ID           string     `json:"id"`
	Handle       string     `json:"handle,omitempty"`
	DisplayName  string     `json:"display_name,omitempty"`
	AvatarURL    string     `json:"avatar_url,omitempty"`
	Bio          string     `json:"bio,omitempty"`
	Followers    int64      `json:"followers,omitempty"`
	Following    int64      `json:"following,omitempty"`
	Posts        int64      `json:"posts,omitempty"`
	Verified     bool       `json:"verified,omitempty"`
	VerifiedNote string     `json:"verified_note,omitempty"`
	CanonicalURL string     `json:"canonical_url"`
	Source       string     `json:"source"`
	FetchedAt    time.Time  `json:"fetched_at"`
	Partial      bool       `json:"partial,omitempty"`
}

// AccountInspector 可选能力：读取目标账号的公开资料。
// 不实现该接口的适配器仍可参与 fetch 选源，只是不能用于 inspect。
type AccountInspector interface {
	InspectAccount(ctx context.Context, ch Channel, t Target) (AccountProfile, error)
}

// AsInspector 判断适配器是否支持详情识别。
func AsInspector(a Adapter) (AccountInspector, bool) {
	i, ok := a.(AccountInspector)
	return i, ok
}

// IsZero 是否空目标。
func (t Target) IsZero() bool {
	return t.Domain == "" && t.Platform == "" && t.Kind == "" && t.Raw == ""
}

// CredentialKey 凭据种类。
type CredentialKey string

const (
	CredAPIKey         CredentialKey = "api_key"
	CredBaseURL        CredentialKey = "base_url"
	CredCookie         CredentialKey = "cookie"
	CredOAuth          CredentialKey = "oauth"
	CredClientID       CredentialKey = "client_id"
	CredDesktopSession CredentialKey = "desktop_session"
)

// Credentials 渠道凭据。与 model.Upstream 的扁平字段兼容。
type Credentials struct {
	APIKey          string `json:"api_key,omitempty"`
	BaseURL         string `json:"base_url,omitempty"`
	Cookie          string `json:"cookie,omitempty"`
	AuthToken       string `json:"auth_token,omitempty"`
	CT0             string `json:"ct0,omitempty"`
	OAuth           string `json:"oauth,omitempty"`
	ClientID        string `json:"client_id,omitempty"`
	ClientSecret    string `json:"client_secret,omitempty"`
	SessionProvider string `json:"session_provider,omitempty"` // x | weibo | xiaohongshu | instagram | truth | bluesky | youtube | bilibili | twitch
	Proxy           string `json:"proxy,omitempty"`
}

// Has 凭据是否就绪。
func (c Credentials) Has(k CredentialKey) bool {
	switch k {
	case CredAPIKey:
		return strings.TrimSpace(c.APIKey) != ""
	case CredBaseURL:
		return strings.TrimSpace(c.BaseURL) != ""
	case CredCookie:
		return strings.TrimSpace(c.Cookie) != ""
	case CredOAuth:
		return strings.TrimSpace(c.OAuth) != ""
	case CredClientID:
		return strings.TrimSpace(c.ClientID) != ""
	case CredDesktopSession:
		return strings.TrimSpace(c.SessionProvider) != ""
	case "":
		return true
	}
	return false
}

// Channel 渠道 = 适配器实例 + 凭据。
type Channel struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Domain         Domain      `json:"domain"`
	AdapterID      string      `json:"adapter_id"`
	Enabled        bool        `json:"enabled"`
	Credentials    Credentials `json:"credentials"`
	RefreshSeconds int         `json:"refresh_seconds,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	Sort           int         `json:"sort,omitempty"`
}

// Transport 访问平台的方式。
type Transport string

const (
	TransportDirect        Transport = "direct"
	TransportPageFetch     Transport = "page_fetch"
	TransportCapture       Transport = "capture"
	TransportRemoteFetch   Transport = "remote_fetch"
	TransportRemoteCapture Transport = "remote_capture"
)

// Info 适配器元信息。
type Info struct {
	ID         string                         `json:"id"`
	Name       string                         `json:"name"`
	Domains    []Domain                       `json:"domains"`
	Platforms  []Platform                     `json:"platforms"`
	Kinds      map[Platform][]TargetKind      `json:"kinds"`
	Needs      map[Platform][]CredentialKey   `json:"needs,omitempty"`
	AnyOf      map[Platform][][]CredentialKey `json:"any_of,omitempty"` // 每组内部 AND，组之间 OR
	Transports []Transport                    `json:"transports,omitempty"`
	Modes      []Mode                         `json:"modes,omitempty"`
	Priority   int                            `json:"priority"`
	Visible    bool                           `json:"visible"`
	Hint       string                         `json:"hint,omitempty"`
}

// SupportsDomain 是否支持该域。
func (i Info) SupportsDomain(d Domain) bool {
	for _, v := range i.Domains {
		if v == d {
			return true
		}
	}
	return false
}

// SupportsPlatform 是否支持该平台。
func (i Info) SupportsPlatform(p Platform) bool {
	for _, v := range i.Platforms {
		if NormalizePlatform(string(v)) == NormalizePlatform(string(p)) {
			return true
		}
	}
	return false
}

// KindsFor 该平台支持的 target kind。
func (i Info) KindsFor(p Platform) []TargetKind {
	if i.Kinds == nil {
		return nil
	}
	for k, v := range i.Kinds {
		if NormalizePlatform(string(k)) == NormalizePlatform(string(p)) {
			return v
		}
	}
	return nil
}

// SupportsKind 是否支持该平台的该 kind。
func (i Info) SupportsKind(p Platform, k TargetKind) bool {
	for _, v := range i.KindsFor(p) {
		if v == k {
			return true
		}
	}
	return false
}

// SupportsMode 是否支持该运行模式（空 = 两种都支持）。
func (i Info) SupportsMode(m Mode) bool {
	if len(i.Modes) == 0 {
		return true
	}
	for _, v := range i.Modes {
		if v == m {
			return true
		}
	}
	return false
}

// NeedsFor 该平台必填凭据（AND）。
func (i Info) NeedsFor(p Platform) []CredentialKey {
	if i.Needs == nil {
		return nil
	}
	for k, v := range i.Needs {
		if NormalizePlatform(string(k)) == NormalizePlatform(string(p)) {
			return v
		}
	}
	return nil
}

// AnyOfFor 该平台的备选凭据组（组间 OR）。
func (i Info) AnyOfFor(p Platform) [][]CredentialKey {
	if i.AnyOf == nil {
		return nil
	}
	for k, v := range i.AnyOf {
		if NormalizePlatform(string(k)) == NormalizePlatform(string(p)) {
			return v
		}
	}
	return nil
}

// Match 匹配结果。
type Match struct {
	OK      bool            `json:"ok"`
	Score   int             `json:"score"`
	Reason  string          `json:"reason,omitempty"`
	Missing []CredentialKey `json:"missing,omitempty"`
}

func reject(reason string) Match { return Match{Reason: reason} }

func rejectMissing(reason string, missing []CredentialKey) Match {
	return Match{Reason: reason, Missing: missing}
}

func okMatch(score int) Match {
	if score <= 0 {
		score = 100
	}
	return Match{OK: true, Score: score}
}

// Probe 渠道测试结果。
type Probe struct {
	OK           bool   `json:"ok"`
	Detail       string `json:"detail,omitempty"`
	Count        int    `json:"count,omitempty"`
	Milliseconds int64  `json:"ms,omitempty"`
}

// FeedWorker 拉取目标的最新帖子。twitter.Provider 结构上满足它。
type FeedWorker interface {
	Name() string
	FetchLatest(ctx context.Context, id string, count int) ([]model.Tweet, error)
}

// LiveWorker 拉取直播消息。
type LiveWorker interface {
	Name() string
	Stream(ctx context.Context, on func(LiveMessage)) error
}

// LiveMessage 与 livechat.Message 同形（source 包不依赖 livechat，避免循环）。
type LiveMessage struct {
	ID      string    `json:"id"`
	Channel string    `json:"channel"`
	User    string    `json:"user"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Amount  string    `json:"amount,omitempty"`
	Source  string    `json:"source"`
	// UserLevel 用户等级（如 B 站 UL）。0 = 平台不提供 —— 过滤时 0 一律放行。
	UserLevel int `json:"user_level,omitempty"`
}

// Adapter 适配器基础契约。
type Adapter interface {
	Info() Info
	// Match 只做能力判断（目标→适配器），不检查凭据。
	Match(t Target) Match
}

// FeedAdapter Feed 适配器。
type FeedAdapter interface {
	Adapter
	OpenFeed(ch Channel, t Target) (FeedWorker, error)
	TestFeed(ctx context.Context, ch Channel, t Target) (Probe, error)
}

// LiveAdapter Live 适配器。
type LiveAdapter interface {
	Adapter
	OpenLive(ch Channel, t Target) (LiveWorker, error)
	TestLive(ctx context.Context, ch Channel, t Target) (Probe, error)
}

// AsFeed 类型断言。
func AsFeed(a Adapter) (FeedAdapter, bool) {
	f, ok := a.(FeedAdapter)
	return f, ok
}

// AsLive 类型断言。
func AsLive(a Adapter) (LiveAdapter, bool) {
	l, ok := a.(LiveAdapter)
	return l, ok
}

// Rejection 某个渠道被拒绝的原因。
type Rejection struct {
	ChannelID string          `json:"channel_id"`
	AdapterID string          `json:"adapter_id,omitempty"`
	Reason    string          `json:"reason"`
	Missing   []CredentialKey `json:"missing,omitempty"`
}

// Resolved 解析结果。
type Resolved struct {
	Channel  Channel     `json:"channel"`
	Adapter  Adapter     `json:"-"`
	Target   Target      `json:"target"`
	Score    int         `json:"score"`
	Warnings []string    `json:"warnings,omitempty"`
	Rejected []Rejection `json:"rejected,omitempty"`
}
