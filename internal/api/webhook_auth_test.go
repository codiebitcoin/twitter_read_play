package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		// 公网回调：未登录也必须放行（靠 X-Signature 自校验），否则 socialdata 反向通知拿 401
		{"webhook 免登录", "/api/webhook/socialdata", `{"event":"follow"}`, http.StatusOK},
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
			if c.path == "/api/webhook/socialdata" {
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("webhook 非 JSON: %v (%s)", err, rec.Body.String())
				}
				if body["ok"] != true {
					t.Fatalf("webhook ok != true: %s", rec.Body.String())
				}
			}
		})
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
