package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Fetch 在 provider 页面 origin 内执行 fetch(credentials:'include')。
func (m *Manager) Fetch(ctx context.Context, p Provider, req Request) (Response, error) {
	if !m.Available() {
		return Response{}, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return Response{}, fmt.Errorf("未知平台 %q", p)
	}
	target, err := m.resolveURL(cfg, req.URL)
	if err != nil {
		return Response{}, err
	}
	host := strings.ToLower(target.Hostname())
	if !cfg.hostAllowed(host) {
		return Response{}, fmt.Errorf("host %q 不在 %s 白名单内", host, p)
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = "GET"
	}
	if method != "GET" && method != "POST" {
		return Response{}, fmt.Errorf("只允许 GET/POST")
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()

	headers := req.Headers
	if headers == nil {
		headers = map[string]string{}
	}
	hJSON, _ := json.Marshal(headers)
	uJSON, _ := json.Marshal(target.String())
	mJSON, _ := json.Marshal(method)
	bodyJS := "undefined"
	if len(req.Body) > 0 {
		b, _ := json.Marshal(string(req.Body))
		bodyJS = string(b)
	}
	code := fmt.Sprintf(`(async () => {
  const r = await fetch(%s, { method: %s, headers: %s, body: %s, credentials: 'include' });
  return { status: r.status, body: await r.text() };
})()`, uJSON, mJSON, hJSON, bodyJS)
	raw, err := m.evalMapWork(ctx, p, code)
	if err != nil {
		return Response{}, err
	}
	return Response{
		Status: int(floatValue(raw["status"])),
		Body:   []byte(stringValue(raw["body"])),
	}, nil
}

// Capture 导航目标页、滚动、取回命中的 XHR/GraphQL 响应。
func (m *Manager) Capture(ctx context.Context, p Provider, req CaptureRequest) ([]Captured, error) {
	if !m.Available() {
		return nil, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return nil, fmt.Errorf("未知平台 %q", p)
	}
	var re *regexp.Regexp
	if req.URLRegex != "" {
		var err error
		re, err = regexp.Compile(req.URLRegex)
		if err != nil {
			return nil, fmt.Errorf("URLRegex 非法: %w", err)
		}
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()

	w, err := m.ensureWork(ctx, p)
	if err != nil {
		return nil, err
	}
	if nav := strings.TrimSpace(req.Navigate); nav != "" {
		u, uerr := m.resolveURL(cfg, nav)
		if uerr != nil {
			return nil, uerr
		}
		if !cfg.hostAllowed(strings.ToLower(u.Hostname())) {
			return nil, fmt.Errorf("导航 host 不在 %s 白名单内", p)
		}
		err := w.LoadURL(u.String())
		if err != nil && w.IsDestroyed() {
			// 缓存的窗口刚被发现是死的（远程 tab 丢失）：重开再导航一次。
			if w, err = m.ensureWork(ctx, p); err != nil {
				return nil, err
			}
			err = w.LoadURL(u.String())
		}
		if err != nil {
			return nil, err
		}
		if err := waitReady(ctx, w); err != nil {
			return nil, err
		}
	}
	_, _ = w.Eval(ctx, "window.__trpCapture = []")

	wait := req.Wait
	if wait <= 0 {
		wait = 1200 * time.Millisecond
	}
	scrolls := req.Scroll
	if scrolls <= 0 {
		scrolls = 1
	}
	for i := 0; i < scrolls; i++ {
		_, _ = w.Eval(ctx, "window.scrollBy(0, 4000)")
		if !sleepCtx(ctx, wait) {
			return nil, ctx.Err()
		}
	}

	v, err := m.evalWork(ctx, p, "JSON.stringify(window.__trpCapture || [])")
	if err != nil {
		return nil, err
	}
	return parseCaptured(v, re)
}

// Snapshot 读取当前捕获缓冲（不清空、不导航）。YouTube 直播聊天轮询用。
func (m *Manager) Snapshot(ctx context.Context, p Provider, urlRegex string) ([]Captured, error) {
	if !m.Available() {
		return nil, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	if _, ok := configFor(p); !ok {
		return nil, fmt.Errorf("未知平台 %q", p)
	}
	var re *regexp.Regexp
	if urlRegex != "" {
		var err error
		re, err = regexp.Compile(urlRegex)
		if err != nil {
			return nil, fmt.Errorf("URLRegex 非法: %w", err)
		}
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()
	v, err := m.evalWork(ctx, p, "JSON.stringify(window.__trpCapture || [])")
	if err != nil {
		return nil, err
	}
	return parseCaptured(v, re)
}

func parseCaptured(v any, re *regexp.Regexp) ([]Captured, error) {
	var rows []struct {
		URL    string  `json:"url"`
		Status int     `json:"status"`
		Body   string  `json:"body"`
		At     float64 `json:"at"`
	}
	switch x := v.(type) {
	case string:
		if err := json.Unmarshal([]byte(x), &rows); err != nil {
			return nil, err
		}
	case []any:
		b, _ := json.Marshal(x)
		if err := json.Unmarshal(b, &rows); err != nil {
			return nil, err
		}
	}
	out := make([]Captured, 0, len(rows))
	for _, r := range rows {
		if re != nil && !re.MatchString(r.URL) {
			continue
		}
		c := Captured{URL: r.URL, Status: r.Status, Text: r.Body}
		if r.At > 0 {
			c.At = time.UnixMilli(int64(r.At))
		}
		if json.Valid([]byte(r.Body)) {
			c.JSON = json.RawMessage(r.Body)
		}
		out = append(out, c)
	}
	return out, nil
}

func (m *Manager) resolveURL(cfg providerConfig, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("空 URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if !u.IsAbs() {
		base, err := url.Parse(cfg.HomeURL)
		if err != nil {
			return nil, err
		}
		u = base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("只允许 http/https")
	}
	return u, nil
}
