package livechat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// YouTubeAPI YouTube Live Streaming API 弹幕轮询。
//
// 流程（developers.google.com/youtube/v3/live）：
//  1. Channel（视频 URL 或 11 位 video id）→ GET videos?part=liveStreamingDetails
//     取 liveStreamingDetails.activeLiveChatId（直播未开始时为空 → 退避重试）；
//  2. 循环 GET liveChat/messages?liveChatId=..&part=snippet,authorDetails，
//     用响应里的 nextPageToken 增量翻页、按 pollingIntervalMillis 控制节奏
//     （官方明确要求遵守该间隔，否则可能被限流）。
type YouTubeAPI struct {
	Cfg SourceConfig

	// 测试注入点。零值 = 官方地址。
	APIBase string // 默认 https://www.googleapis.com/youtube/v3
	HTTP    *http.Client

	// MinInterval 轮询下限（服务端给多快都至少等这么久），0 = 2s。
	MinInterval time.Duration
}

func (y *YouTubeAPI) Name() string { return KindYouTubeAPI }

var ytVideoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// ParseVideoID 从 URL 或裸 id 里解析 YouTube video id。
// 支持：https://www.youtube.com/watch?v=xxx、https://youtu.be/xxx、
// https://www.youtube.com/live/xxx、裸 11 位 id。
func ParseVideoID(s string) string {
	s = strings.TrimSpace(s)
	if ytVideoIDRe.MatchString(s) {
		return s
	}
	if u, err := url.Parse(s); err == nil {
		if v := u.Query().Get("v"); ytVideoIDRe.MatchString(v) {
			return v
		}
		for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
			if ytVideoIDRe.MatchString(seg) {
				return seg
			}
		}
	}
	return ""
}

// Stream 阻塞拉取直到 ctx 取消。解析 liveChatId 与轮询失败都会退避重试
// （直播是临时态：开播/结束/断线都很正常，不能让一条直播结束杀掉整路监控）。
func (y *YouTubeAPI) Stream(ctx context.Context, on func(Message)) error {
	if strings.TrimSpace(y.Cfg.YouTubeAPIKey) == "" && strings.TrimSpace(y.Cfg.YouTubeAccessToken) == "" {
		return fmt.Errorf("youtube_api: 未配置 API Key 或 Google 登录 token")
	}
	videoID := ParseVideoID(y.Cfg.Channel)
	if videoID == "" {
		return fmt.Errorf("youtube_api: 无法从 %q 解析 video id（支持 watch?v= / youtu.be / /live/ / 裸 id）", y.Cfg.Channel)
	}

	backoff := 3 * time.Second
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		chatID, err := y.liveChatID(ctx, videoID)
		if err != nil || chatID == "" {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				log.Printf("[livechat/youtube] %s 解析 liveChatId 失败: %v", videoID, err)
			}
			if !y.sleep(ctx, backoff) {
				return ctx.Err()
			}
			if backoff < 60*time.Second {
				backoff *= 2
			}
			continue
		}
		err = y.poll(ctx, videoID, chatID, on)
		if err == nil || ctx.Err() != nil {
			return err
		}
		log.Printf("[livechat/youtube] %s 轮询中断: %v，%v 后重连", videoID, err, backoff)
		if !y.sleep(ctx, backoff) {
			return ctx.Err()
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
	}
}

// poll 单轮循环：翻页拉增量弹幕，直到出错或 ctx 结束。
func (y *YouTubeAPI) poll(ctx context.Context, videoID, chatID string, on func(Message)) error {
	page := ""
	for {
		if ctx.Err() != nil {
			return nil
		}
		var res struct {
			NextPageToken         string            `json:"nextPageToken"`
			PollingIntervalMillis int               `json:"pollingIntervalMillis"`
			Items                 []json.RawMessage `json:"items"`
		}
		q := url.Values{}
		q.Set("liveChatId", chatID)
		q.Set("part", "snippet,authorDetails")
		q.Set("maxResults", "200")
		if page != "" {
			q.Set("pageToken", page)
		}
		if err := y.get(ctx, "/liveChat/messages", q, &res); err != nil {
			return err
		}
		for _, raw := range res.Items {
			if m, ok := parseYouTubeItem(raw); ok {
				m.Source = KindYouTubeAPI
				m.Channel = videoID
				on(m)
			}
		}
		page = res.NextPageToken
		interval := time.Duration(res.PollingIntervalMillis) * time.Millisecond
		if min := y.minInterval(); interval < min {
			interval = min
		}
		if !y.sleep(ctx, interval) {
			return nil
		}
	}
}

