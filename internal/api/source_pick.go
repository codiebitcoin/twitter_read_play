package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"twitter_read_play/internal/browser"
	"twitter_read_play/internal/i18n"
	"twitter_read_play/internal/source"
)

// 角色设置 → 打开内置浏览器选用户：
// 前端 POST /api/source/pick（body 带界面语言 lang），这里开一个注入 PickPreload
// 的查看窗（key=pick-<roleID>，每个角色独立一个），并挂 WatchPick 监听。
// 用户在窗内浏览到目标主页后点右下角按钮，回调里解析 href 回写角色的
// feed_target/platform/username 并自动关窗（只关这一个窗）。
// 注入文案与回显文案都走 internal/i18n（en 兜底）。

// closePickAfter 选定成功到自动关窗的间隔：留时间让用户看到 ✓ 文案。
const closePickAfter = 1500 * time.Millisecond

type pickRequest struct {
	RoleID string `json:"role_id"`
	Lang   string `json:"lang"` // 界面语言（zh/ja/en）；空则回落 Accept-Language，最终 en 兜底
}

// pickSource POST /api/source/pick
func (s *Server) pickSource(w http.ResponseWriter, r *http.Request) {
	var in pickRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	rl, err := s.Roles.Get(strings.TrimSpace(in.RoleID))
	if err != nil {
		s.err(w, http.StatusNotFound, errStr(s.tr(r, "err.role_not_found")))
		return
	}
	if !s.browserAvailable() {
		s.err(w, http.StatusNotImplemented, errStr("desktop_only"))
		return
	}
	pickLang := i18n.Lang(in.Lang)
	if strings.TrimSpace(in.Lang) == "" {
		pickLang = lang(r) // 请求没带语言时按 Accept-Language（en 兜底）
	}

	platform := source.Platform(rl.Platform)
	// 起点：已配目标的主页（方便继续找同类用户）；没配过就用平台首页。
	start := browser.HomeURLFor(browser.Provider(rl.Platform))
	if t, perr := source.ParseFeed(strings.TrimSpace(rl.FeedTarget), platform); perr == nil {
		if u, ok := t.ViewURL(); ok {
			start = u
		}
	}

	key := "pick-" + rl.ID
	vr, oerr := s.Browser.OpenView(r.Context(), browser.ViewOptions{
		Title:         "选择用户 · " + rl.Name,
		URL:           start,
		Key:           key,
		Provider:      browser.Provider(rl.Platform),
		PreloadScript: browser.PickPreload(pickLang),
	})
	if oerr != nil {
		s.err(w, http.StatusBadRequest, oerr)
		return
	}
	s.Browser.WatchPick(key, s.pickRoleTarget(rl.ID, pickLang), closePickAfter)
	s.json(w, map[string]any{"ok": true, "key": vr.Key, "url": vr.URL, "reused": vr.Reused})
}

// pickRoleTarget 返回选用户回调：解析当前页 href，是合法用户主页就写进角色。
func (s *Server) pickRoleTarget(roleID, lang string) func(string) (bool, string) {
	return func(href string) (bool, string) {
		rl, err := s.Roles.Get(roleID)
		if err != nil {
			return false, i18n.T(lang, "err.role_not_found")
		}
		t, perr := source.ParseFeed(href, source.Platform(rl.Platform))
		if perr != nil || t.Kind != source.KindProfile {
			return false, i18n.T(lang, "pick.not_profile")
		}
		u, ok := t.ViewURL()
		if !ok {
			return false, i18n.T(lang, "pick.not_profile")
		}
		// 先对齐平台再解析：parseRoleTargets 里的平台一致性校验以 role.Platform 为准。
		rl.Platform = string(t.Platform)
		rl.FeedTarget = u
		if _, verr := s.parseRoleTargets(rl); verr != nil {
			return false, verr.Error()
		}
		rl.Validation = nil // 不落库，与 upsertRole 一致
		if err := s.Roles.Upsert(rl); err != nil {
			return false, err.Error()
		}
		return true, strings.Replace(i18n.T(lang, "pick.picked"), "%s", t.ID, 1)
	}
}
