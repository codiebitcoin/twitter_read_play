package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// 选用户窗（角色设置 → 打开浏览器选用户）：
// 窗口由 OpenView 以 key=pick-<roleID> 打开并注入 PickPreload，
// 这里只负责轮询页面上的点击并回调，解析/写库在 api 层完成。

// pickPollJS 读取 PickPreload 在点击时写入的 window.__trpPick。
const pickPollJS = `(window.__trpPick ? JSON.stringify(window.__trpPick) : null)`

// pickResultJS 把解析结果回显到按钮上（ok 决定 ✓/✗ 前缀）。
func pickResultJS(ok bool, msg string) string {
	return fmt.Sprintf(`window.__trpPickResult && window.__trpPickResult(%v, %s)`, ok, strconv.Quote(msg))
}

// WatchPick 监听 key 对应查看窗上的「选定当前用户」点击。
// 每秒读一次 window.__trpPick；有新点击就回调 onPick(href)，返回 (ok, 文案)
// 回显到页面上；ok=true 且 closeAfter>0 时展示文案后只销毁这一个窗口。
// 同一 key 已在监听时直接返回：窗口复用，旧监听继续生效。
func (m *Manager) WatchPick(key string, onPick func(href string) (bool, string), closeAfter time.Duration) {
	m.mu.Lock()
	if _, running := m.pickWatch[key]; running {
		m.mu.Unlock()
		return
	}
	e, ok := m.views[key]
	if !ok || e.win == nil || e.win.IsDestroyed() {
		m.mu.Unlock()
		return
	}
	stop := make(chan struct{})
	m.pickWatch[key] = stop
	w := e.win
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.pickWatch, key)
			m.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		last := int64(0)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-tick.C:
			}
			if w.IsDestroyed() {
				return
			}
			raw, err := w.Eval(ctx, pickPollJS)
			if err != nil {
				continue // 导航/刷新瞬间 Eval 可能失败，下一秒再看
			}
			s, isStr := raw.(string)
			if !isStr || s == "" {
				continue
			}
			var pick struct {
				Href string `json:"href"`
				At   int64  `json:"at"`
			}
			if json.Unmarshal([]byte(s), &pick) != nil || pick.Href == "" || pick.At == last {
				continue
			}
			last = pick.At
			ok, msg := onPick(pick.Href)
			_, _ = w.Eval(ctx, pickResultJS(ok, msg))
			if !ok || closeAfter <= 0 {
				continue
			}
			// 选定成功：给用户一点时间看到 ✓ 文案，然后只关这一个窗。
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-time.After(closeAfter):
			}
			m.mu.Lock()
			if cur, still := m.views[key]; still && cur.win == w {
				delete(m.views, key)
			}
			m.mu.Unlock()
			if !w.IsDestroyed() {
				w.Destroy()
			}
			return
		}
	}()
}
