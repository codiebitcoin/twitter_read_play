package livechat

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/store"
)

// Manager 弹幕源管理器：配置 CRUD + 启停 + 最近弹幕环形缓冲。
// 零持久化弹幕：每路内存保留最近 maxBuffer 条，重启即丢（流式数据，可接受）。
type Manager struct {
	st *store.Store

	// streamerFor 由 kind 构造 Streamer；测试可替换注入假实现。
	streamerFor func(cfg SourceConfig) (Streamer, error)

	mu           sync.Mutex
	cfgs         []SourceConfig
	runs         map[string]*runState
	seq          int64
	buffers      map[string][]Message // cfg id → 最近弹幕（按 seq 递增）
	oauth        OAuthSettings
	oauthPending map[string]oauthPending

	// biliCookie 可选：本路没填 Cookie 时从浏览器登录态取 B 站会话。
	// 只在启动时补到运行配置上，不落库。
	biliCookie func(ctx context.Context) string

	wg sync.WaitGroup
}

const cfgBlob = "livechat.json"
const maxBuffer = 1000

// runState 一路的运行态（状态接口回显用）。
type runState struct {
	cancel  context.CancelFunc
	done    chan struct{}
	running bool
	lastErr string
	lastAt  time.Time
	count   int64
}

// Status 单路状态快照（API 用）。
type Status struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Channel   string    `json:"channel"`
	Title     string    `json:"title"`
	Running   bool      `json:"running"`
	LastError string    `json:"last_error,omitempty"`
	LastAt    time.Time `json:"last_at,omitempty"`
	Count     int64     `json:"count"`
}

// NewManager 读取持久化配置并自动启动 Enabled 的源。
func NewManager(st *store.Store) (*Manager, error) {
	return NewManagerWithFactory(st, nil)
}

// Option 构造选项。
type Option func(*Manager)

// WithBilibiliCookieProvider 让 KindBilibili 配置在本路没填 Cookie 时，
// 启动前从浏览器登录态取一份（游客 getDanmuInfo 会 -352，弹幕为空）。
// 必须在构造时提供：Enabled 的配置会在构造函数里立即启动。
func WithBilibiliCookieProvider(fn func(ctx context.Context) string) Option {
	return func(m *Manager) { m.biliCookie = fn }
}

// NewManagerWithFactory 同 NewManager，但允许为 KindSource 注入动态 Streamer 工厂。
// 工厂必须在构造前提供：Enabled 的 source 配置会在 NewManager 里立即启动。
func NewManagerWithFactory(st *store.Store, sourceFactory func(SourceConfig) (Streamer, error), opts ...Option) (*Manager, error) {
	m := &Manager{
		st:      st,
		runs:    map[string]*runState{},
		buffers: map[string][]Message{},
	}
	for _, opt := range opts {
		opt(m)
	}
	m.streamerFor = func(cfg SourceConfig) (Streamer, error) {
		if cfg.Kind == KindSource {
			if sourceFactory == nil {
				return nil, fmt.Errorf("source streamer 未配置")
			}
			return sourceFactory(cfg)
		}
		return defaultStreamerFor(cfg)
	}
	if err := st.Load(cfgBlob, &m.cfgs); err != nil {
		return nil, err
	}
	// 没配过弹幕源也要在 SQLite 落一份空配置：Stream Watch 才能列出/选中上游。
	if m.cfgs == nil {
		m.cfgs = []SourceConfig{}
		if err := st.Save(cfgBlob, m.cfgs); err != nil {
			return nil, err
		}
	}
	if err := st.Load(oauthBlob, &m.oauth); err != nil {
		return nil, err
	}
	for i := range m.cfgs {
		if m.cfgs[i].Enabled {
			if err := m.start(m.cfgs[i].ID); err != nil {
				log.Printf("[livechat] 自动启动 %s(%s) 失败: %v", m.cfgs[i].ID, m.cfgs[i].Kind, err)
			}
		}
	}
	return m, nil
}

