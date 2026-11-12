package api

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadLogsPacksAndScrubs(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "desktop.log")
	// t= 是 desktop-start 凭据、code= 是 OAuth 回调码：都必须被打码
	content := "2026/11/12 twitter_read_play 桌面模式（本地服务 http://localhost:7991/desktop-start?t=0123456789abcdef0123456789abcdef）\n" +
		"[webview] 回调 /callback/google?code=4/0AbcdEfGh_IjKlMnOp &state=xyz\n" +
		"普通日志行不动：http://localhost:7991/api/health\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "desktop.log.1"), []byte("旧的轮转日志\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 非 .log 文件不该被打进包
	if err := os.WriteFile(filepath.Join(dir, "其他.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Server{Desktop: true, LogPath: logPath}
	rec := httptest.NewRecorder()
	s.downloadLogs(rec, httptest.NewRequest("GET", "/api/logs/download", nil))

	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="trp-logs-`) {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(rc)
		_ = rc.Close()
		got[f.Name] = string(b)
	}
	if _, ok := got["其他.txt"]; ok {
		t.Errorf("非日志文件被打进了 zip")
	}
	want, ok := got["desktop.log"]
	if !ok {
		t.Fatalf("desktop.log 不在 zip 里: %v", got)
	}
	if strings.Contains(want, "0123456789abcdef0123456789abcdef") {
		t.Errorf("desktop-start 凭据没有被打码:\n%s", want)
	}
	if strings.Contains(want, "4/0AbcdEfGh_IjKlMnOp") {
		t.Errorf("OAuth code 没有被打码:\n%s", want)
	}
	for _, keep := range []string{"t=***", "code=***", "普通日志行不动", "旧的轮转日志"} {
		if want != "旧的轮转日志\n" && !strings.Contains(want, keep) && keep != "旧的轮转日志" {
			t.Errorf("期望包含 %q，实际:\n%s", keep, want)
		}
	}
	if got["desktop.log.1"] != "旧的轮转日志\n" {
		t.Errorf("desktop.log.1 内容不符: %q", got["desktop.log.1"])
	}
}

func TestDownloadLogsWithoutFile(t *testing.T) {
	s := &Server{Desktop: true, LogPath: ""}
	rec := httptest.NewRecorder()
	s.downloadLogs(rec, httptest.NewRequest("GET", "/api/logs/download", nil))
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "README.txt" {
		t.Fatalf("无日志时应只含 README.txt: %v", zr.File)
	}
}

// 日志模块是桌面端功能：服务模式直接 404。
func TestDownloadLogsServiceMode404(t *testing.T) {
	s := &Server{Desktop: false, LogPath: "/tmp/whatever/desktop.log"}
	rec := httptest.NewRecorder()
	s.downloadLogs(rec, httptest.NewRequest("GET", "/api/logs/download", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("服务模式应 404，实际 %d", rec.Code)
	}
}
