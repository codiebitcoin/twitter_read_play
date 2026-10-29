package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"twitter_read_play/internal/machine"
	"twitter_read_play/internal/role"
	"twitter_read_play/internal/store"
	"twitter_read_play/internal/upstream"
	"twitter_read_play/internal/watch"
)

func newQueueTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	mm, err := machine.NewManager(st)
	if err != nil {
		t.Fatalf("machine.NewManager: %v", err)
	}
	rm, err := role.NewManager(st)
	if err != nil {
		t.Fatalf("role.NewManager: %v", err)
	}
	um, err := upstream.NewManager(st)
	if err != nil {
		t.Fatalf("upstream.NewManager: %v", err)
	}
	wm, err := watch.NewManager(st, nil, um, mm, rm)
	if err != nil {
		t.Fatalf("watch.NewManager: %v", err)
	}
	return &Server{Store: st, Machines: mm, Roles: rm, Upstreams: um, Watches: wm}
}

// 空队列必须返回 [] 而不是 null（前端 res.items 直接当数组用）。
func TestBroadcastQueueGetEmpty(t *testing.T) {
	s := newQueueTestServer(t)
	rec := httptest.NewRecorder()
	s.broadcastQueueGet(rec, httptest.NewRequest(http.MethodGet, "/api/broadcast/queue", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("非 JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Items == nil {
		t.Fatalf("items 是 null，前端会炸：%s", rec.Body.String())
	}
	if len(body.Items) != 0 {
		t.Fatalf("空队列 items 应为 0 条，got %d", len(body.Items))
	}
}

func TestBroadcastQueueClearEmpty(t *testing.T) {
	s := newQueueTestServer(t)
	rec := httptest.NewRecorder()
	s.broadcastQueueClear(rec, httptest.NewRequest(http.MethodDelete, "/api/broadcast/queue?machine_id=m1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		OK      bool `json:"ok"`
		Cleared int  `json:"cleared"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("非 JSON: %v", err)
	}
	if !body.OK || body.Cleared != 0 {
		t.Fatalf("body = %+v", body)
	}
}
