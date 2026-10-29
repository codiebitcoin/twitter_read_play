// Package netcfg 把页面上的 HTTP 代理 / NO_HTTP 应用到进程环境。
// 出站客户端要用 ProxyFunc（自己每次读 env）；标准库 http.ProxyFromEnvironment
// 会把首次读到的配置缓存住，运行期保存的新代理不会生效。
package netcfg

import (
	"os"
	"strings"
)

// Apply 按两个参数设置出站代理。
//
//	httpProxy  形如 http://127.0.0.1:7892。空字符串表示不设置 HTTP 代理。
//	noHTTP     "1" / "true" / "yes" / "*"：直连，不走 HTTP 代理。
//	           其他非空值：当作 NO_PROXY（逗号分隔的主机，这些主机不走代理）。
//
// 只覆盖这里显式给出的含义；两个都空则清掉本进程里的 HTTP_PROXY / NO_PROXY，
// 避免上次保存的值残留。部署时没进数据库的环境变量会在下次进程启动时重新生效。
func Apply(httpProxy, noHTTP string) {
	httpProxy = strings.TrimSpace(httpProxy)
	noHTTP = strings.TrimSpace(noHTTP)
	if direct(noHTTP) {
		clearProxy()
		setPair("NO_PROXY", "no_proxy", "*")
		return
	}
	if noHTTP != "" {
		setPair("NO_PROXY", "no_proxy", noHTTP)
	} else {
		os.Unsetenv("NO_PROXY")
		os.Unsetenv("no_proxy")
	}
	if httpProxy != "" {
		setPair("HTTP_PROXY", "http_proxy", httpProxy)
		setPair("HTTPS_PROXY", "https_proxy", httpProxy)
	} else {
		clearProxy()
	}
}

func direct(noHTTP string) bool {
	switch strings.ToLower(noHTTP) {
	case "1", "true", "yes", "*":
		return true
	default:
		return false
	}
}

func setPair(a, b, v string) {
	_ = os.Setenv(a, v)
	_ = os.Setenv(b, v)
}

func clearProxy() {
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		os.Unsetenv(k)
	}
}
