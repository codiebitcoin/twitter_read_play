package browser

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// ViewOptions 打开一个「查看账号」窗口。
type ViewOptions struct {
	Title         string
	URL           string
	Key           string // 按平台复用：x / weibo / xiaohongshu / ... / generic
	Provider      Provider
	Width, Height int
	// PreloadScript 需要注入脚本的窗口传（目前只有「选用户」窗）；
	// 普通「查看账号」窗保持不注入，与工作窗完全隔离。
	PreloadScript string
}

// ViewResult 查看窗口打开结果。
type ViewResult struct {
	Key    string `json:"key"`
	URL    string `json:"url"`
	Reused bool   `json:"reused"`
}

type viewEntry struct {
	win      Window
	url      string
	provider Provider
}

// OpenView 打开/复用一个可见的内置窗口。
// 与工作窗（Hidden=true，供 capture 使用）完全隔离：不共享串行队列，也不注入 preload。
func (m *Manager) OpenView(ctx context.Context, opts ViewOptions) (ViewResult, error) {
	if !m.Available() {
		return ViewResult{}, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	if err := ctx.Err(); err != nil {
		return ViewResult{}, err
	}
	raw := strings.TrimSpace(opts.URL)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ViewResult{}, fmt.Errorf("只允许 http/https URL")
	}
	if opts.Provider != "" && !ValidProvider(opts.Provider) {
		return ViewResult{}, fmt.Errorf("未知平台 %q", opts.Provider)
	}

	key := strings.TrimSpace(opts.Key)
	if key == "" {
		key = string(opts.Provider)
	}
	if key == "" {
		key = "generic"
	}
	width, height := opts.Width, opts.Height
	if width <= 0 {
		width = 1100
	}
	if height <= 0 {
		height = 820
	}
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = "查看 · " + key
	}
	target := u.String()

	m.mu.Lock()
	if e, ok := m.views[key]; ok {
		if e.win == nil || e.win.IsDestroyed() {
			delete(m.views, key)
		} else {
			changed := e.url != target
			m.views[key] = viewEntry{win: e.win, url: target, provider: opts.Provider}
			m.mu.Unlock()
			if changed {
				if err := e.win.LoadURL(target); err != nil {
					return ViewResult{}, err
				}
			}
			e.win.Show()
			e.win.Focus()
			return ViewResult{Key: key, URL: target, Reused: true}, nil
		}
	}
	m.mu.Unlock()

	w, err := m.driver.Open(OpenOptions{
		Title: title, URL: target,
		PreloadScript: opts.PreloadScript,
		Width:         width, Height: height,
		Hidden: false, StateKey: "view-" + key,
	})
	if err != nil {
		return ViewResult{}, err
	}

	m.mu.Lock()
	m.views[key] = viewEntry{win: w, url: target, provider: opts.Provider}
	m.mu.Unlock()

	w.OnClosed(func() {
		m.mu.Lock()
		if cur, ok := m.views[key]; ok && cur.win == w {
			delete(m.views, key)
		}
		m.mu.Unlock()
	})
	return ViewResult{Key: key, URL: target, Reused: false}, nil
}

// reloadViewsForProvider 登录成功后刷新该平台已打开的查看窗口：
// 页面之前可能停在登录页/403，重新加载同一 URL 就能用新会话展示目标账号。
func (m *Manager) reloadViewsForProvider(p Provider) {
	m.mu.Lock()
	type item struct {
		win Window
		url string
	}
	var items []item
	for _, e := range m.views {
		if e.provider != p || e.win == nil || e.win.IsDestroyed() {
			continue
		}
		items = append(items, item{win: e.win, url: e.url})
	}
	m.mu.Unlock()
	for _, it := range items {
		it.win.Show()
		it.win.Focus()
		_ = it.win.LoadURL(it.url)
	}
}

// CloseView 关闭某个查看窗口（key 为空时不动）。
func (m *Manager) CloseView(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	m.mu.Lock()
	e, ok := m.views[key]
	if ok {
		delete(m.views, key)
	}
	m.mu.Unlock()
	if ok && e.win != nil && !e.win.IsDestroyed() {
		e.win.Destroy()
	}
}
