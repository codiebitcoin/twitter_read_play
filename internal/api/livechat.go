// 直播弹幕源配置与拉取（/api/livechat*）。
// 配置即接口：POST 保存 → Enabled=true 自动开拉；消息走增量游标（after=seq）。
package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"twitter_read_play/internal/livechat"
)

// listLivechat GET /api/livechat → { configs, statuses, kinds }
func (s *Server) listLivechat(w http.ResponseWriter, r *http.Request) {
	s.json(w, map[string]any{
		"configs":         s.Live.Configs(),
		"statuses":        s.Live.Statuses(),
		"kinds":           livechat.Kinds,
		"subscribe_kinds": livechat.TwitchSubscribeKinds,
		"oauth":           s.Live.OAuth().Public(),
		"oauth_redirects": oauthRedirects(s, r),
	})
}

// saveLivechat POST /api/livechat → 新建/更新并按需启停。
func (s *Server) saveLivechat(w http.ResponseWriter, r *http.Request) {
	var c livechat.SourceConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	// H2：python_path/extra_args 不再接受页面配置，由 SaveConfig 统一剥离
	// （Validate 对非空值报错，兜底拦住绕过 Manager 的直写路径）。
	saved, err := s.Live.SaveConfig(c)
	if err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	s.json(w, saved)
}

// randomLivechat POST /api/livechat/random  body: {kind}
// 在对应平台公开列表里随机挑一路正在直播的，写成 Enabled 配置并开拉。
func (s *Server) randomLivechat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	saved, err := s.Live.CreateRandom(r.Context(), in.Kind)
	if err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	s.json(w, saved)
}

// deleteLivechat DELETE /api/livechat/{id}
func (s *Server) deleteLivechat(w http.ResponseWriter, r *http.Request) {
	s.Live.DeleteConfig(r.PathValue("id"))
	s.json(w, map[string]bool{"ok": true})
}

// livechatStart POST /api/livechat/{id}/start
func (s *Server) livechatStart(w http.ResponseWriter, r *http.Request) {
	if err := s.Live.Start(r.PathValue("id")); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// livechatStop POST /api/livechat/{id}/stop
func (s *Server) livechatStop(w http.ResponseWriter, r *http.Request) {
	s.Live.Stop(r.PathValue("id"))
	s.json(w, map[string]bool{"ok": true})
}

// livechatMessages GET /api/livechat/{id}/messages?after=0&before=0&limit=200
// 增量拉取：前端记住上次最大 seq 作为 after，返回 (after, 最新] 的弹幕。
// 分页翻历史：传 before=当前最早一条的 seq，返回它之前的一页；has_older 表示还能继续翻。
func (s *Server) livechatMessages(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	msgs, hasOlder := s.Live.Messages(id, after, before, limit)
	resp := map[string]any{
		"messages":   msgs,
		"latest_seq": s.Live.LatestSeq(id),
		"has_older":  hasOlder,
	}
	if _, ok := s.Live.GetConfig(id); !ok {
		s.err(w, http.StatusNotFound, errNotFound)
		return
	}
	s.json(w, resp)
}

var errNotFound = errStr("not found")
