package watch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/model"
)

// ---- 播报队列 ----
// 所有播报入口（手动点播 / 随机播报 / 自动新推文 / 自动随机播报）统一先入队，再消费：
//   - 按机器分组，同一台机器串行 —— 一台设备同时只能念一条，避免连点/多角色抢同一台机器时叠着播；
//   - 不同机器之间并行。
// 队列只在内存里（重启即清空），用于排队与可视化；已完成条目保留一段时间供前端查看。

const (
	queueMaxKeep  = 200              // 全局最多保留多少条（超长时先丢最旧的已完成条目）
	queueKeepDone = 10 * time.Minute // 已完成条目保留时长

	// 空闲快路径：该机器队列已空、且距上次播完超过这个时长，就跳过播前角色包比对
	// （省 2 次设备 HTTP，直接下发朗读）。角色包漂移由手动 sync/check 兜底。
	idleFastPathAfter = 3 * time.Minute
)

// QueueItem 队列里的一条播报任务。
type QueueItem struct {
	ID        string `json:"id"`
	RoleID    string `json:"role_id"`
	RoleName  string `json:"role_name"`
	Platform  string `json:"platform,omitempty"` // twitter / weibo / xiaohongshu
	TweetID   string `json:"tweet_id"`
	Text      string `json:"text,omitempty"` // 自定义台词（ChatRoom）：非推文，空=播 TweetID 对应推文
	Preview   string `json:"preview"`
	MachineID string `json:"machine_id"`
	Machine   string `json:"machine_name"`
	Slot      int    `json:"slot"`
	Source    string `json:"source"`              // manual | random | auto | poll | chatroom
	GroupID   string `json:"group_id,omitempty"`  // ChatRoom 会话 id：非空=组内跨机器严格串行
	GroupSeq  int    `json:"group_seq,omitempty"` // 组内行号（1 起）
	Status    string `json:"status"`              // pending | running | done | failed
	Error     string `json:"error,omitempty"`

	EnqueuedAt time.Time  `json:"enqueued_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// 执行所需；不下发前端。
	tw      model.Tweet
	binding model.Binding
	// gapAfter 本行播完后，组内下一行还要等多久才开播（ChatRoom 播放间隔）。
	// 挂在「前一行」上：行间隔、轮次间隔共用同一套机制。
	gapAfter time.Duration
	// ctx/cancel 本条开播后生效：ChatRoom stop 取消它即可立刻中止这一行
	//（停止等待 + 让设备停声），见 CancelRunningGroup。
	ctx    context.Context
	cancel context.CancelFunc
}

// unfinished 未播完（前端据此决定要不要显示队列弹窗）。
func (it *QueueItem) unfinished() bool { return it.Status == "pending" || it.Status == "running" }

type broadcastQueue struct {
	m     *Manager
	exec  func(*QueueItem) error // 执行体（测试可注入）
	mu    sync.Mutex
	items []*QueueItem
	busy  map[string]bool // machineID -> 该机器的 worker 是否在跑
	seq   int

	lastDone map[string]time.Time // machineID -> 上次播完时间（空闲快路径用）
}

func newBroadcastQueue(m *Manager) *broadcastQueue {
	q := &broadcastQueue{m: m, busy: map[string]bool{}, lastDone: map[string]time.Time{}}
	q.exec = q.execReal
	return q
}

// fastPathEligible 空闲快路径判定：该机器没有待播条目，且距上次播完（进程内记录）
// 超过 idleFastPathAfter（从没播过也视为空闲）。
func (q *broadcastQueue) fastPathEligible(machineID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if t, ok := q.lastDone[machineID]; ok && time.Since(t) < idleFastPathAfter {
		return false
	}
	for _, it := range q.items {
		if it.MachineID == machineID && it.Status == "pending" {
			return false
		}
	}
	return true
}

// idleFor 距该机器上次播完的时长（没播过返回上限，仅日志展示用）。
func (q *broadcastQueue) idleFor(machineID string) time.Duration {
	q.mu.Lock()
	defer q.mu.Unlock()
	if t, ok := q.lastDone[machineID]; ok {
		return time.Since(t)
	}
	return idleFastPathAfter
}

// enqueue 入队一条；该机器没有 worker 时顺手拉起一个。返回入队后的快照（供接口回显）。
func (q *broadcastQueue) enqueue(it *QueueItem) QueueItem {
	q.mu.Lock()
	q.seq++
	it.ID = fmt.Sprintf("bq-%d-%d", time.Now().UnixMilli(), q.seq)
	it.Status = "pending"
	it.EnqueuedAt = time.Now()
	// 兜底：没有目标机器的条目排不出去（没有对应 worker），直接标失败而不是永远 pending
	if it.MachineID == "" {
		now := time.Now()
		it.Status, it.Error, it.FinishedAt = "failed", "no target machine", &now
	}
	q.items = append(q.items, it)
	q.pruneLocked()
	snap := *it
	machineID := it.MachineID
	startWorker := machineID != "" && it.Status == "pending" && !q.busy[machineID]
	if startWorker {
		q.busy[machineID] = true
	}
	q.mu.Unlock()
	if startWorker {
		go q.worker(machineID)
	}
	return snap
}

// worker 消费某台机器的队列：串行取 pending，直到队列空才退出。
// 取任务与「清 busy 标记」在同一把锁里完成，所以 enqueue 不会丢唤醒。
func (q *broadcastQueue) worker(machineID string) {
	for {
		q.mu.Lock()
		it := q.takePendingLocked(machineID)
		if it == nil {
			// 只被「行间隔」挡住时不能直接退出：没有新事件会再来唤醒。
			if wait, ok := q.gapWaitLocked(machineID); ok {
				q.mu.Unlock()
				if wait < 20*time.Millisecond {
					wait = 20 * time.Millisecond
				}
				if wait > 2*time.Second {
					wait = 2 * time.Second
				}
				time.Sleep(wait)
				continue
			}
			delete(q.busy, machineID)
			q.mu.Unlock()
			return
		}
		q.mu.Unlock()
		q.run(it)
	}
}

// gapWaitLocked 该机器是否有「只差间隔到点」的待播行；返回还要等多久（调用方持锁）。
// 被前一行未播完挡住时返回 false：那种情况由 finish 后的 kickNext 唤醒。
func (q *broadcastQueue) gapWaitLocked(machineID string) (time.Duration, bool) {
	for _, it := range q.items {
		if it.MachineID != machineID || it.Status != "pending" || it.GroupID == "" {
			continue
		}
		ready, wait := q.groupReadyLocked(it)
		if ready {
			return 0, false // 理论不可达：takePendingLocked 会先取走
		}
		if wait > 0 {
			return wait, true
		}
	}
	return 0, false
}

func (q *broadcastQueue) takePendingLocked(machineID string) *QueueItem {
	for _, it := range q.items {
		if it.MachineID == machineID && it.Status == "pending" {
			// 组接播（ChatRoom）：组内前面的行没播完（含还没开播）时本行必须等
			// —— 跨机器严格 A→B→C。「前面的行 done 才轮到我」与机器无关，天然无竞态。
			// 另外前一行播完后还要等 gapAfter，形成行间隔/轮次间隔。
			if it.GroupID != "" {
				if ready, _ := q.groupReadyLocked(it); !ready {
					continue
				}
			}
			now := time.Now()
			it.Status, it.StartedAt = "running", &now
			// 取消句柄与 running 状态同锁建立：stop 不会错过「刚开播、还没进 run」的这条
			it.ctx, it.cancel = context.WithCancel(context.Background())
			return it
		}
	}
	return nil
}

// groupReadyLocked 组内序号在 it 之前的行是否都已播完且间隔已到（调用方持锁）。
// 第二个返回值是「只差间隔」时还要等多久；被未播完的行挡住时返回 0。
func (q *broadcastQueue) groupReadyLocked(it *QueueItem) (bool, time.Duration) {
	prevSeq := 0
	var latest *time.Time
	var gap time.Duration
	for _, o := range q.items {
		if o.GroupID != it.GroupID || o.GroupSeq >= it.GroupSeq {
			continue
		}
		if o.unfinished() {
			return false, 0
		}
		if o.GroupSeq > prevSeq {
			prevSeq = o.GroupSeq
			latest = o.FinishedAt
			gap = o.gapAfter
			continue
		}
		if o.GroupSeq == prevSeq {
			// 同一行多台机器并行：以最晚播完的那台为准，间隔取最大。
			if o.FinishedAt != nil && (latest == nil || o.FinishedAt.After(*latest)) {
				latest = o.FinishedAt
			}
			if o.gapAfter > gap {
				gap = o.gapAfter
			}
		}
	}
	if latest != nil && gap > 0 {
		if wait := time.Until(latest.Add(gap)); wait > 0 {
			return false, wait
		}
	}
	return true, 0
}

// run 执行一条队列任务（执行体可注入，测试用）。
func (q *broadcastQueue) run(it *QueueItem) {
	if it.cancel != nil {
		defer it.cancel()
	}
	q.finish(it, q.exec(it))
}

// execReal 默认执行体：解析角色、下发设备（播报统一走 SDK 原子 PlayChat，无需上下文）。
// ChatRoom 台词行（Text 非空）：不走推文，构造内存伪推文直接按绑定朗读。
func (q *broadcastQueue) execReal(it *QueueItem) error {
	m := q.m
	r, err := m.roles.Get(it.RoleID)
	if err != nil {
		return err
	}
	tw := it.tw
	if it.TweetID == "" && it.Text != "" {
		tw = model.Tweet{Text: it.Text}
	}
	ctx := it.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return m.broadcast(ctx, r, it.binding, tw, it.Source)
}

func (q *broadcastQueue) finish(it *QueueItem, err error) {
	now := time.Now()
	q.mu.Lock()
	it.FinishedAt = &now
	switch {
	case err == nil:
		it.Status = "done"
	case errors.Is(err, errPlaybackStopped):
		// 用户手动停止（ChatRoom stop）：当作正常结束，别把整行标红
		it.Status = "done"
	default:
		it.Status, it.Error = "failed", err.Error()
	}
	q.lastDone[it.MachineID] = now
	q.pruneLocked()
	q.mu.Unlock()
	if err != nil && !errors.Is(err, errPlaybackStopped) {
		log.Printf("[queue] broadcast failed: %s slot %d %s tweet %s: %v", it.MachineID, it.Slot, it.RoleName, it.TweetID, err)
	}
	// 组接播唤醒：本行播完可能解锁了下一行（在别的机器上，enqueue 时没拉过它的 worker）
	q.kickNext()
}

// kickNext 组接播：找第一条「未被组压住」的 pending，按需拉起其机器 worker。
// enqueue 只在入队机器无 worker 时拉起；下一行在另一台机器且那台空闲时，全靠这里补唤醒。
func (q *broadcastQueue) kickNext() {
	q.mu.Lock()
	start := map[string]bool{}
	for _, it := range q.items {
		if it.Status != "pending" || it.MachineID == "" {
			continue
		}
		if it.GroupID != "" {
			ready, wait := q.groupReadyLocked(it)
			// 被前一行挡住：等它播完时再唤醒；只差间隔：现在就拉起 worker 让它自己等。
			if !ready && wait <= 0 {
				continue
			}
		}
		start[it.MachineID] = true
	}
	var toStart []string
	for machineID := range start {
		if q.busy[machineID] {
			continue
		}
		q.busy[machineID] = true
		toStart = append(toStart, machineID)
	}
	q.mu.Unlock()
	for _, machineID := range toStart {
		go q.worker(machineID)
	}
}

// Snapshot 队列快照（入队顺序）。
func (q *broadcastQueue) Snapshot() []QueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pruneLocked()
	out := make([]QueueItem, 0, len(q.items))
	for _, it := range q.items {
		out = append(out, *it)
	}
	return out
}

// ClearPending 丢弃待播条目（machineID 为空 = 所有机器）；已在播的不动。返回丢弃条数。
func (q *broadcastQueue) ClearPending(machineID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	out := q.items[:0]
	for _, it := range q.items {
		if it.Status == "pending" && (machineID == "" || it.MachineID == machineID) {
			n++
			continue
		}
		out = append(out, it)
	}
	q.items = out
	return n
}

// ClearPendingRole 丢弃某角色的全部待播条目（Stream 面板「清空本角色待播」用）；
// 已在播的不动。返回丢弃条数。
func (q *broadcastQueue) ClearPendingRole(roleID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	out := q.items[:0]
	for _, it := range q.items {
		if it.Status == "pending" && it.RoleID == roleID {
			n++
			continue
		}
		out = append(out, it)
	}
	q.items = out
	return n
}

// ClearPendingGroup 丢弃某组（ChatRoom 会话）的全部待播条目；正在播的不动。返回丢弃条数。
func (q *broadcastQueue) ClearPendingGroup(groupID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	out := q.items[:0]
	for _, it := range q.items {
		if it.Status == "pending" && it.GroupID != "" && it.GroupID == groupID {
			n++
			continue
		}
		out = append(out, it)
	}
	q.items = out
	return n
}

// CancelRunningGroup 立刻中止该组**正在播**的条目（ChatRoom stop）：取消其 ctx，
// 执行体据此停止等待并让设备停声。返回取消的条数。
func (q *broadcastQueue) CancelRunningGroup(groupID string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, it := range q.items {
		if it.GroupID == groupID && it.Status == "running" && it.cancel != nil {
			it.cancel()
			n++
		}
	}
	return n
}

// pruneLocked 清掉过期的已完成条目并限制总长度（调用方持锁）。
// pending/running 永不丢弃 —— 丢一条待播等于静默取消用户的操作。
func (q *broadcastQueue) pruneLocked() {
	cut := time.Now().Add(-queueKeepDone)
	out := make([]*QueueItem, 0, len(q.items))
	for _, it := range q.items {
		if !it.unfinished() && it.FinishedAt != nil && it.FinishedAt.Before(cut) {
			continue
		}
		out = append(out, it)
	}
	if drop := len(out) - queueMaxKeep; drop > 0 {
		kept := make([]*QueueItem, 0, len(out))
		for _, it := range out {
			if drop > 0 && !it.unfinished() {
				drop--
				continue
			}
			kept = append(kept, it)
		}
		out = kept
	}
	q.items = out
}

// RemoveItem 删掉一条「待播」条目（正在播/已完成的不给删，返回 false）。返回是否删掉。
func (q *broadcastQueue) RemoveItem(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	removed := false
	out := q.items[:0]
	for _, it := range q.items {
		if !removed && it.ID == id && it.Status == "pending" {
			removed = true
			continue
		}
		out = append(out, it)
	}
	q.items = out
	return removed
}

// BroadcastQueue 队列快照（API 用）。
func (m *Manager) BroadcastQueue() []QueueItem { return m.queue().Snapshot() }

// ClearBroadcastQueue 丢弃待播条目（machineID 为空=所有机器）；返回丢弃条数。
func (m *Manager) ClearBroadcastQueue(machineID string) int { return m.queue().ClearPending(machineID) }

// ClearBroadcastQueueByRole 丢弃某角色的全部待播条目；返回丢弃条数。
func (m *Manager) ClearBroadcastQueueByRole(roleID string) int {
	return m.queue().ClearPendingRole(roleID)
}

// RemoveBroadcastQueueItem 删掉一条待播条目；返回是否删掉。
func (m *Manager) RemoveBroadcastQueueItem(id string) bool { return m.queue().RemoveItem(id) }

// ClearChatRoomGroup 丢弃某 ChatRoom 会话的待播条目（正在播的不动）；返回丢弃条数。
func (m *Manager) ClearChatRoomGroup(groupID string) int { return m.queue().ClearPendingGroup(groupID) }

// queuePreview 队列弹窗里显示的正文预览（口语化文本前 80 字）。
func queuePreview(tw model.Tweet) string {
	s := strings.Join(strings.Fields(playbackText(tw)), " ")
	if s == "" {
		s = strings.Join(strings.Fields(tw.EffectiveText()), " ")
	}
	r := []rune(s)
	if len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}
