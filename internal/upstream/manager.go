// Package upstream 上游数据源实体管理 + 按 API 构建推文 Provider。
//
// 上游(Upstream) = 一个可用的数据源实例（某 API + 凭据 + base_url + 刷新时间）。
// API = 内置适配器：socialdata(twitter) / socialread(微博/小红书) / rss / weibo / truth。
package upstream

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
	"twitter_read_play/internal/twitter"
)

// APIInfo 内置 API 适配器元信息。
type APIInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Platforms  []string `json:"platforms"`
	NeedKey    bool     `json:"need_key"`
	NeedBase   bool     `json:"need_base"`
	NeedCookie bool     `json:"need_cookie"`
	DefaultURL string   `json:"default_url,omitempty"`
	Hint       string   `json:"hint,omitempty"`
}

// APIs 内置 API 注册表。
var APIs = []APIInfo{
	{ID: "socialdata", Name: "socialdata.tools", Platforms: []string{"twitter"}, NeedKey: true,
		DefaultURL: "https://api.socialdata.tools", Hint: "按量计费 $0.2/1k 条，socialdata.tools 充值"},
	// twitter-read / social_read：统一格式（Bearer 鉴权）。生产把 base 指到 twitter-read 即可同时拉微博/小红书/B 站。
	{ID: "socialread", Name: "twitter-read（微博/小红书/B站）", Platforms: []string{"weibo", "xiaohongshu", "bilibili"}, NeedKey: true, NeedBase: true,
		DefaultURL: twitter.DefaultSocialReadBaseURL, Hint: "twitter-read 统一社媒读取；Authorization: Bearer <key>。平台：微博 / 小红书 / B 站"},
	{ID: "xtoken", Name: "X 登录 token", Platforms: []string{"twitter"}, NeedKey: true,
		Hint: "填 auth_token（浏览器 Cookie）。服务端用它直接调 x.com 接口，不等 Chrome 打开页面。ct0 可写在 Cookie 栏，不写会自动取"},
	{ID: "twitter_official", Name: "X 官方 API v2", Platforms: []string{"twitter"}, NeedKey: true,
		DefaultURL: "https://api.twitter.com", Hint: "免费档无读取权限，Basic $200/月起"},
	{ID: "rss", Name: "RSS / Atom", Platforms: []string{"rss"}, NeedBase: true,
		DefaultURL: "", Hint: "任意 RSS/Atom 源；推荐自建 RSSHub：/twitter/user/{id} 等路由"},
	{ID: "weibo", Name: "微博", Platforms: []string{"weibo"}, NeedCookie: true,
		DefaultURL: "https://m.weibo.cn", Hint: "m.weibo.cn container 接口；需登录 Cookie（否则 432 反爬）"},
	{ID: "truth", Name: "Truth Social", Platforms: []string{"truth"}, NeedBase: true,
		DefaultURL: "https://truthsocial.com", Hint: "Mastodon 风格 /api/v1；国内直连需反代 base_url"},
	{ID: "bluesky", Name: "Bluesky", Platforms: []string{"bluesky"},
		DefaultURL: "https://public.api.bsky.app", Hint: "公开 AppView API，无需登录/Key；可填自建 appview"},
}

func APIInfoByID(id string) *APIInfo {
	for i := range APIs {
		if APIs[i].ID == id {
			return &APIs[i]
		}
	}
	return nil
}

type Manager struct {
	mu   sync.Mutex
	st   *store.Store
	list []*model.Upstream
	// defaultBaseURL socialdata 兜底上游的 API 地址（[api] base_url 配置）。
	// 空 = 官方 https://api.socialdata.tools；可指向自建兼容服务。
	defaultBaseURL string
}

func NewManager(st *store.Store) (*Manager, error) {
	m := &Manager{st: st}
	var list []*model.Upstream
	if err := st.Load("upstreams", &list); err != nil {
		return nil, err
	}
	m.list = list
	// 没配过任何上游也要落一份默认 RSS 适配器：角色 Watch / Settings 才能选到。
	if err := m.EnsureDefaults(); err != nil {
		return nil, err
	}
	return m, nil
}

const defaultRSSUpstreamID = "u-rss-default"

// EnsureDefaults SQLite 默认迁移：即使用户从没打开过 Settings，也写入 RSS 上游。
// 幂等：已有任意 rss 上游则不动（尊重用户删掉/改过的配置）。
func (m *Manager) EnsureDefaults() error {
	for _, u := range m.List() {
		if u != nil && strings.EqualFold(u.API, "rss") {
			return nil
		}
	}
	return m.Upsert(&model.Upstream{
		ID:             defaultRSSUpstreamID,
		Name:           "RSS / Atom",
		API:            "rss",
		Platform:       "rss",
		Enabled:        true,
		RefreshSeconds: 300,
	})
}

func (m *Manager) save() error {
	return m.st.Save("upstreams", m.list)
}

func (m *Manager) List() []*model.Upstream {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*model.Upstream, len(m.list))
	copy(out, m.list)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (m *Manager) Get(id string) (*model.Upstream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.list {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, fmt.Errorf("upstream %q 不存在", id)
}

func (m *Manager) Upsert(u *model.Upstream) error {
	m.mu.Lock()
	if u.ID == "" {
		u.ID = fmt.Sprintf("u-%d", time.Now().UnixNano())
	}
	if u.CreatedAt.IsZero() {
		for _, old := range m.list {
			if old.ID == u.ID {
				u.CreatedAt = old.CreatedAt
				break
			}
		}
		if u.CreatedAt.IsZero() {
			u.CreatedAt = time.Now()
		}
	}
	found := false
	for i, old := range m.list {
		if old.ID == u.ID {
			m.list[i] = u
			found = true
			break
		}
	}
	if !found {
		m.list = append(m.list, u)
	}
	m.mu.Unlock()
	return m.save()
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	out := m.list[:0]
	for _, u := range m.list {
		if u.ID != id {
			out = append(out, u)
		}
	}
	m.list = out
	m.mu.Unlock()
	return m.save()
}

// SetDefaultBaseURL 设置 socialdata 兜底上游的 API 地址（来自设置页 [api] base_url）。
func (m *Manager) SetDefaultBaseURL(u string) {
	u = strings.TrimSuffix(strings.TrimSpace(u), "/")
	m.mu.Lock()
	m.defaultBaseURL = u
	m.mu.Unlock()
	SetDefaultBaseURLValue(u) // 包级默认值：ProviderFor 走的是这条
}

// DefaultBaseURL 取当前兜底 API 地址。
func (m *Manager) DefaultBaseURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultBaseURL
}

