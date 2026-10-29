package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// remoteDriver 把远程 chrome-driverless（POST /mcp）适配成 browser.Driver。
//
// 这样桌面端用 mygo WebView、Web/Docker 用远程真实 Chrome，两边共用同一套
// Manager + 平台 adapter（探针 / capture / token / 登录），不需要分叉实现。
type remoteDriver struct {
	// baseURL 每次调用都重新读设置：用户在设置页改地址后立即生效。
	baseURL func() string
	client  *http.Client

	mu      sync.Mutex
	healthy bool
	at      time.Time
	tabs    map[string]*remoteWindow
}

// NewRemoteDriver 创建远程 driver。baseURL 返回空串时 Available()=false，
// 前端会退回到「填地址」的提示卡片。
func NewRemoteDriver(baseURL func() string) Driver {
	return &remoteDriver{
		baseURL: baseURL,
		// 控制面（MCP 与文件下载）连的是内网 ingress，不能走设置页里给平台抓取
		// 用的 HTTP 代理，否则配了代理后 /health 直接不可达。
		client: &http.Client{
			Timeout:   120 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		},
		tabs: map[string]*remoteWindow{},
	}
}

func (d *remoteDriver) endpoint() string {
	base := strings.TrimSpace(d.baseURL())
	if base == "" {
		return ""
	}
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/mcp") {
		return base
	}
	return base + "/mcp"
}

