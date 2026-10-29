package source

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
)

const channelsBlob = "channels"

// Manager 渠道持久化管理（SQLite blob channels）。
type Manager struct {
	mu   sync.Mutex
	st   *store.Store
	list []Channel
}

// NewManager 读取渠道；首次启动时从旧 upstreams / settings 幂等迁移。
func NewManager(st *store.Store) (*Manager, error) {
	m := &Manager{st: st}
	if err := st.Load(channelsBlob, &m.list); err != nil {
		return nil, err
	}
	if m.list == nil {
		m.list = []Channel{}
	}
	if err := m.migrateLegacy(); err != nil {
		return nil, err
	}
	if err := m.ensureDefaults(); err != nil {
		return nil, err
	}
	return m, nil
}

// migrateLegacy 旧 upstreams（APIKey/BaseURL/Cookie 扁平结构）→ 新 Channel。
// 只在 channels 为空时执行；幂等。旧 blob 保留不删，便于回滚。
func (m *Manager) migrateLegacy() error {
	if len(m.list) > 0 {
		return nil
	}
	var ups []*model.Upstream
	if err := m.st.Load("upstreams", &ups); err != nil {
		return err
	}
	if len(ups) == 0 {
		return nil
	}
	for _, u := range ups {
		if u == nil {
			continue
		}
		m.list = append(m.list, channelFromUpstream(u))
	}
	return m.saveLocked()
}

func channelFromUpstream(u *model.Upstream) Channel {
	adapter := strings.ToLower(strings.TrimSpace(u.API))
	base := strings.TrimSpace(u.BaseURL)
	if base == "" {
		base = strings.TrimSpace(u.Extra)
	}
	domain := DomainFeed
	if strings.EqualFold(adapter, "livechat") {
		domain = DomainLive
	}
	return Channel{
		ID:        u.ID,
		Name:      u.Name,
		Domain:    domain,
		AdapterID: adapter,
		Enabled:   u.Enabled,
		Credentials: Credentials{
			APIKey:  u.APIKey,
			BaseURL: base,
			Cookie:  u.Cookie,
		},
		RefreshSeconds: u.RefreshSeconds,
		CreatedAt:      u.CreatedAt,
	}
}

func (m *Manager) ensureDefaults() error {
	changed := false
	key, _ := m.st.GetSetting("socialdata_api_key")
	base, _ := m.st.GetSetting("socialdata_base_url")
	if strings.TrimSpace(key) != "" && !m.hasAdapterLocked("socialdata") {
		m.list = append(m.list, Channel{
			ID:        "ch-socialdata-default",
			Name:      "socialdata.tools",
			Domain:    DomainFeed,
			AdapterID: "socialdata",
			Enabled:   true,
			Credentials: Credentials{
				APIKey:  strings.TrimSpace(key),
				BaseURL: strings.TrimRight(strings.TrimSpace(base), "/"),
			},
			RefreshSeconds: 600,
			CreatedAt:      time.Now(),
		})
		changed = true
	}
	if !m.hasAdapterLocked("rss") {
		m.list = append(m.list, Channel{
			ID:             "ch-rss-default",
			Name:           "RSS / Atom",
			Domain:         DomainFeed,
			AdapterID:      "rss",
			Enabled:        true,
			RefreshSeconds: 300,
			CreatedAt:      time.Now(),
		})
		changed = true
	}
	// Bluesky 公开 API 不需要任何凭据：直接落一个可用渠道，角色选平台即可拉取。
	if !m.hasAdapterLocked("bluesky") {
		m.list = append(m.list, Channel{
			ID:             "ch-bluesky-default",
			Name:           "Bluesky（公开 API）",
			Domain:         DomainFeed,
			AdapterID:      "bluesky",
			Enabled:        true,
			RefreshSeconds: 600,
			CreatedAt:      time.Now(),
		})
		changed = true
	}
	if changed {
		return m.saveLocked()
	}
	return nil
}

