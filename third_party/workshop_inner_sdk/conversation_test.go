package clt01

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeHistory 构造 /data/user/chat/history 的响应体。
func fakeHistory(turns []map[string]any) map[string]any {
	return map[string]any{
		"success": true,
		"data":    map[string]any{"items": turns, "has_more": false},
	}
}

func turn(id, role, content string, at time.Time) map[string]any {
	return map[string]any{
		"chat_id": id, "role": role, "content": content,
		"created_at": at.UTC().Format(time.RFC3339), "slot_number": 1,
	}
}

func newConvTestClient(t *testing.T, mu *sync.Mutex, history *[]map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		// 签名头不校验（假设备），只回数据
		_ = json.NewEncoder(w).Encode(fakeHistory(*history))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	// 签名请求需要凭据；假设备不验签，随便给一组
	c.Apply(&Credentials{LocalSecret: "test-secret", LocalSessionID: "sess", UserID: "user"})
	return c
}

// 冷启动：首拉不回调；有新记录→active（1s 节奏）；窗口过后→inactive。
func TestConversationWatcherStates(t *testing.T) {
	mu := &sync.Mutex{}
	history := &[]map[string]any{
		turn("c1", "user", "你好", time.Now().Add(-2*time.Minute)),
	}
	c := newConvTestClient(t, mu, history)

	var muCalls sync.Mutex
	var turns []ConversationTurn
	var states []ConversationState
	now := time.Now()
	opt := ConversationWatchOptions{
		Slot:             1,
		ActiveInterval:   10 * time.Millisecond,
		InactiveInterval: 20 * time.Millisecond,
		ActiveWindow:     30 * time.Millisecond,
		OnTurn: func(_ context.Context, t2 ConversationTurn) {
			muCalls.Lock()
			turns = append(turns, t2)
			muCalls.Unlock()
		},
		OnStateChange: func(_ context.Context, s ConversationState) {
			muCalls.Lock()
			states = append(states, s)
			muCalls.Unlock()
		},
		Now: func() time.Time { return now },
	}
	w, err := c.WatchConversations(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()

	// 等首轮拉取完成（冷启动只入账）
	time.Sleep(60 * time.Millisecond)
	if got := w.State(); got != ConversationInactive {
		t.Fatalf("冷启动后应 inactive（最近记录在窗口外），got %s", got)
	}

	// 新记录进来 → active + OnTurn 回调
	mu.Lock()
	*history = append(*history, turn("c2", "user", "开灯", now.Add(-time.Millisecond)))
	mu.Unlock()
	deadline := now.Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if w.State() == ConversationActive {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := w.State(); got != ConversationActive {
		t.Fatalf("新记录后应 active，got %s", got)
	}

	muCalls.Lock()
	defer muCalls.Unlock()
	if len(turns) == 0 || turns[0].ChatID != "c2" || turns[0].Content != "开灯" {
		t.Fatalf("OnTurn 应回调 c2/开灯，got %+v", turns)
	}
	if len(states) == 0 || states[0] != ConversationActive {
		t.Fatalf("OnStateChange 应回调 active，got %+v", states)
	}

	// 记录留存
	recs := w.Records()
	if len(recs) != 2 {
		t.Fatalf("Records 应有 2 条，got %d", len(recs))
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run 未随 ctx 取消退出")
	}
}

// 正序：手册明确页内记录按消息编号正序排列。断言 Records 末尾是最新一条，
// 且 OnTurn 回调顺序是 old→new（倒序处理会两头都错）。
func TestConversationWatcherPreservesOrder(t *testing.T) {
	base := time.Now().Add(-10 * time.Minute)
	mu := &sync.Mutex{}
	history := &[]map[string]any{
		turn("c1", "user", "第一句", base),
		turn("c2", "assistant", "第二句", base.Add(time.Minute)),
		turn("c3", "user", "第三句", base.Add(2*time.Minute)),
	}
	c := newConvTestClient(t, mu, history)

	var muCalls sync.Mutex
	var got []string
	opt := ConversationWatchOptions{
		Slot:             1,
		ActiveInterval:   10 * time.Millisecond,
		InactiveInterval: 20 * time.Millisecond,
		ActiveWindow:     30 * time.Millisecond,
		OnTurn: func(_ context.Context, t2 ConversationTurn) {
			muCalls.Lock()
			got = append(got, t2.ChatID)
			muCalls.Unlock()
		},
	}
	w, err := c.WatchConversations(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()

	// 首轮只入账：Records 应按 old→new
	time.Sleep(60 * time.Millisecond)
	recs := w.Records()
	if len(recs) != 3 {
		t.Fatalf("Records 应有 3 条，got %d", len(recs))
	}
	if recs[0].ChatID != "c1" || recs[2].ChatID != "c3" {
		t.Fatalf("Records 应按 old→new（c1..c3），got %s..%s", recs[0].ChatID, recs[2].ChatID)
	}
	if last := recs[len(recs)-1]; last.Content != "第三句" {
		t.Fatalf("末尾应是最新一条「第三句」，got %q", last.Content)
	}

	// 新增一条 → 回调一次且是最新的
	mu.Lock()
	*history = append(*history, turn("c4", "user", "第四句", base.Add(3*time.Minute)))
	mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		muCalls.Lock()
		n := len(got)
		muCalls.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	muCalls.Lock()
	defer muCalls.Unlock()
	if len(got) != 1 || got[0] != "c4" {
		t.Fatalf("OnTurn 应只回调新增的 c4，got %+v", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run 未随 ctx 取消退出")
	}
}

// Stop 必须真正停止 Run 并让 Done 关闭；可重复调用。
func TestConversationWatcherStop(t *testing.T) {
	mu := &sync.Mutex{}
	history := &[]map[string]any{}
	c := newConvTestClient(t, mu, history)

	w, err := c.WatchConversations(context.Background(), ConversationWatchOptions{
		Slot: 1, ActiveInterval: time.Hour, InactiveInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.Start(context.Background())
	// 轮询间隔被拉成 1h，说明它确实在等 time.After —— Stop 必须能打断。
	time.Sleep(30 * time.Millisecond)
	w.Stop()
	select {
	case <-w.Done():
	case <-time.After(6 * time.Second):
		t.Fatal("Stop 后 Run 未退出")
	}
	w.Stop() // 重复调用不应 panic / 阻塞
}

func TestConversationWatcherRequiresSlot(t *testing.T) {
	c, err := New("10.0.0.1:8081")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.WatchConversations(context.Background(), ConversationWatchOptions{}); err == nil {
		t.Fatal("缺 Slot 应报错")
	}
}
