// Package twitcheventsub 实现 Twitch EventSub channel.chat.message 订阅 Provider。
// 需要 user access token（scope: user:read:chat）+ client_id。
// 流程：连接 wss://eventsub-ws.twitch.tv/ws → 拿 session_id →
// POST /helix/eventsub/subscriptions 创建订阅 → 收 notification 消息。
package twitcheventsub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"livechat/pkg/provider"
)

const (
	wsURL     = "wss://eventsub-wss.twitch.tv/ws"
	apiBase   = "https://api.twitch.tv/helix"
	subscribe = apiBase + "/eventsub/subscriptions"
)

type Options struct {
	ClientID      string
	AccessToken   string // user access token，scope 需含 user:read:chat
	BroadcasterID string // 主播 user id（从 users 接口解析或直接提供）
	UserID        string // 读弹幕的用户 id（一般与 token 所属用户一致）
}

type Provider struct {
	opts Options
	mu   sync.Mutex
}

func New(opts Options) *Provider { return &Provider{opts: opts} }

func (p *Provider) Name() string { return "twitch_eventsub" }

// usersLookup 通过 login 名解析 user id。
func (p *Provider) usersLookup(ctx context.Context, login string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		apiBase+"/users?login="+login, nil)
	req.Header.Set("Client-Id", p.opts.ClientID)
	req.Header.Set("Authorization", "Bearer "+p.opts.AccessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("helix users: status %d body %.300s", resp.StatusCode, string(body))
	}
	var out struct {
		Data []struct {
			ID    string `json:"id"`
			Login string `json:"login"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if len(out.Data) == 0 {
		return "", fmt.Errorf("user %q not found", login)
	}
	return out.Data[0].ID, nil
}

type subRequest struct {
	Type      string      `json:"type"`
	Version   string      `json:"version"`
	Condition interface{} `json:"condition"`
	Transport struct {
		Method    string `json:"method"`
		SessionID string `json:"session_id"`
	} `json:"transport"`
}

type wsMessage struct {
	Metadata struct {
		MessageType  string `json:"message_type"` // session_welcome | notification | ...
		MessageID    string `json:"message_id"`
		MessageRetry int    `json:"message_retry"`
	} `json:"metadata"`
	Payload struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Subscription struct {
			ID string `json:"id"`
		} `json:"subscription"`
		Event struct {
			ChatterUserID   string `json:"chatter_user_id"`
			ChatterUserName string `json:"chatter_user_name"`
			Message         struct {
				Text string `json:"text"`
			} `json:"message"`
		} `json:"event"`
	} `json:"payload"`
}

func (p *Provider) Stream(ctx context.Context, channel string) (<-chan provider.ChatMessage, <-chan error, error) {
	if p.opts.ClientID == "" || p.opts.AccessToken == "" {
		return nil, nil, fmt.Errorf("twitch_eventsub 需要 client_id 与 user access token (scope user:read:chat)")
	}
	broadcaster := p.opts.BroadcasterID
	if broadcaster == "" {
		id, err := p.usersLookup(ctx, strings.ToLower(channel))
		if err != nil {
			return nil, nil, fmt.Errorf("resolve broadcaster id: %w", err)
		}
		broadcaster = id
	}
	user := p.opts.UserID
	if user == "" {
		user = broadcaster
	}

	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("dial eventsub ws: %w", err)
	}
	c.SetReadLimit(1 << 20)

	msgs := make(chan provider.ChatMessage)
	errs := make(chan error, 1)

	go func() {
		defer c.Close(websocket.StatusNormalClosure, "done")
		defer close(msgs)

		// 1) 等 session_welcome
		var welcome wsMessage
		if err := readJSON(ctx, c, &welcome); err != nil {
			errs <- err
			return
		}
		sessionID := welcome.Payload.Session.ID
		if sessionID == "" {
			errs <- fmt.Errorf("no session id in welcome frame")
			return
		}

		// 2) 创建订阅
		var body subRequest
		body.Type, body.Version = "channel.chat.message", "1"
		body.Condition = map[string]string{"broadcaster_user_id": broadcaster, "to_user_id": user}
		body.Transport.Method, body.Transport.SessionID = "websocket", sessionID
		b, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, subscribe, bytes.NewReader(b))
		req.Header.Set("Client-Id", p.opts.ClientID)
		req.Header.Set("Authorization", "Bearer "+p.opts.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			errs <- err
			return
		}
		rbody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
			errs <- fmt.Errorf("eventsub subscribe: status %d body %.300s", resp.StatusCode, string(rbody))
			return
		}

		// 3) 循环读通知
		for {
			var m wsMessage
			if err := readJSON(ctx, c, &m); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
			switch m.Metadata.MessageType {
			case "session_keepalive":
				continue
			case "notification":
				ev := m.Payload.Event
				select {
				case msgs <- provider.ChatMessage{
					Platform: "twitch",
					Author:   ev.ChatterUserName,
					Text:     ev.Message.Text,
					TS:       time.Now(),
				}:
				case <-ctx.Done():
					return
				}
			case "session_reconnect":
				// 服务端要求重连：此处简单结束，调用方可重建 Stream
				errs <- fmt.Errorf("server requested reconnect")
				return
			case "revocation":
				errs <- fmt.Errorf("subscription revoked")
				return
			}
		}
	}()
	return msgs, errs, nil
}

func readJSON(ctx context.Context, c *websocket.Conn, v any) error {
	ctx2, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx2)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
