package watch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"twitter_read_play/internal/model"
)

// 队列条目下发前端时不能泄露执行用的内部字段。
func TestQueueItemJSONHidesInternals(t *testing.T) {
	it := QueueItem{
		ID: "bq-1", RoleID: "r1", RoleName: "Elon", TweetID: "100", Preview: "hi",
		MachineID: "m1", Machine: "desk", Slot: 2, Source: "manual", Status: "pending",
		tw:      model.Tweet{ID: "100", Text: "secret body"},
		binding: model.Binding{MachineID: "m1", Slot: 2},
	}
	b, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, leaked := range []string{"\"tw\"", "\"binding\"", "secret body"} {
		if strings.Contains(s, leaked) {
			t.Errorf("下发 JSON 泄露了内部字段 %s: %s", leaked, s)
		}
	}
	for _, want := range []string{"\"machine_id\":\"m1\"", "\"status\":\"pending\"", "\"preview\":\"hi\""} {
		if !strings.Contains(s, want) {
			t.Errorf("下发 JSON 缺少 %s: %s", want, s)
		}
	}
}

// ChatRoom stop：CancelRunningGroup 立刻中断正在播的那一条（执行体 ctx 被取消），
// 且停止不算失败（状态 done），而不是等这句自然念完。
func TestCancelRunningGroupStopsInFlight(t *testing.T) {
	q := newBroadcastQueue(nil)
	started := make(chan struct{})
	finished := make(chan error, 1)
	q.exec = func(it *QueueItem) error {
		close(started)
		<-it.ctx.Done()
		finished <- it.ctx.Err()
		return errPlaybackStopped
	}
	q.enqueue(&QueueItem{MachineID: "m1", GroupID: "cr-1", GroupSeq: 1})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("条目未开播")
	}
	if n := q.CancelRunningGroup("cr-1"); n != 1 {
		t.Fatalf("应取消 1 条正在播的条目，实际 %d", n)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("执行体的 ctx 应被取消，实际 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("执行体没有被立刻中断（stop 仍在等这一句播完）")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := ""
		for _, it := range q.Snapshot() {
			if it.GroupID == "cr-1" {
				st = it.Status
			}
		}
		if st == "done" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("用户停止后状态应为 done，实际 %q", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// 队列核心语义：同机器串行（一条播完才播下一条）、FIFO、不同机器并行。
func TestBroadcastQueueSerialPerMachine(t *testing.T) {
	q := newBroadcastQueue(nil)
	var mu sync.Mutex
	active := map[string]int{}
	maxActive := map[string]int{}
	order := map[string][]string{}

	q.exec = func(it *QueueItem) error {
		mu.Lock()
		active[it.MachineID]++
		if active[it.MachineID] > maxActive[it.MachineID] {
			maxActive[it.MachineID] = active[it.MachineID]
		}
		order[it.MachineID] = append(order[it.MachineID], it.TweetID)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // 模拟设备播报耗时
		mu.Lock()
		active[it.MachineID]--
		mu.Unlock()
		return nil
	}

	for _, s := range []struct{ m, tid string }{
		{"A", "1"}, {"B", "1"}, {"A", "2"}, {"B", "2"}, {"A", "3"},
	} {
		q.enqueue(&QueueItem{MachineID: s.m, TweetID: s.tid})
	}

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
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("队列没跑完，仍有 %d 条未完成", pending)
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	for m, n := range maxActive {
		if n != 1 {
			t.Errorf("机器 %s 最大并发 %d，应串行(=1)", m, n)
		}
	}
	if got := order["A"]; len(got) != 3 || got[0] != "1" || got[1] != "2" || got[2] != "3" {
		t.Errorf("机器 A 顺序 = %v，应 FIFO [1 2 3]", got)
	}
	if got := order["B"]; len(got) != 2 || got[0] != "1" || got[1] != "2" {
		t.Errorf("机器 B 顺序 = %v，应 FIFO [1 2]", got)
	}
	// 全绿：所有条目 done
	for _, it := range q.Snapshot() {
		if it.Status != "done" {
			t.Errorf("条目 %s 状态 = %s，want done", it.ID, it.Status)
		}
	}
}

// 失败条目要落到队列上（前端能看到原因），且不阻塞该机器后续条目。
func TestBroadcastQueueFailureRecordedAndContinues(t *testing.T) {
	q := newBroadcastQueue(nil)
	ran := 0
	q.exec = func(it *QueueItem) error {
		ran++
		if it.TweetID == "bad" {
			return errors.New("device 400: nothing to speak")
		}
		return nil
	}
	q.enqueue(&QueueItem{MachineID: "A", TweetID: "bad"})
	q.enqueue(&QueueItem{MachineID: "A", TweetID: "good"})

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := q.Snapshot()
		done := true
		for i := range snap {
			if snap[i].unfinished() {
				done = false
			}
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("队列没跑完")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ran != 2 {
		t.Fatalf("执行了 %d 条，want 2（失败不应中断该机器后续）", ran)
	}
	snap := q.Snapshot()
	if snap[0].Status != "failed" || snap[0].Error == "" {
		t.Errorf("失败条目应记 failed + 原因，got %s / %q", snap[0].Status, snap[0].Error)
	}
	if snap[1].Status != "done" {
		t.Errorf("失败后的条目仍应执行，got %s", snap[1].Status)
	}
}

// 清空待播：只丢 pending，不丢正在播的；可按机器清。
func TestBroadcastQueueClearPending(t *testing.T) {
	q := newBroadcastQueue(nil)
	block := make(chan struct{})
	q.exec = func(it *QueueItem) error {
		if it.TweetID == "run-a" || it.TweetID == "run-b" {
			<-block // 卡住两条「正在播」，制造 running 状态
		}
		return nil
	}
	q.enqueue(&QueueItem{MachineID: "A", TweetID: "run-a"})
	q.enqueue(&QueueItem{MachineID: "A", TweetID: "a1"})
	q.enqueue(&QueueItem{MachineID: "A", TweetID: "a2"})
	q.enqueue(&QueueItem{MachineID: "B", TweetID: "run-b"})
	q.enqueue(&QueueItem{MachineID: "B", TweetID: "b1"})

	// 等 A、B 各有一条进入 running（两条 worker 都卡住）
	deadline := time.Now().Add(time.Second)
	for {
		snap := q.Snapshot()
		running := 0
		for i := range snap {
			if snap[i].Status == "running" {
				running++
			}
		}
		if running == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if n := q.ClearPending("A"); n != 2 {
		t.Errorf("清 A 的待播 = %d 条，want 2（running 的那条不能被丢）", n)
	}
	statuses := map[string]string{}
	for _, it := range q.Snapshot() {
		statuses[it.MachineID+"/"+it.TweetID] = it.Status
	}
	if statuses["A/a1"] != "" || statuses["A/a2"] != "" {
		t.Errorf("A 的待播应被清掉，got a1=%q a2=%q", statuses["A/a1"], statuses["A/a2"])
	}
	if statuses["A/run-a"] != "running" {
		t.Errorf("正在播的条目不能被清掉，got %q", statuses["A/run-a"])
	}
	if statuses["B/b1"] != "pending" {
		t.Errorf("按机器清不应影响 B 的待播，got %q", statuses["B/b1"])
	}
	close(block)
}

// 单条删除：只有待播能删，正在播的删不掉。
func TestBroadcastQueueRemoveItem(t *testing.T) {
	q := newBroadcastQueue(nil)
	block := make(chan struct{})
	q.exec = func(it *QueueItem) error {
		if it.TweetID == "run" {
			<-block
		}
		return nil
	}
	run := q.enqueue(&QueueItem{MachineID: "A", TweetID: "run"})
	q.enqueue(&QueueItem{MachineID: "A", TweetID: "1"})
	second := q.enqueue(&QueueItem{MachineID: "A", TweetID: "2"})

	deadline := time.Now().Add(time.Second)
	for {
		snap := q.Snapshot()
		if len(snap) > 0 && snap[0].Status == "running" || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if q.RemoveItem(run.ID) {
		t.Error("正在播的条目不该被删掉")
	}
	if !q.RemoveItem(second.ID) {
		t.Error("待播条目应能删掉")
	}
	if q.RemoveItem(second.ID) {
		t.Error("重复删除应返回 false")
	}
	got := map[string]bool{}
	for _, it := range q.Snapshot() {
		got[it.TweetID] = true
	}
	if got["2"] {
		t.Error("被删的条目还在队列里")
	}
	if !got["1"] || !got["run"] {
		t.Errorf("其它条目不该受影响，got %v", got)
	}
	close(block)
}

// 按角色清空待播（Stream 面板「清空本角色待播」）：只丢该角色的 pending，别人的和正在播的不动。
func TestClearPendingByRole(t *testing.T) {
	block := make(chan struct{})
	q := newBroadcastQueue(nil)
	q.exec = func(*QueueItem) error {
		<-block
		return nil
	}
	defer close(block)

	q.enqueue(&QueueItem{MachineID: "A", RoleID: "r1", TweetID: "run-r1"})
	q.enqueue(&QueueItem{MachineID: "A", RoleID: "r1", TweetID: "r1-1"})
	q.enqueue(&QueueItem{MachineID: "A", RoleID: "r2", TweetID: "r2-1"})

	deadline := time.Now().Add(time.Second)
	for {
		snap := q.Snapshot()
		running := 0
		for i := range snap {
			if snap[i].Status == "running" {
				running++
			}
		}
		if running >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if n := q.ClearPendingRole("r1"); n != 1 {
		t.Errorf("清 r1 的待播 = %d 条，want 1（running 的那条不能被丢）", n)
	}
	statuses := map[string]string{}
	for _, it := range q.Snapshot() {
		statuses[it.TweetID] = it.Status
	}
	if statuses["r1-1"] != "" {
		t.Errorf("r1 的待播应被清掉，got %q", statuses["r1-1"])
	}
	if statuses["run-r1"] != "running" {
		t.Errorf("正在播的不能被清掉，got %q", statuses["run-r1"])
	}
	if statuses["r2-1"] != "pending" {
		t.Errorf("别的角色的待播不该受影响，got %q", statuses["r2-1"])
	}
}
