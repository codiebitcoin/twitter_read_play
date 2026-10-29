// Package bootstrap 组装 store / managers / API 路由，供两种入口共用：
//
//   - 服务模式（main.go，Docker / 裸进程）：监听 addr，对外提供 Web + REST。
//   - 桌面模式（-desktop，MyGo 窗口）：先起 127.0.0.1 固定端口段（7991 起，
//     被占顺延）的本机服务，再用系统 WebView 打开窗口加载它；
//     同一份嵌入前端与 API，代码零分叉。
package bootstrap

import (
	"context"
	"embed"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"twitter_read_play/internal/api"
	"twitter_read_play/internal/book"
	"twitter_read_play/internal/browser"
	"twitter_read_play/internal/guides"
	"twitter_read_play/internal/livechat"
	"twitter_read_play/internal/machine"
	"twitter_read_play/internal/netcfg"
	"twitter_read_play/internal/role"
	"twitter_read_play/internal/source"
	"twitter_read_play/internal/store"
	"twitter_read_play/internal/twitter"
	"twitter_read_play/internal/upstream"
	"twitter_read_play/internal/watch"
)

type Options struct {
	Addr    string // 服务模式监听地址
	DataDir string // SQLite 数据目录
	APIKey  string // socialdata.tools API Key
	BaseURL string // socialdata API 地址（[api] base_url，空=官方 api.socialdata.tools）
	Static  embed.FS
	Version string
	Desktop bool // 桌面模式（ServeDesktop 会自动置位）
	PCMode  bool // PC 模式（-ldflags "-X main.pcMode=1" / TRP_PC_MODE=1）：无公网回调，前端隐藏 webhook 配置；桌面模式强制置位

	// Origin 固定的对外 origin（http://localhost:真实端口）。桌面模式由 ServeDesktop
	// 探得端口后注入，api 层的 OAuth 回调一律用它拼地址（host 固定，不可配置）。
	Origin string

	// browserDriver 桌面模式的 mygo 窗口驱动；服务模式为 nil。
	browserDriver browser.Driver

	// desktopLogPath 桌面模式日志文件（windowsgui 下 stderr 不可见）；webViewArgs
	// 是注入 WebView2 的浏览器参数。二者由 ServeDesktop 填好，供诊断页展示。
	desktopLogPath string
	webViewArgs    string
}

