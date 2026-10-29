// Package twitter 推文获取抽象与 socialdata.tools 实现。
package twitter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/model"
)

// Provider 拉取某用户最新推文。
type Provider interface {
	FetchLatest(ctx context.Context, screenName string, count int) ([]model.Tweet, error)
	Name() string
}

// QuotaError 上游账号级故障（欠费 402 / Key 无效 401 / 403）。
// 这类错误重试不会自愈，调用方应把角色标记为 dead，等用户充值/换 Key 后手动恢复。
// 各适配器共用（socialdata.tools / social_read …），Source 只是错误文案里的服务名。
type QuotaError struct {
	Code int
	Body string
	// Source 产生该错误的服务名；空 = socialdata.tools（保持旧文案不变）。
	Source string
}

func (e *QuotaError) Error() string {
	name := e.Source
	if name == "" {
		name = "socialdata.tools"
	}
	switch e.Code {
	case http.StatusPaymentRequired:
		return name + ": 欠费（HTTP 402），请到服务方后台充值"
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Sprintf("%s: API Key 无效（HTTP %d）", name, e.Code)
	}
	return fmt.Sprintf("%s: HTTP %d %s", name, e.Code, e.Body)
}

// IsQuotaError 判断 err 是否为账号级（欠费/Key 无效）错误。
func IsQuotaError(err error) bool {
	var qe *QuotaError
	return errors.As(err, &qe)
}

func httpErr(code int, body string) error {
	return httpErrSource("socialdata.tools", code, body)
}

// httpErrSource 同 httpErr，但错误文案带来源服务名（social_read 等复用 QuotaError 的 markDead 语义）。
func httpErrSource(source string, code int, body string) error {
	if code == http.StatusPaymentRequired || code == http.StatusUnauthorized || code == http.StatusForbidden {
		return &QuotaError{Code: code, Body: body, Source: source}
	}
	return fmt.Errorf("%s: HTTP %d %s", source, code, body)
}

// Package-level screen_name → user_id 缓存：按 key|base 隔离。
// Provider 实例可能被反复重建（upstream.ProviderFor 每次新建），
// 缓存放在包级才能跨实例命中，省掉重复的 /twitter/user/{name} 计费调用。
var (
	uidMu    sync.Mutex
	uidCache = map[string]string{} // key: apiKey|baseURL|screen
)

// Socialdata socialdata.tools 按量计费 API（$0.2/1k 条）。
// 接口：GET /twitter/user/{screen_name}（解析 user_id）→ GET /twitter/user/{user_id}/tweets。
// 监控：POST /monitors/user-tweets（webhook 反向通知）。
type Socialdata struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client

	mu sync.Mutex
}

func (s *Socialdata) Name() string { return "socialdata.tools" }

func (s *Socialdata) client() *http.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.HTTP == nil {
		s.HTTP = &http.Client{Timeout: 20 * time.Second}
	}
	return s.HTTP
}

func (s *Socialdata) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.key())
	req.Header.Set("Accept", "application/json")
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpErr(resp.StatusCode, "")
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// userID 解析 screen_name → 数字 user_id（带缓存）。
func (s *Socialdata) userID(ctx context.Context, screenName string) (string, error) {
	screenName = strings.ToLower(strings.TrimPrefix(screenName, "@"))
	ck := s.key() + "|" + s.base() + "|" + screenName
	uidMu.Lock()
	if id, ok := uidCache[ck]; ok {
		uidMu.Unlock()
		return id, nil
	}
	uidMu.Unlock()

	var user struct {
		IDStr string `json:"id_str"`
	}
	if err := s.get(ctx, "/twitter/user/"+screenName, &user); err != nil {
		return "", fmt.Errorf("解析用户 %s 失败: %w", screenName, err)
	}
	if user.IDStr == "" {
		return "", fmt.Errorf("解析用户 %s 失败: 空 id", screenName)
	}
	uidMu.Lock()
	uidCache[ck] = user.IDStr
	uidMu.Unlock()
	return user.IDStr, nil
}

