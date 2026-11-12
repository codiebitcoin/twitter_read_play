package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"twitter_read_play/internal/browser"
	"twitter_read_play/internal/model"
	"twitter_read_play/internal/role"
	"twitter_read_play/internal/store"
)

// 选用户链路（POST /api/source/pick）全链路测试：假 driver 模拟内置窗口，
// Eval 按 code 内容应答（轮询 __trpPick / 回写 __trpPickResult），不用真 WebView。

// fakePickWindow 假窗口：pollJS 命中时逐个吐出 canned 结果，resultJS 命中时记录文案。
type fakePickWindow struct {
	mu        sync.Mutex
	polls     []any    // 每次「读取 __trpPick」的应答（依次弹出，耗尽后回 nil）
	results   []string // __trpPickResult 回写的文案
	closedFns []func()
	destroyed bool
}

func (w *fakePickWindow) LoadURL(string) error { return nil }
func (w *fakePickWindow) Eval(_ context.Context, code string) (any, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.Contains(code, "__trpPick ?") {
		if len(w.polls) == 0 {
			return nil, nil
		}
		v := w.polls[0]
		w.polls = w.polls[1:]
		return v, nil
	}
	if strings.Contains(code, "__trpPickResult") {
		// pickResultJS(ok, "msg")：取最后一个字符串字面量
		if i := strings.LastIndex(code, `"`); i > 0 {
			if j := strings.LastIndex(code[:i], `"`); j >= 0 {
				w.results = append(w.results, code[j+1:i])
			}
		}
	}
	return nil, nil
}
func (w *fakePickWindow) Show()             {}
func (w *fakePickWindow) Hide()             {}
func (w *fakePickWindow) Focus()            {}
func (w *fakePickWindow) Destroy()          { w.mu.Lock(); w.destroyed = true; w.mu.Unlock() }
func (w *fakePickWindow) IsDestroyed() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.destroyed }
func (w *fakePickWindow) OnClosed(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closedFns = append(w.closedFns, fn)
}

// fakePickDriver 假 driver：记住 Open 出来的窗口，供测试注入 Eval 应答。
type fakePickDriver struct {
	mu   sync.Mutex
	last *fakePickWindow
}

func (d *fakePickDriver) Available() bool { return true }
func (d *fakePickDriver) Open(browser.OpenOptions) (browser.Window, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w := &fakePickWindow{}
	d.last = w
	return w, nil
}

func newPickTestServer(t *testing.T) (*Server, *fakePickDriver) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	rm, err := role.NewManager(st)
	if err != nil {
		t.Fatalf("role.NewManager: %v", err)
	}
	drv := &fakePickDriver{}
	bm := browser.NewManager(drv, st)
	s := &Server{Store: st, Roles: rm, Browser: bm, Mode: "desktop"}
	if err := rm.Upsert(&model.Role{ID: "r1", Name: "测试角色", Platform: "x"}); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	return s, drv
}

func pickPost(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.pickSource(rec, httptest.NewRequest(http.MethodPost, "/api/source/pick", strings.NewReader(body)))
	return rec
}

func waitPickSettled(t *testing.T, d *fakePickDriver, check func() bool, desc string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", desc)
}

// 点击用户主页 → 解析出 @elonmusk 写进角色（feed_target/platform/username），窗口自动关闭。
func TestPickUserWritesRoleAndCloses(t *testing.T) {
	s, drv := newPickTestServer(t)
	rec := pickPost(t, s, `{"role_id":"r1","lang":"en"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("pick status = %d, body = %s", rec.Code, rec.Body.String())
	}
	drv.mu.Lock()
	w := drv.last
	w.polls = []any{`{"href":"https://x.com/elonmusk","at":111}`}
	drv.mu.Unlock()

	waitPickSettled(t, drv, func() bool {
		rl, err := s.Roles.Get("r1")
		if err != nil || rl.FeedTarget != "https://x.com/elonmusk" {
			return false
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		// feed_target 写库后还要等 closePickAfter 的关窗延迟
		return w.destroyed
	}, "角色应被写入且窗口自动关闭")
	w.mu.Lock()
	msg := strings.Join(w.results, "|")
	w.mu.Unlock()
	if !strings.Contains(msg, "elonmusk") {
		t.Fatalf("回显文案应含用户名，实际 %q", msg)
	}
	rl, _ := s.Roles.Get("r1")
	if rl.Username != "elonmusk" || rl.Platform != "x" {
		t.Fatalf("username/platform = %q/%q, 期望 elonmusk/x", rl.Username, rl.Platform)
	}
}

// 点击非用户主页（x.com/home 在保留路径里）→ 不写库、不关窗，回显错误文案。
func TestPickRejectsNonProfilePage(t *testing.T) {
	s, drv := newPickTestServer(t)
	if rec := pickPost(t, s, `{"role_id":"r1","lang":"zh"}`); rec.Code != http.StatusOK {
		t.Fatalf("pick status = %d", rec.Code)
	}
	drv.mu.Lock()
	drv.last.polls = []any{`{"href":"https://x.com/home","at":222}`}
	drv.mu.Unlock()

	waitPickSettled(t, drv, func() bool {
		drv.mu.Lock()
		defer drv.mu.Unlock()
		return len(drv.last.results) > 0
	}, "应回显失败文案")
	drv.mu.Lock()
	destroyed, msg := drv.last.destroyed, drv.last.results[0]
	drv.mu.Unlock()
	if destroyed {
		t.Fatal("解析失败不应关窗")
	}
	if !strings.Contains(msg, "用户主页") {
		t.Fatalf("失败文案应说明不是用户主页，实际 %q", msg)
	}
	if rl, _ := s.Roles.Get("r1"); rl.FeedTarget != "" {
		t.Fatalf("不应写库，feed_target = %q", rl.FeedTarget)
	}
}

// 未知角色 → 404；角色不存在时窗口/监听不应启动。
func TestPickUnknownRole404(t *testing.T) {
	s, _ := newPickTestServer(t)
	rec := pickPost(t, s, `{"role_id":"nope"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// 已配目标的角色再开选用户窗：起点应是已配用户的主页（响应 url 字段）。
func TestPickStartURLOpensExistingTarget(t *testing.T) {
	s, _ := newPickTestServer(t)
	rm := s.Roles
	rl, _ := rm.Get("r1")
	rl.FeedTarget = "https://x.com/naval"
	if _, err := s.parseRoleTargets(rl); err != nil {
		t.Fatalf("parseRoleTargets: %v", err)
	}
	if err := rm.Upsert(rl); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rec := pickPost(t, s, `{"role_id":"r1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("pick status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		URL string `json:"url"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("非 JSON: %v", err)
	}
	if out.Key != "pick-r1" {
		t.Fatalf("key = %q, 期望 pick-r1", out.Key)
	}
	if out.URL != "https://x.com/naval" {
		t.Fatalf("起点应为已配用户主页，实际 %q", out.URL)
	}
}