// Build 初始化全部管理器并返回路由。
func Build(opt Options) (http.Handler, error) {
	st, err := store.New(opt.DataDir)
	if err != nil {
		return nil, err
	}
	// 直播弹幕申请教程：HTML 打进二进制，SQLite provider_guides 当索引
	if err := guides.Seed(st); err != nil {
		return nil, err
	}
	mm, err := machine.NewManager(st)
	if err != nil {
		return nil, err
	}
	rm, err := role.NewManager(st)
	if err != nil {
		return nil, err
	}
	provider := &twitter.Socialdata{APIKey: opt.APIKey, BaseURL: opt.BaseURL}
	um, err := upstream.NewManager(st)
	if err != nil {
		return nil, err
	}
	um.SetDefaultBaseURL(opt.BaseURL)
	// 浏览器会话：
	//   桌面 → mygo 内置 WebView（opt.browserDriver）
	//   Web   → 远程 chrome-driverless（地址来自设置页，改完立即生效）
	// 两者实现同一个 browser.Driver，平台 adapter 不需要分叉。
	driver := opt.browserDriver
	if driver == nil {
		driver = browser.NewRemoteDriver(func() string {
			v, _ := st.GetSetting("chrome_driverless_url")
			return v
		})
	}
	bm := browser.NewManager(driver, st)
	// 新渠道模型：注册直接 Feed 适配器 + 浏览器 Feed/Live 适配器。
	reg := source.NewRegistry()
	if err := upstream.RegisterFeedAdapters(reg, func() string {
		k, _ := st.GetSetting("socialdata_api_key")
		return k
	}); err != nil {
		return nil, err
	}
	if err := browser.RegisterFeedAdapters(reg, bm); err != nil {
		return nil, err
	}
	if err := livechat.RegisterBilibiliLiveAdapter(reg, bm); err != nil {
		return nil, err
	}
	if err := livechat.RegisterBrowserLiveAdapters(reg, bm); err != nil {
		return nil, err
	}
	cm, err := source.NewManager(st)
	if err != nil {
		return nil, err
	}
	mode := source.ModeWeb
	if opt.Desktop {
		mode = source.ModeDesktop
	}
	if err := cm.EnsureLiveDefaults(reg); err != nil {
		return nil, err
	}
	if err := cm.EnsureBrowserChannels(mode, reg); err != nil {
		return nil, err
	}
	// 直播运行时：KindSource 配置通过新渠道 + 适配器动态构造 Streamer。
	lcm, err := livechat.NewManagerWithFactory(st, makeLiveFactory(cm, reg),
		livechat.WithBilibiliCookieProvider(func(ctx context.Context) string {
			return livechat.BilibiliCookieFromBrowser(ctx, bm)
		}))
	if err != nil {
		return nil, err
	}
	if err := reconcileRemovedLiveChannels(cm, rm, lcm, reg); err != nil {
		return nil, err
	}
	wm, err := watch.NewManager(st, provider, um, mm, rm)
	if err != nil {
		return nil, err
	}
	wm.SetSource(cm, reg, mode)
	// sqlite 里保存的 key / base_url 优先于 env
	if dbKey, _ := st.GetSetting("socialdata_api_key"); dbKey != "" {
		provider.SetKey(dbKey)
	}
	if dbBase, _ := st.GetSetting("socialdata_base_url"); dbBase != "" {
		provider.SetBaseURL(dbBase)
		um.SetDefaultBaseURL(dbBase)
	}
	// 数据库里配过才覆盖进程环境。只写了 NO_HTTP 时，代理地址仍用部署环境里的 HTTP_PROXY。
	p, _ := st.GetSetting("http_proxy")
	n, _ := st.GetSetting("no_http")
	p, n = strings.TrimSpace(p), strings.TrimSpace(n)
	if p != "" || n != "" {
		if p == "" {
			p = os.Getenv("HTTPS_PROXY")
			if p == "" {
				p = os.Getenv("HTTP_PROXY")
			}
		}
		netcfg.Apply(p, n)
	}
	// 图书（角色 Book 页）：书库 + 朗读器。
	// 书归属角色、不跨角色共享；启动时给升级前的旧书补归属（只写 owner，不删任何书）。
	lib, err := book.NewLibrary(st)
	if err != nil {
		return nil, err
	}
	lib.LoadProgress()
	roles := rm.List()
	roleIDs := make([]string, 0, len(roles))
	for _, r := range roles {
		roleIDs = append(roleIDs, r.ID)
	}
	lib.MigrateOwners(roleIDs, legacyBookOwner(roles))
	wm.SetBooks(lib)
	wm.SetLive(lcm)
	wm.Start()

	srv := &api.Server{
		Machines: mm, Roles: rm, Watches: wm, Store: st, Upstreams: um, Live: lcm, Static: opt.Static,
		Channels: cm, Adapters: reg, Mode: mode, Browser: bm,
		EnvAuthUser: os.Getenv("AUTH_USER"), EnvAuthPass: os.Getenv("AUTH_PASS"),
		Desktop: opt.Desktop,
		PCMode:  opt.PCMode || opt.Desktop, // 桌面打包天然是 PC：没有公网回调
		Origin:  opt.Origin,
	}
	return srv.Routes(), nil
}