type sdTweet struct {
	IDStr          string `json:"id_str"`
	FullText       string `json:"full_text"`
	Text           string `json:"text"`
	TweetCreatedAt string `json:"tweet_created_at"`
	CreatedAt      string `json:"created_at"`
	User           struct {
		ScreenName string `json:"screen_name"`
	} `json:"user"`
	QuotedStatus *sdTweet `json:"quoted_status"`
	// 纯转发的目标推文（RT 的 full_text 是 "RT @x: …" 且常被截断，内容以这里为准）
	RetweetedStatus *sdTweet `json:"retweeted_status"`
	// 点赞数：socialdata 返回 Twitter API 形状的字段；没有就是 0（random 模式「点赞≥N」用）
	FavoriteCount int `json:"favorite_count"`
	Entities      *struct {
		URLs []struct {
			URL         string `json:"url"`
			ExpandedURL string `json:"expanded_url"`
		} `json:"urls"`
		Media []struct {
			URL         string `json:"url"`
			ExpandedURL string `json:"expanded_url"`
			Type        string `json:"type"`
		} `json:"media"`
	} `json:"entities"`
}

// ExpandText 把正文里的 t.co 短链还原成真实链接；媒体类 t.co（视频/图片占位）直接去掉。
func (t *sdTweet) ExpandText(text string) string {
	if t.Entities == nil {
		return text
	}
	for _, m := range t.Entities.Media {
		if m.URL != "" {
			text = strings.ReplaceAll(text, m.URL, "")
		}
	}
	for _, u := range t.Entities.URLs {
		if u.URL != "" && u.ExpandedURL != "" {
			text = strings.ReplaceAll(text, u.URL, u.ExpandedURL)
		}
	}
	return strings.TrimSpace(text)
}

// refText 取引用/转发目标的正文。
func (r *sdTweet) refText() string {
	if r == nil {
		return ""
	}
	if r.FullText != "" {
		return r.FullText
	}
	return r.Text
}

// toTweet 上游推文对象 → 归一化 model.Tweet（t.co 还原、引用/转发递归解析）。
func (t *sdTweet) toTweet() model.Tweet {
	text := t.FullText
	if text == "" {
		text = t.Text
	}
	created := t.TweetCreatedAt
	if created == "" {
		created = t.CreatedAt
	}
	tw := model.Tweet{
		ID:        t.IDStr,
		User:      strings.ToLower(t.User.ScreenName),
		Text:      model.CleanText(t.ExpandText(text), 300),
		CreatedAt: created,
		Likes:     t.FavoriteCount,
		Source:    "real",
	}
	// 媒体类型：纯视频/图片推文的 full_text 只有一条 t.co 占位链接，清洗后为空
	if t.Entities != nil && len(t.Entities.Media) > 0 {
		tw.Media = t.Entities.Media[0].Type
	}
	if q := t.QuotedStatus; q != nil {
		tw.QuotedID = q.IDStr
		tw.QuotedUser = strings.ToLower(q.User.ScreenName)
		tw.QuotedText = model.CleanText(q.ExpandText(q.refText()), 300)
	}
	if rt := t.RetweetedStatus; rt != nil {
		tw.RetweetID = rt.IDStr
		tw.RetweetUser = strings.ToLower(rt.User.ScreenName)
		tw.RetweetText = model.CleanText(rt.ExpandText(rt.refText()), 300)
	}
	return tw
}

// FetchLatest 拉取用户最新推文（新→旧，~20 条/页）。
func (s *Socialdata) FetchLatest(ctx context.Context, screenName string, count int) ([]model.Tweet, error) {
	tweets, _, err := s.FetchPage(ctx, screenName, count, "")
	return tweets, err
}

