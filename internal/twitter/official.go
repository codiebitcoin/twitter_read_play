package twitter

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/netcfg"
)

// 共享的宽松 HTTP 客户端（部分平台 TLS/反爬严格）。
var sharedClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		Proxy:           netcfg.ProxyFunc,
	},
}

// TwitterOfficial X 官方 API v2（Basic $200/月，Bearer Token）。
type TwitterOfficial struct {
	Bearer  string
	BaseURL string
}

func (t *TwitterOfficial) Name() string { return "twitter_official" }

func (t *TwitterOfficial) FetchLatest(ctx context.Context, screenName string, count int) ([]model.Tweet, error) {
	if t.Bearer == "" {
		return nil, fmt.Errorf("twitter_official: 未配置 Bearer Token")
	}
	if count <= 0 || count > 100 {
		count = 20
	}
	hc := *sharedClient
	hc.Timeout = 20 * time.Second
	// 用户名 -> id
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, t.BaseURL+"/2/users/by/username/"+screenName, nil)
	req.Header.Set("Authorization", "Bearer "+t.Bearer)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var lu struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
		Errors []struct{ Detail string } `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lu); err != nil {
		return nil, err
	}
	if lu.Data.ID == "" {
		return nil, fmt.Errorf("twitter_official: 用户解析失败")
	}
	// 时间线
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/2/users/%s/tweets?max_results=%d&tweet.fields=created_at", t.BaseURL, lu.Data.ID, count), nil)
	req2.Header.Set("Authorization", "Bearer "+t.Bearer)
	resp2, err := hc.Do(req2)
	if err != nil {
		return nil, err
	}
	defer resp2.Body.Close()
	var tl struct {
		Data []struct {
			ID        string `json:"id"`
			Text      string `json:"text"`
			CreatedAt string `json:"created_at"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&tl); err != nil {
		return nil, err
	}
	out := make([]model.Tweet, 0, len(tl.Data))
	for _, t := range tl.Data {
		out = append(out, model.Tweet{ID: t.ID, User: screenName, Text: model.CleanText(t.Text, 300), CreatedAt: t.CreatedAt})
	}
	return out, nil
}
