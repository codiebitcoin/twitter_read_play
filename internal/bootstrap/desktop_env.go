package bootstrap

import (
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
)

// 桌面模式的日志与 WebView2 代理参数。
//
// 为什么需要日志：mygo build 的正式产物是 windowsgui 子系统，没有控制台，
// log 写到 stderr 等于扔掉；用户只能看到白屏，连「本地服务起没起来」都不知道。
// 所以桌面模式把日志同时写进 <数据目录>/logs/desktop.log。
//
// 为什么需要代理参数：WebView2 走系统代理（Clash / v2ray / 公司 GPO）。如果代理
// 把发往 127.0.0.1 的请求也劫走，窗口加载本机页面就会失败 → 白屏。mygo 没有暴露
// CoreWebView2EnvironmentOptions.AdditionalBrowserArguments，只能通过 WebView2
// 运行时认的环境变量 WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS 注入浏览器参数。
//
// 默认只把回环地址排除出代理（不影响内置浏览器访问外网）；确认是代理问题时，
// 可以用 TRP_WEBVIEW_NO_PROXY=1 或数据目录下的 webview_no_proxy 文件让 WebView2
// 彻底不走代理（--no-proxy-server）验证。

const (
	webviewArgsEnv = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"
	noProxyEnv     = "TRP_WEBVIEW_NO_PROXY"
	noProxyMarker  = "webview_no_proxy"

	// Chromium 的 --proxy-bypass-list：分号分隔，IPv6 字面量要带方括号。
	loopbackBypassArg = "--proxy-bypass-list=localhost;127.0.0.1;[::1];<local>"
	noProxyServerArg  = "--no-proxy-server"

	desktopLogMaxBytes = 5 << 20 // 单文件 5MB，超过就在启动时轮转一次
)

// setupDesktopLog 让桌面模式的 log 同时写到 stderr 和控制台看不到时也能查的文件。
// 返回日志文件路径与关闭函数；路径为空表示文件日志不可用（只保留 stderr）。
func setupDesktopLog(dataDir string) (string, func()) {
	if dataDir == "" {
		dataDir = DesktopDataDir()
	}
	dir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", func() {}
	}
	path := filepath.Join(dir, "desktop.log")
	if st, err := os.Stat(path); err == nil && st.Size() > desktopLogMaxBytes {
		_ = os.Remove(path + ".1")
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", func() {}
	}
	log.SetOutput(bestEffortWriter{writers: []io.Writer{os.Stderr, f}})
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	return path, func() { _ = f.Close() }
}

// bestEffortWriter 逐个写，任一目标失败都不影响其它目标。
// windowsgui 下 os.Stderr 是个无效句柄，io.MultiWriter 会因为第一个写失败就返回。
type bestEffortWriter struct {
	writers []io.Writer
}

func (w bestEffortWriter) Write(p []byte) (int, error) {
	for _, dst := range w.writers {
		_, _ = dst.Write(p)
	}
	return len(p), nil
}

// applyWebViewProxyArgs 在创建 WebView2 环境前设置浏览器参数，返回实际生效的参数串。
// 只对 Windows 有意义：WKWebView / WebKitGTK 不认这个环境变量。
func applyWebViewProxyArgs(dataDir string) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	args := mergeWebViewArgs(os.Getenv(webviewArgsEnv), webViewNoProxyRequested(dataDir))
	if args == "" {
		return ""
	}
	_ = os.Setenv(webviewArgsEnv, args)
	return args
}

// webViewNoProxyRequested 是否要求 WebView2 彻底不走代理（诊断/兜底开关）。
func webViewNoProxyRequested(dataDir string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(noProxyEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	if dataDir == "" {
		dataDir = DesktopDataDir()
	}
	_, err := os.Stat(filepath.Join(dataDir, noProxyMarker))
	return err == nil
}

// mergeWebViewArgs 合并用户已有的浏览器参数：
//   - 用户显式配了 --no-proxy-server / --proxy-bypass-list 时不重复添加
//   - 开关打开时追加 --no-proxy-server（与已有 bypass-list 同时存在时后者失效）
func mergeWebViewArgs(existing string, noProxy bool) string {
	existing = strings.TrimSpace(existing)
	lower := strings.ToLower(existing)
	hasNoProxy := strings.Contains(lower, noProxyServerArg)
	hasBypass := strings.Contains(lower, "--proxy-bypass-list")
	switch {
	case noProxy:
		if hasNoProxy {
			return existing
		}
		return strings.TrimSpace(existing + " " + noProxyServerArg)
	case hasNoProxy || hasBypass:
		return existing
	default:
		return strings.TrimSpace(existing + " " + loopbackBypassArg)
	}
}

// attachMainWindowDiagnostics 给主窗口挂诊断：加载失败/RenderProcessGone 写日志，
// 并把白屏换成一个能说明原因、能重试的页面。
func attachMainWindowDiagnostics(win *mygo.Window, baseURL, dataDir, logPath, webViewArgs string) {
	if win == nil {
		return
	}
	page := win.Page()
	if page == nil {
		return
	}
	var loaded atomic.Bool
	watchdog := time.AfterFunc(30*time.Second, func() {
		if loaded.Load() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		state, err := page.EvalContext(ctx, "document.readyState")
		if err != nil {
			// 环境都没建起来时（WebView2 Runtime 缺失 / 创建失败），连脚本都执行不了。
			log.Printf("[webview] 页面脚本执行失败（WebView2 环境可能没建起来）: %v", err)
		} else {
			log.Printf("[webview] 主页面 30s 内没有完成加载（白屏看门狗），readyState=%v", state)
		}
		log.Printf("[webview] 排查：%s；WebView2 参数: %s", desktopLogHint(logPath), webViewArgs)
	})
	page.OnDOMReady(func() {
		loaded.Store(true)
		watchdog.Stop()
	})
	page.OnDidFailLoad(func(e *mygo.LoadError) {
		// 诊断页自己是 data: URL，加载失败不进这个分支，避免来回套娃。
		if e == nil || strings.HasPrefix(e.URL, "data:") {
			return
		}
		log.Printf("[webview] 主页面加载失败: url=%s code=%d desc=%s", safeURL(e.URL), e.Code, e.Description)
		log.Printf("[webview] WebView2 参数: %s；排查：%s", webViewArgs, desktopLogHint(logPath))
		loaded.Store(false)
		if err := page.LoadURL(fallbackDataURL(baseURL, e, dataDir, logPath, webViewArgs)); err != nil {
			log.Printf("[webview] 诊断页也加载失败: %v", err)
		}
	})
	page.OnRenderProcessGone(func(reason string) {
		log.Printf("[webview] 渲染进程退出（reason=%s），1s 后重新加载 %s", reason, baseURL)
		time.AfterFunc(time.Second, func() {
			if err := page.LoadURL(baseURL); err != nil {
				log.Printf("[webview] 重新加载失败: %v", err)
			}
		})
	})
}

func desktopLogHint(logPath string) string {
	if logPath == "" {
		return "日志文件不可用（检查数据目录是否可写）"
	}
	return "日志 " + logPath
}

// safeURL 去掉 query / fragment 再写日志：登录回调里可能带 code、token。
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return raw
	}
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String()
}

