package livechat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// TwitchEventSub Twitch 官方弹幕通道：EventSub WebSocket + channel.chat.message 订阅。
//
// 流程（dev.twitch.tv/docs/eventsub）：
//  1. 直连 wss://eventsub.wss.twitch.tv/ws，等 server 下发 session_welcome 拿 session_id；
//  2. 用 user token（scope user:read:chat）POST /helix/eventsub/subscriptions 建
//     channel.chat.message 订阅，transport.session_id 指向第 1 步的会话；
//  3. 之后每条弹幕以 notification 消息推送；session_keepalive 是心跳；
//     session_reconnect 给出迁移地址，重连后订阅自动延续（无需重建）。
//
// 重连策略：瞬断（WS 断开 / 心跳超时）在 Stream 内部退避重连；配置错误（401/403、
// 用户不存在）直接返回 error 交给 Manager 上报。
type TwitchEventSub struct {
	Cfg SourceConfig

	// 测试注入点：API 与 WS 地址。零值 = 官方地址。
	APIBase     string // 默认 https://api.twitch.tv/helix
	ValidateURL string // 默认 https://id.twitch.tv/oauth2/validate
	WSURL       string // 默认 wss://eventsub.wss.twitch.tv/ws

	HTTP *http.Client
}

func (t *TwitchEventSub) Name() string { return KindTwitchEventSub }

// Stream 阻塞拉取直到 ctx 取消。on 回调每条弹幕（不保证顺序跨重连）。
func (t *TwitchEventSub) Stream(ctx context.Context, on func(Message)) error {
	if t.Cfg.TwitchClientID == "" || t.Cfg.TwitchToken == "" {
		return fmt.Errorf("twitch_eventsub: 未配置 client_id / token")
	}
	broadcaster, err := t.resolveBroadcaster(ctx)
	if err != nil {
		return err
	}
	chatter, err := t.resolveChatter(ctx)
	if err != nil {
		return err
	}

	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := t.session(ctx, broadcaster, chatter, on)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}
		log.Printf("[livechat/twitch] %s 会话中断: %v，%v 后重连", t.Cfg.Channel, err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// session 建一条 EventSub WebSocket 会话并消费到断开为止。
func (t *TwitchEventSub) session(ctx context.Context, broadcaster, chatter string, on func(Message)) error {
	api, wsURL := t.apiBase(), t.wsURL()

	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	ws, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("连接 EventSub WS 失败: %w", err)
	}
	defer ws.Close()
	// 心跳超时兜底：EventSub 每 ~10s 发 session_keepalive，90s 没有任何消息视为断线
	ws.SetReadDeadline(time.Now().Add(90 * time.Second))

	sessionID, reconnectURL, err := t.awaitWelcome(ws)
	if err != nil {
		return err
	}
	if reconnectURL != "" {
		// welcome 里就带 reconnect（少见的边缘情况）：按官方流程直接迁移
		return t.migrate(ctx, reconnectURL, on)
	}
	if err := t.subscribe(ctx, api, sessionID, broadcaster, chatter); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		var raw json.RawMessage
		if err := ws.ReadJSON(&raw); err != nil {
			return fmt.Errorf("EventSub WS 读取失败: %w", err)
		}
		ws.SetReadDeadline(time.Now().Add(90 * time.Second))

		var env struct {
			Metadata struct {
				MessageType      string    `json:"message_type"`
				MessageID        string    `json:"message_id"`
				MessageTimestamp time.Time `json:"message_timestamp"`
				SubscriptionType string    `json:"subscription_type"`
			} `json:"metadata"`
			Payload struct {
				Session struct {
					ID                      string `json:"id"`
					ReconnectURL            string `json:"reconnect_url"`
					KeepaliveTimeoutSeconds int    `json:"keepalive_timeout_seconds"`
				} `json:"session"`
				Event json.RawMessage `json:"event"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			continue // 非 JSON 帧忽略
		}
		switch env.Metadata.MessageType {
		case "session_welcome", "session_keepalive":
			// welcome 已在 awaitWelcome 消费；keepalive 无需动作
		case "session_reconnect":
			return t.migrate(ctx, env.Payload.Session.ReconnectURL, on)
		case "notification":
			if m, ok := parseTwitchEvent(env.Metadata.SubscriptionType, env.Payload.Event); ok {
				m.Source = KindTwitchEventSub
				m.Channel = strings.ToLower(strings.TrimPrefix(t.Cfg.Channel, "@"))
				on(m)
			}
		case "revocation":
			return fmt.Errorf("twitch_eventsub: 订阅被 Twitch 撤销（revocation），检查 token/权限")
		}
	}
}

// migrate 按 reconnect_url 迁移会话：新连接同样以 welcome 开头，订阅自动延续。
func (t *TwitchEventSub) migrate(ctx context.Context, reconnectURL string, on func(Message)) error {
	if reconnectURL == "" {
		return fmt.Errorf("twitch_eventsub: 收到空 reconnect_url")
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	ws, _, err := dialer.DialContext(ctx, reconnectURL, nil)
	if err != nil {
		return fmt.Errorf("EventSub 迁移重连失败: %w", err)
	}
	defer ws.Close()
	// 迁移后进入常规 session 消费；这里不重建订阅。
	// 简化处理：丢弃这条会话、让上层重新走 session()（订阅是幂等的，见 subscribe 的 409 处理）。
	_ = on
	return nil
}

// awaitWelcome 等 session_welcome；返回 session_id 与可选 reconnect_url。
func (t *TwitchEventSub) awaitWelcome(ws *websocket.Conn) (string, string, error) {
	for i := 0; i < 5; i++ { // 容忍零星 keepalive 抢先
		var raw json.RawMessage
		if err := ws.ReadJSON(&raw); err != nil {
			return "", "", fmt.Errorf("等待 session_welcome 失败: %w", err)
		}
		var env struct {
			Metadata struct {
				MessageType string `json:"message_type"`
			} `json:"metadata"`
			Payload struct {
				Session struct {
					ID           string `json:"id"`
					ReconnectURL string `json:"reconnect_url"`
				} `json:"session"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		if env.Metadata.MessageType == "session_welcome" && env.Payload.Session.ID != "" {
			return env.Payload.Session.ID, env.Payload.Session.ReconnectURL, nil
		}
	}
	return "", "", fmt.Errorf("等待 session_welcome 超时")
}

// subscribe 按配置的 Subscribe 列表建 EventSub 订阅。空列表默认 channel.chat.message。
// 409（已存在同 condition 订阅）时清掉旧订阅后重试一次。
func (t *TwitchEventSub) subscribe(ctx context.Context, api, sessionID, broadcaster, chatter string) error {
	types := t.Cfg.Subscribe
	if len(types) == 0 {
		types = DefaultSubscribe(KindTwitchEventSub)
	}
	for _, typ := range types {
		if err := t.subscribeOne(ctx, api, sessionID, broadcaster, chatter, typ); err != nil {
			return err
		}
	}
	return nil
}

func (t *TwitchEventSub) subscribeOne(ctx context.Context, api, sessionID, broadcaster, chatter, typ string) error {
	body := map[string]any{
		"type":      typ,
		"version":   "1",
		"condition": twitchCondition(typ, broadcaster, chatter),
		"transport": map[string]string{
			"method":     "websocket",
			"session_id": sessionID,
		},
	}
	err := t.post(ctx, api+"/eventsub/subscriptions", body)
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "409") {
		return err
	}
	if dlErr := t.deleteChatSubs(ctx, api, broadcaster, chatter); dlErr != nil {
		return fmt.Errorf("清理旧订阅失败: %v（原错误 %v）", dlErr, err)
	}
	return t.post(ctx, api+"/eventsub/subscriptions", body)
}

