package twitter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/model"
)

// DefaultSocialReadBaseURL 统一社媒读取服务的默认根地址。留空表示必须在设置里填写。
const DefaultSocialReadBaseURL = ""

const (
	// socialReadTimeout 默认超时：小红书要现起浏览器、微博要走反爬，比 socialdata 慢得多。
	socialReadTimeout = 120 * time.Second
	// srUIDFailTTL 资料接口（handle→uid）失败后的负缓存时长：uid 只是调试信息，失败不该反复打上游。
	srUIDFailTTL = 10 * time.Minute
)

// handle → uid 小缓存（包级：Provider 每次 ProviderFor 都会新建，实例级缓存永远命不中）。
// key: apiKey|baseURL|platform|handle；id 为空表示「取过但失败了」的负缓存。
var (
	srUIDMu    sync.Mutex
	srUIDCache = map[string]srUIDEntry{}
)

type srUIDEntry struct {
	id  string
	exp time.Time
}

// SocialRead 自建 social_read 适配器：统一格式同时支持微博 / 小红书。
//
// 接口（统一格式，非 socialdata 兼容）：
//
//	GET /{platform}/user/{handle}                      → 统一用户对象（拿 id 缓存，仅日志/调试）
//	GET /{platform}/user/{handle}/posts?count=&cursor=  → {posts:[统一帖子], next_cursor, platform}
//
// 鉴权：Authorization: Bearer {APIKey}。
// 帖子 id 是定宽数字串（微博 16 位 / 小红书 24 位 hex），可直接按字符串比较做水位（gtID）。
type SocialRead struct {
	Platform string // weibo | xiaohongshu | bilibili
	BaseURL  string
	APIKey   string
	HTTP     *http.Client

	mu sync.Mutex
}

// Name 带平台：日志 / 去重 / 缓存键都要能区分微博与小红书（否则两条线会串台）。
func (s *SocialRead) Name() string {
	if p := s.platform(); p != "" {
		return "socialread:" + p
	}
	return "socialread"
}

func (s *SocialRead) platform() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.ToLower(strings.TrimSpace(s.Platform))
}

// client 懒初始化 HTTP 客户端（超时 120s；测试可注入 httptest 的 client）。
func (s *SocialRead) client() *http.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.HTTP == nil {
		s.HTTP = &http.Client{Timeout: socialReadTimeout}
	}
	return s.HTTP
}

func (s *SocialRead) base() string {
	s.mu.Lock()
	b := strings.TrimSuffix(strings.TrimSpace(s.BaseURL), "/")
	s.mu.Unlock()
	if b == "" {
		return DefaultSocialReadBaseURL
	}
	return b
}

func (s *SocialRead) key() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.APIKey
}

// SetKey 动态更新 API Key。
func (s *SocialRead) SetKey(key string) {
	s.mu.Lock()
	s.APIKey = key
	s.mu.Unlock()
}

// SetBaseURL 动态更新服务地址（空串回落到 DefaultSocialReadBaseURL）。
func (s *SocialRead) SetBaseURL(u string) {
	s.mu.Lock()
	s.BaseURL = strings.TrimSuffix(strings.TrimSpace(u), "/")
	s.mu.Unlock()
}