// defaultStreamerFor kind → Streamer。
func defaultStreamerFor(cfg SourceConfig) (Streamer, error) {
	switch cfg.Kind {
	case KindTwitchEventSub:
		return &TwitchEventSub{Cfg: cfg}, nil
	case KindYouTubeAPI:
		return &YouTubeAPI{Cfg: cfg}, nil
	case KindChatDownloader:
		return &ChatDownloader{Cfg: cfg}, nil
	case KindYouTubeNoAuth:
		return youtubeNoAuthStreamer(cfg), nil
	case KindBilibili:
		return biliLibStreamer(cfg), nil
	}
	return nil, fmt.Errorf("未知来源类型 %q", cfg.Kind)
}

// Configs 全部配置（返回拷贝）。
func (m *Manager) Configs() []SourceConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SourceConfig, len(m.cfgs))
	copy(out, m.cfgs)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SaveConfig 新建或更新配置（ID 为空自动生成）。Enabled 且当前没在跑 → 立即启动；
// 改成禁用 → 停掉。返回保存后的配置。
func (m *Manager) SaveConfig(c SourceConfig) (SourceConfig, error) {
	c.Channel = strings.TrimSpace(c.Channel)
	m.applyOAuthDefaults(&c)
	if err := c.Validate(); err != nil {
		return c, err
	}
	m.mu.Lock()
	idx := -1
	for i := range m.cfgs {
		if m.cfgs[i].ID == c.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		if c.ID == "" {
			c.ID = newID()
		}
		m.cfgs = append(m.cfgs, c)
		idx = len(m.cfgs) - 1
	} else {
		m.cfgs[idx] = c
	}
	cfgs := make([]SourceConfig, len(m.cfgs))
	copy(cfgs, m.cfgs)
	m.mu.Unlock()

	if err := m.st.Save(cfgBlob, cfgs); err != nil {
		return c, err
	}
	if !c.Enabled {
		m.Stop(c.ID)
	} else if m.Running(c.ID) {
		// 配置变了（token/频道等）：重启让新配置生效
		m.Stop(c.ID)
		m.Start(c.ID)
	} else {
		m.Start(c.ID)
	}
	return c, nil
}

// DeleteConfig 删除配置并停掉对应源。
func (m *Manager) DeleteConfig(id string) {
	m.Stop(id) // 先停（要拿 runs 里的 cancel），再清理
	m.mu.Lock()
	out := m.cfgs[:0]
	for _, c := range m.cfgs {
		if c.ID != id {
			out = append(out, c)
		}
	}
	m.cfgs = out
	cfgs := make([]SourceConfig, len(out))
	copy(cfgs, out)
	delete(m.runs, id)
	delete(m.buffers, id)
	m.mu.Unlock()
	_ = m.st.Save(cfgBlob, cfgs)
}

// GetConfig 单个配置。
func (m *Manager) GetConfig(id string) (SourceConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.cfgs {
		if c.ID == id {
			return c, true
		}
	}
	return SourceConfig{}, false
}

// RemapSourceChannels 重写 KindSource 配置里的 ChannelID（升级迁移用，幂等）。
// fn 收到 (当前渠道 ID, 目标平台)，返回新渠道 ID；返回 "" 表示清空绑定交给自动解析。
// 命中的配置落库，正在跑的重启让新渠道生效。返回改写条数。
func (m *Manager) RemapSourceChannels(fn func(channelID, platform string) string) (int, error) {
	m.mu.Lock()
	changed := 0
	var restart []string
	for i := range m.cfgs {
		c := &m.cfgs[i]
		if c.Kind != KindSource {
			continue
		}
		next := strings.TrimSpace(fn(c.ChannelID, c.TargetPlatform))
		if next == c.ChannelID {
			continue
		}
		c.ChannelID = next
		changed++
		if m.runs[c.ID] != nil && m.runs[c.ID].running {
			restart = append(restart, c.ID)
		}
	}
	cfgs := make([]SourceConfig, len(m.cfgs))
	copy(cfgs, m.cfgs)
	m.mu.Unlock()
	if changed == 0 {
		return 0, nil
	}
	if err := m.st.Save(cfgBlob, cfgs); err != nil {
		return 0, err
	}
	for _, id := range restart {
		m.Stop(id)
		_ = m.Start(id)
	}
	log.Printf("[livechat] 迁移 %d 条 KindSource 配置的渠道绑定", changed)
	return changed, nil
}