// legacyBookOwner 升级前旧书（没有 RoleID）的统一归属目标：
// 优先 twitter 账号 elonmusk 的角色，其次展示名里带 Elon / 马斯克 的角色。
// 找不到返回空 = 按朗读进度 / 单角色兜底（见 book.Library.MigrateOwners）。
func legacyBookOwner(roles []*role.Role) string {
	target := func(r *role.Role) string {
		s := strings.ToLower(strings.TrimSpace(r.Username))
		s = strings.TrimPrefix(s, "@")
		s = strings.TrimSuffix(s, "/")
		if i := strings.LastIndex(s, "/"); i >= 0 {
			s = s[i+1:] // 主页 URL 形式
		}
		return s
	}
	for _, r := range roles {
		if target(r) == "elonmusk" {
			return r.ID
		}
	}
	for _, r := range roles {
		n := strings.ToLower(r.Name)
		if strings.Contains(n, "elon") || strings.Contains(r.Name, "马斯克") ||
			strings.Contains(strings.ToLower(r.FeedTarget), "elonmusk") {
			return r.ID
		}
	}
	return ""
}

// Serve 阻塞运行服务模式。
func Serve(opt Options) error {
	mux, err := Build(opt)
	if err != nil {
		return err
	}
	log.Printf("twitter_read_play %s 监听 %s（数据目录 %s）", opt.Version, opt.Addr, opt.DataDir)
	return http.ListenAndServe(opt.Addr, mux)
}

// desktopStartPort 桌面模式监听端口起点。OAuth 回调 host 固定为 localhost+真实端口，
// 端口要预先登记进 Twitch/Google 后台，必须稳定可预期：不从随机端口分配，
// 也不用 8080 这类易撞车的常用端口。被占用时顺延 +1。
const desktopStartPort = 7991

// listenDesktop 绑定 127.0.0.1，端口从 desktopStartPort 起找，被占用就 +1 换下一个。
func listenDesktop() (net.Listener, error) {
	var err error
	for port := desktopStartPort; port < desktopStartPort+64; port++ {
		var ln net.Listener
		ln, err = net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln, nil
		}
	}
	return nil, err
}

// ServeDesktop 起本机服务（7991 起，被占顺延），再打开 MyGo 窗口加载它（阻塞到窗口关闭）。
func ServeDesktop(opt Options) error {
	opt.Desktop = true
	if opt.DataDir == "" {
		opt.DataDir = DesktopDataDir()
	}
	// 日志先落盘：windowsgui 产物没有控制台，白屏时否则一点线索都没有。
	logPath, closeLog := setupDesktopLog(opt.DataDir)
	defer closeLog()
	opt.desktopLogPath = logPath
	// WebView2 的环境在第一次开窗时创建，参数必须在这之前注入进程环境。
	opt.webViewArgs = applyWebViewProxyArgs(opt.DataDir)
	log.Printf("twitter_read_play %s 启动（%s/%s，数据目录 %s）",
		opt.Version, runtime.GOOS, runtime.GOARCH, opt.DataDir)
	if logPath != "" {
		log.Printf("日志文件: %s", logPath)
	}
	if opt.webViewArgs != "" {
		log.Printf("WebView2 浏览器参数: %s", opt.webViewArgs)
	}
	log.Printf("进程代理: %s（只影响本进程的 HTTP 客户端，WebView2 走系统代理）", desktopProxySummary())

	drv := newMygoDriver()
	opt.browserDriver = drv
	// 先探端口再 Build：OAuth 回调 origin（localhost+真实端口）要随 opt 传进 api.Server
	ln, err := listenDesktop()
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	opt.Origin = fmt.Sprintf("http://localhost:%d", port)
	base := opt.Origin + "/"
	mux, err := Build(opt)
	if err != nil {
		_ = ln.Close()
		return err
	}
	go func() {
		if err := http.Serve(ln, mux); err != nil && err != http.ErrServerClosed {
			log.Printf("本地服务退出: %v", err)
			os.Exit(1)
		}
	}()

	// 等服务就绪再开窗口（store 初始化很快，这里只是兜底）。
	// 走 base（localhost）探测，顺带验证窗口要用的入口真实可达。
	// 用直连 client：进程里可能配了 http_proxy，探测不该依赖代理实现细节。
	probe := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	ready := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := probe.Get(base + "api/health")
		if err == nil {
			resp.Body.Close()
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		log.Printf("警告: %s 5s 内没能自测通过，仍然开窗（窗口加载失败时会显示诊断页）", base)
	}
	log.Printf("twitter_read_play %s 桌面模式（本地服务 %s，数据目录 %s）", opt.Version, base, opt.DataDir)
	log.Printf("OAuth 回调（登记到 Twitch / Google 后台）: %s/callback/twitch 、 %s/callback/google", opt.Origin, opt.Origin)
	return runWindow(opt, base, drv)
}

