package livechat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const oauthBlob = "livechat_oauth.json"

// OAuthSettings 全局 OAuth 应用凭证 + 登录后拿到的 token。
// 页面顶部「用 Google / Twitch 登录」写这里；保存弹幕源时若本路没填 token 会自动带上。
type OAuthSettings struct {
	GoogleClientID     string `json:"google_client_id"`
	GoogleClientSecret string `json:"google_client_secret"`
	GoogleAccessToken  string `json:"google_access_token,omitempty"`
	GoogleRefreshToken string `json:"google_refresh_token,omitempty"`
	GoogleExpiry       int64  `json:"google_expiry,omitempty"`
	GoogleEmail        string `json:"google_email,omitempty"`

	TwitchClientID     string `json:"twitch_client_id"`
	TwitchClientSecret string `json:"twitch_client_secret"`
	TwitchAccessToken  string `json:"twitch_access_token,omitempty"`
	TwitchUserID       string `json:"twitch_user_id,omitempty"`
	TwitchLogin        string `json:"twitch_login,omitempty"`

	// BilibiliCookie 全局 B 站登录 Cookie（SESSDATA + bili_jct）。弹幕源本路没填时自动带上。
	BilibiliCookie string `json:"bilibili_cookie,omitempty"`

	// 各来源一个 HTTP 服务根。空字符串的含义：
	// youtube_api → 官方 googleapis；twitch → 官方 Helix；
	// chat_downloader / bilibili_blivedm → twitter-read。
	YouTubeServiceURL        string `json:"youtube_service_url,omitempty"`
	ChatDownloaderServiceURL string `json:"chat_downloader_service_url,omitempty"`
	TwitchServiceURL         string `json:"twitch_service_url,omitempty"`
	BilibiliServiceURL       string `json:"bilibili_service_url,omitempty"`
}

// OAuthPublic 给前端的脱敏视图（secret / token 只回是否已配置）。
type OAuthPublic struct {
	GoogleClientID        string `json:"google_client_id"`
	GoogleClientSecretSet bool   `json:"google_client_secret_set"`
	GoogleConnected       bool   `json:"google_connected"`
	GoogleEmail           string `json:"google_email,omitempty"`

	TwitchClientID        string `json:"twitch_client_id"`
	TwitchClientSecretSet bool   `json:"twitch_client_secret_set"`
	TwitchConnected       bool   `json:"twitch_connected"`
	TwitchLogin           string `json:"twitch_login,omitempty"`
	TwitchUserID          string `json:"twitch_user_id,omitempty"`

	BilibiliCookieSet bool `json:"bilibili_cookie_set"`

	YouTubeServiceURL        string `json:"youtube_service_url,omitempty"`
	ChatDownloaderServiceURL string `json:"chat_downloader_service_url,omitempty"`
	TwitchServiceURL         string `json:"twitch_service_url,omitempty"`
	BilibiliServiceURL       string `json:"bilibili_service_url,omitempty"`
}

// Public 脱敏。
func (o OAuthSettings) Public() OAuthPublic {
	return OAuthPublic{
		GoogleClientID:           o.GoogleClientID,
		GoogleClientSecretSet:    o.GoogleClientSecret != "",
		GoogleConnected:          o.GoogleAccessToken != "",
		GoogleEmail:              o.GoogleEmail,
		TwitchClientID:           o.TwitchClientID,
		TwitchClientSecretSet:    o.TwitchClientSecret != "",
		TwitchConnected:          o.TwitchAccessToken != "",
		TwitchLogin:              o.TwitchLogin,
		TwitchUserID:             o.TwitchUserID,
		BilibiliCookieSet:        o.BilibiliCookie != "",
		YouTubeServiceURL:        o.YouTubeServiceURL,
		ChatDownloaderServiceURL: o.chatDownloaderServiceURL(),
		TwitchServiceURL:         o.TwitchServiceURL,
		BilibiliServiceURL:       o.bilibiliServiceURL(),
	}
}

// 服务地址不做隐式兜底：twitter-read（socialread）服务没有 chat 端点，
// 把默认 base 填进去只会 GET 到后台 HTML 页（200 空流 → 无限重连）。
// 留空 = B 站走 blivedm 直连 WebSocket、chat_downloader 走本机 python3。
func (o OAuthSettings) chatDownloaderServiceURL() string {
	return strings.TrimRight(strings.TrimSpace(o.ChatDownloaderServiceURL), "/")
}

func (o OAuthSettings) bilibiliServiceURL() string {
	return strings.TrimRight(strings.TrimSpace(o.BilibiliServiceURL), "/")
}

// OAuth 读当前全局凭证（拷贝）。
func (m *Manager) OAuth() OAuthSettings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.oauth
}

