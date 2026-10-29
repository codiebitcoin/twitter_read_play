package watch

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/twitter"
)

// DefaultWebhookBase 公网 webhook 默认地址：留空 —— 必须由部署方在
// 「设置 → API Keys → Webhook 反向通知」填写自己的公网地址，否则无法创建监控。
const DefaultWebhookBase = ""

// socialdataProvider 取一个 socialdata.tools provider（用于监控管理调用）。
func (m *Manager) socialdataProvider() (*twitter.Socialdata, error) {
	fallback, _ := m.st.GetSetting("socialdata_api_key")
	p, _, err := m.upstreams.ProviderForRole("twitter", fallback)
	if err != nil {
		// 没配上游时兜底：settings key 直连官方地址
		if fallback != "" {
			return &twitter.Socialdata{APIKey: fallback, BaseURL: m.upstreams.DefaultBaseURL()}, nil
		}
		return nil, err
	}
	sd, ok := p.(*twitter.Socialdata)
	if !ok {
		return nil, fmt.Errorf("webhook monitoring only supports socialdata.tools upstream, current: %s", p.Name())
	}
	return sd, nil
}

// normalizePlatform 统一平台口径：空值与历史值 "x" 都按 twitter 处理。
func normalizePlatform(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" || p == "x" {
		return "twitter"
	}
	return p
}

// searchUsernames 收集适合 search monitor 打包的用户名：
// twitter 平台、启用中、且未走独立 per-user 监控的角色。
// （search/from: 查询只有 socialdata 的 twitter 上游支持，微博/小红书不能混进来）
func (m *Manager) searchUsernames() []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range m.roles.List() {
		if !m.effectivelyEnabled(r) || r.Username == "" || r.MonitorID != "" {
			continue
		}
		if normalizePlatform(r.Platform) != "twitter" {
			continue
		}
		name := strings.ToLower(strings.TrimPrefix(r.Username, "@"))
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// SearchMonitorStatus 查看共享 search monitor 状态。
func (m *Manager) SearchMonitorStatus() map[string]any {
	id, _ := m.st.GetSetting("socialdata_search_monitor_id")
	query, _ := m.st.GetSetting("socialdata_search_query")
	users := m.searchUsernames()
	inSync := query == strings.Join(users, ",")
	return map[string]any{
		"monitor_id": id, "query": query, "usernames": users, "in_sync": inSync && id != "",
	}
}

// SyncSearchMonitor 按当前角色列表重建共享 search monitor：
// from:a OR from:b … 打包全部（未走独立监控的）用户，一条监控覆盖所有人。
func (m *Manager) SyncSearchMonitor(refreshFrequency int) error {
	users := m.searchUsernames()
	if len(users) == 0 {
		return fmt.Errorf("no eligible role users (need enabled twitter roles on socialdata upstream)")
	}
	sd, err := m.socialdataProvider()
	if err != nil {
		return err
	}
	webhookURL, err := m.webhookURL()
	if err != nil {
		return err
	}
	query := strings.Join(users, ",") // 存储用；真实 query 为 from: 形式
	q := "from:" + strings.Join(users, " OR from:")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 先删旧监控（query 变了必须重建，search monitor 不支持改 query）
	oldID, _ := m.st.GetSetting("socialdata_search_monitor_id")
	if oldID != "" {
		_ = sd.DeleteMonitor(ctx, oldID)
	}
	id, err := sd.CreateSearchMonitor(ctx, q, refreshFrequency, webhookURL)
	if err != nil {
		return err
	}
	_ = m.st.SetSetting("socialdata_search_monitor_id", id)
	_ = m.st.SetSetting("socialdata_search_query", query)
	log.Printf("[watch] search monitor %s created: %s (every %ds)", id, q, refreshFrequency)
	return nil
}

// DeleteSearchMonitor 删除共享 search monitor。
func (m *Manager) DeleteSearchMonitor() error {
	id, _ := m.st.GetSetting("socialdata_search_monitor_id")
	if id == "" {
		return nil
	}
	sd, err := m.socialdataProvider()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sd.DeleteMonitor(ctx, id); err != nil {
		return err
	}
	_ = m.st.SetSetting("socialdata_search_monitor_id", "")
	_ = m.st.SetSetting("socialdata_search_query", "")
	return nil
}

// webhookBase 取公网地址（设置覆盖 → 默认值，默认值为空）。
func (m *Manager) webhookBase() string {
	if v, _ := m.st.GetSetting("socialdata_webhook_base"); v != "" {
		return v
	}
	return DefaultWebhookBase
}

// webhookURL 公网推送地址 = 公网地址 + /api/webhook/socialdata。
// 公网地址未配置（无默认值）时返回错误，避免把相对路径提交给 socialdata.tools。
func (m *Manager) webhookURL() (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(m.webhookBase()), "/")
	if base == "" {
		return "", fmt.Errorf("public webhook base URL is not configured; set it in Settings → API Keys → Webhook push")
	}
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		return "", fmt.Errorf("public webhook base URL must start with http:// or https://")
	}
	return base + "/api/webhook/socialdata", nil
}