// DesktopDataDir 桌面模式默认数据目录：各平台用户配置目录下的 TwitterReadPlay。
func DesktopDataDir() string {
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, "TwitterReadPlay")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".twitter_read_play")
}

// reconcileRemovedLiveChannels 升级清理（幂等）：删掉 adapter 已移除的渠道，
// 并把角色 / livechat KindSource 配置里指向被删渠道的绑定迁到替代渠道。
//
// 历史背景：Live 域去重后只留 twitch_browser / bilibili_blivedm / youtube_browser
// 三个入口（docs/PLAN-live-channel-dedupe.md），twitch_eventsub / youtube_live_api /
// chatdl_go / bilibili_browser 的默认渠道随之消失。没有确凿替代渠道时清空绑定，
// 交给自动解析（空 = auto）。
func reconcileRemovedLiveChannels(cm *source.Manager, rm *role.Manager, lcm *livechat.Manager, reg *source.Registry) error {
	pruned, err := cm.PruneMissingAdapters(reg)
	if err != nil {
		return err
	}
	if len(pruned) == 0 {
		return nil
	}
	wasPruned := map[string]bool{}
	for _, id := range pruned {
		wasPruned[id] = true
	}
	remap := func(chID, platform string) string {
		if !wasPruned[chID] {
			return chID
		}
		switch chID {
		case "ch-browser-bilibili":
			return "ch-live-bilibili"
		case "ch-live-twitch-eventsub":
			return "ch-browser-twitch"
		case "ch-live-youtube-api":
			return "ch-browser-youtube"
		case "ch-live-chatdl-go":
			if strings.EqualFold(platform, "twitch") {
				return "ch-browser-twitch"
			}
			if strings.EqualFold(platform, "youtube") {
				return "ch-browser-youtube"
			}
			return ""
		default:
			return ""
		}
	}
	changed := 0
	for _, r := range rm.List() {
		next := strings.TrimSpace(remap(r.LiveChannelID, r.LivePlatform))
		if next == r.LiveChannelID {
			continue
		}
		r.LiveChannelID = next
		if err := rm.Upsert(r); err != nil {
			return err
		}
		changed++
	}
	if changed > 0 {
		log.Printf("[upgrade] 迁移 %d 个角色的 Live 渠道绑定", changed)
	}
	_, err = lcm.RemapSourceChannels(remap)
	return err
}

// makeLiveFactory 把 KindSource 配置解析成具体渠道 + Live 适配器。
func makeLiveFactory(cm *source.Manager, reg *source.Registry) func(livechat.SourceConfig) (livechat.Streamer, error) {
	return func(cfg livechat.SourceConfig) (livechat.Streamer, error) {
		ch, err := cm.Get(cfg.ChannelID)
		if err != nil {
			return nil, err
		}
		a, ok := reg.Get(ch.AdapterID)
		if !ok {
			return nil, fmt.Errorf("适配器不存在: %s", ch.AdapterID)
		}
		la, ok := source.AsLive(a)
		if !ok {
			return nil, fmt.Errorf("适配器 %s 不支持 Live", ch.AdapterID)
		}
		t := source.Target{
			Domain: source.DomainLive, Platform: source.Platform(cfg.TargetPlatform),
			Kind: source.TargetKind(cfg.TargetKind), ID: cfg.TargetID, Raw: cfg.Channel,
		}
		w, err := la.OpenLive(ch, t)
		if err != nil {
			return nil, err
		}
		return livechat.WrapLiveWorker(w), nil
	}
}
