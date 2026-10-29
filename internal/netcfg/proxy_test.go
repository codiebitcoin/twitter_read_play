package netcfg

import (
	"os"
	"testing"
)

func TestApply(t *testing.T) {
	keys := []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy"}
	prev := map[string]string{}
	for _, k := range keys {
		prev[k] = os.Getenv(k)
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

	Apply("http://127.0.0.1:7892", "localhost,127.0.0.1")
	if os.Getenv("HTTPS_PROXY") != "http://127.0.0.1:7892" {
		t.Fatalf("HTTPS_PROXY = %q", os.Getenv("HTTPS_PROXY"))
	}
	if os.Getenv("NO_PROXY") != "localhost,127.0.0.1" {
		t.Fatalf("NO_PROXY = %q", os.Getenv("NO_PROXY"))
	}

	Apply("http://127.0.0.1:7892", "1")
	if os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "" {
		t.Fatal("NO_HTTP=1 应清掉 HTTP 代理")
	}
	if os.Getenv("NO_PROXY") != "*" {
		t.Fatalf("NO_PROXY = %q, want *", os.Getenv("NO_PROXY"))
	}

	Apply("", "")
	if os.Getenv("NO_PROXY") != "" || os.Getenv("HTTP_PROXY") != "" {
		t.Fatal("两个都空时应清掉代理环境变量")
	}
}
