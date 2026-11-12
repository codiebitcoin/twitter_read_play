package api

import (
	"context"
	"net/http"
	"time"

	"twitter_read_play/internal/browser"
)

func (s *Server) browserAvailable() bool {
	return s.Browser != nil && s.Browser.Available()
}

// listBrowserSessions GET /api/browser/session
func (s *Server) listBrowserSessions(w http.ResponseWriter, r *http.Request) {
	if !s.browserAvailable() {
		out := map[string]any{"available": false, "providers": []any{}, "mode": s.serverMode()}
		if s.Browser != nil {
			// Web 模式：把已填的地址和操作页回给前端，提示卡片据此展示。
			if u := s.Browser.RemoteOperationURL(); u != "" {
				out["operation_url"] = u
			}
		}
		s.json(w, out)
		return
	}
	s.json(w, map[string]any{
		"available":     true,
		"providers":     s.Browser.Statuses(),
		"mode":          s.serverMode(),
		"remote":        s.Browser.UsesRemote(),
		"operation_url": s.Browser.RemoteOperationURL(),
	})
}

// openBrowserLogin POST /api/browser/session/{provider}/open
func (s *Server) openBrowserLogin(w http.ResponseWriter, r *http.Request) {
	if !s.browserAvailable() {
		s.err(w, http.StatusNotImplemented, errStr("desktop_only"))
		return
	}
	p := browser.Provider(r.PathValue("provider"))
	if !browser.ValidProvider(p) {
		s.err(w, http.StatusBadRequest, errStr("未知平台: "+string(p)))
		return
	}
	var err error
	if s.Browser.UsesRemote() {
		// Web 模式：远程浏览器里开一个登录 tab，用户到自己浏览器的操作页里登录。
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		err = s.Browser.RemoteLogin(ctx, p)
	} else {
		err = s.Browser.OpenLogin(r.Context(), p, lang(r))
	}
	if err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	s.json(w, map[string]any{
		"ok":            true,
		"provider":      p,
		"remote":        s.Browser.UsesRemote(),
		"operation_url": s.Browser.RemoteOperationURL(),
	})
}

// probeBrowserSession GET /api/browser/session/{provider} 和 POST .../probe
func (s *Server) probeBrowserSession(w http.ResponseWriter, r *http.Request) {
	if !s.browserAvailable() {
		s.err(w, http.StatusNotImplemented, errStr("desktop_only"))
		return
	}
	p := browser.Provider(r.PathValue("provider"))
	if !browser.ValidProvider(p) {
		s.err(w, http.StatusBadRequest, errStr("未知平台: "+string(p)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	st, err := s.Browser.Probe(ctx, p)
	if err != nil && st.Error == "" {
		st.Error = err.Error()
	}
	// 即使探测失败也回完整状态：前端据此保留上次的「已连接」，
	// 只把 error 显示出来，而不是误报成未登录。
	s.json(w, st)
}

// browserTokens GET /api/browser/session/{provider}/tokens
// 只回字段是否存在，不回明文。
func (s *Server) browserTokens(w http.ResponseWriter, r *http.Request) {
	if !s.browserAvailable() {
		s.err(w, http.StatusNotImplemented, errStr("desktop_only"))
		return
	}
	p := browser.Provider(r.PathValue("provider"))
	if !browser.ValidProvider(p) {
		s.err(w, http.StatusBadRequest, errStr("未知平台: "+string(p)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	ts, err := s.Browser.Tokens(ctx, p)
	if err != nil {
		s.json(w, map[string]any{"provider": p, "error": err.Error()})
		return
	}
	s.json(w, map[string]any{
		"provider": p,
		"account":  ts.Account,
		"cookies":  keysOf(ts.Cookies),
		"storage":  keysOf(ts.Storage),
		"headers":  keysOf(ts.Headers),
	})
}

// logoutBrowserSession POST /api/browser/session/{provider}/logout
func (s *Server) logoutBrowserSession(w http.ResponseWriter, r *http.Request) {
	if !s.browserAvailable() {
		s.err(w, http.StatusNotImplemented, errStr("desktop_only"))
		return
	}
	p := browser.Provider(r.PathValue("provider"))
	if !browser.ValidProvider(p) {
		s.err(w, http.StatusBadRequest, errStr("未知平台: "+string(p)))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := s.Browser.Logout(ctx, p); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// closeBrowserSession POST /api/browser/session/{provider}/close
// 登录成功后前端延迟 5 秒调用：自动关掉登录窗（Web 模式无本地窗，CloseLogin 为空操作）。
func (s *Server) closeBrowserSession(w http.ResponseWriter, r *http.Request) {
	if !s.browserAvailable() {
		s.err(w, http.StatusNotImplemented, errStr("desktop_only"))
		return
	}
	p := browser.Provider(r.PathValue("provider"))
	if !browser.ValidProvider(p) {
		s.err(w, http.StatusBadRequest, errStr("未知平台: "+string(p)))
		return
	}
	s.Browser.CloseLogin(p)
	s.json(w, map[string]bool{"ok": true})
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
