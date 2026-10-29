// Package youtubenoauth 将 internal/youtube（集成自 abhinavxd/youtube-live-chat-downloader）
// 包装为统一 Provider。免认证，依赖 innertube continuation 循环。
package youtubenoauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"livechat/internal/youtube"
	"livechat/pkg/provider"
)

type Options struct {
	// Proxy 可选，形如 http://127.0.0.1:7892；为空则走环境变量。
	Proxy string
	// Cookies 可选，用于绕过 CONSENT/CAPTCHA。
	Cookies []*http.Cookie
}

type Provider struct{ opts Options }

func New(opts Options) *Provider { return &Provider{opts: opts} }

func (p *Provider) Name() string { return "youtube_noauth" }

func (p *Provider) Stream(ctx context.Context, target string) (<-chan provider.ChatMessage, <-chan error, error) {
	if p.opts.Proxy != "" {
		proxyURL, err := url.Parse(p.opts.Proxy)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid proxy url: %w", err)
		}
		youtube.SetClient(&http.Client{
			Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
			Timeout:   120 * time.Second,
		})
	}
	if len(p.opts.Cookies) > 0 {
		youtube.AddCookies(p.opts.Cookies)
	}

	continuation, cfg, err := youtube.ParseInitialData(ctx, target)
	if err != nil {
		return nil, nil, err // ErrStreamNotLive / 网络错误等
	}

	msgs := make(chan provider.ChatMessage)
	errs := make(chan error, 1)
	go func() {
		defer close(msgs)
		for {
			chat, newCont, err := youtube.FetchContinuationChat(ctx, continuation, cfg)
			if errors.Is(err, youtube.ErrLiveStreamOver) {
				errs <- nil // 正常结束
				return
			}
			if errors.Is(err, context.Canceled) {
				errs <- nil
				return
			}
			if err != nil {
				select {
				case errs <- err:
				default:
				}
				time.Sleep(2 * time.Second) // 退避后重试，continuation 不变
				continue
			}
			continuation = newCont
			for _, m := range chat {
				select {
				case msgs <- provider.ChatMessage{
					Platform: "youtube",
					Author:   m.AuthorName,
					Text:     m.Message,
					TS:       m.Timestamp,
				}:
				case <-ctx.Done():
					errs <- nil
					return
				}
			}
		}
	}()
	return msgs, errs, nil
}