// EnsureBrowserChannels 为内置/远程浏览器创建渠道（幂等）。
// 只有对应 adapter 已在当前模式注册时才创建，避免出现无法解析的渠道：
// 桌面端用 mygo WebView，Web 端配了 chrome-driverless 后同一批 adapter 也支持 web。
func (m *Manager) EnsureBrowserChannels(mode Mode, reg *Registry) error {
	specs := []struct {
		id       string
		adapter  string
		provider string
		name     string
		domain   Domain
	}{
		{"ch-browser-x", "x_browser", "x", "X（内置浏览器）", DomainFeed},
		{"ch-browser-weibo", "weibo_browser", "weibo", "微博（内置浏览器）", DomainFeed},
		{"ch-browser-xhs", "xiaohongshu_browser", "xiaohongshu", "小红书（内置浏览器）", DomainFeed},
		{"ch-browser-instagram", "instagram_browser", "instagram", "Instagram（内置浏览器）", DomainFeed},
		{"ch-browser-truth", "truth_browser", "truth", "Truth Social（内置浏览器）", DomainFeed},
		{"ch-browser-youtube", "youtube_browser", "youtube", "YouTube（内置浏览器）", DomainFeed},
		{"ch-browser-twitch", "twitch_browser", "twitch", "Twitch（内置浏览器）", DomainLive},
	}
	m.mu.Lock()
	changed := false
	for _, s := range specs {
		a, ok := reg.Get(s.adapter)
		if !ok {
			continue
		}
		// 适配器在当前模式不可用（例如纯 Web 没配远程浏览器）就不建渠道，
		// 否则会出现「显示就绪、实际永远失败」的假渠道。
		if !a.Info().SupportsMode(mode) {
			continue
		}
		// 渠道 domain 必须落在 adapter 的能力范围内（例如 YouTube/Bilibili
		// 内置浏览器目前只做 Live），否则该渠道在对应 Tab 里会显示为就绪却永远解析不到。
		domain, ok := PickDomain(a.Info(), s.domain)
		if !ok {
			continue
		}
		if i := m.indexOfIDLocked(s.id); i >= 0 {
			if m.list[i].Domain != domain {
				m.list[i].Domain = domain
				changed = true
			}
			continue
		}
		m.list = append(m.list, Channel{
			ID:        s.id,
			Name:      s.name,
			Domain:    domain,
			AdapterID: s.adapter,
			Enabled:   true,
			Credentials: Credentials{
				SessionProvider: s.provider,
			},
			RefreshSeconds: 600,
			CreatedAt:      time.Now(),
		})
		changed = true
	}
	m.mu.Unlock()
	if changed {
		return m.save()
	}
	return nil
}

// EnsureLiveDefaults Bilibili 默认渠道（幂等）。
// blivedm 公开 WebSocket 游客即可读；默认启用，否则新装用户选不到任何 B 站渠道。
// 其余平台的 Live 由 EnsureBrowserChannels 的内置浏览器渠道覆盖。
func (m *Manager) EnsureLiveDefaults(reg *Registry) error {
	specs := []struct {
		id      string
		adapter string
		name    string
		enabled bool
	}{
		{"ch-live-bilibili", "bilibili_blivedm", "Bilibili 弹幕", true},
	}
	m.mu.Lock()
	changed := false
	for _, s := range specs {
		if _, ok := reg.Get(s.adapter); !ok {
			continue
		}
		if m.hasIDLocked(s.id) {
			continue
		}
		m.list = append(m.list, Channel{
			ID: s.id, Name: s.name, Domain: DomainLive, AdapterID: s.adapter,
			Enabled: s.enabled, RefreshSeconds: 300, CreatedAt: time.Now(),
		})
		changed = true
	}
	m.mu.Unlock()
	if changed {
		return m.save()
	}
	return nil
}

// PruneMissingAdapters 删除 adapter 已不在注册表的渠道（幂等，升级清理用）。
// 只删指向已移除适配器的渠道，不碰用户自建渠道；被删渠道 ID 通过返回值告知，
// 供上层做角色 / livechat 配置的重映射。
func (m *Manager) PruneMissingAdapters(reg *Registry) (pruned []string, err error) {
	m.mu.Lock()
	out := m.list[:0]
	for _, ch := range m.list {
		if _, ok := reg.Get(ch.AdapterID); ok {
			out = append(out, ch)
			continue
		}
		pruned = append(pruned, ch.ID)
	}
	if len(pruned) == 0 {
		m.mu.Unlock()
		return nil, nil
	}
	m.list = out
	err = m.saveLocked()
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	log.Printf("[source] 清理指向已移除适配器的渠道: %s", strings.Join(pruned, ", "))
	return pruned, nil
}

// SaveOAuthToDefaultSocialdata 把设置页保存的 socialdata key/base 同步到默认渠道。
func (m *Manager) SaveOAuthToDefaultSocialdata(key, base string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	found := false
	for i := range m.list {
		if m.list[i].AdapterID != "socialdata" {
			continue
		}
		if !found {
			m.list[i].Credentials.APIKey = strings.TrimSpace(key)
			m.list[i].Credentials.BaseURL = strings.TrimRight(strings.TrimSpace(base), "/")
			found = true
		}
	}
	if !found && strings.TrimSpace(key) != "" {
		m.list = append(m.list, Channel{
			ID:        "ch-socialdata-default",
			Name:      "socialdata.tools",
			Domain:    DomainFeed,
			AdapterID: "socialdata",
			Enabled:   true,
			Credentials: Credentials{
				APIKey:  strings.TrimSpace(key),
				BaseURL: strings.TrimRight(strings.TrimSpace(base), "/"),
			},
			CreatedAt: time.Now(),
		})
	}
	return m.saveLocked()
}