// Start 启动（幂等）。
func (m *Manager) Start(id string) error { return m.start(id) }

// applyRuntimeDefaults 启动前补全只对本次运行有效的字段（不写回 SQLite）。
func (m *Manager) applyRuntimeDefaults(ctx context.Context, c *SourceConfig) {
	if c.Kind != KindBilibili || strings.TrimSpace(c.BilibiliCookie) != "" {
		return
	}
	m.mu.Lock()
	provider := m.biliCookie
	m.mu.Unlock()
	if provider == nil {
		return
	}
	if v := strings.TrimSpace(provider(ctx)); v != "" {
		c.BilibiliCookie = v
	}
}

func (m *Manager) start(id string) error {
	cfg, ok := m.GetConfig(id)
	if !ok {
		return fmt.Errorf("livechat 配置不存在: %s", id)
	}
	m.applyOAuthDefaults(&cfg)
	// 游客 getDanmuInfo 会 -352（房间连上但弹幕为空），启动时用浏览器登录态补 Cookie。
	cookieCtx, cancelCookie := context.WithTimeout(context.Background(), 25*time.Second)
	m.applyRuntimeDefaults(cookieCtx, &cfg)
	cancelCookie()
	m.mu.Lock()
	if rs, ok := m.runs[id]; ok && rs.running {
		m.mu.Unlock()
		return nil
	}
	if err := cfg.Validate(); err != nil {
		if rs, ok := m.runs[id]; ok {
			rs.lastErr = err.Error()
			rs.lastAt = time.Now()
			rs.running = false
		} else {
			m.runs[id] = &runState{lastErr: err.Error(), lastAt: time.Now()}
		}
		m.mu.Unlock()
		return err
	}
	src, err := m.streamerFor(cfg)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	rs := &runState{cancel: cancel, done: make(chan struct{}), running: true}
	m.runs[id] = rs
	buf := m.buffers[id]
	m.mu.Unlock()

	m.wg.Add(1)
	go m.pump(ctx, id, cfg, src, rs, &buf)
	return nil
}

// Stop 停止（幂等，等待拉取循环退出，最多等 3s）。
func (m *Manager) Stop(id string) {
	m.mu.Lock()
	rs := m.runs[id]
	m.mu.Unlock()
	if rs == nil || rs.cancel == nil {
		return
	}
	rs.cancel()
	select {
	case <-rs.done:
	case <-time.After(3 * time.Second):
	}
}

// Running 是否在跑。
func (m *Manager) Running(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if rs, ok := m.runs[id]; ok {
		return rs.running
	}
	return false
}

// Statuses 全部配置的运行状态。
func (m *Manager) Statuses() []Status {
	cfgs := m.Configs()
	out := make([]Status, 0, len(cfgs))
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range cfgs {
		s := Status{ID: c.ID, Kind: c.Kind, Channel: c.Channel, Title: c.Title}
		if rs, ok := m.runs[c.ID]; ok {
			s.Running = rs.running
			s.LastError = rs.lastErr
			s.LastAt = rs.lastAt
			s.Count = rs.count
		}
		out = append(out, s)
	}
	return out
}

