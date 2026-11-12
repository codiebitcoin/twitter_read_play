package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"twitter_read_play/internal/dbg"
)

func (m *Manager) ensureWork(ctx context.Context, p Provider) (Window, error) {
	m.mu.Lock()
	if w := m.work[p]; w != nil && !w.IsDestroyed() {
		m.mu.Unlock()
		return w, nil
	}
	m.mu.Unlock()

	cfg, ok := configFor(p)
	if !ok {
		return nil, fmt.Errorf("未知平台 %q", p)
	}
	dbg.Logf("work: 打开 %s 工作窗 url=%s hidden=true", p, cfg.HomeURL)
	w, err := m.driver.Open(OpenOptions{
		Title: "TRP · " + cfg.Name, URL: cfg.HomeURL, Hidden: true,
		PreloadScript: capturePreload(p), UserAgent: providerUA(cfg),
		Width: 1024, Height: 768,
	})
	if err != nil {
		return nil, err
	}
	if err := waitReady(ctx, w); err != nil {
		dbg.Logf("work: %s 工作窗 readyState 等待失败 err=%v", p, err)
		w.Destroy()
		return nil, fmt.Errorf("%s 页面加载失败: %w", cfg.Name, err)
	}
	// 刚 Open 的窗口里 about:blank 已经是 complete，waitReady 会立刻返回，
	// 后续 Eval 就打到空白页上（cookie 全空 → 误判未登录）。这里再等一拍，
	// 确认真的导航到了 http(s) 页面。
	if !waitNavigated(ctx, w) {
		if v, e := w.Eval(ctx, "location.href"); e == nil {
			dbg.Logf("work: %s 工作窗导航超时，停在 %v（多为代理/网络加载失败）", p, v)
		} else {
			dbg.Logf("work: %s 工作窗导航超时", p)
		}
		w.Destroy()
		return nil, fmt.Errorf("%s 页面加载超时", cfg.Name)
	}
	m.mu.Lock()
	m.work[p] = w
	m.mu.Unlock()
	return w, nil
}

// evalWork 在平台工作窗里执行一段 JS。
// 远程模式下 tab 可能被浏览器侧清掉而缓存窗口还"活着"；那次失败会把窗口标记
// 为已销毁（remoteWindow.markGone），这里随即重开工作窗重试一次，让轮询方在
// 同一个周期内自愈，不用等下一轮。
func (m *Manager) evalWork(ctx context.Context, p Provider, code string) (any, error) {
	w, err := m.ensureWork(ctx, p)
	if err != nil {
		return nil, err
	}
	v, err := w.Eval(ctx, code)
	if err == nil || !w.IsDestroyed() {
		return v, err
	}
	dbg.Logf("eval: %s 窗口已失效（重开重试）err=%v", p, err)
	w, err = m.ensureWork(ctx, p)
	if err != nil {
		return nil, err
	}
	return w.Eval(ctx, code)
}

// evalMapWork 同 evalWork，但把结果转成对象（探针/取 token 用）。
func (m *Manager) evalMapWork(ctx context.Context, p Provider, code string) (map[string]any, error) {
	v, err := m.evalWork(ctx, p, code)
	if err != nil {
		return nil, err
	}
	return toMap(v)
}

// Eval 在 provider 工作窗口执行 JS（串行）。
func (m *Manager) Eval(ctx context.Context, p Provider, code string) (any, error) {
	if !m.Available() {
		return nil, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	if _, ok := configFor(p); !ok {
		return nil, fmt.Errorf("未知平台 %q", p)
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()
	return m.evalWork(ctx, p, code)
}

// Text 当前工作页面的 body 文本（微博 JSON 视图兜底用）。
func (m *Manager) Text(ctx context.Context, p Provider) (string, error) {
	v, err := m.Eval(ctx, p, "document.body ? document.body.innerText : ''")
	if err != nil {
		return "", err
	}
	s, _ := v.(string)
	return s, nil
}

func evalMap(ctx context.Context, w Window, code string) (map[string]any, error) {
	v, err := w.Eval(ctx, code)
	if err != nil {
		return nil, err
	}
	return toMap(v)
}

func toMap(v any) (map[string]any, error) {
	switch x := v.(type) {
	case map[string]any:
		return x, nil
	case string:
		var m map[string]any
		if err := json.Unmarshal([]byte(x), &m); err != nil {
			return nil, fmt.Errorf("脚本返回非 JSON: %s", x)
		}
		return m, nil
	case nil:
		return map[string]any{}, nil
	default:
		b, _ := json.Marshal(x)
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		return m, nil
	}
}

func waitReady(ctx context.Context, w Window) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		v, err := w.Eval(ctx, "document.readyState")
		if err == nil {
			if s, ok := v.(string); ok && (s == "interactive" || s == "complete") {
				sleepCtx(ctx, 300*time.Millisecond)
				return nil
			}
		}
		if !sleepCtx(ctx, 300*time.Millisecond) {
			return ctx.Err()
		}
	}
	return fmt.Errorf("页面加载超时")
}

// waitNavigated 等到页面真正落在 http(s) 地址上。
// about:blank 的 readyState 也是 complete，只看 readyState 会把空白页当成加载完成。
// 拿不到 href（非字符串）时无法判断，按已导航处理，避免卡住。
func waitNavigated(ctx context.Context, w Window) bool {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		v, err := w.Eval(ctx, "location.href")
		if err == nil {
			s, ok := v.(string)
			if !ok {
				return true
			}
			if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
				return true
			}
		}
		if !sleepCtx(ctx, 300*time.Millisecond) {
			return false
		}
	}
	return false
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(x, "true") || x == "1"
	case float64:
		return x != 0
	}
	return false
}

func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func floatValue(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}
