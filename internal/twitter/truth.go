package twitter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"twitter_read_play/internal/model"
)

// TruthSocial Truth Social（Mastodon 风格 /api/v1）。国内直连需反代 base_url。
type TruthSocial struct {
	BaseURL string
}

func (t *TruthSocial) Name() string { return "truth" }

type tsAccount struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

func (t *TruthSocial) FetchLatest(ctx context.Context, acct string, count int) ([]model.Tweet, error) {
	if count <= 0 || count > 40 {
		count = 20
	}
	hc := *sharedClient
	base := strings.TrimSuffix(t.BaseURL, "/")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/accounts/lookup?acct="+acct, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("truth: lookup 失败（可能需要反代 base_url）: %w", err)
	}
	defer resp.Body.Close()
	var acc tsAccount
	if err := json.NewDecoder(resp.Body).Decode(&acc); err != nil || acc.ID == "" {
		return nil, fmt.Errorf("truth: 用户解析失败")
	}
	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/api/v1/accounts/%s/statuses?limit=%d", base, acc.ID, count), nil)
	req2.Header.Set("Accept", "application/json")
	resp2, err := hc.Do(req2)
	if err != nil {
		return nil, err
	}
	defer resp2.Body.Close()
	var statuses []struct {
		ID        string `json:"id"`
		Content   string `json:"content"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&statuses); err != nil {
		return nil, err
	}
	out := make([]model.Tweet, 0, len(statuses))
	for _, s := range statuses {
		text := tagRe.ReplaceAllString(s.Content, "")
		out = append(out, model.Tweet{ID: s.ID, User: strings.ToLower(acc.Username), Text: model.CleanText(text, 300), CreatedAt: s.CreatedAt})
	}
	return out, nil
}
