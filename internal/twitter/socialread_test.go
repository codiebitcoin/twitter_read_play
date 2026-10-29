package twitter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeSocialRead 假 social_read 服务：记录请求，返回固定 JSON。
type fakeSocialRead struct {
	mu     sync.Mutex
	paths  []string
	bodies map[string]any
	status int
	srv    *httptest.Server
}

func newFakeSocialRead(t *testing.T, bodies map[string]any, status int) *fakeSocialRead {
	t.Helper()
	f := &fakeSocialRead{bodies: bodies, status: status}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path+"?"+r.URL.RawQuery)
		f.mu.Unlock()
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		for suffix, body := range bodies {
			if strings.HasSuffix(r.URL.Path, suffix) {
				_ = json.NewEncoder(w).Encode(body)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSocialRead) provider() *SocialRead {
	return &SocialRead{
		Platform: "weibo",
		BaseURL:  f.srv.URL,
		APIKey:   "test-key",
		HTTP:     f.srv.Client(),
	}
}

func (f *fakeSocialRead) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.paths))
	copy(out, f.paths)
	return out
}

func weiboBodies() map[string]any {
	return map[string]any{
		"/posts": map[string]any{
			"platform": "weibo",
			"posts": []map[string]any{
				{
					"platform": "weibo",
					"id":       "5021234567890123",
					"user":     map[string]any{"id": "1234", "handle": "PeopleDaily", "name": "人民日报"},
					"text":     "  这是一条微博正文  ",
					"images":   []string{"https://img.example/a.jpg", "https://img.example/b.jpg"},
					"video":    nil,
					"link":     "https://weibo.com/1234/5021234567890123",
					// 点赞/评论/转发/收藏
					"like_count": 42, "comment_count": 7, "share_count": 3, "collect_count": 1,
					"created_at":     "2026-10-14T20:00:00+08:00",
					"created_at_raw": "Tue Oct 14 20:00:00 +0800 2026",
				},
				{
					"platform":   "weibo",
					"id":         "5021234567890122",
					"user":       map[string]any{"id": "1234", "handle": "peopledaily"},
					"text":       "纯视频帖",
					"images":     []string{},
					"video":      map[string]any{"url": "https://video.example/v.mp4"},
					"link":       "https://weibo.com/1234/5021234567890122",
					"like_count": 1, "comment_count": 0, "share_count": 0, "collect_count": 0,
					"created_at":     "1750000000", // 10 位 unix 秒
					"created_at_raw": "3分钟前",
				},
				{
					"platform":       "weibo",
					"id":             "5021234567890121",
					"user":           map[string]any{"id": "1234", "handle": "peopledaily"},
					"text":           "微博中文时间",
					"created_at":     "",
					"created_at_raw": "10月15日 09:54",
				},
			},
			"next_cursor": "CURSOR-2",
		},
		"/user/peopledaily": map[string]any{
			"platform": "weibo", "id": "1234", "handle": "peopledaily", "name": "人民日报",
			"followers_count": 100, "verified": true, "web_url": "https://weibo.com/peopledaily",
		},
	}
}

// FetchLatest 必须把统一帖子对象逐字段映射到 model.Tweet（含配图/点赞/时间归一化）。
func TestSocialReadFetchLatest(t *testing.T) {
	f := newFakeSocialRead(t, weiboBodies(), http.StatusOK)
	p := f.provider()

	if got := p.Name(); got != "socialread:weibo" {
		t.Fatalf("Name() = %q, want socialread:weibo", got)
	}
	tweets, err := p.FetchLatest(context.Background(), "PeopleDaily", 20)
	if err != nil {
		t.Fatalf("FetchLatest: %v", err)
	}
	if len(tweets) != 3 {
		t.Fatalf("len(tweets) = %d, want 3", len(tweets))
	}

	first := tweets[0]
	if first.ID != "5021234567890123" {
		t.Errorf("ID = %q", first.ID)
	}
	if first.User != "peopledaily" { // handle 统一小写
		t.Errorf("User = %q, want peopledaily", first.User)
	}
	if first.Text != "这是一条微博正文" {
		t.Errorf("Text = %q, want 去掉首尾空白", first.Text)
	}
	if first.Likes != 42 || first.CommentCount != 7 || first.ShareCount != 3 || first.CollectCount != 1 {
		t.Errorf("likes/comments/shares/collect = %d/%d/%d/%d, want 42/7/3/1",
			first.Likes, first.CommentCount, first.ShareCount, first.CollectCount)
	}
	// 配图会被换成本服务的图片代理地址（微博图床对浏览器 UA 403，必须服务端代取）
	if len(first.Images) != 2 {
		t.Fatalf("Images = %v", first.Images)
	}
	wantProxy := f.srv.URL + "/weibo/image?url=" + url.QueryEscape("https://img.example/a.jpg")
	if first.Images[0] != wantProxy {
		t.Errorf("Images[0] = %q, want %q", first.Images[0], wantProxy)
	}
	if first.Images[1] != f.srv.URL+"/weibo/image?url="+url.QueryEscape("https://img.example/b.jpg") {
		t.Errorf("Images[1] = %q", first.Images[1])
	}
	if first.Link != "https://weibo.com/1234/5021234567890123" {
		t.Errorf("Link = %q", first.Link)
	}
	if first.Media != "photo" {
		t.Errorf("Media = %q, want photo", first.Media)
	}
	if first.CreatedAt != "2026-10-14T20:00:00+08:00" {
		t.Errorf("CreatedAt = %q, want RFC3339 归一化", first.CreatedAt)
	}
	if first.Source != "real" {
		t.Errorf("Source = %q, want real", first.Source)
	}

	// 视频对象形状 + unix 秒时间戳
	second := tweets[1]
	if second.Media != "video" {
		t.Errorf("Media = %q, want video", second.Media)
	}
	if second.CreatedAt != "2025-06-15T15:06:40Z" { // 1750000000 → UTC RFC3339
		t.Errorf("CreatedAt = %q, want 1750000000 的 RFC3339", second.CreatedAt)
	}
	// 微博中文时间原样保留，交给 watch.parseTweetTime 解析
	if tweets[2].CreatedAt != "10月15日 09:54" {
		t.Errorf("CreatedAt = %q, want 原文保留", tweets[2].CreatedAt)
	}
}

