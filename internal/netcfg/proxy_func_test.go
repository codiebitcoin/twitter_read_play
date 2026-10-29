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