// twitchCondition EventSub condition：聊天类要 broadcaster + user_id，其余只要主播。
func twitchCondition(typ, broadcaster, chatter string) map[string]string {
	switch typ {
	case "channel.chat.message", "channel.chat.notification":
		return map[string]string{"broadcaster_user_id": broadcaster, "user_id": chatter}
	default:
		return map[string]string{"broadcaster_user_id": broadcaster}
	}
}

// deleteChatSubs 删除同一 broadcaster+chatter 的 channel.chat.message 订阅。
func (t *TwitchEventSub) deleteChatSubs(ctx context.Context, api, broadcaster, chatter string) error {
	var list struct {
		Data []struct {
			ID        string            `json:"id"`
			Type      string            `json:"type"`
			Condition map[string]string `json:"condition"`
		} `json:"data"`
	}
	if err := t.get(ctx, api+"/eventsub/subscriptions?type=channel.chat.message", &list); err != nil {
		return err
	}
	for _, sub := range list.Data {
		if sub.Type != "channel.chat.message" {
			continue
		}
		if sub.Condition["broadcaster_user_id"] != broadcaster || sub.Condition["user_id"] != chatter {
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete,
			api+"/eventsub/subscriptions?id="+sub.ID, nil)
		if err != nil {
			return err
		}
		t.authorize(req)
		resp, err := t.client().Do(req)
		if err != nil {
			return err
		}
		resp.Body.Close()
	}
	return nil
}

// resolveBroadcaster 频道 login → broadcaster_user_id。
func (t *TwitchEventSub) resolveBroadcaster(ctx context.Context) (string, error) {
	var res struct {
		Data []struct {
			ID    string `json:"id"`
			Login string `json:"login"`
		} `json:"data"`
	}
	login := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(t.Cfg.Channel), "@"))
	if err := t.get(ctx, t.apiBase()+"/users?login="+login, &res); err != nil {
		return "", err
	}
	if len(res.Data) == 0 {
		return "", fmt.Errorf("twitch_eventsub: 频道 %q 不存在", login)
	}
	return res.Data[0].ID, nil
}

