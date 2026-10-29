package bootstrap

import (
	"fmt"
	"log"

	"github.com/egoist/mygo"
)

// runWindow 打开 MyGo 桌面窗口加载本机服务，阻塞到窗口关闭（应用退出）。
// 页面通过 HTTP 与同一进程内的 API 通信，前端零改动（与 Docker 模式完全一致）。
func runWindow(opt Options, baseURL string, drv *mygoDriver) error {
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
		// 白屏时不留下任何线索是最糟的情况：加载失败直接换成能看的诊断页。
		attachMainWindowDiagnostics(win, baseURL, opt.DataDir, opt.desktopLogPath, opt.webViewArgs)
	})
	return mygo.App.Run()
}
