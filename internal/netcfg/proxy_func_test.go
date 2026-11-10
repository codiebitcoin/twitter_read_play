package netcfg

import (
	"net/http/httptest"
	"os"
	"testing"
)

func TestProxyFuncReadsEnvEachCall(t *testing.T) {
	keys := []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy"}
	prev := map[string]string{}
	for _, k := range keys {
		prev[k] = os.Getenv(k)
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for _, k := range keys {
			if prev[k] == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, prev[k])
			}
		}
	})

	req := httptest.NewRequest("GET", "https://public.api.bsky.app/xrpc/x", nil)
	if u, _ := ProxyFunc(req); u != nil {
		t.Fatalf("未配置时应直连，得到 %v", u)
	}

	os.Setenv("HTTPS_PROXY", "127.0.0.1:7890")
	u, err := ProxyFunc(req)
	if err != nil || u == nil || u.Host != "127.0.0.1:7890" {
		t.Fatalf("运行期设置的代理未生效: %v %v", u, err)
	}

	os.Setenv("NO_PROXY", "bsky.app,.internal.example")
	if u, _ := ProxyFunc(req); u != nil {
		t.Fatalf("NO_PROXY 后缀应旁路，得到 %v", u)
	}
	if u, _ := ProxyFunc(httptest.NewRequest("GET", "https://app.internal.example/", nil)); u != nil {
		t.Fatalf("NO_PROXY 域名后缀应旁路，得到 %v", u)
	}
	if u, _ := ProxyFunc(httptest.NewRequest("GET", "https://x.com/", nil)); u == nil {
		t.Fatal("未命中 NO_PROXY 时应走代理")
	}
}

// NO_PROXY 支持 IP 与 CIDR 网段（设置页常配局域网网段：代理只管外网抓取）。
func TestProxyFuncBypassIPAndCIDR(t *testing.T) {
	st := httptest.NewRequest("GET", "http://192.168.1.208:8081/v1/api/auth/pair/request", nil)

	if got := bypass("192.168.1.208", "192.168.1.1/23"); !got {
		t.Error("CIDR 网段应旁路局域网设备")
	}
	if !bypass("192.168.1.208", "192.168.1.208") {
		t.Error("单 IP 应旁路")
	}
	if bypass("192.168.3.5", "192.168.1.1/23") {
		t.Error("网段外地址不应旁路")
	}
	if !bypass("192.168.1.208", "192.168.1.0/24") {
		t.Error(".208 属于 192.168.1.0/24，应旁路")
	}
	// 生效链路：设了代理 + 局域网网段 NO_PROXY，设备请求必须直连
	t.Setenv("HTTP_PROXY", "http://192.168.1.14:7890")
	t.Setenv("NO_PROXY", "192.168.1.1/23")
	if u, _ := ProxyFunc(st); u != nil {
		t.Errorf("局域网设备请求不应走代理，得到 %v", u)
	}
	t.Setenv("NO_PROXY", "inner.internal.example.net")
	if u, _ := ProxyFunc(st); u == nil {
		t.Error("未命中 NO_PROXY 时应走代理")
	}
}
