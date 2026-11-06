// Package api HTTP 接口层（stdlib net/http，Go 1.22 method pattern）。
// 登录：POST /api/login 换 session cookie；其余接口与页面需已登录。
// 页面路由（SPA，子目录）：/machines、/roles、/roles/{id}、/settings。
package api

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"twitter_read_play/internal/assets"
	"twitter_read_play/internal/browser"
	"twitter_read_play/internal/guides"
	"twitter_read_play/internal/i18n"
	"twitter_read_play/internal/livechat"
	"twitter_read_play/internal/machine"
	"twitter_read_play/internal/role"
	"twitter_read_play/internal/source"
	"twitter_read_play/internal/store"
	"twitter_read_play/internal/upstream"
	"twitter_read_play/internal/watch"
)

type Server struct {
	Machines  *machine.Manager
	Roles     *role.Manager
	Watches   *watch.Manager
	Store     *store.Store
	Upstreams *upstream.Manager
	Live      *livechat.Manager
	// 新渠道模型（Phase 1+）：Channels 存渠道，Adapters 是能力注册表。
	Channels *source.Manager
	Adapters *source.Registry
	Mode     source.Mode
	Browser  *browser.Manager

	provisionMu sync.Mutex // 初始角色资源下发串行化

	// Feed 目标识别：进程内缓存 + 同目标串行 + 全局并发上限。
	inspectMu    sync.Mutex
	inspectLocks map[string]*sync.Mutex
	inspectCache map[string]inspectCacheEntry
	inspectSem   chan struct{}

	vrmMu  sync.Mutex // 全局 VRM 上传 job（单例，防重复上传）
	vrmCur *vrmJob

	EnvAuthUser string // env AUTH_USER（兜底）
	EnvAuthPass string // env AUTH_PASS
	Static      embed.FS

	logins *loginGuard // /api/login 防爆破（进程内存态）

	// Desktop 桌面模式（本机单机窗口）：不做账号密码，但 Host/Origin/桌面凭据
	// 仍校验（见 auth 内的 Desktop 分支与 desktopStart）。服务端部署不设该字段。
	Desktop bool

	// DesktopToken 桌面模式凭据（ServeDesktop 每次启动随机生成）。
	// 为空（内嵌调用方未注入）时跳过凭据校验，维持旧行为。
	DesktopToken string

	// PCMode PC 模式（打包期 -X main.pcMode=1 / 桌面打包自动置位）：无公网回调地址，
	// /api/setup/status 返回 pc_mode=true，前端隐藏 webhook 反向通知等回调配置。
	PCMode bool

	// Origin 固定的对外 origin（http://localhost:真实端口，桌面模式由 ServeDesktop 注入）。
	// 非空时 OAuth 回调一律用它拼，不跟随请求 Host —— PC 端回调 host 固定不可编辑。
	Origin string
}

const sessionCookie = "trp_session"

// DefaultAuthUser 登录用户名兜底（db / env 都未配置时）。密码不再有默认值
// （M3：默认 admin/11111 可直接进后台）——未配置密码时登录页转为「首次设置」，
// 或用 AUTH_PASS env 预置。
const DefaultAuthUser = "admin"

//go:embed login.html
var loginHTML []byte

func (s *Server) authUser() string {
	if v, _ := s.Store.GetSetting("auth_user"); v != "" {
		return v
	}
	if s.EnvAuthUser != "" {
		return s.EnvAuthUser
	}
	return DefaultAuthUser
}

// loginGuard 惰性创建（Server 由调用方按字段构造，不强制经过 New）。
func (s *Server) loginGuard() *loginGuard {
	if s.logins == nil {
		s.logins = newLoginGuard()
	}
	return s.logins
}

