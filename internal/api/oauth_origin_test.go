package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// PC（桌面）端设置了 Origin 时，OAuth 回调一律用它，不跟随请求 Host ——
// 回调地址要和 Twitch/Google 后台登记的完全一致，host 固定不可编辑。
func TestRequestOriginPrefersFixedOrigin(t *testing.T) {
	s := &Server{Origin: "http://localhost:7991"}
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7991/api/x", nil)
	got := requestOrigin(s, r)
	if got != "http://localhost:7991" {
		t.Fatalf("requestOrigin = %q, want http://localhost:7991", got)
	}
	redirects := oauthRedirects(s, r)
	// 统一回调规则：<origin>/callback/<平台>
	wantTw := "http://localhost:7991/callback/twitch"
	if redirects["twitch"] != wantTw {
		t.Fatalf("twitch redirect = %q, want %q", redirects["twitch"], wantTw)
	}
	if redirects["google"] != "http://localhost:7991/callback/google" {
		t.Fatalf("google redirect = %q", redirects["google"])
	}
}

// Web 部署没设 Origin 时保持原行为：跟随请求 Host（含反代 X-Forwarded-Proto）。
func TestRequestOriginFollowsHostWithoutOverride(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest(http.MethodGet, "http://demo.example.com/api/x", nil)
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := requestOrigin(s, r); got != "https://demo.example.com" {
		t.Fatalf("requestOrigin = %q, want https://demo.example.com", got)
	}
}

// /callback/<平台> 是对外登记的统一回调入口：免登录可达（带 state 时才换 token，
// 这里只验证路由与免登录，乱 state 走到报错弹窗即可）。
func TestCallbackRouteIsPublic(t *testing.T) {
	if !isPublicPath("/callback/twitch") || !isPublicPath("/callback/google") {
		t.Fatal("/callback/<平台> 应该免登录")
	}
	// 旧路径也仍可达（保留兼容）
	if !isPublicPath("/api/livechat/oauth/twitch/callback") {
		t.Fatal("旧回调路径应保留免登录")
	}
}
