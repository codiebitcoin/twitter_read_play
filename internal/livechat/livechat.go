// Package livechat 直播弹幕拉取：一路直播 = 一个 SourceConfig，来源 kind 可配置切换。
//
// 四种内置来源：
//   - twitch_eventsub：Twitch 官方 EventSub WebSocket（channel.chat.message 订阅），
//     需要 Twitch App 的 client_id + 一个 user access token（scope user:read:chat）。
//   - youtube_api：YouTube Live Streaming API 的 liveChatMessages.list 轮询，
//     需要 API Key（只读弹幕够用）+ 直播视频 URL / video id。
//   - chat_downloader：本机 python3 + chat_downloader 库（pip install chat-downloader），
//     免认证，同时支持 YouTube / Twitch 的直播与回放 —— 前两种没条件时的兜底。
//   - youtube_noauth：集成 abhinavxd/youtube-live-chat-downloader（innertube continuation，免认证）。
//   - bilibili_blivedm：B 站公开直播弹幕，走社区 SDK github.com/Akegarasu/blivedm-go
//     （WebSocket；B 站没有对第三方开放的官方弹幕 pull API）。Cookie 可选。
//
// 配置持久化在 SQLite blobs（livechat.json），Manager 负责 start/stop、断线重连与
// 最近弹幕的内存环形缓冲（不做磁盘持久化：弹幕是流式数据，重启后从头收即可）。
package livechat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Kind 来源类型（SourceConfig.Kind 取值）。
const (
	KindTwitchEventSub = "twitch_eventsub"
	KindYouTubeAPI     = "youtube_api"
	KindChatDownloader = "chat_downloader"
	KindYouTubeNoAuth  = "youtube_noauth"
	KindBilibili       = "bilibili_blivedm"
	// KindSource 由 Role 的 live_url + 新渠道模型动态构造（内部使用，不出现在 UI 下拉）。
	KindSource = "source"
)

// Kinds 全部可选来源（前端下拉/校验用）。
// twitch_irc 已移除：IRC 6697 被常见代理节点黑洞，Twitch 改走 twitch_browser 页面轮询。
var Kinds = []string{KindTwitchEventSub, KindYouTubeAPI, KindChatDownloader, KindYouTubeNoAuth, KindBilibili}

// ValidKind kind 是否受支持。
func ValidKind(k string) bool {
	if k == KindSource {
		return true
	}
	for _, v := range Kinds {
		if v == k {
			return true
		}
	}
	return false
}