// get 发起一次 GET 并解码 JSON。
// 401/402/403 → *QuotaError（复用 markDead 机制）；其它非 2xx → 带响应体片段的 error；
// 网络错误原样返回（调用方按可重试处理）。
func (s *SocialRead) get(ctx context.Context, path string, out any) error {
	base := s.base()
	if base == "" {
		return fmt.Errorf("socialread: base url not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return httpErrSource("social_read", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// srUser social_read 统一用户对象。
type srUser struct {
	Platform       string         `json:"platform"`
	ID             string         `json:"id"`
	Handle         string         `json:"handle"`
	Name           string         `json:"name"`
	Avatar         string         `json:"avatar"`
	Bio            string         `json:"bio"`
	FollowersCount int            `json:"followers_count"`
	FollowingCount int            `json:"following_count"`
	PostsCount     int            `json:"posts_count"`
	Verified       bool           `json:"verified"`
	WebURL         string         `json:"web_url"`
	Extra          map[string]any `json:"extra"`
}

// srPost social_read 统一帖子对象。
type srPost struct {
	Platform string `json:"platform"`
	ID       string `json:"id"`
	User     struct {
		ID     string `json:"id"`
		Handle string `json:"handle"`
		Name   string `json:"name"`
		Avatar string `json:"avatar"`
	} `json:"user"`
	Text string `json:"text"`
	// Images 配图 URL 列表；各平台数量不定。
	Images []string `json:"images"`
	// Video 可能是字符串，也可能是 {url:…} 对象 —— 用 RawMessage 容错。
	Video        json.RawMessage `json:"video"`
	Link         string          `json:"link"`
	LikeCount    int             `json:"like_count"`
	CommentCount int             `json:"comment_count"`
	ShareCount   int             `json:"share_count"`
	CollectCount int             `json:"collect_count"`
	// CreatedAt 统一为 RFC3339；CreatedAtRaw 是平台原始串（微博中文时间等）。
	CreatedAt    string `json:"created_at"`
	CreatedAtRaw string `json:"created_at_raw"`
	CursorHint   string `json:"cursor_hint"`
}

// srPostsRes posts 接口响应。
type srPostsRes struct {
	Posts      []srPost `json:"posts"`
	NextCursor string   `json:"next_cursor"`
	Platform   string   `json:"platform"`
}

// videoURL 从 video 字段（字符串或对象）里取视频地址；取不到返回空。
func (p *srPost) videoURL() string {
	raw := strings.TrimSpace(string(p.Video))
	if raw == "" || raw == "null" {
		return ""
	}
	var str string
	if err := json.Unmarshal(p.Video, &str); err == nil {
		return strings.TrimSpace(str)
	}
	var obj struct {
		URL      string `json:"url"`
		VideoURL string `json:"video_url"`
		Src      string `json:"src"`
	}
	if err := json.Unmarshal(p.Video, &obj); err == nil {
		for _, v := range []string{obj.URL, obj.VideoURL, obj.Src} {
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// socialReadCreatedAt 统一 created_at：RFC3339 直接规范化；纯数字（秒/毫秒）转 RFC3339；
// 其它（微博中文时间等）保留原串 —— 交给 watch.parseTweetTime 的宽口径解析。
func socialReadCreatedAt(p *srPost) string {
	raw := strings.TrimSpace(p.CreatedAt)
	if raw == "" {
		raw = strings.TrimSpace(p.CreatedAtRaw)
	}
	if raw == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Format(time.RFC3339)
	}
	if len(raw) == 10 || len(raw) == 13 {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			// 数字时间戳没有时区信息：统一按 UTC 输出，避免同一份数据在
			// 不同机器时区下生成不同的 created_at（回归测试也因此稳定）。
			if len(raw) == 13 {
				return time.UnixMilli(n).UTC().Format(time.RFC3339)
			}
			return time.Unix(n, 0).UTC().Format(time.RFC3339)
		}
	}
	return raw
}

// proxyImage 把原始配图地址换成本服务的图片代理地址。
//
// 原因：微博图床（*.sinaimg.cn）对浏览器 UA 直接回 403（防盗链），网页里 <img> 原图只会
// 显示碎图；social_read 的 /{platform}/image 由服务端用中性 UA 代取。小红书同理（可能有 referer 校验）。
// 代理接口不需要 key（否则 <img> 带不上），只放行图床白名单域名。
func proxyImage(base, platform, raw string) string {
	if base == "" || platform == "" || raw == "" {
		return raw
	}
	// 已经是本服务的代理地址（或数据被重复处理过）就别套娃
	if strings.Contains(raw, "/image?url=") && strings.Contains(raw, "/"+platform+"/") {
		return raw
	}
	return strings.TrimSuffix(base, "/") + "/" + platform + "/image?url=" + url.QueryEscape(raw)
}

// toTweet 统一帖子 → model.Tweet。base 用于把配图换成本服务的图片代理地址（见 proxyImage）。
func (p *srPost) toTweet(base, platform string) model.Tweet {
	images := make([]string, 0, len(p.Images))
	for _, u := range p.Images {
		if u = strings.TrimSpace(u); u != "" {
			images = append(images, proxyImage(base, platform, u))
		}
	}
	tw := model.Tweet{
		ID:           strings.TrimSpace(p.ID),
		User:         strings.ToLower(strings.TrimSpace(p.User.Handle)),
		Text:         model.CleanText(strings.TrimSpace(p.Text), 300),
		CreatedAt:    socialReadCreatedAt(p),
		Likes:        p.LikeCount,
		Images:       images,
		Link:         strings.TrimSpace(p.Link),
		CommentCount: p.CommentCount,
		ShareCount:   p.ShareCount,
		CollectCount: p.CollectCount,
		Source:       "real",
	}
	// Media 与既有口径一致（photo/video/animated_gif）：纯媒体帖子正文为空，靠它兜底播报。
	switch {
	case p.videoURL() != "":
		tw.Media = "video"
	case len(images) > 0:
		tw.Media = "photo"
	}
	return tw
}

// ParseSocialReadPost 解析 social_read 推送的统一帖子对象（webhook event=new_post 的 data）。
func ParseSocialReadPost(raw []byte) (model.Tweet, error) {
	var p srPost
	if err := json.Unmarshal(raw, &p); err != nil {
		return model.Tweet{}, fmt.Errorf("social_read: 解析帖子失败: %w", err)
	}
	// webhook 推送里带的是原始图片地址，这里不套图片代理（调用方没有上游 base 上下文；
	// 轮询路径见 FetchPage：那里会把配图换成本服务的图片代理地址）。若 webhook 模式要显示
	// 图片，可在此处按 meta.platform 查上游 base 后自行 proxyImage。
	return p.toTweet("", ""), nil
}

// userID 解析 handle → uid（带包级缓存 + 失败负缓存）。
func (s *SocialRead) userID(ctx context.Context, handle string) (string, error) {
	handle = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(handle), "@"))
	if handle == "" {
		return "", fmt.Errorf("social_read: 空用户名")
	}
	ck := s.key() + "|" + s.base() + "|" + s.platform() + "|" + handle
	srUIDMu.Lock()
	if e, ok := srUIDCache[ck]; ok && (e.id != "" || time.Now().Before(e.exp)) {
		srUIDMu.Unlock()
		return e.id, nil
	}
	srUIDMu.Unlock()

	var u srUser
	if err := s.get(ctx, "/"+s.platform()+"/user/"+url.PathEscape(handle), &u); err != nil {
		srUIDMu.Lock()
		srUIDCache[ck] = srUIDEntry{exp: time.Now().Add(srUIDFailTTL)}
		srUIDMu.Unlock()
		return "", fmt.Errorf("social_read: 解析用户 %s 失败: %w", handle, err)
	}
	srUIDMu.Lock()
	srUIDCache[ck] = srUIDEntry{id: u.ID}
	srUIDMu.Unlock()
	return u.ID, nil
}

// warmUser 后台预热 handle→uid 缓存。uid 只用于日志/调试（posts 接口按 handle 取），
// 所以绝不阻塞帖子拉取 —— 微博/小红书的资料接口本身可能很慢。
func (s *SocialRead) warmUser(handle string) {
	handle = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(handle), "@"))
	if handle == "" {
		return
	}
	ck := s.key() + "|" + s.base() + "|" + s.platform() + "|" + handle
	srUIDMu.Lock()
	e, ok := srUIDCache[ck]
	srUIDMu.Unlock()
	if ok && (e.id != "" || time.Now().Before(e.exp)) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_, _ = s.userID(ctx, handle)
	}()
}