// authPass 生效的口令存储值：DB（明文或 bcrypt: 哈希）> env AUTH_PASS（明文）。
// 都未配置时返回 ""（未初始化，登录一律失败，登录页转首次设置流程）。
// 注意：返回的是存储原文（可能是哈希），校验请走 verifyStoredPass。
func (s *Server) authPass() (v string, fromDB bool) {
	if v, _ := s.Store.GetSetting("auth_pass"); v != "" {
		return v, true
	}
	if s.EnvAuthPass != "" {
		return s.EnvAuthPass, false
	}
	return "", false
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { s.json(w, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)
	// 首次设置账号（M3：去掉默认密码后的初始化入口）；状态查询给登录页切换表单用
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/setup/auth", s.setupAuth)
	// 桌面窗口入口（H4）：?t= 换会话 cookie，仅桌面模式可达
	mux.HandleFunc("GET /desktop-start", s.desktopStart)

	// 机器
	mux.HandleFunc("GET /api/machines", s.listMachines)
	mux.HandleFunc("POST /api/machines", s.addMachine)
	// 局域网扫描：给「添加设备」表单自动发现设备地址（探测本机网段内的设备端口）
	mux.HandleFunc("GET /api/machines/discover", s.discoverMachines)
	mux.HandleFunc("DELETE /api/machines/{id}", s.delMachine)
	mux.HandleFunc("POST /api/machines/{id}/pair/start", s.pairStart)
	mux.HandleFunc("POST /api/machines/{id}/pair/confirm", s.pairConfirm)
	mux.HandleFunc("POST /api/machines/{id}/pair/cancel", s.pairCancel)
	mux.HandleFunc("POST /api/machines/{id}/roles", s.machineToggleRoles)
	mux.HandleFunc("GET /api/machines/{id}/status", s.machineStatus)
	mux.HandleFunc("GET /api/machines/{id}/voices", s.machineVoices)
	// 设备诊断：system / 各槽 character / 已上传模型（排查卡住的下载任务）
	mux.HandleFunc("GET /api/machines/{id}/probe/device", s.machineDeviceProbe)

	// 内置 VRM 资源（角色页 VRM 缩略图渲染用）
	mux.HandleFunc("GET /api/assets/seed.vrm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "model/gltf-binary")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(assets.SeedVRM)
	})

	// 角色
	mux.HandleFunc("GET /api/roles", s.listRoles)
	mux.HandleFunc("GET /api/roles/{id}", s.getRole)
	mux.HandleFunc("POST /api/roles", s.upsertRole)
	mux.HandleFunc("PUT /api/roles/{id}", s.upsertRole)
	mux.HandleFunc("DELETE /api/roles/{id}", s.delRole)
	mux.HandleFunc("GET /api/roles/{id}/tweets", s.roleTweets)
	mux.HandleFunc("POST /api/roles/{id}/tweets/select", s.tweetSelect)
	mux.HandleFunc("POST /api/roles/{id}/tweets/edit", s.tweetEdit)
	mux.HandleFunc("POST /api/roles/{id}/broadcast", s.roleBroadcast)
	mux.HandleFunc("POST /api/roles/{id}/poll", s.rolePoll)
	mux.HandleFunc("POST /api/roles/{id}/random", s.roleRandomMode) // random 模式：立即按规则随机播
	mux.HandleFunc("GET /api/roles/{id}/parody", s.roleParodyList)
	mux.HandleFunc("POST /api/roles/{id}/parody/generate", s.roleParodyGenerate)
	mux.HandleFunc("DELETE /api/roles/{id}/parody/{tid}", s.roleParodyDelete)
	mux.HandleFunc("POST /api/roles/{id}/sync", s.roleSync)
	mux.HandleFunc("POST /api/roles/{id}/check", s.roleCheck)
	mux.HandleFunc("POST /api/roles/{id}/apply", s.roleApplyConfig)
	mux.HandleFunc("POST /api/roles/{id}/vrm/push", s.roleVRMPush)
	mux.HandleFunc("GET /api/roles/{id}/voice", s.roleVoiceCheck)
	mux.HandleFunc("POST /api/roles/{id}/voice/reclone", s.roleVoiceReclone)
	mux.HandleFunc("GET /api/clone/tasks", s.cloneTasks)
	mux.HandleFunc("POST /api/roles/{id}/voice/audition", s.roleVoiceAudition)
	mux.HandleFunc("POST /api/roles/{id}/vrm", s.roleVRMUpload)
	mux.HandleFunc("GET /api/vrm/job", s.vrmJobGet)
	mux.HandleFunc("GET /api/roles/{id}/vrm/file", s.roleVrmFile)
	mux.HandleFunc("POST /api/roles/{id}/icon", s.roleIconUpload)
	mux.HandleFunc("GET /api/roles/{id}/icon", s.roleIconFile)
	mux.HandleFunc("POST /api/roles/{id}/vrm/select", s.roleVrmSelect)
	mux.HandleFunc("POST /api/roles/{id}/vrm/rename", s.roleVrmRename)
	mux.HandleFunc("POST /api/roles/{id}/voice/select", s.roleVoiceSelect)
	mux.HandleFunc("DELETE /api/roles/{id}/vrm", s.roleVrmDelete)
	mux.HandleFunc("DELETE /api/roles/{id}/voice", s.roleVoiceDelete)
	mux.HandleFunc("GET /api/roles/{id}/asset", s.roleAssetFile)
	mux.HandleFunc("POST /api/roles/{id}/clone", s.roleClone)

	// 上游数据源
	mux.HandleFunc("GET /api/upstreams", s.listUpstreams)
	mux.HandleFunc("GET /api/upstreams/apis", s.listUpstreamAPIs)
	mux.HandleFunc("POST /api/upstreams", s.upsertUpstream)
	mux.HandleFunc("PUT /api/upstreams/{id}", s.upsertUpstream)
	mux.HandleFunc("DELETE /api/upstreams/{id}", s.deleteUpstream)
	mux.HandleFunc("GET /api/upstreams/{id}/test", s.testUpstream)

	// 新渠道模型：Feed/Live 共用；Upstreams 页读只读 Binding 视图
	mux.HandleFunc("GET /api/channels", s.listChannels)
	mux.HandleFunc("POST /api/channels", s.upsertChannel)
	mux.HandleFunc("PUT /api/channels/{id}", s.upsertChannel)
	mux.HandleFunc("POST /api/channels/{id}/disable", s.disableChannel)
	mux.HandleFunc("DELETE /api/channels/{id}", s.deleteChannel)
	mux.HandleFunc("POST /api/channels/{id}/test", s.testChannel)
	mux.HandleFunc("GET /api/source/adapters", s.listAdapters)
	mux.HandleFunc("POST /api/source/parse", s.parseSource)
	mux.HandleFunc("POST /api/source/resolve", s.resolveSource)
	mux.HandleFunc("POST /api/source/inspect", s.inspectSource)
	mux.HandleFunc("POST /api/source/view", s.viewSource)
	mux.HandleFunc("GET /api/source/bindings", s.listBindings)

	// 桌面内置浏览器会话（登录/探针/token/退出）
	mux.HandleFunc("GET /api/browser/session", s.listBrowserSessions)
	mux.HandleFunc("POST /api/browser/session/{provider}/open", s.openBrowserLogin)
	mux.HandleFunc("GET /api/browser/session/{provider}", s.probeBrowserSession)
	mux.HandleFunc("POST /api/browser/session/{provider}/probe", s.probeBrowserSession)
	mux.HandleFunc("GET /api/browser/session/{provider}/tokens", s.browserTokens)
	mux.HandleFunc("POST /api/browser/session/{provider}/logout", s.logoutBrowserSession)

	// 日志 / 设置 / 配置状态（首页「导航页」一次拿全，避免串 3 个请求）
	mux.HandleFunc("GET /api/logs", s.listLogs)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("GET /api/setup/status", s.setupStatus)
	mux.HandleFunc("POST /api/system/open/local-network", s.openLocalNetworkSettings)

	// socialdata.tools webhook 反向通知（公网可达，靠 HMAC 签名校验而非登录态）
	mux.HandleFunc("POST /api/webhook/socialdata", s.socialdataWebhook)

	// 角色的 socialdata webhook 监控（创建后该角色走反向通知，不再轮询）
	mux.HandleFunc("POST /api/roles/{id}/monitor", s.roleMonitorCreate)
	mux.HandleFunc("DELETE /api/roles/{id}/monitor", s.roleMonitorDelete)

	// 共享 search monitor（一条 from:a OR from:b… 打包全部角色，模式 C）
	mux.HandleFunc("GET /api/monitor/search", s.searchMonitorGet)
	mux.HandleFunc("POST /api/monitor/search", s.searchMonitorSync)
	mux.HandleFunc("DELETE /api/monitor/search", s.searchMonitorDelete)

	// 图书（角色 Book 页）：书归属角色、不跨角色共享；上传 → 拆章节 → 点章/点段/随机朗读
	mux.HandleFunc("GET /api/roles/{id}/books", s.roleBookList)
	mux.HandleFunc("POST /api/roles/{id}/books", s.roleBookUpload)
	mux.HandleFunc("POST /api/roles/{id}/books/claim", s.roleBookClaim)
	mux.HandleFunc("GET /api/books/state", s.bookState)
	mux.HandleFunc("GET /api/roles/{id}/books/{book_id}", s.roleBookGet)
	mux.HandleFunc("DELETE /api/roles/{id}/books/{book_id}", s.roleBookDelete)
	mux.HandleFunc("POST /api/roles/{id}/books/{book_id}/redecode", s.roleBookRedecode)
	mux.HandleFunc("GET /api/roles/{id}/books/{book_id}/chapters/{idx}", s.roleBookChapter)
	mux.HandleFunc("POST /api/roles/{id}/book/play", s.roleBookPlay)
	mux.HandleFunc("POST /api/roles/{id}/book/random", s.roleBookRandom)
	mux.HandleFunc("POST /api/roles/{id}/book/stop", s.roleBookStop)
	mux.HandleFunc("POST /api/roles/{id}/book/progress", s.roleBookProgress)

	// 播报队列（所有播报统一入队、按机器串行消费；前端全局弹窗轮询）
	mux.HandleFunc("GET /api/broadcast/queue", s.broadcastQueueGet)
	mux.HandleFunc("DELETE /api/broadcast/queue", s.broadcastQueueClear)
	mux.HandleFunc("DELETE /api/broadcast/queue/{id}", s.broadcastQueueDeleteItem)

	// 直播弹幕源（Twitch EventSub / YouTube Live API / chat_downloader，kind 可配置）
	mux.HandleFunc("GET /api/livechat", s.listLivechat)
	mux.HandleFunc("POST /api/livechat", s.saveLivechat)
	mux.HandleFunc("POST /api/livechat/random", s.randomLivechat)
	mux.HandleFunc("DELETE /api/livechat/{id}", s.deleteLivechat)
	mux.HandleFunc("POST /api/livechat/{id}/start", s.livechatStart)
	mux.HandleFunc("POST /api/livechat/{id}/stop", s.livechatStop)
	mux.HandleFunc("GET /api/livechat/{id}/messages", s.livechatMessages)
	mux.HandleFunc("PUT /api/livechat/oauth", s.saveLivechatOAuth)
	mux.HandleFunc("GET /api/livechat/oauth/{provider}/start", s.livechatOAuthStart)
	mux.HandleFunc("GET /api/livechat/oauth/{provider}/callback", s.livechatOAuthCallback)
	mux.HandleFunc("POST /api/livechat/oauth/{provider}/logout", s.livechatOAuthLogout)
	// 对外登记用的统一回调规则：/callback/<平台>（/callback/twitch、/callback/google），
	// 以后新平台直接多一段。旧路径保留兼容已登记过的人。
	mux.HandleFunc("GET /callback/{platform}", s.livechatOAuthCallback)
	// 分来源申请教程：HTML 在 internal/guides/html，索引在 SQLite provider_guides
	mux.HandleFunc("GET /api/providers/{kind}/guide", guides.Serve(s.Store))

	// ChatRoom 多角色对话接播 + 对话脚本持久化
	mux.HandleFunc("POST /api/chatroom/play", s.chatRoomPlay)
	mux.HandleFunc("POST /api/chatroom/stop", s.chatRoomStop)
	mux.HandleFunc("GET /api/chatroom/scripts", s.chatScriptList)
	mux.HandleFunc("POST /api/chatroom/scripts", s.chatScriptSave)
	mux.HandleFunc("DELETE /api/chatroom/scripts/{id}", s.chatScriptDelete)

	// 消息队列（播报 + 设备操作统一展示；前端 MessageQueue 面板轮询）
	mux.HandleFunc("GET /api/messages/queue", s.messageQueueGet)

	// 静态页 + SPA 子目录回退（登录后走这里）
	mux.HandleFunc("GET /", s.serveStatic)

	return s.auth(mux)
}

