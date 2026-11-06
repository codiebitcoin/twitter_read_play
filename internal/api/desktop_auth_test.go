package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// H4 回归：桌面模式曾对任何请求无条件放行（只靠绑定本机做边界），
// DNS 重绑定 / 跨站 text/plain POST 可读全部密钥。修复后：
// Host 白名单 + Origin 同源 + /api/* 需桌面凭据。
func desktopTestServer(t *testing.T) *Server {
	t.Helper()
	s := newQueueTestServer(t)
	s.Desktop = true
	s.DesktopToken = "tok-123"
	s.Origin = "http://localhost:7991"
	return s
}

func TestDesktopHostAllowed(t *testing.T) {
	s := desktopTestServer(t)
	h := s.Routes()

	for _, host := range []string{"evil.com:7991", "localhost:1234", "127.0.0.1:7992", "localhost.evil.com:7991"} {
		req := httptest.NewRequest(http.MethodGet, "/api/roles", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Host %q: status = %d, want 403", host, rec.Code)
		}
	}
	// 合法 host：仍需凭据（401 而不是 200/403）
	for _, host := range []string{"localhost:7991", "127.0.0.1:7991"} {
		req := httptest.NewRequest(http.MethodGet, "/api/roles", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Host %q without token: status = %d, want 401", host, rec.Code)
		}
	}
}

func TestDesktopCrossOriginBlocked(t *testing.T) {
	s := desktopTestServer(t)
	h := s.Routes()

	req := httptest.NewRequest(http.MethodPost, "/api/roles/1/broadcast", nil)
	req.Host = "localhost:7991"
	req.Header.Set("Origin", "https://evil.com")
	req.Header.Set("X-Desktop-Token", "tok-123") // 凭据对，跨源也拒
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-origin POST: status = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/roles/1/broadcast", nil)
	req.Host = "localhost:7991"
	req.Header.Set("Origin", "http://localhost:7991")
	req.Header.Set("X-Desktop-Token", "tok-123")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Errorf("same-origin POST should pass auth, got %d", rec.Code)
	}
}

func TestDesktopTokenAndCookie(t *testing.T) {
	s := desktopTestServer(t)
	h := s.Routes()

	// X-Desktop-Token 头
	req := httptest.NewRequest(http.MethodGet, "/api/roles", nil)
	req.Host = "localhost:7991"
	req.Header.Set("X-Desktop-Token", "tok-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("header token: status = %d, want 200", rec.Code)
	}

	// /desktop-start?t= 换 cookie：错 token 403、对 token 302 + cookie
	req = httptest.NewRequest(http.MethodGet, "/desktop-start?t=wrong", nil)
	req.Host = "localhost:7991"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("desktop-start bad token: status = %d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/desktop-start?t=tok-123", nil)
	req.Host = "localhost:7991"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("desktop-start: status = %d, want 302", rec.Code)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Value != "tok-123" {
		t.Fatalf("desktop-start must seed session cookie, got %v", cookies)
	}

	// 页面加载（非 /api）不需要凭据
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "localhost:7991"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
		t.Errorf("page load should not require token, got %d", rec.Code)
	}

	// 免登录 publicPath（如 /api/health）不受影响
	req = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = "localhost:7991"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("health must stay public in desktop mode, got %d", rec.Code)
	}
}

// 未注入 DesktopToken（内嵌调用方）维持旧行为：只校验 Host/Origin。
func TestDesktopNoTokenFallsOpen(t *testing.T) {
	s := newQueueTestServer(t)
	s.Desktop = true
	s.Origin = "http://localhost:7991"
	h := s.Routes()

	req := httptest.NewRequest(http.MethodGet, "/api/roles", nil)
	req.Host = "localhost:7991"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("no-token embed: status = %d, want 200 (legacy behavior)", rec.Code)
	}
}