// SaveOAuthApp 只更新应用 Client ID / Secret（空 secret 表示保持原值）。
func (m *Manager) SaveOAuthApp(in OAuthSettings) (OAuthSettings, error) {
	m.mu.Lock()
	cur := m.oauth
	cur.GoogleClientID = strings.TrimSpace(in.GoogleClientID)
	if strings.TrimSpace(in.GoogleClientSecret) != "" {
		cur.GoogleClientSecret = strings.TrimSpace(in.GoogleClientSecret)
	}
	cur.TwitchClientID = strings.TrimSpace(in.TwitchClientID)
	if strings.TrimSpace(in.TwitchClientSecret) != "" {
		cur.TwitchClientSecret = strings.TrimSpace(in.TwitchClientSecret)
	}
	if strings.TrimSpace(in.BilibiliCookie) != "" {
		cur.BilibiliCookie = strings.TrimSpace(in.BilibiliCookie)
	}
	m.oauth = cur
	m.mu.Unlock()
	return cur, m.st.Save(oauthBlob, cur)
}

// SaveServiceURLs 保存三个平台的 HTTP 服务根地址。空字符串表示改回官方/本机。
// 正在跑的对应来源会停掉再拉起，让新地址生效。
func (m *Manager) SaveServiceURLs(youtube, chatDL, twitch, bili string) (OAuthSettings, error) {
	m.mu.Lock()
	m.oauth.YouTubeServiceURL = strings.TrimRight(strings.TrimSpace(youtube), "/")
	m.oauth.ChatDownloaderServiceURL = strings.TrimRight(strings.TrimSpace(chatDL), "/")
	m.oauth.TwitchServiceURL = strings.TrimRight(strings.TrimSpace(twitch), "/")
	m.oauth.BilibiliServiceURL = strings.TrimRight(strings.TrimSpace(bili), "/")
	cur := m.oauth
	ids := make([]string, 0)
	for _, c := range m.cfgs {
		if serviceKind(c.Kind) != "" {
			ids = append(ids, c.ID)
		}
	}
	running := make([]string, 0)
	for _, id := range ids {
		if _, ok := m.runs[id]; ok {
			running = append(running, id)
		}
	}
	m.mu.Unlock()
	if err := m.st.Save(oauthBlob, cur); err != nil {
		return cur, err
	}
	for _, id := range running {
		m.Stop(id)
		_ = m.Start(id)
	}
	return cur, nil
}

func serviceKind(kind string) string {
	switch kind {
	case KindYouTubeAPI:
		return "youtube"
	case KindChatDownloader:
		return "chat_downloader"
	case KindTwitchEventSub:
		return "twitch"
	case KindBilibili:
		return "bilibili"
	default:
		return ""
	}
}

// SetGoogleToken 登录成功后写入 Google token。
func (m *Manager) SetGoogleToken(access, refresh, email string, expiry int64) error {
	m.mu.Lock()
	m.oauth.GoogleAccessToken = access
	if refresh != "" {
		m.oauth.GoogleRefreshToken = refresh
	}
	m.oauth.GoogleEmail = email
	m.oauth.GoogleExpiry = expiry
	cur := m.oauth
	m.mu.Unlock()
	return m.st.Save(oauthBlob, cur)
}

// SetTwitchToken 登录成功后写入 Twitch token。
func (m *Manager) SetTwitchToken(access, userID, login string) error {
	m.mu.Lock()
	m.oauth.TwitchAccessToken = access
	m.oauth.TwitchUserID = userID
	m.oauth.TwitchLogin = login
	cur := m.oauth
	m.mu.Unlock()
	return m.st.Save(oauthBlob, cur)
}

// ClearOAuthToken 退出某一侧登录（保留 Client ID / Secret）。
func (m *Manager) ClearOAuthToken(provider string) error {
	m.mu.Lock()
	switch provider {
	case "google":
		m.oauth.GoogleAccessToken = ""
		m.oauth.GoogleRefreshToken = ""
		m.oauth.GoogleEmail = ""
		m.oauth.GoogleExpiry = 0
	case "twitch":
		m.oauth.TwitchAccessToken = ""
		m.oauth.TwitchUserID = ""
		m.oauth.TwitchLogin = ""
	}
	cur := m.oauth
	m.mu.Unlock()
	return m.st.Save(oauthBlob, cur)
}

type oauthPending struct {
	Provider string
	Created  time.Time
}

// NewOAuthState 生成一次性 state（10 分钟有效）。
func (m *Manager) NewOAuthState(provider string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	state := hex.EncodeToString(b)
	m.mu.Lock()
	if m.oauthPending == nil {
		m.oauthPending = map[string]oauthPending{}
	}
	m.oauthPending[state] = oauthPending{Provider: provider, Created: time.Now()}
	m.mu.Unlock()
	return state
}

