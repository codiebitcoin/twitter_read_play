package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// /api/setup/status 必须带 host_os：前端据此在 macOS 上显示「本地网络」授权提示与打开按钮。
func TestSetupStatusHostOS(t *testing.T) {
	s := newQueueTestServer(t)

	rec := httptest.NewRecorder()
	s.setupStatus(rec, httptest.NewRequest(http.MethodGet, "/api/setup/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("setup/status 失败: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		HostOS string `json:"host_os"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
	if out.HostOS != runtime.GOOS {
		t.Fatalf("host_os 应为 %q，实际 %q", runtime.GOOS, out.HostOS)
	}
}

// 打开系统设置「本地网络」面板：非 darwin 直接报错（不该去 exec open）。
// 注：项目里 s.err 的惯例是错误放 body.error（前端 api() 对 data.error 抛错），不写 HTTP 状态码。
func TestOpenLocalNetworkSettingsNonDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("darwin 上会真的执行 open，跳过")
	}
	s := newQueueTestServer(t)

	rec := httptest.NewRecorder()
	s.openLocalNetworkSettings(rec, httptest.NewRequest(http.MethodPost, "/api/system/open/local-network", nil))
	if !strings.Contains(rec.Body.String(), "only exists on macOS") {
		t.Fatalf("非 darwin 应返回 macOS 限定错误，实际 %s", rec.Body.String())
	}
}
