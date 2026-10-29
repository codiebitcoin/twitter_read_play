package browser

import (
	"context"
	"errors"
	"testing"
)

// 远程浏览器重启/清理后工作 tab 会凭空消失，而 Manager 缓存里的窗口对象还"活着"。
// 旧行为是每次轮询都复用它、稳定报「找不到 tab」，直到重启进程才恢复。
// 现在第一次失败即把窗口标记销毁并重开，同一轮里自愈。
func TestEvalReopensWorkWindowWhenTabDisappears(t *testing.T) {
	d := &fakeDriver{available: true}
	d.evalFor = func(w *fakeWindow, code string) (any, error) {
		if w.lost {
			// 模拟 remoteWindow.tabIndex 找不到 tab 时的 markGone。
			w.destroyed = true
			return nil, errors.New(`远程浏览器里找不到 tab "TRP · X"`)
		}
		switch code {
		case "document.readyState":
			return "complete", nil
		case "location.href":
			return "https://x.com/home", nil
		}
		return "ok", nil
	}
	m := NewManager(d, nil)
	ctx := context.Background()

	if _, err := m.Eval(ctx, ProviderX, "1+1"); err != nil {
		t.Fatalf("首次 Eval 失败: %v", err)
	}
	if len(d.windows) != 1 {
		t.Fatalf("应只开一个工作窗，实际 %d", len(d.windows))
	}

	d.windows[0].lost = true
	if _, err := m.Eval(ctx, ProviderX, "1+1"); err != nil {
		t.Fatalf("tab 丢失后应自动重开工作窗: %v", err)
	}
	if len(d.windows) != 2 {
		t.Fatalf("应重开一个新工作窗，实际 %d", len(d.windows))
	}
	if Window(d.windows[1]) != m.work[ProviderX] {
		t.Fatal("缓存应指向新窗口")
	}
}
