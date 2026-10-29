package livechat

import (
	"context"
	"os"
	"strings"

	"livechat/pkg/provider"
	"livechat/pkg/provider/bilibili"
	"livechat/pkg/provider/youtubenoauth"
)

// libStreamer 把 livechat 库的 channel 式 Provider 适配成本包的回调式 Streamer。
// 后台实现来自 third_party/livechat（集成 abhinavxd/youtube-live-chat-downloader、
// Twitch IRC、blivedm-go）。
type libStreamer struct {
	kind   string
	target string
	start  func(ctx context.Context, target string) (<-chan provider.ChatMessage, <-chan error, error)
}

func (s *libStreamer) Name() string { return s.kind }

func (s *libStreamer) Stream(ctx context.Context, on func(Message)) error {
	msgs, errs, err := s.start(ctx, s.target)
	if err != nil {
		return err
	}
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				return nil
			}
			kind := m.Kind
			if kind == "" {
				kind = "text"
			}
			on(Message{
				ID:        m.Raw,
				Channel:   s.target,
				User:      m.Author,
				Text:      m.Text,
				At:        m.TS,
				Kind:      kind,
				Amount:    m.Amount,
				Source:    s.kind,
				UserLevel: m.UserLevel,
			})
		case e, ok := <-errs:
			if !ok {
				return nil
			}
			return e
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func envProxy() string {
	// NO_HTTP=1 时 netcfg 把 NO_PROXY 设为 * 并清掉 HTTP_PROXY，这里再挡一层，
	// 避免 youtubenoauth 用固定 ProxyURL 绕过 NO_PROXY。
	if os.Getenv("NO_PROXY") == "*" || os.Getenv("no_proxy") == "*" {
		return ""
	}
	if v := os.Getenv("HTTPS_PROXY"); v != "" {
		return v
	}
	return os.Getenv("HTTP_PROXY")
}

func youtubeNoAuthStreamer(cfg SourceConfig) Streamer {
	p := youtubenoauth.New(youtubenoauth.Options{Proxy: envProxy()})
	return &libStreamer{kind: KindYouTubeNoAuth, target: cfg.Channel, start: p.Stream}
}

func biliLibStreamer(cfg SourceConfig) Streamer {
	if strings.TrimSpace(cfg.BaseURL) != "" {
		return &httpLineStreamer{kind: KindBilibili, channel: cfg.Channel, base: cfg.BaseURL}
	}
	p := bilibili.New(bilibili.Options{Cookie: cfg.BilibiliCookie})
	return &libStreamer{kind: KindBilibili, target: cfg.Channel, start: p.Stream}
}
