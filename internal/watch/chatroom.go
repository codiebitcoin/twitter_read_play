package watch

import (
	"fmt"
	"strings"
	"time"

	"twitter_read_play/internal/model"
)

// ---- ChatRoom 多角色对话接播 ----
// 一段对话 = 若干行（每行：说话角色 + 台词 [+ 可选 machines]）。逐行入队播报队列并打同一
// GroupID，组内按行号严格串行（A 播完 → B 播 → B 播完 → C 播）。
// 一行默认下发到该角色**全部**启用绑定（所有机器一起播，与 Feed 播报同口径）；行内多台
// 机器共用同一 GroupSeq 并行播（组串行只看「更小的 seq」，不拦同行），下一行等本行全部
// 机器播完才接。Machines 指定时只在这些机器上播。由 takePendingLocked 的「同组有 running
// 不取」约束 + finish 后的 kickNext 唤醒共同保证。
// 台词不走推文：TweetID 为空、Text 非空，execReal 构造内存伪推文按角色绑定朗读。

// ChatLine 对话脚本的一行。Machines 空 = 该角色全部启用绑定（默认）；非空 = 只播这些机器。
type ChatLine struct {
	RoleID   string   `json:"role_id"`
	Text     string   `json:"text"`
	Machines []string `json:"machines,omitempty"`
}

// ChatRoomOpts 播放节奏。
type ChatRoomOpts struct {
	// Interval 相邻两行之间的停顿（一轮结束到下一轮开始也用它）。
	Interval time.Duration
	// Repeats 播放轮数：0 = 一轮（默认），N>1 = N 轮，-1 = 一直重复（直到手动停止）。
	Repeats int
}

const (
	// ChatRoomDefaultRepeats 不传重复次数时播一轮。
	ChatRoomDefaultRepeats = 1
	// ChatRoomRepeatForever 一直重复（直到手动停止）。
	ChatRoomRepeatForever = -1
)

// chatRoomLine 校验并解析后的一行（角色 + 台词 + 目标绑定），重复播放时复用。
type chatRoomLine struct {
	role  *model.Role
	text  string
	binds []model.Binding
}

type chatRoomSession struct {
	groupID   string
	lines     []chatRoomLine
	opts      ChatRoomOpts
	iteration int
	stopCh    chan struct{}
}

