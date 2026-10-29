// Package watch 调度：按角色的监控配置轮询推文 → 角色下发 → 设备播报。
package watch

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"twitter_read_play/internal/livechat"
	"twitter_read_play/internal/machine"
	"twitter_read_play/internal/model"
	"twitter_read_play/internal/role"
	"twitter_read_play/internal/source"
	"twitter_read_play/internal/store"
	"twitter_read_play/internal/twitter"
	"twitter_read_play/internal/upstream"
)

const defaultInterval = 600 // 秒

// sharedHTTP 翻译等外部 HTTP 调用共用客户端。
var sharedHTTP = &http.Client{Timeout: 10 * time.Second}

const maxLogs = 500

// errNeedBinding 角色未绑定机器/槽位：前端据此弹出槽位设置。
var ErrNeedBinding = errors.New("NEED_BINDING")

type Manager struct {
	mu   sync.Mutex
	st   *store.Store
	logs []*model.BroadcastLog
	// voice_clone 后台任务记录（机器级互斥 + 持久化，见 clonetask.go）
	cloneTasks []*CloneTask
	// 设备操作消息队列（PATCH设置 / Check&fix / VRM上传 / voice_clone / poll，见 opqueue.go）
	opsMu sync.Mutex
	ops   []*OpItem
	// 同一角色同时只跑一次轮询（手动「立即轮询」与定时扫描互斥）
	pollMu   sync.Mutex
	pollBusy map[string]bool
	// ChatRoom 重复播放会话（groupID → 会话）：一轮播完后按需续排下一轮。
	chatMu       sync.Mutex
	chatSessions map[string]*chatRoomSession
	provider     twitter.Provider
	upstreams    *upstream.Manager
	// 新渠道模型（bootstrap 注入；为空时走旧 upstreams 路径）
	channels *source.Manager
	registry *source.Registry
	mode     source.Mode
	machines *machine.Manager
	roles    *role.Manager
	stop     chan struct{}
	wg       sync.WaitGroup
	syncMu   sync.Mutex // 串行化 Sync/Check（避免并发检查互踩 voice 绑定）

	// 拉取缓存：同一上游同一 username 短时间内只拉一次，多角色共享结果。
	fetchMu   sync.Mutex
	fetchPool map[string]cachedFetch

	// 翻历史进行中的角色（防并发重复计费拉取）
	historyMu   sync.Mutex
	historyBusy map[string]bool

	// 播报队列（按机器串行消费；懒初始化，测试里只塞了 st 也能用）
	bq    *broadcastQueue
	qOnce sync.Once

	// 图书朗读（角色 Book 页；未调用 SetBooks 时为 nil）
	books *Books

	// 直播弹幕（角色 Stream 页；未调用 SetLive 时为 nil，scanLive 空转）
	live liveReader
}

// liveReader 角色 Stream 消费弹幕用的最小接口（测试可注入假源）。
type liveReader interface {
	Messages(id string, after, before int64, limit int) ([]livechat.Message, bool)
	LatestSeq(id string) int64
}

// queue 播报队列的懒初始化访问器。
func (m *Manager) queue() *broadcastQueue {
	m.qOnce.Do(func() { m.bq = newBroadcastQueue(m) })
	return m.bq
}

type cachedFetch struct {
	tweets []model.Tweet
	at     time.Time
}

func NewManager(st *store.Store, provider twitter.Provider, um *upstream.Manager, mm *machine.Manager, rm *role.Manager) (*Manager, error) {
	m := &Manager{
		st: st, provider: provider, upstreams: um, machines: mm, roles: rm,
		stop: make(chan struct{}), historyBusy: map[string]bool{},
	}
	var logs []*model.BroadcastLog
	if err := st.Load("broadcast_logs", &logs); err != nil {
		return nil, err
	}
	m.logs = logs
	m.fetchPool = map[string]cachedFetch{}
	m.loadCloneTasks()
	return m, nil
}

