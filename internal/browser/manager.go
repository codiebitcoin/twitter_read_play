package browser

import (
	"encoding/json"
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
	pickWatch map[string]chan struct{} // 选用户窗监听去重（key = pick-<roleID>）
}

// NewManager 创建；driver 为 nil 时 Available=false（Web 模式）。
// st 用于持久化最近一次登录态探测结果，重启后先展示上次状态，等下一次探测再更新。
func NewManager(d Driver, st *store.Store) *Manager {
	m := &Manager{
		driver:    d,
		st:        st,
		login:     map[Provider]Window{},
		work:      map[Provider]Window{},
		views:     map[string]viewEntry{},
		status:    map[Provider]Status{},
		tokens:    map[Provider]TokenSet{},
		queues:    map[Provider]*sync.Mutex{},
		pickWatch: map[string]chan struct{}{},
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

// Hide 隐藏登录窗（登录完成后调用）。
func (m *Manager) Hide(p Provider) {
	m.mu.Lock()
	w := m.login[p]
	m.mu.Unlock()
	if w != nil && !w.IsDestroyed() {
		w.Hide()
	}
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
