package bootstrap

import (
	"fmt"
	"log"

	"github.com/egoist/mygo"
)

// runWindow 打开 MyGo 桌面窗口加载本机服务，阻塞到应用退出。
// 页面通过 HTTP 与同一进程内的 API 通信，前端零改动（与 Docker 模式完全一致）。
// shutdown 是退出善后（停轮询/直播/浏览器窗，见 Build），退出路径都会触发它。
func runWindow(opt Options, baseURL string, drv *mygoDriver, shutdown func()) error {
	// 菜单退出 / 信号（SIGINT/SIGTERM）等不经过主窗口 OnClosed 的路径，也要善后。
	// 善后异步跑：Quit 之后主进程很快退出，不能反过来卡住退出链路。
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { go shutdown() })
	mygo.App.WhenReady(func() {
		drv.markReady()
		ver := opt.Version
		// mygo build 会把 mygo.json 的版本链进二进制；没有 -ldflags 注入时用它
		if v := mygo.App.Version(); v != "" && (ver == "" || ver == "dev") {
			ver = v
		}
		log.Printf("[webview] 打开主窗口: %s", baseURL)
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     fmt.Sprintf("Twitter Read Play %s", ver),
			URL:       baseURL,
			Width:     1280,
			Height:    840,
			MinWidth:  960,
			MinHeight: 600,
			StateKey:  "main", // 记住窗口位置与大小
		})
		if win == nil {
			log.Printf("[webview] 主窗口创建失败（WebView2 环境不可用？）")
			return
		}
		// 主窗口 X = 退出程序：内置浏览器渠道的隐藏抓取工作窗常驻不关，
		// mygo 的「最后一个窗口关闭」退不出来，进程会缩在后台继续轮询和播报
		//（Windows 上没有任何可见痕迹）。这里关主窗就主动退出。
		win.OnClosed(func() {
			go shutdown()
			mygo.App.Quit()
		})
		// 白屏时不留下任何线索是最糟的情况：加载失败直接换成能看的诊断页。
		attachMainWindowDiagnostics(win, baseURL, opt.DataDir, opt.desktopLogPath, opt.webViewArgs)
	})
	return mygo.App.Run()
}
