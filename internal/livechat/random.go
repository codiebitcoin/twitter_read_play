package livechat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// RandomPick 随机选到的一路正在直播。
type RandomPick struct {
	Kind    string `json:"kind"`
	Channel string `json:"channel"`
	Title   string `json:"title"`
}

// PickRandomLive 按 kind 在对应平台公开列表里随机挑一路正在直播的。
// youtube_api / twitch_eventsub 需要本路或全局 OAuth；bilibili_blivedm 走公开推荐接口。
func PickRandomLive(ctx context.Context, kind string, oa OAuthSettings, httpc *http.Client) (RandomPick, error) {
	if httpc == nil {
		httpc = &http.Client{Timeout: 15 * time.Second}
	}
	switch kind {
	case KindYouTubeAPI:
		return pickYouTubeLive(ctx, httpc, oa)
	case KindTwitchEventSub:
		return pickTwitchLive(ctx, httpc, oa)
	case KindBilibili:
		return pickBilibiliLive(ctx, httpc, oa.BilibiliCookie)
	default:
		return RandomPick{}, fmt.Errorf("随机选播只支持 youtube_api / twitch_eventsub / bilibili_blivedm")
	}
}

// 测试可替换这些 URL（默认官方接口）。
var (
	youtubeSearchURL = "https://www.googleapis.com/youtube/v3/search"
	twitchStreamsURL = "https://api.twitch.tv/helix/streams"
	biliRecommendURL = "https://api.live.bilibili.com/xlive/web-interface/v1/second/getUserRecommend?page=1&page_size=30&platform=web"
)