// parseYouTubeItem liveChatMessage 资源 → Message。
// displayMessage 覆盖 text/superChat/superSticker/sponsor 等，只缺 memberMilestone
// 的极少数老事件类型才回退 snippet.textMessageDetails.messageText。
func parseYouTubeItem(raw json.RawMessage) (Message, bool) {
	var it struct {
		ID      string `json:"id"`
		Snippet struct {
			Type               string `json:"type"`
			PublishedAt        string `json:"publishedAt"`
			DisplayMessage     string `json:"displayMessage"`
			TextMessageDetails struct {
				MessageText string `json:"messageText"`
			} `json:"textMessageDetails"`
			SuperChatDetails struct {
				AmountDisplayValue string `json:"amountDisplayValue"`
				Currency           string `json:"currency"`
			} `json:"superChatDetails"`
			SuperStickerDetails struct {
				AmountDisplayValue string `json:"amountDisplayValue"`
				Currency           string `json:"currency"`
			} `json:"superStickerDetails"`
		} `json:"snippet"`
		AuthorDetails struct {
			DisplayName string `json:"displayName"`
		} `json:"authorDetails"`
	}
	if err := json.Unmarshal(raw, &it); err != nil {
		return Message{}, false
	}
	m := Message{ID: it.ID, Text: it.Snippet.DisplayMessage, Kind: "text"}
	if m.Text == "" {
		m.Text = it.Snippet.TextMessageDetails.MessageText
	}
	m.User = it.AuthorDetails.DisplayName
	if m.User == "" {
		m.User = "anonymous"
	}
	if t, err := time.Parse(time.RFC3339, it.Snippet.PublishedAt); err == nil {
		m.At = t
	} else {
		m.At = time.Now()
	}
	switch it.Snippet.Type {
	case "superChatEvent":
		m.Kind = "superchat"
		m.Amount = it.Snippet.SuperChatDetails.AmountDisplayValue + " " + it.Snippet.SuperChatDetails.Currency
	case "superStickerEvent":
		m.Kind = "superchat"
		m.Amount = it.Snippet.SuperStickerDetails.AmountDisplayValue + " " + it.Snippet.SuperStickerDetails.Currency
	case "newSponsorEvent":
		m.Kind = "sub"
	case "memberMilestoneChatEvent":
		m.Kind = "sub"
	}
	if strings.TrimSpace(m.Text) == "" && m.Kind == "text" {
		return Message{}, false
	}
	return m, true
}

// liveChatId 视频 → activeLiveChatId（未开播 / 直播已结束 / 没开聊天都返回空）。
func (y *YouTubeAPI) liveChatID(ctx context.Context, videoID string) (string, error) {
	var res struct {
		Items []struct {
			LiveStreamingDetails struct {
				ActiveLiveChatID string `json:"activeLiveChatId"`
			} `json:"liveStreamingDetails"`
		} `json:"items"`
	}
	q := url.Values{}
	q.Set("part", "liveStreamingDetails")
	q.Set("id", videoID)
	if err := y.get(ctx, "/videos", q, &res); err != nil {
		return "", err
	}
	if len(res.Items) == 0 {
		return "", fmt.Errorf("video %s 不存在或无权限", videoID)
	}
	return res.Items[0].LiveStreamingDetails.ActiveLiveChatID, nil
}

// ---- HTTP 小工具 ----

func (y *YouTubeAPI) apiBase() string {
	if y.APIBase != "" {
		return y.APIBase
	}
	if b := strings.TrimRight(strings.TrimSpace(y.Cfg.BaseURL), "/"); b != "" {
		return b
	}
	return "https://www.googleapis.com/youtube/v3"
}

func (y *YouTubeAPI) minInterval() time.Duration {
	if y.MinInterval > 0 {
		return y.MinInterval
	}
	return 2 * time.Second
}

func (y *YouTubeAPI) client() *http.Client {
	if y.HTTP != nil {
		return y.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (y *YouTubeAPI) get(ctx context.Context, path string, q url.Values, out any) error {
	if q == nil {
		q = url.Values{}
	}
	if tok := strings.TrimSpace(y.Cfg.YouTubeAccessToken); tok == "" {
		if key := strings.TrimSpace(y.Cfg.YouTubeAPIKey); key != "" {
			q.Set("key", key)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, y.apiBase()+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if tok := strings.TrimSpace(y.Cfg.YouTubeAccessToken); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := y.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("youtube API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (y *YouTubeAPI) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
