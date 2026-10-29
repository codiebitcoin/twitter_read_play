// Package provider 定义统一的多平台直播弹幕采集接口。
// 参考 "Live stream chat APIs" 会话的选型结论：
//   - youtube_noauth   : 集成 abhinavxd/youtube-live-chat-downloader，免认证，解析 innertube continuation
//   - youtube_official : YouTube Data API liveChatMessages.list 轮询（需 OAuth token）
//   - twitch_eventsub  : Twitch EventSub channel.chat.message WebSocket 订阅（需 user token）
//     （twitch_irc 已移除：IRC 6697 被常见代理节点黑洞；主应用改走内置浏览器读 popout 聊天页）
//   - bilibili_blivedm : 社区 SDK blivedm-go（WebSocket）；Cookie 可选，游客可读
package provider

import (
	"context"
	"time"
)

// ChatMessage 是跨平台统一的弹幕结构。
type ChatMessage struct {
	Platform string // youtube | twitch | bilibili
	Author   string
	Text     string
	Kind     string // text | superchat | gift | sub | cheer（空 = text）
	Amount   string // 打赏金额（如 "¥30" / "$5.00" / "100 bits"）
	// UserLevel 用户等级（如 B 站 UL 等级）。0 = 平台不提供或未知 ——
	// 按等级过滤时 0 一律放行，避免不给等级的平台把弹幕全滤没。
	UserLevel int
	TS        time.Time
	Raw       string // 原始负载（JSON 等），可选
}

// Provider 统一的弹幕流接口。
// target 语义由各实现决定：YouTube 为视频/直播 URL，Twitch 为频道名（小写）。
// 返回的 channel 在 ctx 取消或直播结束时关闭；错误通过 error channel 传递。
type Provider interface {
	Name() string
	// Stream 开始拉取弹幕。msgs 关闭即表示流结束。
	// 实现应在独立 goroutine 中运行，Stream 本身立即返回。
	Stream(ctx context.Context, target string) (<-chan ChatMessage, <-chan error, error)
}

// Registry 按名称查找 Provider。
type Registry map[string]Provider

func (r Registry) Get(name string) (Provider, bool) {
	p, ok := r[name]
	return p, ok
}
