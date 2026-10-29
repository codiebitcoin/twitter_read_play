package clt01

// 会话监听（原子能力）：轮询聊天记录实现「接受对话」。
//
// Watcher 内部维护一份活跃/非活跃状态记录（随 ctx 生命周期生效）：
//   - 有新对话记录，或最近一条记录落在 ActiveWindow 内 = 活跃：按 ActiveInterval（默认 1s）轮询
//   - 否则 = 非活跃：按 InactiveInterval（默认 10s）轮询
//
// 新对话轮次经 OnTurn 吐出；活跃/非活跃切换经 OnStateChange 通知，任何时刻可 State() 读取。
// 最近若干轮对话保留在内存里（Records），宿主可自行落库。

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// ConversationState 会话活跃状态（watcher 的 ctx 状态记录）。
type ConversationState string

const (
	ConversationActive   ConversationState = "active"   // 活跃：短间隔轮询（默认 1s）
	ConversationInactive ConversationState = "inactive" // 非活跃：长间隔轮询（默认 10s）
)

// 默认轮询参数。
const (
	DefaultActiveInterval   = 1 * time.Second     // 活跃轮询间隔
	DefaultInactiveInterval = 10 * time.Second    // 非活跃轮询间隔
	DefaultActiveWindow     = 2 * time.Minute     // 最近一条记录在此窗口内 = 活跃
	DefaultConversationTail = 20                  // 每次拉取的记录条数
	MaxConversationRecords  = 500                 // 内存保留的对话记录上限
)

