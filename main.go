// twitter_read_play：Twitter 新推文 → Codie 播报服务。
//
// 两种运行方式：
//   - 服务模式（默认）：Web 页面 + REST API，Docker / 裸进程部署。
//   - 桌面模式（-desktop）：MyGo 桌面窗口加载同一份嵌入前端与本机 API。
package main

import (
	"embed"
	"flag"
	"log"
	"os"
	"runtime"

	"twitter_read_play/internal/bootstrap"
)

//go:embed all:front/dist
var webFS embed.FS

var version = "dev"

// PC 模式（打包期注入）：-ldflags "-X main.pcMode=1"。
// PC 部署没有公网可达的回调地址，webhook 反向通知配了也收不到，
// 因此前端据 /api/setup/status 的 pc_mode 隐藏「Webhook push (socialdata.tools
// Monitoring)」等回调配置。桌面打包（exe/AppImage/.app，-desktop）自动视为 PC 模式。
var pcMode = "0"

func main() {
	var (
		addr    = flag.String("addr", envOr("LISTEN_ADDR", ":8080"), "HTTP 监听地址")
		dataDir = flag.String("data", "", "数据目录（默认：服务模式 ./data，桌面模式用户配置目录）")
		apiKey  = flag.String("socialdata-key", envOr("SOCIALDATA_API_KEY", ""), "socialdata.tools API Key")
		baseURL = flag.String("socialdata-base", envOr("SOCIALDATA_BASE_URL", ""), "socialdata API 地址（[api] base_url，空=官方；可指向自建兼容服务）")
		desktop = flag.Bool("desktop", envOr("TRP_DESKTOP", bootstrap.DesktopDefault()) == "1", "以桌面窗口模式运行（MyGo）")
		showVer = flag.Bool("version", false, "打印版本")
	)
	flag.Parse()

	if *showVer {
		log.Printf("twitter_read_play %s (%s/%s)", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	opt := bootstrap.Options{
		Addr: *addr, APIKey: *apiKey, BaseURL: *baseURL, Static: webFS, Version: version,
		PCMode: pcMode == "1" || envOr("TRP_PC_MODE", "") == "1",
	}
	if *dataDir != "" {
		opt.DataDir = *dataDir
	} else if *desktop {
		opt.DataDir = bootstrap.DesktopDataDir() // ~/Library/Application Support、%APPDATA% 等
	} else {
		opt.DataDir = envOr("DATA_DIR", "data")
	}

	var err error
	if *desktop {
		err = bootstrap.ServeDesktop(opt)
	} else {
		err = bootstrap.Serve(opt)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