// FetchPage 拉取用户帖子分页（新→旧）；cursor 非空时向后翻页（历史）。
// next_cursor 为空表示没有更早的帖子了。
// 签名与 watch.pager 接口逐字一致：FetchPage(ctx, screenName, count, cursor) ([]model.Tweet, string, error)。
func (s *SocialRead) FetchPage(ctx context.Context, screenName string, count int, cursor string) ([]model.Tweet, string, error) {
	platform := s.platform()
	if platform == "" {
		return nil, "", fmt.Errorf("social_read: 未配置平台（weibo/xiaohongshu/bilibili）")
	}
	handle := strings.TrimPrefix(strings.TrimSpace(screenName), "@")
	if handle == "" {
		return nil, "", fmt.Errorf("social_read: 未配置用户名")
	}
	if count <= 0 || count > 100 {
		count = 20
	}
	s.warmUser(handle)
	path := fmt.Sprintf("/%s/user/%s/posts?count=%d", platform, url.PathEscape(handle), count)
	if cursor != "" {
		path += "&cursor=" + url.QueryEscape(cursor)
	}
	var res srPostsRes
	if err := s.get(ctx, path, &res); err != nil {
		return nil, "", err
	}
	out := make([]model.Tweet, 0, len(res.Posts))
	for i := range res.Posts {
		out = append(out, res.Posts[i].toTweet(s.base(), s.platform()))
		if len(out) >= count {
			break
		}
	}
	return out, strings.TrimSpace(res.NextCursor), nil
}

// FetchLatest 拉取用户最新帖子（第一页）。
func (s *SocialRead) FetchLatest(ctx context.Context, screenName string, count int) ([]model.Tweet, error) {
	tweets, _, err := s.FetchPage(ctx, screenName, count, "")
	return tweets, err
}

// firstNonEmptyString 返回第一个非空（trim 后）参数；全空返回 ""。
// 与 internal/livechat、internal/upstream 的同名帮助函数同型（bluesky.go 在用）。
func firstNonEmptyString(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
