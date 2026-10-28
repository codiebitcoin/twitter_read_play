package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// OpenLogin 打开可见登录窗口（已有则显示并聚焦）。
func (m *Manager) OpenLogin(ctx context.Context, p Provider) error {
	if !m.Available() {
		return fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return fmt.Errorf("未知平台 %q", p)
	}
	m.mu.Lock()
	if w := m.login[p]; w != nil && !w.IsDestroyed() {
		m.mu.Unlock()
		w.Show()
		w.Focus()
		return nil
	}
	m.mu.Unlock()

	w, err := m.driver.Open(OpenOptions{
		Title: "登录 " + cfg.Name, URL: cfg.LoginURL,
		PreloadScript: loginPreload(cfg),
		UserAgent:     providerUA(cfg),
		Width:         520, Height: 760,
	})
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.login[p] = w
	m.mu.Unlock()
	// 用户关掉登录窗 ≈ 登录流程结束：立刻用工作窗探测一次并持久化状态。
	// 前端不需要等下一次手动「检测」，正在轮询的识别卡/登录状态条会马上看到结果。
	w.OnClosed(func() {
		m.mu.Lock()
		stillOurs := m.login[p] == w
		if stillOurs {
			delete(m.login, p)
		}
		m.mu.Unlock()
		// Logout/Close 会先清 map 再销毁窗口，那种情况不探测（避免把已登出状态又探回来）。
		if !stillOurs {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			if st, err := m.Probe(ctx, p); err == nil && st.Connected {
				// 登录成功：已经打开的「查看账号」窗口可能停在登录页，刷新成目标页。
				m.reloadViewsForProvider(p)
			}
		}()
	})
	return nil
}

