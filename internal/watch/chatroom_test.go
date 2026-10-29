package watch

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"twitter_read_play/internal/machine"
	"twitter_read_play/internal/model"
	"twitter_read_play/internal/role"
	"twitter_read_play/internal/store"
)

// waitQueueIdle 等队列全部跑完（超时 fail）。
func waitQueueIdle(t *testing.T, q *broadcastQueue) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		snap := q.Snapshot()
		pending := 0
		for i := range snap {
			if snap[i].unfinished() {
				pending++
			}
		}
		if pending == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("队列没跑完，仍有 %d 条未完成", pending)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// machines 过滤：空 = 全部（默认所有机器都播）；非空按 machine_id 留（同机多槽都保留）；
// 空白项忽略；不含任何匹配机器时返回空（调用方报错不入队）。
func TestFilterBindingsByMachines(t *testing.T) {
	binds := []model.Binding{
		{MachineID: "m1", Slot: 1, Enabled: true},
		{MachineID: "m2", Slot: 1, Enabled: true},
		{MachineID: "m2", Slot: 2, Enabled: true},
	}
	if got := filterBindingsByMachines(binds, nil); len(got) != 3 {
		t.Errorf("machines 空 = 全部：got %d, want 3", len(got))
	}
	got := filterBindingsByMachines(binds, []string{"m2"})
	if len(got) != 2 || got[0].Slot != 1 || got[1].Slot != 2 {
		t.Errorf("machines=[m2] = %v, want m2 的两个槽位", got)
	}
	if got := filterBindingsByMachines(binds, []string{" ", "m9"}); len(got) != 0 {
		t.Errorf("machines 无匹配 = 空，got %v", got)
	}
}

// 一行多机（同 GroupSeq）：同行多台机器并行播，下一行等本行**全部**机器播完才接
// （组串行只拦「更小的 seq」，不拦同行）。
func TestChatRoomSameSeqParallelAcrossMachines(t *testing.T) {
	q := newBroadcastQueue(nil)
	var mu sync.Mutex
	active, maxActive := 0, 0
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	q.exec = func(it *QueueItem) error {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		if it.GroupSeq == 1 {
			started <- struct{}{}
			<-release // 第 1 行两台机器都停在这里，模拟本行还没播完
		}
		mu.Lock()
		active--
		mu.Unlock()
		return nil
	}
	// 第 1 行同发 m1/m2（同 seq），第 2 行在 m3
	q.enqueue(&QueueItem{MachineID: "m1", GroupID: "cr-1", GroupSeq: 1})
	q.enqueue(&QueueItem{MachineID: "m2", GroupID: "cr-1", GroupSeq: 1})
	q.enqueue(&QueueItem{MachineID: "m3", GroupID: "cr-1", GroupSeq: 2})
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("第 1 行没在两台机器上同时开播")
		}
	}
	close(release)
	waitQueueIdle(t, q)
	mu.Lock()
	defer mu.Unlock()
	if maxActive != 2 {
		t.Errorf("同行多机最大并发 = %d, want 2（并行）", maxActive)
	}
}

// 组接播核心语义：同一 GroupID 的行跨机器严格串行 —— A 播完才轮到 B，B 播完才轮到 C，
// 即使三行分属三台不同机器（普通播报跨机器是并行的，组内不许）。
func TestChatRoomGroupSerialAcrossMachines(t *testing.T) {
	q := newBroadcastQueue(nil)
	var mu sync.Mutex
	active := 0
	maxActive := 0
	var order []string

	q.exec = func(it *QueueItem) error {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // 模拟设备播报耗时
		mu.Lock()
		active--
		order = append(order, fmt.Sprintf("%d", it.GroupSeq))
		mu.Unlock()
		return nil
	}

	// 三行分属三台机器：A(m1) → B(m2) → C(m3)，同一组
	for i, m := range []string{"m1", "m2", "m3"} {
		q.enqueue(&QueueItem{MachineID: m, GroupID: "cr-1", GroupSeq: i + 1, Source: "chatroom"})
	}
	waitQueueIdle(t, q)

	mu.Lock()
	defer mu.Unlock()
	if maxActive != 1 {
		t.Errorf("组内最大并发 %d，应严格串行(=1)", maxActive)
	}
	if len(order) != 3 || order[0] != "1" || order[1] != "2" || order[2] != "3" {
		t.Errorf("组内顺序 = %v，应 [1 2 3]", order)
	}
}