// FetchPage 拉取用户推文分页（新→旧）。cursor 非空时向后翻页（历史推文）。
// 返回 next_cursor 为空表示没有更早的推文了。
func (s *Socialdata) FetchPage(ctx context.Context, screenName string, count int, cursor string) ([]model.Tweet, string, error) {
	if s.key() == "" {
		return nil, "", fmt.Errorf("socialdata.tools: 未配置 API Key")
	}
	if count <= 0 || count > 100 {
		count = 20
	}
	uid, err := s.userID(ctx, screenName)
	if err != nil {
		return nil, "", err
	}
	path := fmt.Sprintf("/twitter/user/%s/tweets?count=%d", uid, count)
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	var res struct {
		Tweets []sdTweet `json:"tweets"`
		Cursor string    `json:"cursor"`
		Next   string    `json:"next_cursor"`
	}
	if err := s.get(ctx, path, &res); err != nil {
		return nil, "", err
	}
	next := res.Cursor
	if next == "" {
		next = res.Next
	}
	out := make([]model.Tweet, 0, len(res.Tweets))
	for i := range res.Tweets {
		out = append(out, res.Tweets[i].toTweet())
		if len(out) >= count {
			break
		}
	}
	return out, next, nil
}

// SetKey 动态更新 API Key（来自设置页）。
func (s *Socialdata) SetKey(key string) {
	s.mu.Lock()
	s.APIKey = key
	s.mu.Unlock()
}

// SetBaseURL 动态更新 API 地址（来自设置页）：可以指向自建的 socialdata 兼容服务
// （兼容 socialdata 格式的自建服务）。空串表示回到官方 https://api.socialdata.tools。
func (s *Socialdata) SetBaseURL(u string) {
	s.mu.Lock()
	s.BaseURL = strings.TrimSuffix(strings.TrimSpace(u), "/")
	s.mu.Unlock()
}

// BaseURLOf 取当前生效的 API 地址。
func (s *Socialdata) BaseURLOf() string { return s.base() }

// base 取当前 API 地址（默认官方）。
func (s *Socialdata) base() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b := strings.TrimSuffix(s.BaseURL, "/"); b != "" {
		return b
	}
	return "https://api.socialdata.tools"
}

// key 取当前 Key。
func (s *Socialdata) key() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.APIKey
}

// post JSON POST 通用（监控 API 用）。
func (s *Socialdata) post(ctx context.Context, path string, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base()+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.key())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return httpErr(resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// CreateUserTweetsMonitor 创建用户推文监控（webhook 反向通知，替代轮询）。
// webhookURL 为空则使用账号的全局 webhook；返回 monitor id。
func (s *Socialdata) CreateUserTweetsMonitor(ctx context.Context, screenName, webhookURL string) (string, error) {
	if s.key() == "" {
		return "", fmt.Errorf("socialdata.tools: 未配置 API Key")
	}
	body := map[string]any{"user_screen_name": strings.TrimPrefix(screenName, "@")}
	if webhookURL != "" {
		body["webhook_url"] = webhookURL
	}
	var res struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := s.post(ctx, "/monitors/user-tweets", body, &res); err != nil {
		return "", err
	}
	if res.Data.ID == "" {
		return "", fmt.Errorf("socialdata.tools: 创建监控返回空 id")
	}
	return res.Data.ID, nil
}

// DeleteMonitor 删除监控。
func (s *Socialdata) DeleteMonitor(ctx context.Context, monitorID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.base()+"/monitors/"+monitorID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.key())
	resp, err := s.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return httpErr(resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// CreateSearchMonitor 创建搜索查询监控（打包多个 from:x，共享一条反向通知通道）。
// refreshFrequency 秒（1-3600），是 search monitor 的主要成本驱动。
func (s *Socialdata) CreateSearchMonitor(ctx context.Context, query string, refreshFrequency int, webhookURL string) (string, error) {
	if s.key() == "" {
		return "", fmt.Errorf("socialdata.tools: 未配置 API Key")
	}
	if refreshFrequency <= 0 || refreshFrequency > 3600 {
		refreshFrequency = 300
	}
	body := map[string]any{"query": query, "refresh_frequency": refreshFrequency}
	if webhookURL != "" {
		body["webhook_url"] = webhookURL
	}
	var res struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := s.post(ctx, "/monitors/search-query", body, &res); err != nil {
		return "", err
	}
	if res.Data.ID == "" {
		return "", fmt.Errorf("socialdata.tools: 创建搜索监控返回空 id")
	}
	return res.Data.ID, nil
}
