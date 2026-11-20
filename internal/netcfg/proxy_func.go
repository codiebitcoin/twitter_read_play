package netcfg

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// ProxyFunc 供 http.Transport.Proxy 使用：每次请求重新读环境变量。
//
// 不能用 http.ProxyFromEnvironment：它首次调用后会把 env 缓存下来，
// 设置页在运行期改代理就不会生效（只能重启进程）。
func ProxyFunc(req *http.Request) (*url.URL, error) {
	if req == nil || req.URL == nil {
		return nil, nil
	}
	if bypass(req.URL.Hostname(), envFirst("NO_PROXY", "no_proxy")) {
		return nil, nil
	}
	raw := envFirst("HTTP_PROXY", "http_proxy")
	if strings.EqualFold(req.URL.Scheme, "https") {
		raw = envFirst("HTTPS_PROXY", "https_proxy")
	}
	if raw == "" {
		return nil, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return url.Parse(raw)
}

func envFirst(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// bypass 实现 NO_PROXY 的常用语义：* 全放行，逗号分隔，域名后缀匹配，
// 以及 IP / CIDR 网段匹配（如 192.168.1.0/24：代理用于外网抓取，
// 局域网设备与内网服务必须直连）。
// 请求的 host 不含端口，所以条目里的 :port 直接忽略。
func bypass(host, noProxy string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || noProxy == "" {
		return false
	}
	if strings.TrimSpace(noProxy) == "*" {
		return true
	}
	hostIP := net.ParseIP(host)
	for _, raw := range strings.Split(noProxy, ",") {
		item := strings.TrimSpace(strings.ToLower(raw))
		if item == "" {
			continue
		}
		item = strings.TrimPrefix(item, ".")
		// CIDR 条目（含 "/"）：按网段匹配
		if strings.Contains(item, "/") {
			if hostIP != nil {
				if _, cidr, err := net.ParseCIDR(strings.TrimSuffix(item, "/")); err == nil && cidr.Contains(hostIP) {
					return true
				}
			}
			continue
		}
		if h, _, ok := strings.Cut(item, ":"); ok {
			item = h
		}
		if item == "" {
			continue
		}
		if hostIP != nil {
			if ip := net.ParseIP(item); ip != nil {
				if hostIP.Equal(ip) {
					return true
				}
				continue
			}
		}
		if host == item || strings.HasSuffix(host, "."+item) {
			return true
		}
	}
	return false
}