// roleMatchesEvent 角色是否绑定该推送事件：平台与用户名都要匹配。
// 平台必须一起比 —— 微博与推特同名账号（都叫 elonmusk）不能互相触发。
func roleMatchesEvent(r *model.Role, platform, username string) bool {
	if normalizePlatform(r.Platform) != normalizePlatform(platform) {
		return false
	}
	want := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	have := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(r.Username), "@"))
	return want != "" && have == want
}

// HandleTweetEvent 处理上游 webhook 推送的新帖子（socialdata new_tweet / social_read new_post）：
// 找到所有绑定该平台该用户且启用的角色，逐个去重后播报（异步，立即返回）。
// platform 为空按 twitter 处理。
// 返回是否命中至少一个角色。
func (m *Manager) HandleTweetEvent(platform, username string, tw model.Tweet) bool {
	platform = normalizePlatform(platform)
	username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	if username == "" || tw.ID == "" {
		return false
	}
	hits := 0
	for _, r := range m.roles.List() {
		if !m.effectivelyEnabled(r) {
			continue
		}
		if !roleMatchesEvent(r, platform, username) {
			continue
		}
		// 雪花 id 去重：已在 last 之后播过的不再播
		if r.LastTweetID != "" && !gtID(tw.ID, r.LastTweetID) {
			continue
		}
		hits++
		m.saveRolePollState(r.ID, tw.ID)
		for _, b := range m.enabledBindings(r) {
			role, binding := r, b
			go m.broadcast(context.Background(), role, binding, tw, "watch")
		}
	}
	return hits > 0
}

// CreateMonitor 为角色创建 socialdata.tools user-tweets 监控（webhook 反向通知，模式 B）。
// 推送地址 = 公网地址（设置 socialdata_webhook_base，无默认值，未配置则报错）+ /api/webhook/socialdata。
func (m *Manager) CreateMonitor(roleID string) error {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return err
	}
	if r.Username == "" {
		return fmt.Errorf("role username not configured")
	}
	provider, _, err := m.providerForRole(r)
	if err != nil {
		return err
	}
	sd, ok := provider.(*twitter.Socialdata)
	if !ok {
		return fmt.Errorf("webhook monitoring only supports socialdata.tools upstream, current: %s", provider.Name())
	}
	webhookURL, err := m.webhookURL()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := sd.CreateUserTweetsMonitor(ctx, r.Username, webhookURL)
	if err != nil {
		return err
	}
	r.MonitorID = id
	return m.roles.Upsert(r)
}

// DeleteMonitor 删除角色的 webhook 监控并回到轮询模式。
func (m *Manager) DeleteMonitor(roleID string) error {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return err
	}
	if r.MonitorID == "" {
		return nil
	}
	provider, _, err := m.providerForRole(r)
	if err == nil {
		if sd, ok := provider.(*twitter.Socialdata); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := sd.DeleteMonitor(ctx, r.MonitorID); err != nil {
				cancel()
				return err
			}
			cancel()
		}
	}
	r.MonitorID = ""
	return m.roles.Upsert(r)
}
