package clt01

import (
	"net/http"
	"net/http/httputil"
	"net/url"
)

// ConsoleProxy 返回设备 Web 控制台（含 /config/pair 授权页）的反向代理 Handler。
//
// 宿主程序把它挂到自己的 HTTP Server（或 WebView 直接加载）即可内嵌授权页面：
//
//	mux := http.NewServeMux()
//	mux.Handle("/", client.ConsoleProxy())
//	http.ListenAndServe("127.0.0.1:8090", mux)
//	// 浏览器/WebView 打开 http://127.0.0.1:8090/config/pair
//
// 页面内的配对、签名由设备自带前端（crypto.ts）完成，凭据保存在浏览器
// localStorage 中，与 SDK 的凭据互相独立。
func (c *Client) ConsoleProxy() http.Handler {
	target, err := url.Parse(c.BaseURL)
	if err != nil {
		// New 已校验过 BaseURL，这里兜底
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "invalid device url", http.StatusInternalServerError)
		})
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host // 保持 Host 一致，避免 gin 路由/CORS 异常
	}
	return proxy
}

// ConsoleURL 返回控制台页面完整地址（如内嵌 WebView 直接加载）。
// path 传 "/config/pair" 即授权/配对页，传 "/" 为控制台首页。
func (c *Client) ConsoleURL(path string) string {
	if path == "" {
		path = "/config/"
	}
	return c.BaseURL + path
}
