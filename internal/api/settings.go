// Package api 设置接口（GET/PUT /api/settings）。
package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"twitter_read_play/internal/netcfg"
	"twitter_read_play/internal/source"
)

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	key, _ := s.Store.GetSetting("socialdata_api_key")
	base, _ := s.Store.GetSetting("socialdata_base_url")
	user, _ := s.Store.GetSetting("auth_user")
	userFromDB := user != ""
	if user == "" {
		user = s.authUser() // env 兜底（AUTH_USER），展示实际生效值
	}
	passSet := false
	passFromDB := false
	if v, _ := s.Store.GetSetting("auth_pass"); v != "" {
		passSet, passFromDB = true, true
	} else if s.EnvAuthPass != "" {
		passSet = true // env 提供了密码，同样视为已配置
	}
	s.json(w, map[string]any{
		"socialdata_api_key":  key,
		"socialdata_base_url": base,
		"webhook_base":        s.effectiveWebhookBase(),
		"webhook_secret_set":  s.mustSetting("socialdata_webhook_secret") != "",
		"auth_user":           user,
		"auth_pass_set":       passSet,
		"auth_user_from_db":   userFromDB,
		"auth_pass_from_db":   passFromDB,
		// parody 模式用的 LLM（OpenAI 兼容）
		"llm_base_url": s.mustSetting("llm_base_url"),
		"llm_api_key":  s.mustSetting("llm_api_key"),
		"llm_model":    s.mustSetting("llm_model"),
		// 出站 HTTP 代理。no_http 为 1/true/* 时直连；其他非空值当作 NO_PROXY 主机列表。
		"http_proxy": s.mustSetting("http_proxy"),
		"no_http":    s.mustSetting("no_http"),
		// Web 模式远程浏览器上游（chrome-driverless）
		"chrome_driverless_url":   s.mustSetting("chrome_driverless_url"),
		"chrome_driverless_proxy": s.mustSetting("chrome_driverless_proxy"),
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		s.err(w, 400, err)
		return
	}
	if v, ok := in["socialdata_api_key"]; ok {
		if err := s.Store.SetSetting("socialdata_api_key", v); err != nil {
			s.err(w, 500, err)
			return
		}
		s.Watches.SetAPIKey(v)
	}
	if v, ok := in["socialdata_base_url"]; ok {
		v = strings.TrimSuffix(strings.TrimSpace(v), "/")
		if err := s.Store.SetSetting("socialdata_base_url", v); err != nil {
			s.err(w, 500, err)
			return
		}
		s.Watches.SetBaseURL(v)
	}
	if v, ok := in["webhook_base"]; ok {
		_ = s.Store.SetSetting("socialdata_webhook_base", strings.TrimSuffix(strings.TrimSpace(v), "/"))
	}
	if v, ok := in["webhook_secret"]; ok {
		_ = s.Store.SetSetting("socialdata_webhook_secret", v)
	}
	if v, ok := in["auth_user"]; ok {
		_ = s.Store.SetSetting("auth_user", v)
	}
	if v, ok := in["auth_pass"]; ok && v != "" {
		_ = s.Store.SetSetting("auth_pass", v)
	}
	// parody 模式的 LLM（OpenAI 兼容）：base_url / api_key / model
	for _, k := range []string{"llm_base_url", "llm_api_key", "llm_model"} {
		if v, ok := in[k]; ok {
			_ = s.Store.SetSetting(k, strings.TrimSpace(v))
		}
	}
	// Web 模式远程浏览器上游（chrome-driverless）
	remoteTouched := false
	remoteURLChanged := false
	for _, k := range []string{"chrome_driverless_url", "chrome_driverless_proxy"} {
		if v, ok := in[k]; ok {
			old, _ := s.Store.GetSetting(k)
			_ = s.Store.SetSetting(k, strings.TrimSpace(v))
			remoteTouched = true
			if k == "chrome_driverless_url" && strings.TrimSpace(old) != strings.TrimSpace(v) {
				remoteURLChanged = true
			}
		}
	}
	if remoteTouched {
		// 换了地址（可能是另一台实例）：旧 tab 索引/登录态都不再有效，全部丢弃。
		if remoteURLChanged && s.Browser != nil {
			s.Browser.ResetSessions()
		}
		// 地址从空变成有值时，浏览器 adapter 才声明支持 web 模式；补建对应渠道，
		// 否则 Web 端会显示「服务可用」但一个浏览器渠道都没有。
		if s.Channels != nil && s.Adapters != nil && s.Browser != nil && s.Browser.UsesRemote() {
			_ = s.Channels.EnsureBrowserChannels(source.ModeWeb, s.Adapters)
		}
	}
	proxyTouched := false
	if v, ok := in["http_proxy"]; ok {
		_ = s.Store.SetSetting("http_proxy", strings.TrimSpace(v))
		proxyTouched = true
	}
	if v, ok := in["no_http"]; ok {
		_ = s.Store.SetSetting("no_http", strings.TrimSpace(v))
		proxyTouched = true
	}
	if proxyTouched {
		s.applyNet()
	}
	// 新渠道模型：把 socialdata key/base 同步到默认渠道（迁移期两套并存）。
	if s.Channels != nil {
		_ = s.Channels.SaveOAuthToDefaultSocialdata(
			s.mustSetting("socialdata_api_key"),
			s.mustSetting("socialdata_base_url"),
		)
	}
	s.json(w, map[string]bool{"ok": true})
}

// applyNet 把数据库里的 http_proxy / no_http 应用到进程环境。
func (s *Server) applyNet() {
	netcfg.Apply(s.mustSetting("http_proxy"), s.mustSetting("no_http"))
}