// Message 归一化弹幕（四种来源统一成这一个形状）。
type Message struct {
	Seq     int64     `json:"seq"` // Manager 内单调递增，前端增量拉取用
	ID      string    `json:"id"`  // 来源侧消息 id（去重）
	Channel string    `json:"channel"`
	User    string    `json:"user"`
	Text    string    `json:"text"`
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`             // text / cheer / superchat / sub / system…
	Amount  string    `json:"amount,omitempty"` // 打赏金额（如 "$5.00" / "100 bits"）
	Source  string    `json:"source"`           // 产生这条弹幕的 kind
	// UserLevel 用户等级（如 B 站 UL）。0 = 平台不提供 —— 过滤时 0 一律放行。
	UserLevel int `json:"user_level,omitempty"`
}

// Streamer 一种弹幕来源实现。Stream 阻塞式拉取：每条弹幕回调 on，直到 ctx 取消
// （返回 nil）或发生需要重启的错误（返回 error，由 Manager 决定退避重连）。
// 网络瞬断在实现内部消化；这里返回的 error 通常是配置错误或上游封禁。
type Streamer interface {
	Name() string
	Stream(ctx context.Context, on func(Message)) error
}

// SourceConfig 一路弹幕源配置。不同 kind 只关心自己的字段，其余留空。
type SourceConfig struct {
	ID      string `json:"id"`
	Title   string `json:"title"`   // 备注（页面上展示），可空
	Kind    string `json:"kind"`    // twitch_eventsub | youtube_api | chat_downloader
	Channel string `json:"channel"` // Twitch login / YouTube video / B 站房间号或 live.bilibili.com URL / chat_downloader·youtube_noauth URL
	Enabled bool   `json:"enabled"` // 服务启动时自动开拉

	// twitch_eventsub：App client_id + user access token（scope user:read:chat）。
	TwitchClientID string `json:"twitch_client_id,omitempty"`
	TwitchToken    string `json:"twitch_token,omitempty"`
	TwitchUserID   string `json:"twitch_user_id,omitempty"` // 可空：自动走 /validate 解析 token 的 user_id

	// youtube_api：API Key 或 Google OAuth access token（二选一）。
	YouTubeAPIKey      string `json:"youtube_api_key,omitempty"`
	YouTubeAccessToken string `json:"youtube_access_token,omitempty"`

	// Subscribe Twitch EventSub 订阅类型（可配置）。空 = ["channel.chat.message"]。
	// youtube_api / chat_downloader 忽略此字段。
	Subscribe []string `json:"subscribe,omitempty"`

	// chat_downloader：解释器路径已不接受页面配置（H2 任意命令执行），
	// 两个遗留字段保留只为兼容存量 JSON，保存时一律清空；解释器改由
	// TRP_CHATDL_PYTHON 环境变量指定（默认 python3），extra_args 彻底弃用。
	PythonPath string `json:"python_path,omitempty"` // Deprecated: 不再使用，保存时清空
	ExtraArgs  string `json:"extra_args,omitempty"`  // Deprecated: 不再使用，保存时清空

	// bilibili_blivedm：登录 Cookie（SESSDATA + bili_jct）。必填——游客 getDanmuInfo 会 -352，弹幕为空。
	BilibiliCookie string `json:"bilibili_cookie,omitempty"`

	// BaseURL 本路覆盖的 HTTP 服务根。空则用设置页上该来源的服务地址。
	// youtube_api：Data API 根。chat_downloader / bilibili_blivedm：GET ?url= 拉逐行 JSON，
	// 留空则用设置页上该来源的服务地址。twitch_eventsub：Helix 根。
	BaseURL string `json:"base_url,omitempty"`

	// ---- KindSource：Role live_url + 新渠道模型动态构造 ----
	RoleID         string `json:"role_id,omitempty"`
	ChannelID      string `json:"channel_id,omitempty"`
	TargetPlatform string `json:"target_platform,omitempty"`
	TargetKind     string `json:"target_kind,omitempty"`
	TargetID       string `json:"target_id,omitempty"`
}

// stripDeprecatedExecFields 清空废弃的 python_path/extra_args（H2）。
// 保存与启动前都会调用：解释器只认 TRP_CHATDL_PYTHON，页面传值一律丢弃。
func (c *SourceConfig) stripDeprecatedExecFields() {
	c.PythonPath, c.ExtraArgs = "", ""
}

// Validate 基础校验：返回用户可读的错误文案（中文，直接给前端）。
func (c SourceConfig) Validate() error {
	if !ValidKind(c.Kind) {
		return fmt.Errorf("不支持的来源类型 %q（可选：%s）", c.Kind, strings.Join(Kinds, " / "))
	}
	if strings.TrimSpace(c.Channel) == "" {
		return fmt.Errorf("channel 不能为空")
	}
	// H2：解释器路径/透传参数不再接受外部配置（可直接拼出 /bin/sh 执行任意命令）。
	if strings.TrimSpace(c.PythonPath) != "" || strings.TrimSpace(c.ExtraArgs) != "" {
		return fmt.Errorf("python_path/extra_args 已废弃：解释器请用 TRP_CHATDL_PYTHON 环境变量配置")
	}
	if c.Kind == KindSource {
		if strings.TrimSpace(c.RoleID) == "" {
			return fmt.Errorf("source 配置缺少 role_id")
		}
		if strings.TrimSpace(c.ChannelID) == "" {
			return fmt.Errorf("source 配置缺少 channel_id")
		}
		return nil
	}
	switch c.Kind {
	case KindTwitchEventSub:
		if strings.TrimSpace(c.TwitchClientID) == "" {
			return fmt.Errorf("twitch_eventsub 需要 TwitchClientID")
		}
		if strings.TrimSpace(c.TwitchToken) == "" {
			return fmt.Errorf("twitch_eventsub 需要 TwitchToken（user access token，scope user:read:chat）")
		}
	case KindYouTubeAPI:
		if strings.TrimSpace(c.YouTubeAPIKey) == "" && strings.TrimSpace(c.YouTubeAccessToken) == "" {
			return fmt.Errorf("youtube_api 需要 YouTubeAPIKey 或先用 Google 登录拿到 token")
		}
	case KindBilibili:
		if ParseBiliRoomID(c.Channel) == 0 {
			return fmt.Errorf("bilibili_blivedm 需要房间号或 https://live.bilibili.com/{房间号}")
		}
		if strings.TrimSpace(c.BilibiliCookie) != "" {
			if err := biliCookieOK(c.BilibiliCookie); err != nil {
				return err
			}
		}
	}
	if c.Kind == KindTwitchEventSub {
		for _, sub := range c.Subscribe {
			if !ValidTwitchSubscribe(sub) {
				return fmt.Errorf("不支持的 EventSub 订阅类型 %q（可选：%s）", sub, strings.Join(TwitchSubscribeKinds, " / "))
			}
		}
	}
	return nil
}

// TwitchSubscribeKinds 页面可勾选的 EventSub 类型。
var TwitchSubscribeKinds = []string{
	"channel.chat.message",
	"channel.chat.notification",
	"channel.cheer",
	"channel.subscribe",
	"channel.subscription.gift",
	"channel.subscription.message",
}

// ValidTwitchSubscribe 是否为支持的 EventSub 类型。
func ValidTwitchSubscribe(s string) bool {
	for _, v := range TwitchSubscribeKinds {
		if v == s {
			return true
		}
	}
	return false
}

// DefaultSubscribe 未勾选时的默认订阅。
func DefaultSubscribe(kind string) []string {
	if kind == KindTwitchEventSub {
		return []string{"channel.chat.message"}
	}
	return nil
}

// newID 生成配置 id（lc_ + 8 字节随机 hex）。
func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("lc_%d", time.Now().UnixNano())
	}
	return "lc_" + hex.EncodeToString(b)
}