// OperationURL 用户登录用的操作页地址（chrome-driverless 根路径）。
func (d *remoteDriver) OperationURL() string {
	base := strings.TrimSpace(d.baseURL())
	if base == "" {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/mcp")
}

// Available 配置了地址且 /health 在最近 15 秒内通过。
func (d *remoteDriver) Available() bool {
	base := strings.TrimSpace(d.baseURL())
	if base == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if time.Since(d.at) < 15*time.Second {
		return d.healthy
	}
	root := strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/mcp")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, root+"/health", nil)
	if err != nil {
		d.at, d.healthy = time.Now(), false
		return false
	}
	resp, err := d.client.Do(req)
	if err != nil {
		d.at, d.healthy = time.Now(), false
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	d.at, d.healthy = time.Now(), resp.StatusCode == http.StatusOK
	return d.healthy
}

// call 发一次 MCP 调用，返回 result 对象。
func (d *remoteDriver) call(ctx context.Context, method string, params map[string]any) (map[string]any, error) {
	ep := d.endpoint()
	if ep == "" {
		return nil, fmt.Errorf("未配置 chrome-driverless 地址")
	}
	if params == nil {
		params = map[string]any{}
	}
	body, _ := json.Marshal(map[string]any{"method": method, "params": params})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("chrome-driverless 不可达: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	var out struct {
		Result map[string]any `json:"result"`
		Error  struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("chrome-driverless 返回非 JSON（HTTP %d）", resp.StatusCode)
	}
	if out.Error.Message != "" {
		return nil, fmt.Errorf("%s", out.Error.Message)
	}
	if out.Result == nil {
		out.Result = map[string]any{}
	}
	return out.Result, nil
}

// Open 在远程浏览器里建一个命名 tab。
//
// 已部署的 chrome-driverless 版本里 pw/new_tab 不一定认 tab 名（只有 pw/tab_tag
// 稳定支持），所以这里先开 about:blank，再打标签，后续调用按标签解析 index。
// 直接让 new_tab 带 URL 导航在部分站点会报 ERR_CONNECTION_CLOSED，故分开做。
func (d *remoteDriver) Open(opts OpenOptions) (Window, error) {
	if !d.Available() {
		return nil, fmt.Errorf("远程浏览器不可用（检查设置里的 chrome-driverless 地址）")
	}
	key := strings.TrimSpace(opts.StateKey)
	if key == "" {
		key = strings.TrimSpace(opts.Title)
	}
	if key == "" {
		key = "trp-tab"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := d.call(ctx, "pw/new_tab", map[string]any{"url": "about:blank"})
	if err != nil {
		return nil, err
	}
	idx := -1
	if n, ok := res["index"].(float64); ok {
		idx = int(n)
	}
	if idx >= 0 {
		// pw/tab_tag 会同时把该 tab 设为 active；带上 index 避免打到别的页。
		_, _ = d.call(ctx, "pw/tab_tag", map[string]any{"index": idx, "tag": key})
	}
	w := &remoteWindow{drv: d, key: key, url: opts.URL, preload: opts.PreloadScript}
	if opts.URL != "" {
		if err := w.LoadURL(opts.URL); err != nil {
			w.Destroy()
			return nil, err
		}
	}
	d.mu.Lock()
	d.tabs[key] = w
	d.mu.Unlock()
	return w, nil
}

type remoteWindow struct {
	drv     *remoteDriver
	key     string
	url     string
	preload string

	mu        sync.Mutex
	destroyed bool
	closed    []func()
}

func (w *remoteWindow) LoadURL(u string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	idx, err := w.tabIndex(ctx)
	if err != nil {
		return err
	}
	// 用 index 定位：部署版 pw/navigate 不一定支持 tab 名。
	if _, err := w.drv.call(ctx, "pw/navigate", map[string]any{"url": u, "index": idx}); err != nil {
		return err
	}
	w.mu.Lock()
	w.url = u
	w.mu.Unlock()
	// 远程没有 PreloadScript 的注入点：导航完成后再注入一次捕获钩子。
	// Manager.Capture 在导航后清空缓冲再滚动，滚动触发的请求仍能拿到。
	if w.preload != "" {
		_, _ = w.Eval(ctx, w.preload)
	}
	return nil
}

func (w *remoteWindow) Eval(ctx context.Context, code string) (any, error) {
	idx, err := w.tabIndex(ctx)
	if err != nil {
		return nil, err
	}
	res, err := w.drv.call(ctx, "pw/evaluate", map[string]any{"expression": code, "index": idx})
	if err != nil {
		return nil, err
	}
	return res["value"], nil
}

func (w *remoteWindow) Show()  {}
func (w *remoteWindow) Hide()  {}
func (w *remoteWindow) Focus() { _ = w.selectTab() }

// tabIndex 按 tab 标签查索引。部署版 pw/evaluate/navigate 对未知 tab 名
// 会静默落到当前页，所以不能靠名字定位，必须自己解析 index。
func (w *remoteWindow) tabIndex(ctx context.Context) (int, error) {
	res, err := w.drv.call(ctx, "pw/tabs", nil)
	if err != nil {
		return -1, err
	}
	var fallback = -1
	for _, raw := range asAnySlice(res["tabs"]) {
		m, _ := raw.(map[string]any)
		if m == nil {
			continue
		}
		if name, _ := m["tag"].(string); name == w.key {
			if n, ok := m["index"].(float64); ok {
				return int(n), nil
			}
		}
		if idx, ok := m["index"].(float64); ok {
			if u, _ := m["url"].(string); u == w.url {
				fallback = int(idx)
			}
		}
	}
	if fallback >= 0 {
		// 标签丢了（旧镜像/被清理）时按 URL 兜底认领。
		_, _ = w.drv.call(ctx, "pw/tab_tag", map[string]any{"index": fallback, "tag": w.key})
		return fallback, nil
	}
	// 标签和 URL 都找不到：远程浏览器重启/清理过，这个窗口已经死了。
	// 标记销毁并摘出缓存，让 Manager.ensureWork 下次重开新 tab 自愈。
	w.markGone()
	return -1, fmt.Errorf("远程浏览器里找不到 tab %q", w.key)
}

// markGone 远程侧 tab 已不存在时把窗口标记为销毁并移出缓存。
func (w *remoteWindow) markGone() {
	w.mu.Lock()
	w.destroyed = true
	w.mu.Unlock()
	w.drv.mu.Lock()
	delete(w.drv.tabs, w.key)
	w.drv.mu.Unlock()
}

func asAnySlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func (w *remoteWindow) selectTab() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	idx, err := w.tabIndex(ctx)
	if err != nil {
		return err
	}
	_, err = w.drv.call(ctx, "pw/tab_select", map[string]any{"index": idx})
	return err
}

func (w *remoteWindow) Destroy() {
	w.mu.Lock()
	if w.destroyed {
		w.mu.Unlock()
		return
	}
	w.destroyed = true
	w.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// 最后一个 tab 不允许关（chrome-driverless 约束），忽略即可。
	if idx, err := w.tabIndex(ctx); err == nil {
		_, _ = w.drv.call(ctx, "pw/tab_close", map[string]any{"index": idx})
	}
	w.drv.mu.Lock()
	delete(w.drv.tabs, w.key)
	w.drv.mu.Unlock()
}

func (w *remoteWindow) IsDestroyed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.destroyed
}

func (w *remoteWindow) OnClosed(fn func()) {
	w.mu.Lock()
	w.closed = append(w.closed, fn)
	w.mu.Unlock()
}

// ---- 远程专属能力 ----

// ResetSessions 丢弃缓存的窗口/tab 和登录态。
//
// 远程地址被改到另一台实例后，旧 tab 索引/标签在新实例里不存在，
// 必须清掉；状态也一并重置，等下一次探测重新判断。
func (m *Manager) ResetSessions() {
	m.mu.Lock()
	logins := make([]Window, 0, len(m.login))
	works := make([]Window, 0, len(m.work))
	for _, w := range m.login {
		logins = append(logins, w)
	}
	for _, w := range m.work {
		works = append(works, w)
	}
	m.login = map[Provider]Window{}
	m.work = map[Provider]Window{}
	m.queues = map[Provider]*sync.Mutex{}
	m.tokens = map[Provider]TokenSet{}
	m.status = map[Provider]Status{}
	m.mu.Unlock()
	for _, w := range append(logins, works...) {
		if w != nil && !w.IsDestroyed() {
			w.Destroy()
		}
	}
}

// RemoteOperationURL 远程浏览器操作页（用户在里面登录各平台）。
func (m *Manager) RemoteOperationURL() string {
	type operationURLer interface{ OperationURL() string }
	if d, ok := m.driver.(operationURLer); ok {
		return d.OperationURL()
	}
	return ""
}

// UsesRemote Web 模式且已填 chrome-driverless 地址（界面按远程浏览器走）。
// 只看配置不看健康状态：地址填了就算启用，可达性由 Available 判断。
func (m *Manager) UsesRemote() bool {
	if m == nil {
		return false
	}
	d, ok := m.driver.(*remoteDriver)
	if !ok {
		return false
	}
	return strings.TrimSpace(d.baseURL()) != ""
}

// RemoteAvailable 远程浏览器此刻可用（地址已填且 /health 通过）。
func (m *Manager) RemoteAvailable() bool {
	return m.UsesRemote() && m.driver.Available()
}

// RemoteLogin 在远程浏览器里打开（或复用）登录 tab；用户在自己的浏览器里
// 打开操作页完成登录。
func (m *Manager) RemoteLogin(ctx context.Context, p Provider) error {
	cfg, ok := configFor(p)
	if !ok {
		return fmt.Errorf("未知平台 %q", p)
	}
	d, ok := m.driver.(*remoteDriver)
	if !ok {
		return fmt.Errorf("当前不是远程浏览器模式")
	}
	key := "trp-login-" + string(p)
	d.mu.Lock()
	w := d.tabs[key]
	d.mu.Unlock()
	if w != nil && w.IsDestroyed() {
		w = nil
	}
	if w == nil {
		// 和 Open 一样：部署版 new_tab 不认 tab 名，先建空白页再打标签，
		// 后续导航/取值都按 index 定位。
		win, err := d.Open(OpenOptions{Title: key, StateKey: key})
		if err != nil {
			return err
		}
		w, _ = win.(*remoteWindow)
	}
	if w == nil {
		return fmt.Errorf("远程浏览器未返回登录 tab")
	}
	if err := w.LoadURL(cfg.LoginURL); err != nil {
		w.Destroy()
		return err
	}
	// 把浏览器切到登录 tab，用户打开操作页就能直接看到。
	w.Focus()
	return nil
}

// RemoteCookies 导出远程浏览器的 cookie（含 HttpOnly）。
//
// chrome-driverless 的 pw/save_auth 会把 storage_state 写到 data 目录，
// 再用 /files/download 取回来。Google/Instagram 这类平台的会话关键 cookie
// 是 HttpOnly，页面里 document.cookie 读不到，只能走这条路。
func (m *Manager) RemoteCookies(ctx context.Context, p Provider) (map[string]string, error) {
	cfg, ok := configFor(p)
	if !ok {
		return nil, fmt.Errorf("未知平台 %q", p)
	}
	d, ok := m.driver.(*remoteDriver)
	if !ok {
		return nil, fmt.Errorf("当前不是远程浏览器模式")
	}
	res, err := d.call(ctx, "pw/save_auth", nil)
	if err != nil {
		return nil, err
	}
	path, _ := res["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("远程浏览器未返回 auth 路径")
	}
	// /files/download 的 path 相对 BROWSER_DATA_DIR。
	rel := path
	if i := strings.Index(path, "/app/data/"); i >= 0 {
		rel = path[i+len("/app/data/"):]
	}
	root := strings.TrimSuffix(strings.TrimSuffix(d.baseURL(), "/"), "/mcp")
	raw, err := d.get(ctx, root+"/files/download?path="+url.QueryEscape(rel))
	if err != nil {
		return nil, err
	}
	var st struct {
		Cookies []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("auth.json 解析失败: %w", err)
	}
	out := map[string]string{}
	for _, c := range st.Cookies {
		if !cookieDomainWanted(cfg, c.Domain) {
			continue
		}
		out[c.Name] = c.Value
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("远程浏览器里没有 %s 的 cookie", p)
	}
	return out, nil
}

func cookieDomainWanted(cfg providerConfig, domain string) bool {
	host := strings.TrimPrefix(strings.ToLower(domain), ".")
	for _, h := range cfg.Hosts {
		h = strings.TrimPrefix(strings.ToLower(h), ".")
		if host == h || strings.HasSuffix(host, "."+h) || strings.HasSuffix(h, "."+host) {
			return true
		}
	}
	return false
}

func (d *remoteDriver) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取远程文件失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("拉取远程文件失败: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}
