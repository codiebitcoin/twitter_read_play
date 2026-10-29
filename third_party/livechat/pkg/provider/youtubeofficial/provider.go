// Package youtubeofficial 实现 YouTube Data API v3 liveChatMessages.list 轮询 Provider。
// 需 OAuth access token（scope: youtube.force-ssl / youtube.readonly）。
// 流程：videos.list 取 activeLiveChatId → 轮询 liveChatMessages.list，
// 按 nextPageToken 增量拉取，遵守响应中的 pollingIntervalMillis。
// 注意：每次调用消耗 5 配额单位（默认配额 10000/天）。
package youtubeofficial

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"livechat/pkg/provider"
)

const apiBase = "https://www.googleapis.com/youtube/v3"

type Options struct {
	// AccessToken OAuth2 token（scope: youtube.force-ssl 或 youtube.readonly）
	AccessToken string
	// VideoID 可选；不传则从 target URL 解析
	HTTPClient *http.Client
}

type Provider struct{ opts Options }

func New(opts Options) *Provider { return &Provider{opts: opts} }

func (p *Provider) Name() string { return "youtube_official" }

func (p *Provider) client() *http.Client {
	if p.opts.HTTPClient != nil {
		return p.opts.HTTPClient
	}
	return http.DefaultClient
}

// resolveVideoID 从 URL 或裸 ID 中解析视频 ID。
func resolveVideoID(target string) string {
	if u, err := url.Parse(target); err == nil && u.Query().Get("v") != "" {
		return u.Query().Get("v")
	}
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		// youtu.be/<id> 形式
		return u.Path[1:]
	}
	return target
}

// resolveLiveChatID 通过 videos.list 获取 activeLiveChatId。
func (p *Provider) resolveLiveChatID(ctx context.Context, videoID string) (string, error) {
	endpoint := fmt.Sprintf("%s/videos?part=liveStreamingDetails&id=%s", apiBase, videoID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+p.opts.AccessToken)
	resp, err := p.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("videos.list: status %d body %.300s", resp.StatusCode, string(body))
	}
	var out struct {
		Items []struct {
			LiveStreamingDetails struct {
				ActiveLiveChatID string `json:"activeLiveChatId"`
			} `json:"liveStreamingDetails"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	if len(out.Items) == 0 || out.Items[0].LiveStreamingDetails.ActiveLiveChatID == "" {
		return "", fmt.Errorf("video %s 不是直播或无 activeLiveChatId", videoID)
	}
	return out.Items[0].LiveStreamingDetails.ActiveLiveChatID, nil
}

type liveChatResp struct {
	NextPageToken         string `json:"nextPageToken"`
	PollingIntervalMillis int64  `json:"pollingIntervalMillis"`
	Items                 []struct {
		ID      string `json:"id"`
		Snippet struct {
			Type               string `json:"type"`
			PublishedAt        string `json:"publishedAt"`
			DisplayMessage     string `json:"displayMessage"`
			TextMessageDetails struct {
				MessageText string `json:"messageText"`
			} `json:"textMessageDetails"`
		} `json:"snippet"`
		AuthorDetails struct {
			DisplayName string `json:"displayName"`
		} `json:"authorDetails"`
	} `json:"items"`
}

func (p *Provider) Stream(ctx context.Context, target string) (<-chan provider.ChatMessage, <-chan error, error) {
	videoID := resolveVideoID(target)
	liveChatID, err := p.resolveLiveChatID(ctx, videoID)
	if err != nil {
		return nil, nil, err
	}

	msgs := make(chan provider.ChatMessage)
	errs := make(chan error, 1)
	pageToken := ""
	go func() {
		defer close(msgs)
		for {
			q := url.Values{}
			q.Set("liveChatId", liveChatID)
			q.Set("part", "snippet,authorDetails")
			q.Set("maxResults", "200")
			if pageToken != "" {
				q.Set("pageToken", pageToken)
			}
			endpoint := apiBase + "/liveChat/messages?" + q.Encode()
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
			req.Header.Set("Authorization", "Bearer "+p.opts.AccessToken)
			resp, err := p.client().Do(req)
			if err != nil {
				select {
				case errs <- err:
				default:
				}
				select {
				case <-time.After(2 * time.Second):
				case <-ctx.Done():
					return
				}
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				select {
				case errs <- fmt.Errorf("liveChatMessages.list: status %d body %.300s", resp.StatusCode, string(body)):
				default:
				}
				select {
				case <-time.After(5 * time.Second):
				case <-ctx.Done():
					return
				}
				continue
			}
			var r liveChatResp
			if err := json.Unmarshal(body, &r); err != nil {
				select {
				case errs <- err:
				default:
				}
				continue
			}
			for _, it := range r.Items {
				if it.Snippet.Type != "textMessageEvent" {
					continue
				}
				ts, _ := time.Parse(time.RFC3339, it.Snippet.PublishedAt)
				select {
				case msgs <- provider.ChatMessage{
					Platform: "youtube",
					Author:   it.AuthorDetails.DisplayName,
					Text:     it.Snippet.TextMessageDetails.MessageText,
					TS:       ts,
				}:
				case <-ctx.Done():
					return
				}
			}
			pageToken = r.NextPageToken
			wait := time.Duration(r.PollingIntervalMillis) * time.Millisecond
			if wait <= 0 {
				wait = 5 * time.Second
			}
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
		}
	}()
	return msgs, errs, nil
}
