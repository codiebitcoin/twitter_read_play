package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 只有 login / health / socialdata webhook 三个路径免登录，其余 /api/* 必须 401。
func TestAuthPublicPaths(t *testing.T) {
	s := newQueueTestServer(t)
	h := s.Routes()

	cases := []struct {
		name string
		path string
		body string
		want int
	}{
		// webhook 免登录可达，但未配 secret 时拒绝一切推送（M1：伪造推文可让设备
		// 播报、把 LastTweetID 推前）；配好 secret 后靠 X-Signature 自校验放行
		{"webhook 未配 secret 拒绝", "/api/webhook/socialdata", `{"event":"follow"}`, http.StatusUnauthorized},
		{"登录接口", "/api/login", `{"user":"x","pass":"y"}`, http.StatusUnauthorized},
		{"健康检查", "/api/health", "", http.StatusOK},
		{"受保护接口仍 401", "/api/settings", "", http.StatusUnauthorized},
		{"受保护接口仍 401（machines）", "/api/machines", "", http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body))
			if c.body == "" {
				req = httptest.NewRequest(http.MethodGet, c.path, nil)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Fatalf("%s %s = %d, want %d (body=%s)", req.Method, c.path, rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// M1 回归：未配置 secret 时 webhook 一律拒绝（历史上放行 = 任何人可伪造推送）。
func TestWebhookRejectsWhenNoSecret(t *testing.T) {
	s := newQueueTestServer(t)
	h := s.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/webhook/socialdata", strings.NewReader(`{"event":"follow"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-secret webhook = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}

	// 配好 secret 且签名正确 → 放行
	if err := s.Store.SetSetting("socialdata_webhook_secret", "s3cret"); err != nil {
		t.Fatal(err)
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	body := `{"event":"follow"}`
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write([]byte("e1." + ts + "." + body))
	sig := "v1=" + hex.EncodeToString(mac.Sum(nil))
	req = httptest.NewRequest(http.MethodPost, "/api/webhook/socialdata", strings.NewReader(body))
	req.Header.Set("X-Event-Id", "e1")
	req.Header.Set("X-Timestamp", ts)
	req.Header.Set("X-Signature", sig)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid signature = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}

// 配了 secret 后，公网无签名/错签名推送必须 401（免登录入口不能变成任意伪造入口）。
func TestWebhookSignatureEnforcedWhenSecretSet(t *testing.T) {
	s := newQueueTestServer(t)
	if err := s.Store.SetSetting("socialdata_webhook_secret", "s3cret"); err != nil {
		t.Fatal(err)
	}
	h := s.Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/webhook/socialdata", strings.NewReader(`{"event":"new_tweet"}`))
	req.Header.Set("X-Event-Id", "e1")
	req.Header.Set("X-Timestamp", "1")
	req.Header.Set("X-Signature", "v1=deadbeef")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错签名 = %d, want 401 (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid signature") {
		t.Fatalf("响应体不是 invalid signature: %s", rec.Body.String())
	}
}
