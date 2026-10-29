package browser

import (
	"context"
	"strings"
	"testing"
	"time"
)

type loginCloseDriver struct {
	available bool
	login     *loginCloseWindow
	work      *loginCloseWindow
	view      *loginCloseWindow
}

func (d *loginCloseDriver) Available() bool { return d.available }
func (d *loginCloseDriver) Open(opts OpenOptions) (Window, error) {
	w := &loginCloseWindow{url: opts.URL}
	switch {
	case opts.Hidden:
		d.work = w
	case strings.Contains(opts.URL, "/i/flow/login"):
		d.login = w
	default:
		d.view = w
	}
	return w, nil
}

type loginCloseWindow struct {
	url       string
	loads     []string
	evals     []string
	destroyed bool
	shown     int
	focused   int
	closed    []func()
}

func (w *loginCloseWindow) LoadURL(u string) error {
	w.url = u
	w.loads = append(w.loads, u)
	return nil
}
func (w *loginCloseWindow) Eval(ctx context.Context, code string) (any, error) {
	w.evals = append(w.evals, code)
	if strings.Contains(code, "document.readyState") {
		return "complete", nil
	}
	return map[string]any{"connected": true, "account": "@test"}, nil
}
func (w *loginCloseWindow) Show()              { w.shown++ }
func (w *loginCloseWindow) Hide()              {}
func (w *loginCloseWindow) Focus()             { w.focused++ }
func (w *loginCloseWindow) Destroy()           { w.destroyed = true }
func (w *loginCloseWindow) IsDestroyed() bool  { return w.destroyed }
func (w *loginCloseWindow) OnClosed(fn func()) { w.closed = append(w.closed, fn) }
func (w *loginCloseWindow) closeByUser() {
	w.destroyed = true
	for _, fn := range w.closed {
		fn()
	}
}

func TestOpenLoginCloseTriggersProbe(t *testing.T) {
	d := &loginCloseDriver{available: true}
	m := NewManager(d, nil)
	if err := m.OpenLogin(context.Background(), ProviderX); err != nil {
		t.Fatal(err)
	}
	if d.login == nil {
		t.Fatal("login window not opened")
	}
	d.login.closeByUser()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st := m.Status(ProviderX)
		if st.Connected && st.Account == "@test" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("close did not trigger a successful probe: %+v", m.Status(ProviderX))
}

func TestLoginCloseReloadsOpenView(t *testing.T) {
	d := &loginCloseDriver{available: true}
	m := NewManager(d, nil)
	if _, err := m.OpenView(context.Background(), ViewOptions{
		URL: "https://x.com/elonmusk", Key: "x", Provider: ProviderX,
	}); err != nil {
		t.Fatal(err)
	}
	if d.view == nil {
		t.Fatal("view window not opened")
	}
	if err := m.OpenLogin(context.Background(), ProviderX); err != nil {
		t.Fatal(err)
	}
	d.login.closeByUser()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m.Status(ProviderX).Connected && len(d.view.loads) > 0 {
			if got := d.view.loads[len(d.view.loads)-1]; got != "https://x.com/elonmusk" {
				t.Fatalf("view reloaded with %q", got)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("login close did not reload the open view (connected=%v loads=%v)",
		m.Status(ProviderX).Connected, d.view.loads)
}
