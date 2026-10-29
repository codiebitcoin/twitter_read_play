package twitter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"twitter_read_play/internal/model"
)

// Weibo 微博（m.weibo.cn container 接口，需登录 Cookie 防 432）。
type Weibo struct {
	BaseURL string
	Cookie  string
}

func (w *Weibo) Name() string { return "weibo" }

type wbRes struct {
	Data struct {
		Cards []struct {
			MBlog struct {
				ID        string `json:"id"`
				BID       string `json:"bid"`
				CreatedAt string `json:"created_at"`
				Text      string `json:"text"`
				LongText  struct {
					Text string `json:"text"`
				} `json:"longText"`
				User struct {
					ScreenName string `json:"screen_name"`
				} `json:"user"`
			} `json:"mblog"`
		} `json:"cards"`
	} `json:"data"`
	OK int `json:"ok"`
}

func (w *Weibo) FetchLatest(ctx context.Context, uid string, count int) ([]model.Tweet, error) {
	if count <= 0 || count > 50 {
		count = 20
	}
	url := fmt.Sprintf("%s/api/container/getIndex?type=uid&value=%s&containerid=107603%s", strings.TrimSuffix(w.BaseURL, "/"), uid, uid)
	hc := *sharedClient
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15")
	req.Header.Set("Referer", strings.TrimSuffix(w.BaseURL, "/")+"/u/"+uid)
	if w.Cookie != "" {
		req.Header.Set("Cookie", w.Cookie)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == 432 {
		return nil, fmt.Errorf("weibo: 被反爬拦截(%d)，请在上游配置登录 Cookie", resp.StatusCode)
	}
	var res wbRes
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	if res.OK != 1 {
		return nil, fmt.Errorf("weibo: 接口返回异常 ok=%d", res.OK)
	}
	out := make([]model.Tweet, 0, len(res.Data.Cards))
	for _, card := range res.Data.Cards {
		m := card.MBlog
		if m.ID == "" {
			continue
		}
		text := m.LongText.Text
		if text == "" {
			text = m.Text
		}
		out = append(out, model.Tweet{
			ID:        m.BID,
			User:      strings.ToLower(m.User.ScreenName),
			Text:      model.CleanText(text, 300),
			CreatedAt: m.CreatedAt,
		})
	}
	return out, nil
}

var _ = strconv.Itoa
var _ = time.Now
