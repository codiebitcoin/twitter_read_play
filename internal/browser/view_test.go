package browser

import (
	"context"
	"testing"
)

type fakeDriver struct {
	available bool
	windows   []*fakeWindow
	// evalFor 可选：给每个窗口装一个 Eval 实现（工作窗自愈等测试用）。
	evalFor func(w *fakeWindow, code string) (any, error)
}

func (d *fakeDriver) Available() bool { return d.available }
func (d *fakeDriver) Open(opts OpenOptions) (Window, error) {
	w := &fakeWindow{url: opts.URL, stateKey: opts.StateKey}
	if d.evalFor != nil {
		w.eval = func(code string) (any, error) { return d.evalFor(w, code) }
	}
	d.windows = append(d.windows, w)
	return w, nil
}

type fakeWindow struct {
	url       string
	stateKey  string
	destroyed bool
	shown     int
	focused   int
	loads     []string
	closed    []func()
	eval      func(code string) (any, error)
	lost      bool
}

func (w *fakeWindow) LoadURL(u string) error {
	w.url = u
	w.loads = append(w.loads, u)
	return nil
}
func (w *fakeWindow) Eval(ctx context.Context, code string) (any, error) {
	if w.eval != nil {
		return w.eval(code)
	}
	return nil, nil
}
func (w *fakeWindow) Show()              { w.shown++ }
func (w *fakeWindow) Hide()              {}
func (w *fakeWindow) Focus()             { w.focused++ }
func (w *fakeWindow) Destroy()           { w.destroyed = true }
func (w *fakeWindow) IsDestroyed() bool  { return w.destroyed }
func (w *fakeWindow) OnClosed(fn func()) { w.closed = append(w.closed, fn) }

func (w *fakeWindow) close() {
	w.destroyed = true
	for _, fn := range w.closed {
		fn()
	}
}

func TestOpenViewReusesWindow(t *testing.T) {
	d := &fakeDriver{available: true}
	m := NewManager(d, nil)

	first, err := m.OpenView(context.Background(), ViewOptions{
		Title: "查看 · X", URL: "https://x.com/elonmusk", Key: "x", Provider: ProviderX,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Reused || len(d.windows) != 1 {
		t.Fatalf("first open: %+v windows=%d", first, len(d.windows))
	}
	if d.windows[0].stateKey != "view-x" || d.windows[0].url != "https://x.com/elonmusk" {
		t.Fatalf("unexpected window: %+v", d.windows[0])
	}

	second, err := m.OpenView(context.Background(), ViewOptions{
		Title: "查看 · X", URL: "https://x.com/other", Key: "x", Provider: ProviderX,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Reused || len(d.windows) != 1 {
		t.Fatalf("second open should reuse: %+v windows=%d", second, len(d.windows))
	}
	if d.windows[0].url != "https://x.com/other" || d.windows[0].shown == 0 || d.windows[0].focused == 0 {
		t.Fatalf("window not navigated/focused: %+v", d.windows[0])
	}
}

func TestOpenViewRejectsBadURL(t *testing.T) {
	m := NewManager(&fakeDriver{available: true}, nil)
	if _, err := m.OpenView(context.Background(), ViewOptions{URL: "javascript:alert(1)"}); err == nil {
		t.Fatal("javascript URL should be rejected")
	}
	if _, err := m.OpenView(context.Background(), ViewOptions{URL: "https://x.com/a", Provider: "nope"}); err == nil {
		t.Fatal("unknown provider should be rejected")
	}
}

func TestOpenViewRecreatesAfterClose(t *testing.T) {
	d := &fakeDriver{available: true}
	m := NewManager(d, nil)
	if _, err := m.OpenView(context.Background(), ViewOptions{URL: "https://x.com/a", Key: "x"}); err != nil {
		t.Fatal(err)
	}
	d.windows[0].close()
	if _, err := m.OpenView(context.Background(), ViewOptions{URL: "https://x.com/b", Key: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(d.windows) != 2 {
		t.Fatalf("want new window after close, got %d", len(d.windows))
	}
}

func TestOpenViewGeneric(t *testing.T) {
	d := &fakeDriver{available: true}
	m := NewManager(d, nil)
	res, err := m.OpenView(context.Background(), ViewOptions{URL: "https://example.com/feed.xml"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Key != "generic" || d.windows[0].stateKey != "view-generic" {
		t.Fatalf("unexpected generic view: %+v %+v", res, d.windows[0])
	}
}