// FetchPage 必须回传 next_cursor，并把 count/cursor 拼进请求。
func TestSocialReadFetchPageCursor(t *testing.T) {
	f := newFakeSocialRead(t, weiboBodies(), http.StatusOK)
	p := f.provider()

	tweets, next, err := p.FetchPage(context.Background(), "@PeopleDaily", 20, "CUR 1")
	if err != nil {
		t.Fatalf("FetchPage: %v", err)
	}
	if next != "CURSOR-2" {
		t.Fatalf("next_cursor = %q, want CURSOR-2", next)
	}
	if len(tweets) != 3 {
		t.Fatalf("len(tweets) = %d, want 3", len(tweets))
	}
	var found bool
	for _, p := range f.requests() {
		if strings.HasPrefix(p, "/weibo/user/PeopleDaily/posts?") {
			found = true
			if !strings.Contains(p, "count=20") || !strings.Contains(p, "cursor=CUR+1") {
				t.Fatalf("posts 请求参数不对: %s", p)
			}
		}
	}
	if !found {
		t.Fatalf("没有发出 posts 请求: %v", f.requests())
	}
}

// count 必须截断到请求条数（上游给多也不越界）。
func TestSocialReadFetchLatestHonoursCount(t *testing.T) {
	f := newFakeSocialRead(t, weiboBodies(), http.StatusOK)
	tweets, err := f.provider().FetchLatest(context.Background(), "peopledaily", 1)
	if err != nil {
		t.Fatalf("FetchLatest: %v", err)
	}
	if len(tweets) != 1 || tweets[0].ID != "5021234567890123" {
		t.Fatalf("count=1 应只返回第一条, got %d", len(tweets))
	}
}

// 401/402/403 必须包成 QuotaError（否则 watch 的 markDead 机制失效）。
func TestSocialReadQuotaError(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden} {
		f := newFakeSocialRead(t, nil, code)
		_, err := f.provider().FetchLatest(context.Background(), "peopledaily", 5)
		if err == nil {
			t.Fatalf("HTTP %d 应返回错误", code)
		}
		if !IsQuotaError(err) {
			t.Fatalf("HTTP %d 的 err 不是 QuotaError: %v", code, err)
		}
		var qe *QuotaError
		if !errors.As(err, &qe) || qe.Code != code {
			t.Fatalf("QuotaError.Code = %v, want %d", qe, code)
		}
	}
}

// 其它非 2xx 不是 QuotaError（可重试），错误里要带响应体片段。
func TestSocialReadOtherHTTPError(t *testing.T) {
	f := newFakeSocialRead(t, nil, http.StatusBadGateway)
	_, err := f.provider().FetchLatest(context.Background(), "peopledaily", 5)
	if err == nil {
		t.Fatal("502 应返回错误")
	}
	if IsQuotaError(err) {
		t.Fatalf("502 不该是 QuotaError: %v", err)
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("错误应含状态码与响应体片段: %v", err)
	}
}

// 未配置平台 / 用户名：直接给出可读错误，不发请求。
func TestSocialReadMissingConfig(t *testing.T) {
	p := &SocialRead{BaseURL: "http://127.0.0.1:1", APIKey: "k"}
	if _, err := p.FetchLatest(context.Background(), "x", 1); err == nil {
		t.Fatal("未配置平台应报错")
	}
	q := &SocialRead{Platform: "weibo", BaseURL: "http://127.0.0.1:1", APIKey: "k"}
	if _, err := q.FetchLatest(context.Background(), "  ", 1); err == nil {
		t.Fatal("空用户名应报错")
	}
}

// ParseSocialReadPost：webhook new_post 的 data → model.Tweet。
func TestParseSocialReadPost(t *testing.T) {
	raw := []byte(`{"platform":"xiaohongshu","id":"65f0a1b2c3d4e5f60718293a",
		"user":{"id":"u1","handle":"ButterZhu","name":"butter"},
		"text":"小红书笔记","images":["https://xhs.example/1.jpg"],
		"like_count":88,"comment_count":9,"share_count":2,"collect_count":30,
		"link":"https://www.xiaohongshu.com/explore/65f0a1b2c3d4e5f60718293a",
		"created_at":"2026-10-16T09:00:00+08:00"}`)
	tw, err := ParseSocialReadPost(raw)
	if err != nil {
		t.Fatalf("ParseSocialReadPost: %v", err)
	}
	if tw.ID != "65f0a1b2c3d4e5f60718293a" || tw.User != "butterzhu" || tw.Text != "小红书笔记" {
		t.Fatalf("解析结果不对: %+v", tw)
	}
	if tw.Likes != 88 || tw.CollectCount != 30 || len(tw.Images) != 1 || tw.Media != "photo" {
		t.Fatalf("字段映射不对: %+v", tw)
	}
	if _, err := ParseSocialReadPost([]byte("{")); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}