// fallbackDataURL 把诊断页编码成 data: URL。页面本身不依赖本机服务，
// 因为失败场景恰恰可能是访问不到本机服务。
func fallbackDataURL(baseURL string, e *mygo.LoadError, dataDir, logPath, webViewArgs string) string {
	code, desc, failedURL := "", "", baseURL
	if e != nil {
		code = fmt.Sprintf("%d", e.Code)
		desc = e.Description
		if e.URL != "" {
			failedURL = e.URL
		}
	}
	logText := logPath
	if logText == "" {
		logText = "(不可用)"
	}
	args := webViewArgs
	if args == "" {
		args = "(未注入)"
	}
	page := fmt.Sprintf(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>页面没能加载</title>
<style>
 :root{color-scheme:dark}
 body{margin:0;padding:40px;background:#14161a;color:#e6e8eb;
      font:15px/1.7 -apple-system,"Segoe UI","Microsoft YaHei",sans-serif}
 main{max-width:760px;margin:0 auto}
 h1{font-size:22px;margin:0 0 8px}
 p{margin:10px 0}
 .err{background:#1f2329;border-left:3px solid #e5534b;padding:12px 16px;
      border-radius:6px;font-family:ui-monospace,Consolas,monospace;font-size:13px;
      white-space:pre-wrap;word-break:break-all}
 ol{padding-left:22px}
 li{margin:8px 0}
 code{background:#252a31;padding:1px 5px;border-radius:4px;font-size:13px}
 button{margin-top:18px;padding:8px 20px;border:0;border-radius:6px;
        background:#3b82f6;color:#fff;font-size:14px;cursor:pointer}
 .muted{color:#9aa4af;font-size:13px}
</style></head><body><main>
<h1>窗口已打开，但页面没能加载</h1>
<p class="muted">本机服务在跑，是这个 WebView 没能把页面取回来。下面是这次失败的真实错误。</p>
<div class="err">URL: %s
code: %s
desc: %s
WebView2 参数: %s
日志: %s</div>
<p><b>最常见的原因</b>：系统代理（Clash / v2ray / 公司组策略代理）把发往
<code>127.0.0.1</code> 的请求也劫走了。更新版本已经默认把回环地址排除出代理
（<code>--proxy-bypass-list</code>）；如果仍然失败，说明代理在更底层拦截。</p>
<ol>
<li><b>先试彻底禁用 WebView2 代理</b>：新建空文件
<code>%s</code>（或设置环境变量 <code>TRP_WEBVIEW_NO_PROXY=1</code>），
然后重启应用。注意：这会让内置浏览器访问外网时也不走代理，验证完可以删掉这个文件。</li>
<li>关掉系统代理 / 代理软件的 TUN 模式后重启应用，看是否恢复。</li>
<li>确认 WebView2 Runtime 正常（Win10/11 自带；可在「设置 → 应用」里修复或重装
Microsoft Edge WebView2 Runtime）。</li>
<li>把上面的日志文件发给开发者；里面有代理参数、端口和完整的启动过程。</li>
</ol>
<button onclick="location.replace(%q)">重试</button>
<p class="muted">如果重试仍然回到这个页面，问题不在临时网络抖动。</p>
</main></body></html>`,
		html.EscapeString(failedURL), html.EscapeString(code), html.EscapeString(desc),
		html.EscapeString(args), html.EscapeString(logText),
		html.EscapeString(markerPathHint(dataDir)), baseURL)
	return "data:text/html;charset=utf-8," + url.PathEscape(page)
}

func markerPathHint(dataDir string) string {
	if dataDir == "" {
		dataDir = DesktopDataDir()
	}
	return filepath.Join(dataDir, noProxyMarker)
}

// desktopProxySummary 给启动日志用：只记录「是否配置」和协议，不落代理凭据。
func desktopProxySummary() string {
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		raw := strings.TrimSpace(os.Getenv(k))
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			return fmt.Sprintf("%s=%s://%s", k, u.Scheme, u.Host)
		}
		return k + "=(已配置)"
	}
	return "未配置"
}