// pump 单路拉取循环：Stream 出错退避重启，直到 ctx 取消。
// buf 指针拷贝自 m.buffers，重启时沿用同一缓冲（seq 连续，前端不跳）。
func (m *Manager) pump(ctx context.Context, id string, cfg SourceConfig, src Streamer, rs *runState, buf *[]Message) {
	defer m.wg.Done()
	defer close(rs.done)
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		err := src.Stream(ctx, func(msg Message) {
			msg.Source = src.Name()
			m.mu.Lock()
			m.seq++
			msg.Seq = m.seq
			b := append(*buf, msg)
			if len(b) > maxBuffer {
				b = b[len(b)-maxBuffer:]
			}
			*buf = b
			m.buffers[id] = b
			rs.lastAt = time.Now()
			rs.count++
			rs.lastErr = ""
			m.mu.Unlock()
		})
		m.mu.Lock()
		rs.lastAt = time.Now()
		if ctx.Err() != nil {
			rs.running = false
			rs.lastErr = ""
			m.mu.Unlock()
			log.Printf("[livechat] %s 已停止", id)
			return
		}
		if err != nil && !isCtxErr(err) {
			rs.lastErr = err.Error()
			m.mu.Unlock()
			log.Printf("[livechat] %s(%s) 退出: %v，%v 后重启", id, cfg.Kind, err, backoff)
		} else {
			rs.lastErr = ""
			m.mu.Unlock()
			log.Printf("[livechat] %s(%s) 退出，%v 后重启", id, cfg.Kind, backoff)
		}
		select {
		case <-ctx.Done():
			m.mu.Lock()
			rs.running = false
			m.mu.Unlock()
			return
		case <-time.After(backoff):
		}
		if backoff < 60*time.Second {
			backoff *= 2
		}
	}
	m.mu.Lock()
	rs.running = false
	m.mu.Unlock()
}

func isCtxErr(err error) bool {
	return err != nil && (err == context.Canceled || err == context.DeadlineExceeded ||
		strings.Contains(err.Error(), context.Canceled.Error()))
}

// Messages 分页/增量拉取某路弹幕（结果按 seq 升序）。
//
//	首次（after<=0、before<=0）：最后 limit 条（一屏最新弹幕）
//	增量（after>0、before<=0）：(after, 最新] 的前 limit 条（积压时多次调用追上）
//	历史（before>0）          ：[最早, before) 的最后 limit 条（向上翻页）
//
// hasOlder 表示返回窗口之前还有更早的弹幕，前端据此显示「加载更早」；
// 增量调用忽略它（更早的内容前端本来就有）。
func (m *Manager) Messages(id string, after, before int64, limit int) ([]Message, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := m.buffers[id]

	var out []Message
	hasOlder := false
	if before > 0 {
		hi := 0
		for hi < len(all) && all[hi].Seq < before {
			hi++
		}
		start := 0
		if limit > 0 && hi > limit {
			start = hi - limit
		}
		out = all[start:hi]
		hasOlder = start > 0
	} else {
		lo := 0
		for lo < len(all) && all[lo].Seq <= after {
			lo++
		}
		pending := len(all) - lo
		switch {
		case limit > 0 && pending > limit && after > 0:
			// 追积压：先给最早的，前端靠下一次 after 继续追。
			out = all[lo : lo+limit]
		case limit > 0 && pending > limit:
			// 首次：直接给最新一屏，更早的留给分页。
			out = all[len(all)-limit:]
			hasOlder = true
		default:
			out = all[lo:]
		}
	}
	// 拷贝一份：后台 pump 会 append 同一底层数组，别把锁外的数据暴露出去。
	res := make([]Message, len(out))
	copy(res, out)
	return res, hasOlder
}

// LatestSeq 当前最大 seq（前端初始化游标用）。
func (m *Manager) LatestSeq(id string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.buffers[id]
	if len(b) == 0 {
		return 0
	}
	return b[len(b)-1].Seq
}

// Close 停掉全部源（进程退出前调用）。
func (m *Manager) Close() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.runs))
	for _, rs := range m.runs {
		cancels = append(cancels, rs.cancel)
	}
	m.mu.Unlock()
	for _, c := range cancels {
		if c != nil {
			c()
		}
	}
	m.wg.Wait()
}