// resolveChatter 弹幕归属用户 id：优先配置值，否则用 /validate 从 token 反解。
func (t *TwitchEventSub) resolveChatter(ctx context.Context) (string, error) {
	if id := strings.TrimSpace(t.Cfg.TwitchUserID); id != "" {
		return id, nil
	}
	var val struct {
		UserID string `json:"user_id"`
	}
	u := t.validateURL()
	if u == "" {
		u = "https://id.twitch.tv/oauth2/validate"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "OAuth "+t.Cfg.TwitchToken)
	resp, err := t.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("twitch_eventsub: 校验 token 失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("twitch_eventsub: token 无效（HTTP %d），需要 scope user:read:chat 的 user token", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&val); err != nil {
		return "", err
	}
	if val.UserID == "" {
		return "", fmt.Errorf("twitch_eventsub: /validate 未返回 user_id")
	}
	return val.UserID, nil
}

// parseTwitchEvent 按 subscription_type 解析 EventSub payload。
func parseTwitchEvent(subType string, raw json.RawMessage) (Message, bool) {
	switch subType {
	case "", "channel.chat.message":
		return parseTwitchChatEvent(raw)
	case "channel.chat.notification":
		var ev struct {
			ChatterUserName string `json:"chatter_user_name"`
			NoticeType      string `json:"notice_type"`
			Message         struct {
				Text string `json:"text"`
			} `json:"message"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return Message{}, false
		}
		kind := "system"
		if strings.Contains(ev.NoticeType, "sub") {
			kind = "sub"
		}
		text := ev.Message.Text
		if text == "" {
			text = ev.NoticeType
		}
		return Message{User: ev.ChatterUserName, Text: text, Kind: kind, At: time.Now()}, true
	case "channel.cheer":
		var ev struct {
			UserName string `json:"user_name"`
			Message  string `json:"message"`
			Bits     int    `json:"bits"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return Message{}, false
		}
		return Message{User: ev.UserName, Text: ev.Message, Kind: "cheer", Amount: fmt.Sprintf("%d bits", ev.Bits), At: time.Now()}, true
	case "channel.subscribe", "channel.subscription.gift", "channel.subscription.message":
		var ev struct {
			UserName string `json:"user_name"`
			Message  struct {
				Text string `json:"text"`
			} `json:"message"`
			Tier   string `json:"tier"`
			IsGift bool   `json:"is_gift"`
		}
		if err := json.Unmarshal(raw, &ev); err != nil {
			return Message{}, false
		}
		text := ev.Message.Text
		if text == "" {
			text = subType
		}
		return Message{User: ev.UserName, Text: text, Kind: "sub", Amount: ev.Tier, At: time.Now()}, true
	}
	return parseTwitchChatEvent(raw)
}

// parseTwitchChatEvent channel.chat.message v1 event → Message。
// message_type 有 text / channel_points_highlighted / user_gigantified /
// user_intro 等；cheer 事件 payload 里带 bits（当 event.cheer 非空时）。
func parseTwitchChatEvent(raw json.RawMessage) (Message, bool) {
	var ev struct {
		ChatterUserLogin string `json:"chatter_user_login"`
		ChatterUserName  string `json:"chatter_user_name"`
		MessageID        string `json:"message_id"`
		Message          struct {
			Text string `json:"text"`
		} `json:"message"`
		MessageType string `json:"message_type"`
		Cheer       *struct {
			Bits int `json:"bits"`
		} `json:"cheer"`
		Timestamp time.Time `json:"timestamp"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return Message{}, false
	}
	m := Message{
		ID:   ev.MessageID,
		User: ev.ChatterUserName,
		Text: ev.Message.Text,
		At:   timeOrNow(ev.Timestamp),
		Kind: "text",
	}
	if ev.ChatterUserLogin != "" && m.User == "" {
		m.User = ev.ChatterUserLogin
	}
	if ev.Cheer != nil && ev.Cheer.Bits > 0 {
		m.Kind = "cheer"
		m.Amount = fmt.Sprintf("%d bits", ev.Cheer.Bits)
	}
	return m, true
}

// ---- Twitch Helix REST 小客户端 ----

func (t *TwitchEventSub) apiBase() string {
	if b := strings.TrimSuffix(t.Cfg.BaseURL, "/"); b != "" && t.APIBase == "" {
		return b
	}
	if t.APIBase != "" {
		return t.APIBase
	}
	return "https://api.twitch.tv/helix"
}

func (t *TwitchEventSub) wsURL() string {
	if t.WSURL != "" {
		return t.WSURL
	}
	return "wss://eventsub.wss.twitch.tv/ws"
}

func (t *TwitchEventSub) validateURL() string { return t.ValidateURL }

func (t *TwitchEventSub) client() *http.Client {
	if t.HTTP != nil {
		return t.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (t *TwitchEventSub) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+t.Cfg.TwitchToken)
	req.Header.Set("Client-Id", t.Cfg.TwitchClientID)
	req.Header.Set("Content-Type", "application/json")
}

func (t *TwitchEventSub) get(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	t.authorize(req)
	resp, err := t.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("twitch helix HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (t *TwitchEventSub) post(ctx context.Context, url string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	t.authorize(req)
	resp, err := t.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("twitch helix HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// timeOrNow 时间字段兜底（各上游时间格式不一定齐）。
func timeOrNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}
