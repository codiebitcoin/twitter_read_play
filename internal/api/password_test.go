package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"twitter_read_play/internal/store"
)

// M3 回归：默认密码 admin/11111 已移除，密码 bcrypt 哈希存储，首次设置走
// /api/setup/auth（仅未配置时可用）。
func TestHashAndVerifyPassword(t *testing.T) {
	stored, err := hashPassword("s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, bcryptPrefix) {
		t.Fatalf("stored value must carry %q prefix, got %q", bcryptPrefix, stored)
	}
	ok, up := verifyStoredPass(stored, true, "s3cret-pass")
	if !ok || up {
		t.Errorf("bcrypt verify: ok=%v upgrade=%v, want true/false", ok, up)
	}
	if ok, _ := verifyStoredPass(stored, true, "wrong"); ok {
		t.Error("wrong password must not verify")
	}
	// 存量明文（DB）匹配 → 标记升级
	if ok, up := verifyStoredPass("legacy-plain", true, "legacy-plain"); !ok || !up {
		t.Errorf("legacy plaintext: ok=%v upgrade=%v, want true/true", ok, up)
	}
	// env 明文匹配 → 不升级（避免 env 改动被 DB 哈希永久覆盖）
	if ok, up := verifyStoredPass("env-plain", false, "env-plain"); !ok || up {
		t.Errorf("env plaintext: ok=%v upgrade=%v, want true/false", ok, up)
	}
}

func setupAuthServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Store: st}
}

func TestSetupAuthFlow(t *testing.T) {
	s := setupAuthServer(t)

	// 未配置：status=false
	rec := httptest.NewRecorder()
	s.authStatus(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	var st struct {
		Configured bool `json:"configured"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Configured {
		t.Fatal("fresh server must be unconfigured")
	}

	// 默认密码已移除：admin/11111 登录必须失败
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"user":"admin","pass":"11111"}`))
	s.login(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("default-password login must fail, got %d", rec.Code)
	}

	// 首次设置：太短的密码被拒（且计入限速窗口）
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/setup/auth", strings.NewReader(`{"user":"admin","pass":"123"}`))
	s.setupAuth(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("short pass must 400, got %d", rec.Code)
	}

	// 正常初始化 → 种下哈希 + 会话 cookie
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/setup/auth", strings.NewReader(`{"user":"boss","pass":"long-enough-pass"}`))
	s.setupAuth(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup failed: %d %s", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) == 0 {
		t.Error("setup must establish a session cookie")
	}
	h, _ := s.Store.GetSetting("auth_pass")
	if !strings.HasPrefix(h, bcryptPrefix) {
		t.Errorf("stored pass must be hashed, got %q", h)
	}
	if u, _ := s.Store.GetSetting("auth_user"); u != "boss" {
		t.Errorf("auth_user = %q, want boss", u)
	}

	// 已配置后再调 → 403
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/setup/auth", strings.NewReader(`{"user":"x","pass":"yyyyyy"}`))
	s.setupAuth(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("second setup must 403, got %d", rec.Code)
	}

	// 新口令登录成功；旧默认口令仍失败
	for _, tc := range []struct {
		pass string
		want int
	}{
		{"long-enough-pass", http.StatusOK},
		{"11111", http.StatusUnauthorized},
	} {
		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"user":"boss","pass":"`+tc.pass+`"}`))
		s.login(rec, req)
		if rec.Code != tc.want {
			t.Errorf("login pass=%q: status %d, want %d", tc.pass, rec.Code, tc.want)
		}
	}
}

// 存量明文口令登录成功后自动升级为哈希。
func TestLegacyPlaintextUpgradedOnLogin(t *testing.T) {
	s := setupAuthServer(t)
	_ = s.Store.SetSetting("auth_user", "admin")
	_ = s.Store.SetSetting("auth_pass", "old-plain-pass")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"user":"admin","pass":"old-plain-pass"}`))
	s.login(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy login failed: %d %s", rec.Code, rec.Body.String())
	}
	h, _ := s.Store.GetSetting("auth_pass")
	if !strings.HasPrefix(h, bcryptPrefix) {
		t.Errorf("legacy plaintext must be upgraded to bcrypt, got %q", h)
	}
	// 升级后原口令仍能登录
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"user":"admin","pass":"old-plain-pass"}`))
	s.login(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("login after upgrade failed: %d", rec.Code)
	}
}