// 组内某行失败：记失败后跳过，后续行继续接播。
func TestChatRoomGroupFailureSkipsAndContinues(t *testing.T) {
	q := newBroadcastQueue(nil)
	var ran []int
	var mu sync.Mutex
	q.exec = func(it *QueueItem) error {
		mu.Lock()
		ran = append(ran, it.GroupSeq)
		mu.Unlock()
		if it.GroupSeq == 1 {
			return errors.New("device 400: nothing to speak")
		}
		return nil
	}
	for i, m := range []string{"m1", "m2", "m3"} {
		q.enqueue(&QueueItem{MachineID: m, GroupID: "cr-1", GroupSeq: i + 1, Source: "chatroom"})
	}
	waitQueueIdle(t, q)

	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 3 {
		t.Fatalf("应播 3 行（1 失败跳过），实际 %v", ran)
	}
	snap := q.Snapshot()
	if snap[0].Status != "failed" {
		t.Errorf("第 1 行状态 = %s，want failed", snap[0].Status)
	}
	if snap[1].Status != "done" || snap[2].Status != "done" {
		t.Errorf("失败后应继续：2=%s 3=%s，want done/done", snap[1].Status, snap[2].Status)
	}
}

// 组内同一角色可重复出现（A→B→A→C→A）：musk@两台机器交替，顺序仍严格按 GroupSeq。
func TestChatRoomGroupRepeatedSpeaker(t *testing.T) {
	q := newBroadcastQueue(nil)
	var mu sync.Mutex
	var order []string
	q.exec = func(it *QueueItem) error {
		mu.Lock()
		order = append(order, fmt.Sprintf("%s@%d", it.RoleID, it.GroupSeq))
		mu.Unlock()
		return nil
	}
	// 5 行同组，musk 出现在 1/3/5，鸣人在 2，trump 在 4；分属不同机器
	type ln struct{ role, m string }
	for i, x := range []ln{
		{"musk", "m1"}, {"naruto", "m2"}, {"musk", "m3"}, {"trump", "m1"}, {"musk", "m2"},
	} {
		q.enqueue(&QueueItem{RoleID: x.role, MachineID: x.m, GroupID: "cr-9", GroupSeq: i + 1, Source: "chatroom"})
	}
	waitQueueIdle(t, q)

	mu.Lock()
	defer mu.Unlock()
	want := []string{"musk@1", "naruto@2", "musk@3", "trump@4", "musk@5"}
	if len(order) != len(want) {
		t.Fatalf("应播 %d 行，实际 %v", len(want), order)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("顺序 = %v，want %v", order, want)
		}
	}
}

