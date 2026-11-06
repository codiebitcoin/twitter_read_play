package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// postWebhook 向 webhook 端点推送一段载荷（自动配好 secret 并计算有效签名 ——
// M1 起未配 secret 一律 401，推送级测试只关心载荷处理）。
func postWebhook(t *testing.T, path, body string) (int, map[string]any) {
	t.Helper()
	s := newQueueTestServer(t)
	if err := s.Store.SetSetting("socialdata_webhook_secret", "test-secret"); err != nil {
		t.Fatal(err)
	}
	h := s.Routes()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("test-secret"))
	mac.Write([]byte("evt." + ts + "." + body))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-Event-Id", "evt")
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", "v1="+hex.EncodeToString(mac.Sum(nil)))
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// social_read 的 new_post 推送（统一帖子对象 + meta.platform）：必须 200 确认且不 panic。
// 没有绑定角色时 matched=false。
func TestWebhookNewPost(t *testing.T) {
	body := `{"event":"new_post","data":{"platform":"weibo","id":"5021234567890123",
		"user":{"id":"1","handle":"peopledaily","name":"人民日报"},"text":"一条微博",
		"images":["https://img.example/a.jpg"],"like_count":42,"comment_count":7,
		"share_count":3,"collect_count":1,"link":"https://weibo.com/1/5021234567890123",
		"created_at":"2026-10-14T20:00:00+08:00","created_at_raw":"Tue Oct 14 20:00:00 +0800 2026"},
		"meta":{"platform":"weibo","monitored_username":"人民日报","monitor_id":"m1"}}`
	code, out := postWebhook(t, "/api/webhook/socialdata", body)
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	if out["ok"] != true {
		t.Fatalf("ok != true: %v", out)
	}
	if out["matched"] != false {
		t.Fatalf("没有匹配角色时 matched 应为 false: %v", out)
	}
}

// 载荷缺字段 / 非法 JSON 也必须 200 确认（上游不重试，慢处理等同故障）。
func TestWebhookNewPostMalformed(t *testing.T) {
	for _, body := range []string{
		`{"event":"new_post","data":{"text":"没有 id"}}`,
		`{"event":"new_post","data":"not-an-object"}`,
		`{"event":"new_post"}`,
		`{`,
		`{"event":"follow"}`,
	} {
		code, out := postWebhook(t, "/api/webhook/socialdata", body)
		if code != http.StatusOK || out["ok"] != true {
			t.Fatalf("body=%s → code=%d out=%v, want 200 + ok", body, code, out)
		}
	}
}

// 老的 new_tweet 分支（socialdata 还在用）必须保持可用。
func TestWebhookNewTweetStillWorks(t *testing.T) {
	body := `{"event":"new_tweet","data":{"id_str":"123","full_text":"hello","user":{"screen_name":"ElonMusk"}},
		"meta":{"monitored_username":"elonmusk"}}`
	code, out := postWebhook(t, "/api/webhook/socialdata", body)
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("code=%d out=%v, want 200 + ok", code, out)
	}
}