func pickYouTubeLive(ctx context.Context, httpc *http.Client, oa OAuthSettings) (RandomPick, error) {
	tok := strings.TrimSpace(oa.GoogleAccessToken)
	if tok == "" {
		return RandomPick{}, fmt.Errorf("随机 YouTube 需要先「用 Google 登录」")
	}
	q := url.Values{}
	q.Set("part", "snippet")
	q.Set("eventType", "live")
	q.Set("type", "video")
	q.Set("maxResults", "25")
	q.Set("order", "viewCount")
	q.Set("safeSearch", "none")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, youtubeSearchURL+"?"+q.Encode(), nil)
	if err != nil {
		return RandomPick{}, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	var res struct {
		Items []struct {
			ID struct {
				VideoID string `json:"videoId"`
			} `json:"id"`
			Snippet struct {
				Title        string `json:"title"`
				ChannelTitle string `json:"channelTitle"`
			} `json:"snippet"`
		} `json:"items"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := doJSON(httpc, req, &res); err != nil {
		return RandomPick{}, fmt.Errorf("YouTube search: %w", err)
	}
	if res.Error.Message != "" {
		return RandomPick{}, fmt.Errorf("YouTube search: %s", res.Error.Message)
	}
	var cands []RandomPick
	for _, it := range res.Items {
		if it.ID.VideoID == "" {
			continue
		}
		title := strings.TrimSpace(it.Snippet.Title)
		if it.Snippet.ChannelTitle != "" {
			title = it.Snippet.ChannelTitle + " · " + title
		}
		cands = append(cands, RandomPick{Kind: KindYouTubeAPI, Channel: it.ID.VideoID, Title: "随机 YouTube · " + title})
	}
	if len(cands) == 0 {
		return RandomPick{}, fmt.Errorf("YouTube 当前搜不到正在直播的频道")
	}
	return cands[rand.Intn(len(cands))], nil
}

func pickTwitchLive(ctx context.Context, httpc *http.Client, oa OAuthSettings) (RandomPick, error) {
	cid := strings.TrimSpace(oa.TwitchClientID)
	tok := strings.TrimSpace(oa.TwitchAccessToken)
	if cid == "" || tok == "" {
		return RandomPick{}, fmt.Errorf("随机 Twitch 需要先填写 Client ID 并「用 Twitch 登录」")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, twitchStreamsURL+"?first=20", nil)
	if err != nil {
		return RandomPick{}, err
	}
	req.Header.Set("Client-Id", cid)
	req.Header.Set("Authorization", "Bearer "+tok)
	var res struct {
		Data []struct {
			UserLogin string `json:"user_login"`
			UserName  string `json:"user_name"`
			Title     string `json:"title"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := doJSON(httpc, req, &res); err != nil {
		return RandomPick{}, fmt.Errorf("Twitch streams: %w", err)
	}
	if res.Message != "" && len(res.Data) == 0 {
		return RandomPick{}, fmt.Errorf("Twitch streams: %s", res.Message)
	}
	if len(res.Data) == 0 {
		return RandomPick{}, fmt.Errorf("Twitch 当前没有可抽的直播")
	}
	it := res.Data[rand.Intn(len(res.Data))]
	title := strings.TrimSpace(it.UserName)
	if it.Title != "" {
		title += " · " + it.Title
	}
	return RandomPick{Kind: KindTwitchEventSub, Channel: it.UserLogin, Title: "随机 Twitch · " + title}, nil
}

func pickBilibiliLive(ctx context.Context, httpc *http.Client, cookie string) (RandomPick, error) {
	cands, err := fetchBiliRecommend(ctx, httpc, cookie, biliRecommendURL)
	if err != nil || len(cands) == 0 {
		// 旧接口兜底（部分网络/风控下新接口空列表）
		fallback := "https://api.live.bilibili.com/room/v1/room/get_user_recommend?page=1"
		cands2, err2 := fetchBiliRecommend(ctx, httpc, cookie, fallback)
		if err2 == nil && len(cands2) > 0 {
			cands = cands2
			err = nil
		} else if err == nil {
			err = err2
		}
	}
	if err != nil {
		return RandomPick{}, err
	}
	if len(cands) == 0 {
		return RandomPick{}, fmt.Errorf("B 站当前没有可抽的直播")
	}
	return cands[rand.Intn(len(cands))], nil
}

func fetchBiliRecommend(ctx context.Context, httpc *http.Client, cookie, rawURL string) ([]RandomPick, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/129.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://live.bilibili.com/")
	if strings.TrimSpace(cookie) != "" {
		req.Header.Set("Cookie", cookie)
	}
	var res struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := doJSON(httpc, req, &res); err != nil {
		return nil, fmt.Errorf("B 站推荐直播: %w", err)
	}
	if res.Code != 0 {
		return nil, fmt.Errorf("B 站推荐直播: code %d %s", res.Code, res.Message)
	}
	type room struct {
		RoomID int    `json:"roomid"`
		Title  string `json:"title"`
		Uname  string `json:"uname"`
	}
	var rooms []room
	if err := json.Unmarshal(res.Data, &rooms); err != nil {
		var wrap struct {
			List []room `json:"list"`
		}
		if err2 := json.Unmarshal(res.Data, &wrap); err2 != nil {
			return nil, fmt.Errorf("B 站推荐直播: 解析列表失败")
		}
		rooms = wrap.List
	}
	var cands []RandomPick
	for _, it := range rooms {
		if it.RoomID <= 0 {
			continue
		}
		title := strings.TrimSpace(it.Uname)
		if it.Title != "" {
			if title != "" {
				title += " · "
			}
			title += it.Title
		}
		cands = append(cands, RandomPick{
			Kind:    KindBilibili,
			Channel: fmt.Sprintf("https://live.bilibili.com/%d", it.RoomID),
			Title:   "随机 B站 · " + title,
		})
	}
	return cands, nil
}

func doJSON(httpc *http.Client, req *http.Request, out any) error {
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// CreateRandom 随机挑一路、写成 Enabled 配置并启动（立刻开始抓弹幕）。
// 已有标题以「随机」开头的同 kind 配置会被覆盖（避免每次点按钮都堆一条）。
func (m *Manager) CreateRandom(ctx context.Context, kind string) (SourceConfig, error) {
	oa := m.OAuth()
	pick, err := PickRandomLive(ctx, kind, oa, nil)
	if err != nil {
		return SourceConfig{}, err
	}
	cfg := SourceConfig{
		Title:   pick.Title,
		Kind:    pick.Kind,
		Channel: pick.Channel,
		Enabled: true,
	}
	for _, c := range m.Configs() {
		if c.Kind == kind && strings.HasPrefix(c.Title, "随机") {
			cfg.ID = c.ID
			cfg.BilibiliCookie = c.BilibiliCookie
			cfg.Subscribe = c.Subscribe
			break
		}
	}
	return m.SaveConfig(cfg)
}