// ConsumeOAuthState 校验并消费 state；错误时返回原因。
func (m *Manager) ConsumeOAuthState(state, provider string) error {
	if state == "" {
		return fmt.Errorf("缺少 state")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.oauthPending[state]
	if !ok {
		return fmt.Errorf("state 无效或已使用")
	}
	delete(m.oauthPending, state)
	if p.Provider != provider {
		return fmt.Errorf("state 与 provider 不匹配")
	}
	if time.Since(p.Created) > 10*time.Minute {
		return fmt.Errorf("state 已过期，请重新登录")
	}
	return nil
}

// applyOAuthDefaults 本路没填 token 时，用全局登录态补齐。
func (m *Manager) applyOAuthDefaults(c *SourceConfig) {
	oa := m.OAuth()
	switch c.Kind {
	case KindTwitchEventSub:
		if strings.TrimSpace(c.TwitchClientID) == "" {
			c.TwitchClientID = oa.TwitchClientID
		}
		if strings.TrimSpace(c.TwitchToken) == "" {
			c.TwitchToken = oa.TwitchAccessToken
		}
		if strings.TrimSpace(c.TwitchUserID) == "" {
			c.TwitchUserID = oa.TwitchUserID
		}
		if len(c.Subscribe) == 0 {
			c.Subscribe = DefaultSubscribe(c.Kind)
		}
	case KindYouTubeAPI:
		if strings.TrimSpace(c.YouTubeAccessToken) == "" {
			c.YouTubeAccessToken = oa.GoogleAccessToken
		}
		if strings.TrimSpace(c.BaseURL) == "" {
			c.BaseURL = oa.YouTubeServiceURL
		}
	case KindChatDownloader:
		if strings.TrimSpace(c.BaseURL) == "" {
			c.BaseURL = oa.chatDownloaderServiceURL()
		}
	case KindBilibili:
		if strings.TrimSpace(c.BilibiliCookie) == "" {
			c.BilibiliCookie = oa.BilibiliCookie
		}
		if strings.TrimSpace(c.BaseURL) == "" {
			c.BaseURL = oa.bilibiliServiceURL()
		}
	}
	if c.Kind == KindTwitchEventSub && strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = oa.TwitchServiceURL
	}
}

// GoogleAuthURL 拼 Google 授权地址（弹窗跳转）。
func GoogleAuthURL(clientID, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", "https://www.googleapis.com/auth/youtube.readonly https://www.googleapis.com/auth/userinfo.email")
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", state)
	q.Set("include_granted_scopes", "true")
	return "https://accounts.google.com/o/oauth2/v2/auth?" + q.Encode()
}

// TwitchAuthURL 拼 Twitch 授权地址（弹窗跳转）。
func TwitchAuthURL(clientID, redirectURI, state string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", "user:read:chat")
	q.Set("state", state)
	q.Set("force_verify", "true")
	return "https://id.twitch.tv/oauth2/authorize?" + q.Encode()
}

type tokenResult struct {
	AccessToken  string
	RefreshToken string
	Expiry       int64
	Email        string
	UserID       string
	Login        string
}

// ExchangeGoogle code → access/refresh + email。
func ExchangeGoogle(ctx context.Context, httpc *http.Client, clientID, secret, redirectURI, code string) (tokenResult, error) {
	var out tokenResult
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpc.Do(req)
	if err != nil {
		return out, fmt.Errorf("Google 换 token 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("Google 换 token HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return out, err
	}
	if tok.AccessToken == "" {
		return out, fmt.Errorf("Google 未返回 access_token")
	}
	out.AccessToken = tok.AccessToken
	out.RefreshToken = tok.RefreshToken
	if tok.ExpiresIn > 0 {
		out.Expiry = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second).Unix()
	}
	email, _ := fetchGoogleEmail(ctx, httpc, tok.AccessToken)
	out.Email = email
	return out, nil
}

func fetchGoogleEmail(ctx context.Context, httpc *http.Client, access string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v2/userinfo", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := httpc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo HTTP %d", resp.StatusCode)
	}
	var u struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", err
	}
	return u.Email, nil
}

// ExchangeTwitch code → access + user_id/login。
func ExchangeTwitch(ctx context.Context, httpc *http.Client, clientID, secret, redirectURI, code string) (tokenResult, error) {
	var out tokenResult
	form := url.Values{}
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("client_secret", secret)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://id.twitch.tv/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpc.Do(req)
	if err != nil {
		return out, fmt.Errorf("Twitch 换 token 失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("Twitch 换 token HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return out, err
	}
	if tok.AccessToken == "" {
		return out, fmt.Errorf("Twitch 未返回 access_token")
	}
	out.AccessToken = tok.AccessToken
	uid, login, _ := fetchTwitchIdentity(ctx, httpc, tok.AccessToken)
	out.UserID, out.Login = uid, login
	return out, nil
}

func fetchTwitchIdentity(ctx context.Context, httpc *http.Client, access string) (userID, login string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://id.twitch.tv/oauth2/validate", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "OAuth "+access)
	resp, err := httpc.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("validate HTTP %d", resp.StatusCode)
	}
	var v struct {
		UserID string `json:"user_id"`
		Login  string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return "", "", err
	}
	return v.UserID, v.Login, nil
}