// auth 登录拦截：未登录时页面渲染登录页，API 返回 401 JSON。
// serveStatic 静态资源（React 构建产物 front/dist）+ SPA 子目录回退。
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/" || path == "/login" {
		path = "/index.html"
	} else if !strings.Contains(path[1:], ".") {
		path = "/index.html" // 子目录路由回退到 SPA 入口
	}
	b, err := fs.ReadFile(s.Static, "front/dist"+path)
	if err != nil {
		b, _ = fs.ReadFile(s.Static, "front/dist/index.html")
		path = "/index.html"
	}
	switch {
	case strings.HasSuffix(path, ".js"):
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case strings.HasSuffix(path, ".css"):
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case strings.HasSuffix(path, ".png"):
		w.Header().Set("Content-Type", "image/png")
	case strings.HasSuffix(path, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	case strings.HasSuffix(path, ".woff2"):
		w.Header().Set("Content-Type", "font/woff2")
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	// index.html no-cache 保证发版即生效；带内容哈希的静态资源可长缓存
	if path == "/index.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Write(b)
}

// publicPaths 免登录路径：登录、健康检查，以及公网回调 ——
// socialdata webhook 靠 X-Signature HMAC 自校验（见 webhook.go），不依赖登录态。
// 之前只放行前两个，公网推送被 auth 拦成 401（模式 C 反向通知实际不可用）。
var publicPaths = map[string]bool{
	"/api/login":              true,
	"/api/health":             true,
	"/api/webhook/socialdata": true,
	// 首次设置账号与其状态查询（setupAuth 自身仅在未配置口令时生效，见 setupAuth）
	"/api/auth/status": true,
	"/api/setup/auth":  true,
	// 桌面窗口入口：?t= 校验通过后种会话 cookie（desktopStart 自身校验 token）
	"/desktop-start": true,
}

func isPublicPath(path string) bool {
	if publicPaths[path] {
		return true
	}
	// Google / Twitch 授权完成后会 302 到 callback，此时没有我们的 session cookie。
	// /callback/<平台> 是对外登记的统一回调规则，同样免登录。
	return (strings.HasPrefix(path, "/api/livechat/oauth/") && strings.HasSuffix(path, "/callback")) ||
		strings.HasPrefix(path, "/callback/")
}

func (s *Server) auth(next *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// 桌面模式（PC 端）：不做账号密码，但不再无条件放行（H4：只靠「绑定本机」
		// 做边界，DNS 重绑定 / 跨站请求可读全部密钥）——
		//  1) Host 必须是 Origin 的 loopback 主机 + 真实端口（evil.com 重绑定到
		//     127.0.0.1 时 Host 是 evil.com:port，直接 403）；
		//  2) 带 Origin 头的请求（跨站 fetch/POST 必带）必须与 Origin 同源；
		//  3) /api/* 要求桌面凭据：/desktop-start?t= 换的会话 cookie（<img> 等
		//     无头请求也能带），或 X-Desktop-Token 头。
		if s.Desktop {
			if !s.desktopHostAllowed(r) {
				w.WriteHeader(http.StatusForbidden)
				s.json(w, map[string]string{"error": "desktop: invalid host"})
				return
			}
			if o := r.Header.Get("Origin"); o != "" && s.Origin != "" && !sameOrigin(o, s.Origin) {
				w.WriteHeader(http.StatusForbidden)
				s.json(w, map[string]string{"error": "desktop: cross-origin request"})
				return
			}
			if strings.HasPrefix(r.URL.Path, "/api/") && !s.desktopAuthorized(r) {
				w.WriteHeader(http.StatusUnauthorized)
				s.json(w, map[string]string{"error": "desktop: session required"})
				return
			}
			s.logStatus(next, w, r)
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil && s.Store.ValidSession(c.Value) {
			s.logStatus(next, w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.WriteHeader(http.StatusUnauthorized)
			s.json(w, map[string]string{"error": i18n.T(lang(r), "err.unauthorized")})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(loginHTML)
	})
}

// desktopHostAllowed 桌面模式 Host 白名单：Origin 注入时只放行
// localhost/127.0.0.1/[::1] + Origin 的真实端口（H4 杀 DNS 重绑定）；
// 未注入时退化为只认 loopback 主机名（任意端口）。
func (s *Server) desktopHostAllowed(r *http.Request) bool {
	host := r.Host
	if host == "" {
		return false
	}
	if s.Origin != "" {
		if u, err := url.Parse(s.Origin); err == nil {
			if p := u.Port(); p != "" {
				return host == "localhost:"+p || host == "127.0.0.1:"+p || host == "[::1]:"+p
			}
		}
	}
	h := host
	if i := strings.LastIndex(h, ":"); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i] // 去端口（IPv6 裸地址不含冒号段此分支不触发）
	}
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

// sameOrigin scheme://host:port 级比较（OAuth 回调 origin 与桌面 Origin 同形）。
func sameOrigin(a, b string) bool {
	ua, erra := url.Parse(a)
	ub, errb := url.Parse(b)
	if erra != nil || errb != nil {
		return a == b
	}
	return ua.Scheme == ub.Scheme && ua.Host == ub.Host
}

// desktopAuthorized /api/* 的桌面凭据校验：X-Desktop-Token 头，或
// /desktop-start 种的会话 cookie（值 = token，或 DB 里的有效会话）。
func (s *Server) desktopAuthorized(r *http.Request) bool {
	if s.DesktopToken == "" {
		return true // 内嵌调用方未注入 token：维持旧行为
	}
	if credEq(r.Header.Get("X-Desktop-Token"), s.DesktopToken) {
		return true
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if credEq(c.Value, s.DesktopToken) {
			return true
		}
		return s.Store.ValidSession(c.Value)
	}
	return false
}

// desktopStart GET /desktop-start?t=<token>：桌面窗口入口（ServeDesktop 注入）。
// token 对上即种桌面会话 cookie（值 = token，7 天），302 到 / 去掉 URL 里的
// token；之后同源请求（含 <img> 等无头请求）都带凭据。仅桌面模式可达。
func (s *Server) desktopStart(w http.ResponseWriter, r *http.Request) {
	if !s.Desktop || s.DesktopToken == "" || !credEq(r.URL.Query().Get("t"), s.DesktopToken) {
		w.WriteHeader(http.StatusForbidden)
		s.json(w, map[string]string{"error": "desktop: bad token"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.DesktopToken, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Expires: time.Now().Add(7 * 24 * time.Hour),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

// logStatus 记录非 2xx 响应（排查 404 重复调用等问题）。
func (s *Server) logStatus(next http.Handler, w http.ResponseWriter, r *http.Request) {
	defer func() {
		// 兜底 recover：handler panic 返回 500 JSON（带堆栈日志），不再让 nginx 兜 502
		if p := recover(); p != nil {
			log.Printf("[http] panic %s %s: %v\n%s", r.Method, r.URL.RequestURI(), p, debug.Stack())
			if rec, ok := w.(*statusRecorder); ok && rec.code != 200 {
				return // 已开始写响应，无法再改状态
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"internal panic"}`))
		}
	}()
	rec := &statusRecorder{ResponseWriter: w, code: 200}
	next.ServeHTTP(rec, r)
	if rec.code >= 400 {
		log.Printf("[http] %d %s %s (ua=%s referer=%s)", rec.code, r.Method, r.URL.RequestURI(), r.UserAgent(), r.Referer())
	}
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		User string `json:"user"`
		Pass string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, 400, err)
		return
	}
	ip := clientIP(r)
	guard := s.loginGuard()
	if guard.limited(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		s.json(w, map[string]string{"error": i18n.T(lang(r), "err.too_many_attempts")})
		return
	}
	u := s.authUser()
	p, pFromDB := s.authPass()
	// 两个比较都无条件执行，避免「用户名错 / 密码错」的时序差异
	userOK := credEq(in.User, u)
	passOK, needsUpgrade := verifyStoredPass(p, pFromDB, in.Pass)
	if u == "" || p == "" || !userOK || !passOK {
		guard.fail(ip)
		w.WriteHeader(http.StatusUnauthorized)
		s.json(w, map[string]string{"error": i18n.T(lang(r), "err.bad_credentials")})
		return
	}
	// 存量明文口令登录成功 → 立即升级为 bcrypt 哈希（M3：明文存储淘汰）
	if needsUpgrade {
		if h, herr := hashPassword(in.Pass); herr == nil {
			_ = s.Store.SetSetting("auth_pass", h)
		}
	}
	guard.clear(ip)
	if err := s.startSession(w, r, in.User); err != nil {
		s.err(w, 500, err)
	}
	s.json(w, map[string]bool{"ok": true})
}

// authStatus GET /api/auth/status（免登录）：{configured:false} 时登录页显示
// 「首次设置」表单而不是登录表单（M3 去掉默认密码后的入口）。
func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	p, _ := s.authPass()
	s.json(w, map[string]bool{"configured": p != ""})
}

// setupAuth POST /api/setup/auth（免登录）：首次设置账号口令。仅在口令未配置时
// 可调用；走 loginGuard 限速。成功后直接建立会话，前端跳转控制台。
func (s *Server) setupAuth(w http.ResponseWriter, r *http.Request) {
	if p, _ := s.authPass(); p != "" {
		s.err(w, http.StatusForbidden, errStr("账号已初始化，请登录后在设置页修改密码"))
		return
	}
	var in struct {
		User string `json:"user"`
		Pass string `json:"pass"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, 400, err)
		return
	}
	ip := clientIP(r)
	guard := s.loginGuard()
	if guard.limited(ip) {
		w.WriteHeader(http.StatusTooManyRequests)
		s.json(w, map[string]string{"error": i18n.T(lang(r), "err.too_many_attempts")})
		return
	}
	in.User = strings.TrimSpace(in.User)
	// 与 login 同一限速口径：非法输入也计数，防止用初始化接口爆破/刷库
	if in.User == "" || len(in.Pass) < 6 || len(in.Pass) > 72 {
		guard.fail(ip)
		s.err(w, 400, errStr("用户名不能为空；密码长度需在 6-72 个字符之间"))
		return
	}
	h, herr := hashPassword(in.Pass)
	if herr != nil {
		s.err(w, 500, herr)
		return
	}
	if err := s.Store.SetSetting("auth_user", in.User); err != nil {
		s.err(w, 500, err)
		return
	}
	if err := s.Store.SetSetting("auth_pass", h); err != nil {
		s.err(w, 500, err)
		return
	}
	guard.clear(ip)
	if err := s.startSession(w, r, in.User); err != nil {
		s.err(w, 500, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// startSession 建立会话并种 cookie。HTTPS（直连 TLS 或反代 X-Forwarded-Proto）
// 部署时加 Secure（L6：会话 cookie 未设 Secure）。
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user string) error {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	token := hex.EncodeToString(buf)
	if err := s.Store.CreateSession(token, user); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure:  r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		Expires: time.Now().Add(7 * 24 * time.Hour),
	})
	return nil
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DropSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1})
	s.json(w, map[string]bool{"ok": true})
}

// ---- 设置 ----

// lang 解析请求语言。
func lang(r *http.Request) string {
	if l := r.URL.Query().Get("lang"); l != "" {
		return i18n.Lang(l)
	}
	return i18n.FromAcceptLanguage(r.Header.Get("Accept-Language"))
}

func (s *Server) tr(r *http.Request, key string) string { return i18n.T(lang(r), key) }

func (s *Server) mustSetting(key string) string {
	v, _ := s.Store.GetSetting(key)
	return v
}

func (s *Server) json(w http.ResponseWriter, v any) {
	// 接口响应禁止缓存：前端在向导保存后会立刻重新拉 /api/setup/status 等 GET 接口，
	// 浏览器启发式缓存会让首页读到旧状态（参考项目里踩过这个坑）。
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// err 统一错误响应。曾漏写状态码（code 参数被忽略），所有错误都是 HTTP 200，
// 前端只能靠 body 里的 error 字段判错、日志里的 ≥400 统计全部失效 —— 现在
// 显式写出状态码（clamp 到 400-599，调用点传 0/负数时兜底 500）。
func (s *Server) err(w http.ResponseWriter, code int, e error) {
	if code < 400 || code > 599 {
		code = http.StatusInternalServerError
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
}

// ---- 机器 ----

type strErr string

func errStr(s string) error { return strErr(s) }

func (e strErr) Error() string { return string(e) }