// effectivelyEnabled 角色全局启用 且 至少有一个启用的绑定（机器存在且该机启用了此角色）。
func (m *Manager) effectivelyEnabled(r *model.Role) bool {
	if !r.Enabled || r.Username == "" {
		return false
	}
	for _, b := range r.Bindings {
		if !b.Enabled || b.MachineID == "" || b.Slot <= 0 {
			continue
		}
		mc, err := m.machines.Get(b.MachineID)
		if err != nil {
			continue
		}
		if len(mc.EnabledRoles) == 0 {
			return true // 未配置时默认全部启用
		}
		for _, id := range mc.EnabledRoles {
			if id == r.ID {
				return true
			}
		}
	}
	return false
}

// enabledBindings 角色当前参与播报的绑定列表（机器可达性由调用方处理）。
func (m *Manager) enabledBindings(r *model.Role) []model.Binding {
	var out []model.Binding
	for _, b := range r.Bindings {
		if !b.Enabled || b.MachineID == "" || b.Slot <= 0 {
			continue
		}
		if _, err := m.machines.Get(b.MachineID); err != nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

// SetLive 注入直播弹幕管理器（角色 Stream 页消费用）。
func (m *Manager) SetLive(lm *livechat.Manager) {
	m.live = lm
}

// Start 启动调度循环：Feed/Watch 每 30s 扫一次；Stream 每 1s 扫一次（默认间隔就是 1s）。
func (m *Manager) Start() {
	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		m.scan() // 启动立刻扫一遍：不等第一个 30s tick，否则重启后第一条推文要空等 30s
		for {
			select {
			case <-m.stop:
				return
			case <-time.After(30 * time.Second):
				m.scan()
			}
		}
	}()
	go func() {
		defer m.wg.Done()
		m.scanLive()
		for {
			select {
			case <-m.stop:
				return
			case <-time.After(time.Second):
				m.scanLive()
			}
		}
	}()
}

func (m *Manager) Stop() {
	close(m.stop)
	m.wg.Wait()
}

func (m *Manager) appendLog(e *model.BroadcastLog) {
	m.mu.Lock()
	m.logs = append(m.logs, e)
	if len(m.logs) > maxLogs {
		m.logs = m.logs[len(m.logs)-maxLogs:]
	}
	_ = m.st.Save("broadcast_logs", m.logs)
	m.mu.Unlock()
}

func (m *Manager) Logs(limit int) []*model.BroadcastLog {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > len(m.logs) {
		limit = len(m.logs)
	}
	out := make([]*model.BroadcastLog, limit)
	for i := 0; i < limit; i++ {
		out[i] = m.logs[len(m.logs)-1-i]
	}
	return out
}

// ForgetBookProgress 角色被删除时清掉它的图书朗读进度（否则库里留下指向已删角色的孤儿记录）。
func (m *Manager) ForgetBookProgress(roleID string) {
	if m.books != nil && m.books.Lib != nil {
		m.books.Lib.ForgetRole(roleID)
	}
}

// SetSource 注入新渠道模型与运行模式（bootstrap 调用）。
func (m *Manager) SetSource(channels *source.Manager, reg *source.Registry, mode source.Mode) {
	m.channels = channels
	m.registry = reg
	m.mode = mode
}

// SetAPIKey 动态更新 twitter provider 的 API Key。
func (m *Manager) SetAPIKey(key string) {
	if sd, ok := m.provider.(*twitter.Socialdata); ok {
		sd.SetKey(key)
	}
}

// SetBaseURL 动态更新 socialdata API 地址（[api] base_url）：
// 兜底 provider 立即生效，之后按上游构建的 provider 也会带上这个默认地址。
func (m *Manager) SetBaseURL(u string) {
	if sd, ok := m.provider.(*twitter.Socialdata); ok {
		sd.SetBaseURL(u)
	}
	m.upstreams.SetDefaultBaseURL(u)
}

func upIDorEmpty(u *model.Upstream) string {
	if u == nil {
		return ""
	}
	return u.ID
}
