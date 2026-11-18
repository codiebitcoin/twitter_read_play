package bootstrap

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeWebViewArgs(t *testing.T) {
	cases := []struct {
		name     string
		existing string
		noProxy  bool
		noGPU    bool
		want     []string
		absent   []string
	}{
		{
			name:  "软件渲染开关追加 --disable-gpu",
			noGPU: true,
			want:  []string{"--disable-gpu", "--proxy-bypass-list=localhost"},
		},
		{
			name:     "已有 --disable-gpu 时不重复追加",
			existing: "--disable-gpu",
			noGPU:    true,
			want:     []string{"--disable-gpu"},
		},
		{
			name:    "禁代理优先于 GPU 开关",
			noProxy: true,
			noGPU:   true,
			want:    []string{"--no-proxy-server"},
			absent:  []string{"--proxy-bypass-list"},
		},
		{
			name: "默认只绕过回环",
			want: []string{"--proxy-bypass-list=localhost;127.0.0.1;[::1];<local>"},
		},
		{
			name:     "保留用户已有参数并追加绕过规则",
			existing: "--disable-gpu",
			want:     []string{"--disable-gpu", "--proxy-bypass-list=localhost"},
		},
		{
			name:    "开关打开时彻底禁用代理",
			noProxy: true,
			want:    []string{"--no-proxy-server"},
		},
		{
			name:     "用户已禁用代理时不重复",
			existing: "--no-proxy-server",
			noProxy:  true,
			want:     []string{"--no-proxy-server"},
			absent:   []string{"--proxy-bypass-list"},
		},
		{
			name:     "用户自己配了 bypass list 时不覆盖",
			existing: "--proxy-bypass-list=*.internal",
			want:     []string{"--proxy-bypass-list=*.internal"},
			absent:   []string{"localhost;127.0.0.1"},
		},
		{
			name:     "用户已有 no-proxy-server 时保持原样",
			existing: "--no-proxy-server",
			want:     []string{"--no-proxy-server"},
			absent:   []string{"--proxy-bypass-list"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeWebViewArgs(tc.existing, tc.noProxy, tc.noGPU)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("mergeWebViewArgs(%q, %v, %v) = %q，缺少 %q", tc.existing, tc.noProxy, tc.noGPU, got, want)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(got, absent) {
					t.Fatalf("mergeWebViewArgs(%q, %v, %v) = %q，不应包含 %q", tc.existing, tc.noProxy, tc.noGPU, got, absent)
				}
			}
		})
	}
}

func TestWebViewNoProxyRequested(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(noProxyEnv, "")
	if webViewNoProxyRequested(dir) {
		t.Fatal("没有开关时不应禁用代理")
	}

	t.Setenv(noProxyEnv, "1")
	if !webViewNoProxyRequested(dir) {
		t.Fatal("环境变量 TRP_WEBVIEW_NO_PROXY=1 应开启开关")
	}

	t.Setenv(noProxyEnv, "off")
	if webViewNoProxyRequested(dir) {
		t.Fatal("TRP_WEBVIEW_NO_PROXY=off 不应开启开关")
	}

	if err := os.WriteFile(filepath.Join(dir, noProxyMarker), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !webViewNoProxyRequested(dir) {
		t.Fatal("存在 webview_no_proxy 标记文件时应开启开关")
	}
}

func TestSetupDesktopLogWritesFile(t *testing.T) {
	oldOut, oldFlags := log.Writer(), log.Flags()
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	})

	dir := t.TempDir()
	path, closeLog := setupDesktopLog(dir)
	defer closeLog()
	if path == "" {
		t.Fatal("应该返回日志文件路径")
	}
	log.Printf("白屏排查测试")
	closeLog()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志失败: %v", err)
	}
	if !strings.Contains(string(data), "白屏排查测试") {
		t.Fatalf("日志文件里没有测试内容: %q", string(data))
	}
}
