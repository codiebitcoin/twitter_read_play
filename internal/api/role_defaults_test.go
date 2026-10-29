package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"twitter_read_play/internal/model"
)

// 新建角色默认动作强度 STRONG（3，与设备默认一致）；显式传值时不覆盖。
func TestNewRoleDefaultsStrongMotion(t *testing.T) {
	s := newQueueTestServer(t)

	rec := httptest.NewRecorder()
	s.upsertRole(rec, httptest.NewRequest(http.MethodPost, "/api/roles", strings.NewReader(`{"name":"Default Motion"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("新建角色失败: %d %s", rec.Code, rec.Body.String())
	}
	var def model.Role
	if err := json.Unmarshal(rec.Body.Bytes(), &def); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if def.AnimationIntensity != model.AnimIntensityHigh {
		t.Fatalf("新建角色动作强度默认应为 STRONG(%d)，实际 %d", model.AnimIntensityHigh, def.AnimationIntensity)
	}

	rec = httptest.NewRecorder()
	s.upsertRole(rec, httptest.NewRequest(http.MethodPost, "/api/roles",
		strings.NewReader(`{"name":"Weak Motion","animation_intensity":1}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("新建角色失败: %d %s", rec.Code, rec.Body.String())
	}
	var weak model.Role
	if err := json.Unmarshal(rec.Body.Bytes(), &weak); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if weak.AnimationIntensity != model.AnimIntensityLow {
		t.Fatalf("显式传 1 时应保存 1，实际 %d", weak.AnimationIntensity)
	}
}

func TestUpsertRoleAcceptsRSSPlatform(t *testing.T) {
	s := newQueueTestServer(t)
	rec := httptest.NewRecorder()
	s.upsertRole(rec, httptest.NewRequest(http.MethodPost, "/api/roles",
		strings.NewReader(`{"name":"RSS Role","platform":"rss","username":"https://hnrss.org/frontpage"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("rss 角色应能保存: %d %s", rec.Code, rec.Body.String())
	}
	var got model.Role
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Platform != "rss" {
		t.Fatalf("platform = %q", got.Platform)
	}

	rec = httptest.NewRecorder()
	s.upsertRole(rec, httptest.NewRequest(http.MethodPost, "/api/roles",
		strings.NewReader(`{"name":"Bili Role","platform":"bilibili","username":"2"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("bilibili 角色应能保存: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.upsertRole(rec, httptest.NewRequest(http.MethodPost, "/api/roles",
		strings.NewReader(`{"name":"Bad","platform":"nope"}`)))
	if !strings.Contains(rec.Body.String(), "未知平台") {
		t.Fatalf("未知平台应报错, 实际 %s", rec.Body.String())
	}
}