// Probe 用工作窗口判断登录态。
func (m *Manager) Probe(ctx context.Context, p Provider) (Status, error) {
	if !m.Available() {
		return Status{Provider: p, Error: "desktop_only"}, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return Status{Provider: p}, fmt.Errorf("未知平台 %q", p)
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()

	w, err := m.ensureWork(ctx, p)
	prev := m.Status(p)
	if err != nil {
		prev.Error = err.Error()
		return m.setStatus(p, prev), err
	}
	// 小红书这类把登录态烧进 SSR 的站点：工作窗停在登录前的旧页面时探针永远是 false，
	// 探针前先回首页让服务端重新渲染。
	if cfg.ReloadProbe {
		if err := w.LoadURL(cfg.HomeURL); err == nil {
			_ = waitReady(ctx, w)
			// readyState 变 complete 后导航提交仍可能晚一拍，这时 Eval 会报
			// "Completion handler for function call is no longer reachable"。
			_ = sleepCtx(ctx, 400*time.Millisecond)
		}
	}
	raw, err := evalMap(ctx, w, cfg.ProbeJS)
	if err != nil {
		// 导航提交瞬间 Eval 会报 "Completion handler for function call is no longer
		// reachable" 之类的竞态错误；等一拍重试一次，仍失败就保留上一次状态。
		// 远程 tab 丢失时第一次失败已把窗口标记销毁，重试前先重开工作窗。
		if sleepCtx(ctx, 700*time.Millisecond) {
			if w2, werr := m.ensureWork(ctx, p); werr == nil {
				raw, err = evalMap(ctx, w2, cfg.ProbeJS)
			}
		}
	}
	return m.setStatus(p, applyProbeResult(prev, raw, err, time.Now())), nil
}

// applyProbeResult 把一次探针结果合并到旧状态上。
//
// checked 由探针脚本显式给出：true = 拿到了平台明确答复（登录/未登录都算结论）；
// false = 网络/页面未就绪等无结论情况，此时保留上次的 Connected/Account。
// 探针脚本返回 connected=true 但没有结论（例如 X 请求超时但 ct0 仍在）时，
// 只有在从未探测过的情况下才当作乐观的已登录。
func applyProbeResult(prev Status, raw map[string]any, probeErr error, now time.Time) Status {
	st := prev
	st.LastCheck = now
	if probeErr != nil {
		// 客户端断开（前端轮询被取消）不代表探测结论，别把
		// "context canceled" 写进状态里误导用户。
		if errors.Is(probeErr, context.Canceled) {
			return st
		}
		st.Error = probeErr.Error()
		return st
	}
	if truthy(raw["checked"]) {
		connected := truthy(raw["connected"])
		// 从「已连接」翻成「未连接」= 会话过期（cookie 还在但服务端已失效）。
		// 一旦确认过期就保留这个标记，直到下次登录成功或主动退出。
		st.Expired = !connected && (st.Connected || st.Expired)
		st.Known = true
		st.Connected = connected
		st.Account = stringValue(raw["account"])
	} else if !st.Known && truthy(raw["connected"]) {
		st.Known = true
		st.Connected = true
		st.Account = stringValue(raw["account"])
	}
	st.Error = stringValue(raw["error"])
	return st
}

// Tokens 提取可读 token（非 HttpOnly Cookie / localStorage / ytcfg）。
func (m *Manager) Tokens(ctx context.Context, p Provider) (TokenSet, error) {
	if !m.Available() {
		return TokenSet{Provider: p}, fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return TokenSet{Provider: p}, fmt.Errorf("未知平台 %q", p)
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()

	keys, _ := json.Marshal(cfg.StorageKeys)
	if len(cfg.StorageKeys) == 0 {
		// json.Marshal(nil slice) 是 "null"，到页面里 keys.forEach 会直接抛错。
		keys = []byte("[]")
	}
	code := fmt.Sprintf(`(() => {
  const ck = {};
  (document.cookie || '').split('; ').filter(Boolean).forEach((s) => {
    const i = s.indexOf('=');
    if (i > 0) ck[s.slice(0, i)] = decodeURIComponent(s.slice(i + 1));
  });
  const st = {};
  const keys = %s;
  keys.forEach((k) => { try { const v = localStorage.getItem(k); if (v) st[k] = v; } catch (e) {} });
  let yt = null;
  if (window.ytcfg && window.ytcfg.get) {
    yt = {
      apiKey: ytcfg.get('INNERTUBE_API_KEY') || '',
      visitorData: ytcfg.get('VISITOR_DATA') || '',
      clientVersion: ytcfg.get('INNERTUBE_CLIENT_VERSION') || '',
      loggedIn: !!ytcfg.get('LOGGED_IN'),
    };
  }
  return { cookies: ck, storage: st, ytcfg: yt };
})()`, string(keys))
	raw, err := m.evalMapWork(ctx, p, code)
	if err != nil {
		return TokenSet{Provider: p}, err
	}
	ts := TokenSet{Provider: p, Cookies: map[string]string{}, Storage: map[string]string{}, Headers: map[string]string{}}
	if cookies, ok := raw["cookies"].(map[string]any); ok {
		for k, v := range cookies {
			ts.Cookies[k] = stringValue(v)
		}
	}
	if storage, ok := raw["storage"].(map[string]any); ok {
		for k, v := range storage {
			ts.Storage[k] = stringValue(v)
		}
	}
	// 远程浏览器能导出含 HttpOnly 的完整 cookie（sessionid / twid 这些
	// document.cookie 读不到的）；拿不到就沿用页面里读到的部分。
	if remoteCookies, err := m.RemoteCookies(ctx, p); err == nil {
		for k, v := range remoteCookies {
			ts.Cookies[k] = v
		}
	}
	switch p {
	case ProviderX:
		if v := ts.Cookies["ct0"]; v != "" {
			ts.Headers["x-csrf-token"] = v
		}
	case ProviderTwitch:
		ts.Headers["Client-ID"] = "kimne78kx3ncx6brgo4mv6wki5h1ko"
		if v := ts.Storage["auth-token"]; v != "" {
			ts.Headers["Authorization"] = "OAuth " + v
		}
	case ProviderYouTube:
		if yt, ok := raw["ytcfg"].(map[string]any); ok {
			if v := stringValue(yt["visitorData"]); v != "" {
				ts.Headers["X-Goog-Visitor-Id"] = v
			}
			if v := stringValue(yt["clientVersion"]); v != "" {
				ts.Headers["X-Youtube-Client-Version"] = v
			}
			ts.Headers["X-Youtube-Client-Name"] = "1"
		}
	}
	if st := m.Status(p); st.Known {
		ts.Account = st.Account
	}
	m.mu.Lock()
	m.tokens[p] = ts
	m.mu.Unlock()
	return ts, nil
}

// Logout 退出登录：导航到平台 logout 页并销毁本地窗口。
func (m *Manager) Logout(ctx context.Context, p Provider) error {
	if !m.Available() {
		return fmt.Errorf("内置浏览器仅在桌面模式可用")
	}
	cfg, ok := configFor(p)
	if !ok {
		return fmt.Errorf("未知平台 %q", p)
	}
	lock := m.queue(p)
	lock.Lock()
	defer lock.Unlock()

	m.mu.Lock()
	login := m.login[p]
	work := m.work[p]
	delete(m.login, p)
	delete(m.work, p)
	delete(m.tokens, p)
	// 主动退出是一次明确结论：直接显示未登录，而不是回到「未检测」。
	m.status[p] = Status{Provider: p, Known: true, Connected: false, LastCheck: time.Now()}
	m.mu.Unlock()
	m.persistStatus()

	// 没有服务端登出地址的平台（Bluesky）会话在 localStorage 里：
	// 必须在销毁窗口前主动清掉，否则下次探测又会把旧会话读回来。
	clearWin := work
	if clearWin == nil || clearWin.IsDestroyed() {
		clearWin = login
	}
	tempWin := false
	if cfg.LogoutJS != "" && (clearWin == nil || clearWin.IsDestroyed()) {
		// 重启后只恢复了状态、本次运行还没开过窗口：临时开一个隐藏窗执行清理。
		if w, err := m.ensureWork(ctx, p); err == nil {
			clearWin, tempWin = w, true
		}
	}
	if cfg.LogoutJS != "" && clearWin != nil && !clearWin.IsDestroyed() {
		_, _ = clearWin.Eval(ctx, cfg.LogoutJS)
	}
	if tempWin {
		clearWin.Destroy()
		m.mu.Lock()
		delete(m.work, p)
		m.mu.Unlock()
	}

	if login != nil && !login.IsDestroyed() {
		login.Destroy()
	}
	if work != nil && !work.IsDestroyed() {
		if cfg.LogoutURL != "" {
			_ = work.LoadURL(cfg.LogoutURL)
			_ = waitReady(ctx, work)
		}
		work.Destroy()
	}
	return nil
}
