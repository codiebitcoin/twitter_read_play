package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/source"
	"twitter_read_play/internal/store"
)

const statusBlob = "browser_sessions"

// 登录态状态机的取值。前端按 state 决定按钮，而不是自己拼 known/connected。
const (
	StateUnknown      = "unknown"      // 没探测过，或上次探测无结论
	StateConnected    = "connected"    // 已登录且会话有效
	StateDisconnected = "disconnected" // 明确未登录
	StateExpired      = "expired"      // 之前登录过，本次探测发现会话已失效
)

// Status 单平台登录态。
type Status struct {
	Provider  Provider `json:"provider"`
	Connected bool     `json:"connected"`
	// Known 表示这个状态来自一次有结论的探测（本次运行或上次退出前持久化）。
	// Known=false 时前端不展示状态文字，只给「登录 / 检测」入口。
	Known bool `json:"known"`
	// Expired 上次登录过、这次探测明确失效（例如登录过期）。
	Expired   bool      `json:"expired,omitempty"`
	State     string    `json:"state"`
	Account   string    `json:"account,omitempty"`
	LastCheck time.Time `json:"last_check,omitempty"`
	Error     string    `json:"error,omitempty"`
	// LoginOptional 未登录也能抓公开数据（微博 / Truth Social）。
	LoginOptional bool `json:"login_optional,omitempty"`
}

// LoginState 由已知字段推导状态机取值。
func (s Status) LoginState() string {
	switch {
	case !s.Known:
		return StateUnknown
	case s.Connected:
		return StateConnected
	case s.Expired:
		return StateExpired
	default:
		return StateDisconnected
	}
}