// List 全部渠道（排序稳定）。
func (m *Manager) List() []Channel {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Channel, len(m.list))
	copy(out, m.list)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sort != out[j].Sort {
			return out[i].Sort < out[j].Sort
		}
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get 单个渠道。
func (m *Manager) Get(id string) (Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.list {
		if ch.ID == id {
			return ch, nil
		}
	}
	return Channel{}, fmt.Errorf("渠道不存在: %s", id)
}

// Upsert 新建或更新渠道。
func (m *Manager) Upsert(ch Channel) (Channel, error) {
	ch.Name = strings.TrimSpace(ch.Name)
	ch.AdapterID = strings.TrimSpace(ch.AdapterID)
	ch.Credentials.BaseURL = strings.TrimRight(strings.TrimSpace(ch.Credentials.BaseURL), "/")
	ch.Credentials.APIKey = strings.TrimSpace(ch.Credentials.APIKey)
	ch.Credentials.Cookie = strings.TrimSpace(ch.Credentials.Cookie)
	ch.Credentials.AuthToken = strings.TrimSpace(ch.Credentials.AuthToken)
	ch.Credentials.CT0 = strings.TrimSpace(ch.Credentials.CT0)
	ch.Credentials.OAuth = strings.TrimSpace(ch.Credentials.OAuth)
	ch.Credentials.ClientID = strings.TrimSpace(ch.Credentials.ClientID)
	ch.Credentials.SessionProvider = strings.TrimSpace(ch.Credentials.SessionProvider)
	ch.Credentials.Proxy = strings.TrimSpace(ch.Credentials.Proxy)
	if ch.AdapterID == "" {
		return ch, fmt.Errorf("adapter_id 不能为空")
	}
	if ch.Domain != DomainFeed && ch.Domain != DomainLive {
		ch.Domain = DomainFeed
	}
	if ch.ID == "" {
		ch.ID = fmt.Sprintf("ch-%d", time.Now().UnixNano())
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.list {
		if m.list[i].ID == ch.ID {
			if ch.CreatedAt.IsZero() {
				ch.CreatedAt = m.list[i].CreatedAt
			}
			m.list[i] = ch
			return ch, m.saveLocked()
		}
	}
	if ch.CreatedAt.IsZero() {
		ch.CreatedAt = time.Now()
	}
	m.list = append(m.list, ch)
	return ch, m.saveLocked()
}

// SetEnabled 启用/禁用。
func (m *Manager) SetEnabled(id string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.list {
		if m.list[i].ID == id {
			m.list[i].Enabled = enabled
			return m.saveLocked()
		}
	}
	return fmt.Errorf("渠道不存在: %s", id)
}

// Delete 物理删除（API 层负责检查是否被角色引用）。
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.list[:0]
	found := false
	for _, ch := range m.list {
		if ch.ID == id {
			found = true
			continue
		}
		out = append(out, ch)
	}
	if !found {
		return fmt.Errorf("渠道不存在: %s", id)
	}
	m.list = out
	return m.saveLocked()
}

// ForDomain 某域可用的渠道（按 adapter 能力判断，不只看 Channel.Domain）。
func (m *Manager) ForDomain(d Domain, reg *Registry) []Channel {
	all := m.List()
	out := make([]Channel, 0, len(all))
	for _, ch := range all {
		a, ok := reg.Get(ch.AdapterID)
		if !ok {
			continue
		}
		if a.Info().SupportsDomain(d) {
			out = append(out, ch)
		}
	}
	return out
}

func (m *Manager) hasAdapterLocked(adapter string) bool {
	for _, ch := range m.list {
		if strings.EqualFold(ch.AdapterID, adapter) {
			return true
		}
	}
	return false
}

func (m *Manager) hasIDLocked(id string) bool {
	return m.indexOfIDLocked(id) >= 0
}

// indexOfIDLocked 返回渠道下标，不存在返回 -1（调用方需持锁）。
func (m *Manager) indexOfIDLocked(id string) int {
	for i := range m.list {
		if m.list[i].ID == id {
			return i
		}
	}
	return -1
}

// PickDomain 取适配器能承载的 domain：优先期望值，不支持时退化为第一个可用域。
// want 传空表示不指定，直接取第一个可用域。第二个返回值为 false 表示适配器
// 没有声明任何 feed/live 域，调用方应跳过而不是写入无效渠道。
func PickDomain(info Info, want Domain) (Domain, bool) {
	if want != "" && info.SupportsDomain(want) {
		return want, true
	}
	for _, d := range info.Domains {
		if d == DomainFeed || d == DomainLive {
			return d, true
		}
	}
	return "", false
}

func (m *Manager) save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

func (m *Manager) saveLocked() error {
	return m.st.Save(channelsBlob, m.list)
}