// 停止会话：只清该组 pending，其他条目与正在播的不动。
func TestChatRoomClearPendingGroup(t *testing.T) {
	q := newBroadcastQueue(nil)
	release := make(chan struct{})
	q.exec = func(it *QueueItem) error {
		<-release
		return nil
	}
	// 组1：1 在播（exec 阻塞中）、2/3 待播；组2：一条待播；组外普通播报一条
	q.enqueue(&QueueItem{MachineID: "m1", GroupID: "cr-1", GroupSeq: 1})
	q.enqueue(&QueueItem{MachineID: "m2", GroupID: "cr-1", GroupSeq: 2})
	q.enqueue(&QueueItem{MachineID: "m3", GroupID: "cr-1", GroupSeq: 3})
	q.enqueue(&QueueItem{MachineID: "m1", GroupID: "cr-2", GroupSeq: 1})
	q.enqueue(&QueueItem{MachineID: "m2", Source: "manual"})

	// 等 worker 真把第 1 行标成 running（否则停止会把在播的也清了）
	deadline := time.Now().Add(2 * time.Second)
	for {
		if q.Snapshot()[0].Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("第 1 行一直没开始播")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if n := q.ClearPendingGroup("cr-1"); n != 2 {
		t.Errorf("清掉 %d 条，want 2（正在播的不清）", n)
	}
	close(release)
	waitQueueIdle(t, q)

	snap := q.Snapshot()
	// 组1 剩：1 done；组2 与普通条目都 done
	if snap[0].Status != "done" || snap[0].GroupID != "cr-1" {
		t.Errorf("正在播的组1行应播完 done，got %s", snap[0].Status)
	}
	for _, it := range snap[1:] {
		if it.GroupID == "cr-1" {
			t.Errorf("组1 待播条目应被清掉，还在: %s", it.ID)
		}
	}
	if len(snap) != 3 {
		t.Errorf("应剩 3 条（组1×1 + 组2×1 + 普通×1），got %d", len(snap))
	}
}

// 行间隔：前一行播完后要等 gapAfter 才轮到下一行（跨机器也一样）。
func TestChatRoomGapBetweenLines(t *testing.T) {
	q := newBroadcastQueue(nil)
	var mu sync.Mutex
	var ends, starts []time.Time
	q.exec = func(it *QueueItem) error {
		mu.Lock()
		starts = append(starts, time.Now())
		mu.Unlock()
		time.Sleep(15 * time.Millisecond)
		mu.Lock()
		ends = append(ends, time.Now())
		mu.Unlock()
		return nil
	}
	q.enqueue(&QueueItem{MachineID: "m1", GroupID: "cr-g", GroupSeq: 1, gapAfter: 200 * time.Millisecond})
	q.enqueue(&QueueItem{MachineID: "m2", GroupID: "cr-g", GroupSeq: 2})
	waitQueueIdle(t, q)

	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 2 {
		t.Fatalf("应播 2 行，实际 %d", len(starts))
	}
	if gap := starts[1].Sub(ends[0]); gap < 150*time.Millisecond {
		t.Fatalf("行间隔 = %v，应 ≥150ms（配置 200ms）", gap)
	}
}

// 重复播放：一轮播完后按间隔续排下一轮（seq 继续递增），播够轮数就停。
func TestChatRoomRepeatRounds(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rm, err := role.NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	mm, err := machine.NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := mm.Add("desk", "127.0.0.1:8081", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.Upsert(&model.Role{ID: "r1", Name: "A", Enabled: true,
		Bindings: []model.Binding{{MachineID: mc.ID, Slot: 1, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	m := &Manager{st: st, roles: rm, machines: mm}
	q := m.queue()
	var mu sync.Mutex
	var seqs []int
	q.exec = func(it *QueueItem) error {
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		seqs = append(seqs, it.GroupSeq)
		mu.Unlock()
		return nil
	}

	_, _, err = m.EnqueueChatRoomWithOpts(
		[]ChatLine{{RoleID: "r1", Text: "a"}, {RoleID: "r1", Text: "b"}},
		ChatRoomOpts{Interval: 30 * time.Millisecond, Repeats: 3})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(seqs)
		mu.Unlock()
		if n >= 6 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("3 轮 2 行应播 6 次，实际 %d", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // 再等一会，确认不会多播
	mu.Lock()
	defer mu.Unlock()
	if len(seqs) != 6 {
		t.Fatalf("应正好播 6 次（3 轮 × 2 行），实际 %v", seqs)
	}
	for i, seq := range seqs {
		if seq != i+1 {
			t.Fatalf("跨轮次 seq 应单调递增：%v", seqs)
		}
	}
}

// 一直重复：手动停止后不再续排。
func TestChatRoomRepeatForeverStops(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rm, err := role.NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	mm, err := machine.NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := mm.Add("desk", "127.0.0.1:8081", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.Upsert(&model.Role{ID: "r1", Name: "A", Enabled: true,
		Bindings: []model.Binding{{MachineID: mc.ID, Slot: 1, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	m := &Manager{st: st, roles: rm, machines: mm}
	q := m.queue()
	var mu sync.Mutex
	var seqs []int
	q.exec = func(it *QueueItem) error {
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		seqs = append(seqs, it.GroupSeq)
		mu.Unlock()
		return nil
	}

	gid, _, err := m.EnqueueChatRoomWithOpts([]ChatLine{{RoleID: "r1", Text: "a"}},
		ChatRoomOpts{Interval: 20 * time.Millisecond, Repeats: ChatRoomRepeatForever})
	if err != nil {
		t.Fatal(err)
	}
	// 等至少播到第 3 轮，说明确实在无限续排
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(seqs)
		mu.Unlock()
		if n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("一直重复应持续续排，实际只播 %d 次", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.StopChatRoom(gid)
	mu.Lock()
	after := len(seqs)
	mu.Unlock()
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(seqs) > after+1 { // 停止瞬间可能已有一条在播，允许 +1
		t.Fatalf("停止后仍在续排：停止时 %d，300ms 后 %d", after, len(seqs))
	}
}