// defaultBaseURL 设置页 socialdata_base_url 的当前值。
// ProviderFor 是包级函数（拿不到 Manager），所以另存一份包级默认值给它兜底。
var defaultBaseURL atomic.Value // string

// SetDefaultBaseURLValue 供 Manager.SetDefaultBaseURL 同步包级默认值。
func SetDefaultBaseURLValue(u string) {
	defaultBaseURL.Store(strings.TrimSuffix(strings.TrimSpace(u), "/"))
}

func defaultBaseURLValue() string {
	if v, ok := defaultBaseURL.Load().(string); ok {
		return v
	}
	return ""
}

// ProviderFor 按 API 类型构建推文 Provider。
func ProviderFor(u *model.Upstream, fallbackTwitterKey string) (twitter.Provider, error) {
	if u == nil {
		return nil, fmt.Errorf("upstream 为空")
	}
	switch u.API {
	case "socialdata":
		key := u.APIKey
		if key == "" {
			key = fallbackTwitterKey
		}
		// 上游没写地址时回落到设置页的 socialdata_base_url（DefaultBaseURL）：
		// 否则「设置页改了 base_url、twitter 却仍打官方 api.socialdata.tools」——
		// 因为这条路径原来只认上游自己的 base_url。
		return &twitter.Socialdata{APIKey: key, BaseURL: orDefault(u.BaseURL, defaultBaseURLValue())}, nil
	case "socialread":
		// 自建 social_read：平台由上游自己声明（weibo/xiaohongshu），key 缺省回落到设置页的
		// socialdata_api_key，地址缺省回落到服务默认地址（socialdata_base_url 指向的是 socialdata
		// 兼容服务，没有 /weibo /xiaohongshu 路由，不能拿来兜底）。
		key := u.APIKey
		if key == "" {
			key = fallbackTwitterKey
		}
		return &twitter.SocialRead{
			Platform: u.Platform,
			BaseURL:  orDefault(u.BaseURL, twitter.DefaultSocialReadBaseURL),
			APIKey:   key,
		}, nil
	case "xtoken":
		return &twitter.XToken{AuthToken: u.APIKey, CT0: u.Cookie}, nil
	case "twitter_official":
		return &twitter.TwitterOfficial{Bearer: u.APIKey, BaseURL: orDefault(u.BaseURL, "https://api.twitter.com")}, nil
	case "rss":
		return &twitter.RSSFeed{BaseURL: orDefault(u.BaseURL, u.Extra)}, nil
	case "weibo":
		return &twitter.Weibo{BaseURL: orDefault(u.BaseURL, "https://m.weibo.cn"), Cookie: u.Cookie}, nil
	case "truth":
		return &twitter.TruthSocial{BaseURL: orDefault(u.BaseURL, "https://truthsocial.com")}, nil
	case "bluesky":
		return &twitter.Bluesky{BaseURL: orDefault(u.BaseURL, "https://public.api.bsky.app")}, nil
	default:
		return nil, fmt.Errorf("未知 API: %s", u.API)
	}
}

// ProviderForRole 解析角色应使用的 Provider：第一个启用且平台匹配的上游。
// 角色显式指定 upstream_id 的路径在 watch/scheduler.go（直接用 ProviderFor）。
func (m *Manager) ProviderForRole(platform string, fallbackTwitterKey string) (twitter.Provider, *model.Upstream, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "x" { // 历史值：x 与 twitter 等价
		platform = "twitter"
	}
	var candidates []*model.Upstream
	for _, u := range m.List() {
		if !u.Enabled {
			continue
		}
		if !upstreamMatchesPlatform(u, platform) {
			continue
		}
		candidates = append(candidates, u)
	}
	if len(candidates) == 0 {
		// 兜底：settings 里的 socialdata key
		if platform == "" || platform == "twitter" {
			if fallbackTwitterKey != "" {
				return &twitter.Socialdata{APIKey: fallbackTwitterKey, BaseURL: m.DefaultBaseURL()}, nil, nil
			}
		}
		return nil, nil, fmt.Errorf("平台 %q 没有可用的上游数据源，请在设置中添加", platform)
	}
	p, err := ProviderFor(candidates[0], fallbackTwitterKey)
	return p, candidates[0], err
}

// upstreamMatchesPlatform 上游是否适配该平台。
// 平台为空时的兜底口子收窄了：不再把 socialread（微博/小红书专用）算进来，
// 否则一个没写 platform 的老角色（=twitter）会误用微博上游。
func upstreamMatchesPlatform(u *model.Upstream, platform string) bool {
	if platform == "" {
		return u.API != "socialread"
	}
	if strings.EqualFold(strings.TrimSpace(u.Platform), platform) {
		return true
	}
	// 未标平台的老上游仍按旧行为参与兜底（socialread 除外：它的平台在上游自己身上）
	return strings.TrimSpace(u.Platform) == "" && u.API != "socialread"
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
