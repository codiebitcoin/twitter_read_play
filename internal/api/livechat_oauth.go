package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"twitter_read_play/internal/livechat"
)

// oauthRedirects 对外登记的回调地址：统一规则 <origin>/callback/<平台>，
// 以后新平台沿用该规则多加一段即可。
func oauthRedirects(s *Server, r *http.Request) map[string]string {
	base := requestOrigin(s, r)
	return map[string]string{
		"google": base + "/callback/google",
		"twitch": base + "/callback/twitch",
	}
}

func requestOrigin(s *Server, r *http.Request) string {
	// PC（桌面）端回调 host 固定为 localhost+真实端口（ServeDesktop 注入 Origin），
	// Twitch/Google 后台按它登记；不跟随请求 Host，避免从 127.0.0.1 等入口进来拼错。
	if s.Origin != "" {
		return s.Origin
	}
	proto := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		proto = "https"
	}
	host := r.Host
	if host == "" {
		host = "localhost:8080"
	}
	return proto + "://" + host
}

// saveLivechatOAuth PUT /api/livechat/oauth —— 保存 Google / Twitch 应用 Client ID + Secret。
func (s *Server) saveLivechatOAuth(w http.ResponseWriter, r *http.Request) {
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	body, _ := json.Marshal(raw)
	var in livechat.OAuthSettings
	if err := json.Unmarshal(body, &in); err != nil {
		s.err(w, http.StatusBadRequest, err)
		return
	}
	cur := s.Live.OAuth()
	_, service := raw["youtube_service_url"]
	_, serviceChat := raw["chat_downloader_service_url"]
	_, serviceTw := raw["twitch_service_url"]
	_, serviceBi := raw["bilibili_service_url"]
	if service || serviceChat || serviceTw || serviceBi {
		if !service {
			in.YouTubeServiceURL = cur.YouTubeServiceURL
		}
		if !serviceChat {
			in.ChatDownloaderServiceURL = cur.ChatDownloaderServiceURL
		}
		if !serviceTw {
			in.TwitchServiceURL = cur.TwitchServiceURL
		}
		if !serviceBi {
			in.BilibiliServiceURL = cur.BilibiliServiceURL
		}
		saved, err := s.Live.SaveServiceURLs(in.YouTubeServiceURL, in.ChatDownloaderServiceURL, in.TwitchServiceURL, in.BilibiliServiceURL)
		if err != nil {
			s.err(w, http.StatusInternalServerError, err)
			return
		}
		cur = saved
	}
	if _, ok := raw["google_client_id"]; ok || rawHas(raw, "google_client_secret") || rawHas(raw, "twitch_client_id") || rawHas(raw, "twitch_client_secret") || rawHas(raw, "bilibili_cookie") {
		saved, err := s.Live.SaveOAuthApp(in)
		if err != nil {
			s.err(w, http.StatusInternalServerError, err)
			return
		}
		cur = saved
	}
	s.json(w, cur.Public())
}

func rawHas(raw map[string]json.RawMessage, key string) bool {
	_, ok := raw[key]
	return ok
}

// livechatOAuthLogout POST /api/livechat/oauth/{provider}/logout
func (s *Server) livechatOAuthLogout(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("provider")
	if p != "google" && p != "twitch" {
		s.err(w, http.StatusBadRequest, errStr("provider 只能是 google 或 twitch"))
		return
	}
	if err := s.Live.ClearOAuthToken(p); err != nil {
		s.err(w, http.StatusInternalServerError, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

// livechatOAuthStart GET /api/livechat/oauth/{provider}/start —— 302 到 Google / Twitch 登录页（弹窗打开）。
func (s *Server) livechatOAuthStart(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("provider")
	oa := s.Live.OAuth()
	redirects := oauthRedirects(s, r)
	redirect, ok := redirects[p]
	if !ok {
		s.err(w, http.StatusBadRequest, errStr("provider 只能是 google 或 twitch"))
		return
	}
	state := s.Live.NewOAuthState(p)
	var dest string
	switch p {
	case "google":
		if strings.TrimSpace(oa.GoogleClientID) == "" || strings.TrimSpace(oa.GoogleClientSecret) == "" {
			oauthPopupHTML(w, p, false, "请先填写并保存 Google Client ID 和 Client Secret")
			return
		}
		dest = livechat.GoogleAuthURL(oa.GoogleClientID, redirect, state)
	case "twitch":
		if strings.TrimSpace(oa.TwitchClientID) == "" || strings.TrimSpace(oa.TwitchClientSecret) == "" {
			oauthPopupHTML(w, p, false, "请先填写并保存 Twitch Client ID 和 Client Secret")
			return
		}
		dest = livechat.TwitchAuthURL(oa.TwitchClientID, redirect, state)
	}
	http.Redirect(w, r, dest, http.StatusFound)
}

// livechatOAuthCallback GET /callback/{platform}（对外登记的统一规则）或
// GET /api/livechat/oauth/{provider}/callback（旧路径，保留兼容）—— 换 token，关弹窗通知 opener。
func (s *Server) livechatOAuthCallback(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("platform")
	if p == "" {
		p = r.PathValue("provider")
	}
	if err := r.URL.Query().Get("error"); err != "" {
		oauthPopupHTML(w, p, false, "授权被拒绝: "+err)
		return
	}
	if e := s.Live.ConsumeOAuthState(r.URL.Query().Get("state"), p); e != nil {
		oauthPopupHTML(w, p, false, e.Error())
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		oauthPopupHTML(w, p, false, "回调缺少 code")
		return
	}
	oa := s.Live.OAuth()
	redirect := oauthRedirects(s, r)[p]
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	httpc := &http.Client{Timeout: 15 * time.Second}

	switch p {
	case "google":
		tok, err := livechat.ExchangeGoogle(ctx, httpc, oa.GoogleClientID, oa.GoogleClientSecret, redirect, code)
		if err != nil {
			oauthPopupHTML(w, p, false, err.Error())
			return
		}
		if err := s.Live.SetGoogleToken(tok.AccessToken, tok.RefreshToken, tok.Email, tok.Expiry); err != nil {
			oauthPopupHTML(w, p, false, err.Error())
			return
		}
		msg := "Google 已登录"
		if tok.Email != "" {
			msg += "（" + tok.Email + "）"
		}
		oauthPopupHTML(w, p, true, msg)
	case "twitch":
		tok, err := livechat.ExchangeTwitch(ctx, httpc, oa.TwitchClientID, oa.TwitchClientSecret, redirect, code)
		if err != nil {
			oauthPopupHTML(w, p, false, err.Error())
			return
		}
		if err := s.Live.SetTwitchToken(tok.AccessToken, tok.UserID, tok.Login); err != nil {
			oauthPopupHTML(w, p, false, err.Error())
			return
		}
		msg := "Twitch 已登录"
		if tok.Login != "" {
			msg += "（" + tok.Login + "）"
		}
		oauthPopupHTML(w, p, true, msg)
	default:
		oauthPopupHTML(w, p, false, "未知 provider")
	}
}

func oauthPopupHTML(w http.ResponseWriter, provider string, ok bool, msg string) {
	status := "ok"
	if !ok {
		status = "error"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>登录%v</title>
<body style="font-family:sans-serif;padding:24px">
<p>%s</p>
<script>
try {
  if (window.opener) {
    window.opener.postMessage({type:'livechat-oauth',provider:%q,ok:%v,message:%q}, '*');
  }
} catch (e) {}
setTimeout(function(){ window.close(); }, 400);
</script>
</body>`, status, htmlEscape(msg), provider, ok, msg)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