// filterBindingsByMachines machines 非空时只留这些机器上的绑定（按 machine_id 匹配，
// 忽略空白项）；空 = 原样返回（默认全部）。
func filterBindingsByMachines(binds []model.Binding, machines []string) []model.Binding {
	if len(machines) == 0 {
		return binds
	}
	want := make(map[string]bool, len(machines))
	for _, id := range machines {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	out := make([]model.Binding, 0, len(binds))
	for _, b := range binds {
		if want[b.MachineID] {
			out = append(out, b)
		}
	}
	return out
}

// EnqueueChatRoom 把一段对话逐行入队（播一轮），返回会话 id 与条目快照。
// 先整体校验再入队：任何一行角色不存在 / 没有可播绑定 / 台词为空，整体报错不入队
// （避免播到一半卡住）。
func (m *Manager) EnqueueChatRoom(lines []ChatLine) (string, []QueueItem, error) {
	return m.EnqueueChatRoomWithOpts(lines, ChatRoomOpts{Repeats: ChatRoomDefaultRepeats})
}

// EnqueueChatRoomWithOpts 同 EnqueueChatRoom，但带播放间隔与重复轮数。
// Repeats=-1 表示一直重复；间隔挂在每行末尾，轮次之间也按它停顿。
func (m *Manager) EnqueueChatRoomWithOpts(lines []ChatLine, opts ChatRoomOpts) (string, []QueueItem, error) {
	if len(lines) == 0 {
		return "", nil, fmt.Errorf("对话为空")
	}
	if opts.Interval < 0 {
		opts.Interval = 0
	}
	if opts.Repeats == 0 {
		opts.Repeats = ChatRoomDefaultRepeats
	}
	resolved := make([]chatRoomLine, 0, len(lines))
	for i, ln := range lines {
		text := strings.TrimSpace(ln.Text)
		if text == "" {
			return "", nil, fmt.Errorf("第 %d 行台词为空", i+1)
		}
		r, err := m.roles.Get(ln.RoleID)
		if err != nil {
			return "", nil, fmt.Errorf("第 %d 行：%w", i+1, err)
		}
		binds := filterBindingsByMachines(m.enabledBindings(r), ln.Machines)
		if len(binds) == 0 {
			if len(ln.Machines) > 0 {
				return "", nil, fmt.Errorf("第 %d 行：角色 %s 在所选机器上没有启用的绑定", i+1, r.Name)
			}
			return "", nil, fmt.Errorf("第 %d 行：角色 %s 没有启用的机器绑定", i+1, r.Name)
		}
		resolved = append(resolved, chatRoomLine{role: r, text: text, binds: binds})
	}

	s := &chatRoomSession{
		groupID:   fmt.Sprintf("cr-%d", time.Now().UnixNano()),
		lines:     resolved,
		opts:      opts,
		iteration: 1,
		stopCh:    make(chan struct{}),
	}
	items := m.enqueueChatIteration(s)
	// 只播一轮时不需要后台循环；多轮/无限轮由会话循环在每轮播完后续排。
	if opts.Repeats > 1 || opts.Repeats < 0 {
		m.chatMu.Lock()
		if m.chatSessions == nil {
			m.chatSessions = map[string]*chatRoomSession{}
		}
		m.chatSessions[s.groupID] = s
		m.chatMu.Unlock()
		go m.chatRoomLoop(s)
	}
	return s.groupID, items, nil
}

// enqueueChatIteration 把当前轮次逐行入队。GroupSeq 跨轮次单调递增，
// 队列仍按「上一行播完 + 间隔」放行下一行。
func (m *Manager) enqueueChatIteration(s *chatRoomSession) []QueueItem {
	n := len(s.lines)
	items := make([]QueueItem, 0, n)
	for i, ln := range s.lines {
		seq := (s.iteration-1)*n + i + 1
		for _, b := range ln.binds {
			tw := model.Tweet{Text: ln.text}
			snap := m.queue().enqueue(&QueueItem{
				RoleID:    ln.role.ID,
				RoleName:  ln.role.Name,
				Preview:   queuePreview(tw),
				MachineID: b.MachineID,
				Slot:      b.Slot,
				Source:    "chatroom",
				GroupID:   s.groupID,
				GroupSeq:  seq,
				Text:      ln.text,
				tw:        tw,
				binding:   b,
				gapAfter:  s.opts.Interval,
			})
			items = append(items, snap)
		}
	}
	return items
}

// chatRoomLoop 等当前轮播完，再按 Repeats 决定是否续排下一轮。
func (m *Manager) chatRoomLoop(s *chatRoomSession) {
	defer m.dropChatSession(s)
	for {
		if !m.waitChatIteration(s) {
			return
		}
		if s.opts.Repeats > 0 && s.iteration >= s.opts.Repeats {
			return
		}
		select {
		case <-s.stopCh:
			return
		default:
		}
		s.iteration++
		m.enqueueChatIteration(s)
	}
}

// waitChatIteration 等本轮（GroupSeq ≤ iteration*n）全部播完；停止时返回 false。
func (m *Manager) waitChatIteration(s *chatRoomSession) bool {
	maxSeq := s.iteration * len(s.lines)
	for {
		select {
		case <-s.stopCh:
			return false
		default:
		}
		unfinished := 0
		for _, it := range m.queue().Snapshot() {
			if it.GroupID == s.groupID && it.GroupSeq <= maxSeq && it.unfinished() {
				unfinished++
			}
		}
		if unfinished == 0 {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (m *Manager) dropChatSession(s *chatRoomSession) {
	m.chatMu.Lock()
	if cur := m.chatSessions[s.groupID]; cur == s {
		delete(m.chatSessions, s.groupID)
	}
	m.chatMu.Unlock()
}

// StopChatRoom 停止一场对话：停掉续排循环、丢弃该组待播，并立刻中止正在播的那一行
//（取消等待 + 让设备停声）。返回丢弃的待播条数。
func (m *Manager) StopChatRoom(groupID string) int {
	m.chatMu.Lock()
	s := m.chatSessions[groupID]
	if s != nil {
		close(s.stopCh)
		delete(m.chatSessions, groupID)
	}
	m.chatMu.Unlock()
	// 先停声（用户按下停止就该立刻安静），再清掉还没开播的行
	m.queue().CancelRunningGroup(groupID)
	return m.queue().ClearPendingGroup(groupID)
}