// TokenSet 能安全复制出来的那部分会话（HttpOnly Cookie 不在其中）。
type TokenSet struct {
	Provider Provider          `json:"provider"`
	Cookies  map[string]string `json:"cookies,omitempty"`
	Storage  map[string]string `json:"storage,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Account  string            `json:"account,omitempty"`
}

// Request 页面内 fetch 请求。
type Request struct {
	Method  string
	URL     string // 绝对 URL，或相对 provider home 的 path
	Headers map[string]string
	Body    []byte
	Timeout time.Duration
}

// Response 页面内 fetch 响应。
type Response struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// CaptureRequest 导航目标页 + 拦截页面自身 XHR/GraphQL。
type CaptureRequest struct {
	Navigate string
	URLRegex string
	Scroll   int
	Wait     time.Duration
	Timeout  time.Duration
}

// Captured 一条被拦截的响应。
type Captured struct {
	URL    string          `json:"url"`
	Status int             `json:"status"`
	JSON   json.RawMessage `json:"json,omitempty"`
	Text   string          `json:"text,omitempty"`
	At     time.Time       `json:"at"`
}

// Manager 内置浏览器会话管理。
type Manager struct {
	driver Driver
	st     *store.Store // 登录态快照（browser_sessions），nil 时只存内存

	mu        sync.Mutex
	persistMu sync.Mutex // 串行化快照写盘，避免并发 Probe 互相覆盖
	login     map[Provider]Window
	work      map[Provider]Window
	views     map[string]viewEntry
	status    map[Provider]Status
	tokens    map[Provider]TokenSet
	queues    map[Provider]*sync.Mutex
}

// NewManager 创建；driver 为 nil 时 Available=false（Web 模式）。
// st 用于持久化最近一次登录态探测结果，重启后先展示上次状态，等下一次探测再更新。
func NewManager(d Driver, st *store.Store) *Manager {
	m := &Manager{
		driver: d,
		st:     st,
		login:  map[Provider]Window{},
		work:   map[Provider]Window{},
		views:  map[string]viewEntry{},
		status: map[Provider]Status{},
		tokens: map[Provider]TokenSet{},
		queues: map[Provider]*sync.Mutex{},
	}
	if st != nil {
		var saved map[Provider]Status
		if err := st.Load(statusBlob, &saved); err == nil {
			for p, s := range saved {
				if s.Provider == "" {
					s.Provider = p
				}
				m.status[p] = s
			}
		}
	}
	return m
}

// Available 桌面端且 driver 就绪。
func (m *Manager) Available() bool { return m != nil && m.driver != nil && m.driver.Available() }

// Modes 该浏览器能服务的运行模式。
// 桌面用 mygo WebView，只有 desktop；Web 模式配了 chrome-driverless 后两种都行。
func (m *Manager) Modes() []source.Mode {
	if m.UsesRemote() {
		return []source.Mode{source.ModeDesktop, source.ModeWeb}
	}
	return []source.Mode{source.ModeDesktop}
}

// OpenLogin 打开可见登录窗口（已有则显示并聚焦）。
func (m *Manager) OpenLogin(ctx context.Context, p Provider) error {
	if !m.Available() {
		return fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return fmt.Errorf("未知平台 %q", p)
	}
	m.mu.Lock()
	if w := m.login[p]; w != nil && !w.IsDestroyed() {
		m.mu.Unlock()
		w.Show()
		w.Focus()
		return nil
	}
	m.mu.Unlock()

	w, err := m.driver.Open(OpenOptions{
		Title: "登录 " + cfg.Name, URL: cfg.LoginURL,
		PreloadScript: loginPreload(cfg),
		UserAgent:     providerUA(cfg),
		Width:         520, Height: 760,
	})
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.login[p] = w
	m.mu.Unlock()
	// 用户关掉登录窗 ≈ 登录流程结束：立刻用工作窗探测一次并持久化状态。
	// 前端不需要等下一次手动「检测」，正在轮询的识别卡/登录状态条会马上看到结果。
	w.OnClosed(func() {
		m.mu.Lock()
		stillOurs := m.login[p] == w
		if stillOurs {
			delete(m.login, p)
		}
		m.mu.Unlock()
		// Logout/Close 会先清 map 再销毁窗口，那种情况不探测（避免把已登出状态又探回来）。
		if !stillOurs {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			if st, err := m.Probe(ctx, p); err == nil && st.Connected {
				// 登录成功：已经打开的「查看账号」窗口可能停在登录页，刷新成目标页。
				m.reloadViewsForProvider(p)
			}
		}()
	})
	return nil
}

// Probe 用工作窗口判断登录态。
func (m *Manager) Probe(ctx context.Context, p Provider) (Status, error) {
	if !m.Available() {
		return Status{Provider: p, Error: "desktop_only"}, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return Status{Provider: p}, fmt.Errorf("未知平台 %q", p)
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()

	w, err := m.ensureWork(ctx, p)
	prev := m.Status(p)
	if err != nil {
		prev.Error = err.Error()
		return m.setStatus(p, prev), err
	}
	// 小红书这类把登录态烧进 SSR 的站点：工作窗停在登录前的旧页面时探针永远是 false，
	// 探针前先回首页让服务端重新渲染。
	if cfg.ReloadProbe {
		if err := w.LoadURL(cfg.HomeURL); err == nil {
			_ = waitReady(ctx, w)
			// readyState 变 complete 后导航提交仍可能晚一拍，这时 Eval 会报
			// "Completion handler for function call is no longer reachable"。
			_ = sleepCtx(ctx, 400*time.Millisecond)
		}
	}
	raw, err := evalMap(ctx, w, cfg.ProbeJS)
	if err != nil {
		// 导航提交瞬间 Eval 会报 "Completion handler for function call is no longer
		// reachable" 之类的竞态错误；等一拍重试一次，仍失败就保留上一次状态。
		// 远程 tab 丢失时第一次失败已把窗口标记销毁，重试前先重开工作窗。
		if sleepCtx(ctx, 700*time.Millisecond) {
			if w2, werr := m.ensureWork(ctx, p); werr == nil {
				raw, err = evalMap(ctx, w2, cfg.ProbeJS)
			}
		}
	}
	return m.setStatus(p, applyProbeResult(prev, raw, err, time.Now())), nil
}

// applyProbeResult 把一次探针结果合并到旧状态上。
//
// checked 由探针脚本显式给出：true = 拿到了平台明确答复（登录/未登录都算结论）；
// false = 网络/页面未就绪等无结论情况，此时保留上次的 Connected/Account。
// 探针脚本返回 connected=true 但没有结论（例如 X 请求超时但 ct0 仍在）时，
// 只有在从未探测过的情况下才当作乐观的已登录。
func applyProbeResult(prev Status, raw map[string]any, probeErr error, now time.Time) Status {
	st := prev
	st.LastCheck = now
	if probeErr != nil {
		// 客户端断开（前端轮询被取消）不代表探测结论，别把
		// "context canceled" 写进状态里误导用户。
		if errors.Is(probeErr, context.Canceled) {
			return st
		}
		st.Error = probeErr.Error()
		return st
	}
	if truthy(raw["checked"]) {
		connected := truthy(raw["connected"])
		// 从「已连接」翻成「未连接」= 会话过期（cookie 还在但服务端已失效）。
		// 一旦确认过期就保留这个标记，直到下次登录成功或主动退出。
		st.Expired = !connected && (st.Connected || st.Expired)
		st.Known = true
		st.Connected = connected
		st.Account = stringValue(raw["account"])
	} else if !st.Known && truthy(raw["connected"]) {
		st.Known = true
		st.Connected = true
		st.Account = stringValue(raw["account"])
	}
	st.Error = stringValue(raw["error"])
	return st
}

// Tokens 提取可读 token（非 HttpOnly Cookie / localStorage / ytcfg）。
func (m *Manager) Tokens(ctx context.Context, p Provider) (TokenSet, error) {
	if !m.Available() {
		return TokenSet{Provider: p}, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return TokenSet{Provider: p}, fmt.Errorf("未知平台 %q", p)
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()

	keys, _ := json.Marshal(cfg.StorageKeys)
	if len(cfg.StorageKeys) == 0 {
		// json.Marshal(nil slice) 是 "null"，到页面里 keys.forEach 会直接抛错。
		keys = []byte("[]")
	}
	code := fmt.Sprintf(`(() => {
  const ck = {};
  (document.cookie || '').split('; ').filter(Boolean).forEach((s) => {
    const i = s.indexOf('=');
    if (i > 0) ck[s.slice(0, i)] = decodeURIComponent(s.slice(i + 1));
  });
  const st = {};
  const keys = %s;
  keys.forEach((k) => { try { const v = localStorage.getItem(k); if (v) st[k] = v; } catch (e) {} });
  let yt = null;
  if (window.ytcfg && window.ytcfg.get) {
    yt = {
      apiKey: ytcfg.get('INNERTUBE_API_KEY') || '',
      visitorData: ytcfg.get('VISITOR_DATA') || '',
      clientVersion: ytcfg.get('INNERTUBE_CLIENT_VERSION') || '',
      loggedIn: !!ytcfg.get('LOGGED_IN'),
    };
  }
  return { cookies: ck, storage: st, ytcfg: yt };
})()`, string(keys))
	raw, err := m.evalMapWork(ctx, p, code)
	if err != nil {
		return TokenSet{Provider: p}, err
	}
	ts := TokenSet{Provider: p, Cookies: map[string]string{}, Storage: map[string]string{}, Headers: map[string]string{}}
	if cookies, ok := raw["cookies"].(map[string]any); ok {
		for k, v := range cookies {
			ts.Cookies[k] = stringValue(v)
		}
	}
	if storage, ok := raw["storage"].(map[string]any); ok {
		for k, v := range storage {
			ts.Storage[k] = stringValue(v)
		}
	}
	// 远程浏览器能导出含 HttpOnly 的完整 cookie（sessionid / twid 这些
	// document.cookie 读不到的）；拿不到就沿用页面里读到的部分。
	if remoteCookies, err := m.RemoteCookies(ctx, p); err == nil {
		for k, v := range remoteCookies {
			ts.Cookies[k] = v
		}
	}
	switch p {
	case ProviderX:
		if v := ts.Cookies["ct0"]; v != "" {
			ts.Headers["x-csrf-token"] = v
		}
	case ProviderTwitch:
		ts.Headers["Client-ID"] = "kimne78kx3ncx6brgo4mv6wki5h1ko"
		if v := ts.Storage["auth-token"]; v != "" {
			ts.Headers["Authorization"] = "OAuth " + v
		}
	case ProviderYouTube:
		if yt, ok := raw["ytcfg"].(map[string]any); ok {
			if v := stringValue(yt["visitorData"]); v != "" {
				ts.Headers["X-Goog-Visitor-Id"] = v
			}
			if v := stringValue(yt["clientVersion"]); v != "" {
				ts.Headers["X-Youtube-Client-Version"] = v
			}
			ts.Headers["X-Youtube-Client-Name"] = "1"
		}
	}
	if st := m.Status(p); st.Known {
		ts.Account = st.Account
	}
	m.mu.Lock()
	m.tokens[p] = ts
	m.mu.Unlock()
	return ts, nil
}

// Fetch 在 provider 页面 origin 内执行 fetch(credentials:'include')。
func (m *Manager) Fetch(ctx context.Context, p Provider, req Request) (Response, error) {
	if !m.Available() {
		return Response{}, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return Response{}, fmt.Errorf("未知平台 %q", p)
	}
	target, err := m.resolveURL(cfg, req.URL)
	if err != nil {
		return Response{}, err
	}
	host := strings.ToLower(target.Hostname())
	if !cfg.hostAllowed(host) {
		return Response{}, fmt.Errorf("host %q 不在 %s 白名单内", host, p)
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = "GET"
	}
	if method != "GET" && method != "POST" {
		return Response{}, fmt.Errorf("只允许 GET/POST")
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()

	headers := req.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	hJSON, _ := json.Marshal(headers)
	uJSON, _ := json.Marshal(target.String())
	mJSON, _ := json.Marshal(method)
	bodyJS := "undefined"
	if len(req.Body) > 0 {
		b, _ := json.Marshal(string(req.Body))
		bodyJS = string(b)
	}
	code := fmt.Sprintf(`(async () => {
  const r = await fetch(%s, { method: %s, headers: %s, body: %s, credentials: 'include' });
  return { status: r.status, body: await r.text() };
})()`, uJSON, mJSON, hJSON, bodyJS)
	raw, err := m.evalMapWork(ctx, p, code)
	if err != nil {
		return Response{}, err
	}
	return Response{
		Status: int(floatValue(raw["status"])),
		Body:   []byte(stringValue(raw["body"])),
	}, nil
}

// Capture 导航目标页、滚动、取回命中的 XHR/GraphQL 响应。
func (m *Manager) Capture(ctx context.Context, p Provider, req CaptureRequest) ([]Captured, error) {
	if !m.Available() {
		return nil, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return nil, fmt.Errorf("未知平台 %q", p)
	}
	var re *regexp.Regexp
	if req.URLRegex != "" {
		var err error
		re, err = regexp.Compile(req.URLRegex)
		if err != nil {
			return nil, fmt.Errorf("URLRegex 非法: %w", err)
		}
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()

	w, err := m.ensureWork(ctx, p)
	if err != nil {
		return nil, err
	}
	if nav := strings.TrimSpace(req.Navigate); nav != "" {
		u, uerr := m.resolveURL(cfg, nav)
		if uerr != nil {
			return nil, uerr
		}
		if !cfg.hostAllowed(strings.ToLower(u.Hostname())) {
			return nil, fmt.Errorf("导航 host 不在 %s 白名单内", p)
		}
		err := w.LoadURL(u.String())
		if err != nil && w.IsDestroyed() {
			// 缓存的窗口刚被发现是死的（远程 tab 丢失）：重开再导航一次。
			if w, err = m.ensureWork(ctx, p); err != nil {
				return nil, err
			}
			err = w.LoadURL(u.String())
		}
		if err != nil {
			return nil, err
		}
		if err := waitReady(ctx, w); err != nil {
			return nil, err
		}
	}
	_, _ = w.Eval(ctx, "window.__trpCapture = []")

	wait := req.Wait
	if wait <= 0 {
		wait = 1200 * time.Millisecond
	}
	scrolls := req.Scroll
	if scrolls <= 0 {
		scrolls = 1
	}
	for i := 0; i < scrolls; i++ {
		_, _ = w.Eval(ctx, "window.scrollBy(0, 4000)")
		if !sleepCtx(ctx, wait) {
			return nil, ctx.Err()
		}
	}

	v, err := m.evalWork(ctx, p, "JSON.stringify(window.__trpCapture || [])")
	if err != nil {
		return nil, err
	}
	return parseCaptured(v, re)
}

// Snapshot 读取当前捕获缓冲（不清空、不导航）。YouTube 直播聊天轮询用。
func (m *Manager) Snapshot(ctx context.Context, p Provider, urlRegex string) ([]Captured, error) {
	if !m.Available() {
		return nil, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	if _, ok := configFor(p); !ok {
		return nil, fmt.Errorf("未知平台 %q", p)
	}
	var re *regexp.Regexp
	if urlRegex != "" {
		var err error
		re, err = regexp.Compile(urlRegex)
		if err != nil {
			return nil, fmt.Errorf("URLRegex 非法: %w", err)
		}
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()
	v, err := m.evalWork(ctx, p, "JSON.stringify(window.__trpCapture || [])")
	if err != nil {
		return nil, err
	}
	return parseCaptured(v, re)
}

func parseCaptured(v any, re *regexp.Regexp) ([]Captured, error) {
	var rows []struct {
		URL    string  `json:"url"`
		Status int     `json:"status"`
		Body   string  `json:"body"`
		At     float64 `json:"at"`
	}
	switch x := v.(type) {
	case string:
		if err := json.Unmarshal([]byte(x), &rows); err != nil {
			return nil, err
		}
	case []any:
		b, _ := json.Marshal(x)
		if err := json.Unmarshal(b, &rows); err != nil {
			return nil, err
		}
	}
	out := make([]Captured, 0, len(rows))
	for _, r := range rows {
		if re != nil && !re.MatchString(r.URL) {
			continue
		}
		c := Captured{URL: r.URL, Status: r.Status, Text: r.Body}
		if r.At > 0 {
			c.At = time.UnixMilli(int64(r.At))
		}
		if json.Valid([]byte(r.Body)) {
			c.JSON = json.RawMessage(r.Body)
		}
		out = append(out, c)
	}
	return out, nil
}

// Hide 隐藏登录窗（登录完成后调用）。
func (m *Manager) Hide(p Provider) {
	m.mu.Lock()
	w := m.login[p]
	m.mu.Unlock()
	if w != nil && !w.IsDestroyed() {
		w.Hide()
	}
}

// Logout 退出登录：导航到平台 logout 页并销毁本地窗口。
func (m *Manager) Logout(ctx context.Context, p Provider) error {
	if !m.Available() {
		return fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return fmt.Errorf("未知平台 %q", p)
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()

	m.mu.Lock()
	login := m.login[p]
	work := m.work[p]
	delete(m.login, p)
	delete(m.work, p)
	delete(m.tokens, p)
	// 主动退出是一次明确结论：直接显示未登录，而不是回到「未检测」。
	m.status[p] = Status{Provider: p, Known: true, Connected: false, LastCheck: time.Now()}
	m.mu.Unlock()
	m.persistStatus()

	// 没有服务端登出地址的平台（Bluesky）会话在 localStorage 里：
	// 必须在销毁窗口前主动清掉，否则下次探测又会把旧会话读回来。
	clearWin := work
	if clearWin == nil || clearWin.IsDestroyed() {
		clearWin = login
	}
	tempWin := false
	if cfg.LogoutJS != "" && (clearWin == nil || clearWin.IsDestroyed()) {
		// 重启后只恢复了状态、本次运行还没开过窗口：临时开一个隐藏窗执行清理。
		if w, err := m.ensureWork(ctx, p); err == nil {
			clearWin, tempWin = w, true
		}
	}
	if cfg.LogoutJS != "" && clearWin != nil && !clearWin.IsDestroyed() {
		_, _ = clearWin.Eval(ctx, cfg.LogoutJS)
	}
	if tempWin {
		clearWin.Destroy()
		m.mu.Lock()
		delete(m.work, p)
		m.mu.Unlock()
	}

	if login != nil && !login.IsDestroyed() {
		login.Destroy()
	}
	if work != nil && !work.IsDestroyed() {
		if cfg.LogoutURL != "" {
			_ = work.LoadURL(cfg.LogoutURL)
			_ = waitReady(ctx, work)
		}
		work.Destroy()
	}
	return nil
}

// Close 关闭全部窗口。
func (m *Manager) Close() {
	m.mu.Lock()
	logins := make([]Window, 0, len(m.login))
	works := make([]Window, 0, len(m.work))
	views := make([]Window, 0, len(m.views))
	for _, w := range m.login {
		logins = append(logins, w)
	}
	for _, w := range m.work {
		works = append(works, w)
	}
	for _, v := range m.views {
		views = append(views, v.win)
	}
	m.login = map[Provider]Window{}
	m.work = map[Provider]Window{}
	m.views = map[string]viewEntry{}
	m.mu.Unlock()
	for _, w := range append(append(logins, works...), views...) {
		if w != nil && !w.IsDestroyed() {
			w.Destroy()
		}
	}
}

// Status 最近一次探测结果。
func (m *Manager) Status(p Provider) Status {
	m.mu.Lock()
	st, ok := m.status[p]
	m.mu.Unlock()
	if !ok {
		st = Status{Provider: p}
	}
	st.State = st.LoginState()
	if cfg, ok := configFor(p); ok {
		st.LoginOptional = cfg.LoginOptional
	}
	return st
}

// Statuses 六平台状态。
func (m *Manager) Statuses() []Status {
	out := make([]Status, 0, len(AllProviders()))
	for _, p := range AllProviders() {
		out = append(out, m.Status(p))
	}
	return out
}

func (m *Manager) queue(p Provider) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.queues[p] == nil {
		m.queues[p] = &sync.Mutex{}
	}
	return m.queues[p]
}

func (m *Manager) ensureWork(ctx context.Context, p Provider) (Window, error) {
	m.mu.Lock()
	if w := m.work[p]; w != nil && !w.IsDestroyed() {
		m.mu.Unlock()
		return w, nil
	}
	m.mu.Unlock()

	cfg, ok := configFor(p)
	if !ok {
		return nil, fmt.Errorf("未知平台 %q", p)
	}
	w, err := m.driver.Open(OpenOptions{
		Title: "TRP · " + cfg.Name, URL: cfg.HomeURL, Hidden: true,
		PreloadScript: capturePreload(p), UserAgent: providerUA(cfg),
		Width: 1024, Height: 768,
	})
	if err != nil {
		return nil, err
	}
	if err := waitReady(ctx, w); err != nil {
		w.Destroy()
		return nil, fmt.Errorf("%s 页面加载失败: %w", cfg.Name, err)
	}
	// 刚 Open 的窗口里 about:blank 已经是 complete，waitReady 会立刻返回，
	// 后续 Eval 就打到空白页上（cookie 全空 → 误判未登录）。这里再等一拍，
	// 确认真的导航到了 http(s) 页面。
	if !waitNavigated(ctx, w) {
		w.Destroy()
		return nil, fmt.Errorf("%s 页面加载超时", cfg.Name)
	}
	m.mu.Lock()
	m.work[p] = w
	m.mu.Unlock()
	return w, nil
}

// evalWork 在平台工作窗里执行一段 JS。
// 远程模式下 tab 可能被浏览器侧清掉而缓存窗口还"活着"；那次失败会把窗口标记
// 为已销毁（remoteWindow.markGone），这里随即重开工作窗重试一次，让轮询方在
// 同一个周期内自愈，不用等下一轮。
func (m *Manager) evalWork(ctx context.Context, p Provider, code string) (any, error) {
	w, err := m.ensureWork(ctx, p)
	if err != nil {
		return nil, err
	}
	v, err := w.Eval(ctx, code)
	if err == nil || !w.IsDestroyed() {
		return v, err
	}
	w, err = m.ensureWork(ctx, p)
	if err != nil {
		return nil, err
	}
	return w.Eval(ctx, code)
}

// evalMapWork 同 evalWork，但把结果转成对象（探针/取 token 用）。
func (m *Manager) evalMapWork(ctx context.Context, p Provider, code string) (map[string]any, error) {
	v, err := m.evalWork(ctx, p, code)
	if err != nil {
		return nil, err
	}
	return toMap(v)
}

// Eval 在 provider 工作窗口执行 JS（串行）。
func (m *Manager) Eval(ctx context.Context, p Provider, code string) (any, error) {
	if !m.Available() {
		return nil, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	if _, ok := configFor(p); !ok {
		return nil, fmt.Errorf("未知平台 %q", p)
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()
	return m.evalWork(ctx, p, code)
}

// Text 当前工作页面的 body 文本（微博 JSON 视图兜底用）。
func (m *Manager) Text(ctx context.Context, p Provider) (string, error) {
	v, err := m.Eval(ctx, p, "document.body ? document.body.innerText : ''")
	if err != nil {
		return "", err
	}
	s, _ := v.(string)
	return s, nil
}

func (m *Manager) setStatus(p Provider, st Status) Status {
	st.Provider = p
	st.State = st.LoginState()
	m.mu.Lock()
	m.status[p] = st
	m.mu.Unlock()
	m.persistStatus()
	return st
}

// persistStatus 把最近一次探测结果写入 SQLite；下次启动先展示它，
// 直到用户点「检测」或登录轮询拿到新结论。
func (m *Manager) persistStatus() {
	if m.st == nil {
		return
	}
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	m.mu.Lock()
	snapshot := make(map[Provider]Status, len(m.status))
	for p, st := range m.status {
		snapshot[p] = st
	}
	m.mu.Unlock()
	_ = m.st.Save(statusBlob, snapshot)
}

func (m *Manager) resolveURL(cfg providerConfig, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("空 URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if !u.IsAbs() {
		base, err := url.Parse(cfg.HomeURL)
		if err != nil {
			return nil, err
		}
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("只允许 http/https")
	}
	return u, nil
}

func evalMap(ctx context.Context, w Window, code string) (map[string]any, error) {
	v, err := w.Eval(ctx, code)
	if err != nil {
		return nil, err
	}
	return toMap(v)
}

func toMap(v any) (map[string]any, error) {
	switch x := v.(type) {
	case map[string]any:
		return x, nil
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(x), &m); err != nil {
			return nil, fmt.Errorf("脚本返回非 JSON: %s", x)
		}
		return m, nil
	case nil:
		return map[string]any{}, nil
	default:
		b, _ := json.Marshal(x)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		return m, nil
	}
}

func waitReady(ctx context.Context, w Window) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		v, err := w.Eval(ctx, "document.readyState")
		if err == nil {
			if s, ok := v.(string); ok && (s == "interactive" || s == "complete") {
				sleepCtx(ctx, 300*time.Millisecond)
				return nil
			}
		}
		if !sleepCtx(ctx, 300*time.Millisecond) {
			return ctx.Err()
		}
	}
	return fmt.Errorf("页面加载超时")
}

// waitNavigated 等到页面真正落在 http(s) 地址上。
// about:blank 的 readyState 也是 complete，只看 readyState 会把空白页当成加载完成。
// 拿不到 href（非字符串）时无法判断，按已导航处理，避免卡住。
func waitNavigated(ctx context.Context, w Window) bool {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		v, err := w.Eval(ctx, "location.href")
		if err == nil {
			s, ok := v.(string)
			if !ok {
				return true
			}
			if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
				return true
			}
		}
		if !sleepCtx(ctx, 300*time.Millisecond) {
			return false
		}
	}
	return false
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true") || x == "1"
	case float64:
		return x != 0
	}
	return false
}

func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func floatValue(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}
