package api

import (
	"encoding/json"
	"net/http"

	"twitter_read_play/internal/browser"
	"twitter_read_play/internal/source"
)

type viewRequest struct {
	Domain   source.Domain `json:"domain"`
	Raw      string        `json:"raw"`
	Platform string        `json:"platform"`
}

type viewResponse struct {
	Mode     source.Mode `json:"mode"`
	Opened   bool        `json:"opened"`
	URL      string      `json:"url,omitempty"`
	Provider string      `json:"provider,omitempty"`
	Key      string      `json:"key,omitempty"`
	Reused   bool        `json:"reused,omitempty"`
	Error    string      `json:"error,omitempty"`
}

// viewSource POST /api/source/view
// 桌面端打开内置查看窗口；Web 端只返回规范 URL，由前端新标签打开。
func (s *Server) viewSource(w http.ResponseWriter, r *http.Request) {
	var in viewRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	if in.Domain == "" {
		in.Domain = source.DomainFeed
	}
	t, err := source.Parse(in.Domain, in.Raw, source.Platform(in.Platform))
	if err != nil {
		if s.writeTargetError(w, err) {
			return
		}
		s.err(w, http.StatusBadRequest, err)
		return
	}
	u, ok := t.ViewURL()
	if !ok {
		s.err(w, http.StatusBadRequest, errStr("该目标没有可安全打开的主页地址"))
		return
	}
	resp := viewResponse{Mode: s.serverMode(), URL: u}
	if !s.browserAvailable() {
		resp.Mode = source.ModeWeb
		s.json(w, resp)
		return
	}

	provider := browser.Provider(string(t.Platform))
	if !browser.ValidProvider(provider) {
		provider = ""
	}
	key := string(provider)
	if key == "" {
		key = "generic"
	}
	title := "查看 · " + t.Display
	if title == "查看 · " {
		title = "查看 · " + string(t.Platform)
	}
	vr, oerr := s.Browser.OpenView(r.Context(), browser.ViewOptions{
		Title: title, URL: u, Key: key, Provider: provider,
	})
	if oerr != nil {
		resp.Error = oerr.Error()
		s.json(w, resp)
		return
	}
	resp.Opened = true
	resp.Key = vr.Key
	resp.Reused = vr.Reused
	resp.Provider = string(provider)
	s.json(w, resp)
}