// ConversationTurn 一轮对话（聊天记录里的一条 item）。
type ConversationTurn struct {
	ChatID    string    `json:"chat_id"`
	TurnID    string    `json:"turn_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	Slot      int       `json:"slot_number"`
}

// ConversationWatchOptions 会话监听配置。
type ConversationWatchOptions struct {
	Slot int // 必填：监听的角色槽
	// 轮询节奏（零值取默认值）
	ActiveInterval   time.Duration // 活跃轮询间隔，默认 1s
	InactiveInterval time.Duration // 非活跃轮询间隔，默认 10s
	ActiveWindow     time.Duration // 最近一条记录在此窗口内视为活跃，默认 2min
	Tail             int           // 每次拉取条数，默认 20
	// 回调（可选）。OnTurn 里耗时过长会拖慢轮询，请自行 go 出去处理。
	OnTurn        func(ctx context.Context, turn ConversationTurn)
	OnStateChange func(ctx context.Context, state ConversationState)
	// 测试注入
	Now func() time.Time
}

func (o *ConversationWatchOptions) fill() error {
	if o.Slot <= 0 {
		return fmt.Errorf("clt01: WatchConversations 需要 Slot（1-5）")
	}
	if o.ActiveInterval <= 0 {
		o.ActiveInterval = DefaultActiveInterval
	}
	if o.InactiveInterval <= 0 {
		o.InactiveInterval = DefaultInactiveInterval
	}
	if o.ActiveWindow <= 0 {
		o.ActiveWindow = DefaultActiveWindow
	}
	if o.Tail <= 0 {
		o.Tail = DefaultConversationTail
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return nil
}

type historyEnvelope struct {
	Items []struct {
		ChatID    string    `json:"chat_id"`
		TurnID    string    `json:"turn_id"`
		Role      string    `json:"role"`
		Content   string    `json:"content"`
		CreatedAt time.Time `json:"created_at"`
		Slot      int       `json:"slot_number"`
	} `json:"items"`
}

// seenCap 去重集合上限（超过就淘汰最旧的，避免长跑内存无限涨）。
const seenCap = 2000

// ConversationWatcher 一个运行中的会话监听器。
// Run（阻塞）/Stop/State/Records；由 WatchConversations 创建。
type ConversationWatcher struct {
	c   *Client
	opt ConversationWatchOptions

	mu       sync.Mutex
	state    ConversationState
	seen     map[string]bool // chat_id 去重
	seenRing []string        // 去重集合的淘汰顺序
	records  []ConversationTurn
	lastSeen time.Time // 最近一条记录的 created_at

	cancel context.CancelFunc // Stop 用；Run 退出时清空
	done   chan struct{}       // Run 退出信号（Start 后可用）
	once   sync.Once
}

// WatchConversations 创建一个会话监听器（不启动；调用 Run 开始轮询，或 Start 后台跑）。
func (c *Client) WatchConversations(_ context.Context, opt ConversationWatchOptions) (*ConversationWatcher, error) {
	if err := opt.fill(); err != nil {
		return nil, err
	}
	return &ConversationWatcher{
		c:     c,
		opt:   opt,
		state: ConversationInactive,
		seen:  map[string]bool{},
		done:  make(chan struct{}),
	}, nil
}

// Start 后台 goroutine 跑 Run。返回自身；ctx 取消或 Stop 时退出。
func (w *ConversationWatcher) Start(ctx context.Context) *ConversationWatcher {
	w.once.Do(func() {
		ctx, cancel := context.WithCancel(ctx)
		w.mu.Lock()
		w.cancel = cancel
		w.mu.Unlock()
		go func() {
			defer close(w.done)
			if err := w.Run(ctx); err != nil {
				fmt.Printf("clt01: conversation watcher slot %d exited: %v\n", w.opt.Slot, err)
			}
		}()
	})
	return w
}

// Stop 停止监听并等待 Run 退出（最多等 doneTimeout）。可重复调用。
func (w *ConversationWatcher) Stop() {
	w.mu.Lock()
	cancel := w.cancel
	w.mu.Unlock()
	if cancel == nil {
		return // 从未 Start：无需停止
	}
	cancel()
	select {
	case <-w.done:
	case <-time.After(5 * time.Second): // 卡在网络请求上也不阻塞调用方
	}
}

// Done 返回 Run 退出信号（未 Start 时返回已关闭通道，避免调用方永久阻塞）。
func (w *ConversationWatcher) Done() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return w.done
}

// State 读取当前活跃状态。
func (w *ConversationWatcher) State() ConversationState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

// Records 返回内存里保留的最近对话记录（旧的在前，上限 MaxConversationRecords）。
func (w *ConversationWatcher) Records() []ConversationTurn {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]ConversationTurn, len(w.records))
	copy(out, w.records)
	return out
}

// markSeenLocked 记一个 chat_id，超过 seenCap 就淘汰最旧的（调用方持有 w.mu）。
func (w *ConversationWatcher) markSeenLocked(id string) {
	w.seen[id] = true
	w.seenRing = append(w.seenRing, id)
	if len(w.seenRing) > seenCap {
		drop := len(w.seenRing) - seenCap
		for _, old := range w.seenRing[:drop] {
			delete(w.seen, old)
		}
		w.seenRing = w.seenRing[drop:]
	}
}

// Run 阻塞轮询直到 ctx 取消。首次拉取只入账不回调（冷启动不重放历史）。
func (w *ConversationWatcher) Run(ctx context.Context) error {
	first := true
	for {
		interval := w.opt.InactiveInterval
		if w.State() == ConversationActive {
			interval = w.opt.ActiveInterval
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
		if err := w.pollOnce(ctx, first); err != nil {
			// 轮询失败不打断监听：设备可能临时离线，按当前节奏重试
			fmt.Printf("clt01: conversation poll slot %d: %v\n", w.opt.Slot, err)
			continue
		}
		first = false
	}
}

// pollOnce 拉一次聊天记录并处理新增轮次与活跃状态。
func (w *ConversationWatcher) pollOnce(ctx context.Context, first bool) error {
	raw, err := w.c.ChatHistory(ctx, ChatHistoryParams{Slot: w.opt.Slot, Limit: w.opt.Tail})
	if err != nil {
		return err
	}
	var env historyEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("clt01: 解析聊天记录失败: %w", err)
	}

	// 手册明确：页内记录始终按消息编号正序排列（old→new），逐条顺序处理即可。
	// 不要倒序——倒序会让 records 截断时丢掉最新记录、OnTurn 时序也反。
	items := env.Items
	turns := make([]ConversationTurn, 0, len(items))
	for _, it := range items {
		turns = append(turns, ConversationTurn{
			ChatID: it.ChatID, TurnID: it.TurnID, Role: it.Role,
			Content: it.Content, CreatedAt: it.CreatedAt, Slot: it.Slot,
		})
	}

	now := w.opt.Now()
	newest := time.Time{}
	var fresh []ConversationTurn

	w.mu.Lock()
	for _, t := range turns {
		if t.ChatID != "" && w.seen[t.ChatID] {
			continue
		}
		if t.ChatID != "" {
			w.markSeenLocked(t.ChatID)
		}
		if !first { // 冷启动不回调，只入账
			fresh = append(fresh, t)
		}
		w.records = append(w.records, t)
		if len(w.records) > MaxConversationRecords {
			w.records = w.records[len(w.records)-MaxConversationRecords:]
		}
		if t.CreatedAt.After(newest) {
			newest = t.CreatedAt
		}
	}
	if newest.After(w.lastSeen) {
		w.lastSeen = newest
	}
	// 活跃判定：本轮有新记录，或最近一条记录落在 ActiveWindow 内
	active := len(fresh) > 0 || (!w.lastSeen.IsZero() && now.Sub(w.lastSeen) < w.opt.ActiveWindow)
	prev := w.state
	if active {
		w.state = ConversationActive
	} else {
		w.state = ConversationInactive
	}
	w.mu.Unlock()

	// 回调放到锁外
	for _, t := range fresh {
		if w.opt.OnTurn != nil {
			w.opt.OnTurn(ctx, t)
		}
	}
	if prev != w.state && w.opt.OnStateChange != nil {
		w.opt.OnStateChange(ctx, w.state)
	}
	return nil
}
