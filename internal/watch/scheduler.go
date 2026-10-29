package watch

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/source"
	"twitter_read_play/internal/twitter"
	"twitter_read_play/internal/upstream"
)

// ErrPollBusy 该角色已有一轮轮询在跑。
var ErrPollBusy = errors.New("poll already running")

// PollTracked 同步跑一轮轮询，并把 running → done/failed 记进 MessageQueue。
func (m *Manager) PollTracked(roleID string) error {
	if !m.tryPollLock(roleID) {
		return ErrPollBusy
	}
	defer m.unlockPoll(roleID)
	return m.runPoll(roleID)
}

// StartPoll 手动「立即轮询」：先登记 running 再返回，拉取在后台进行。
func (m *Manager) StartPoll(roleID string) error {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return err
	}
	if !m.tryPollLock(roleID) {
		return ErrPollBusy
	}
	op := m.openPollOp(r)
	go func() {
		defer m.unlockPoll(roleID)
		res, perr := m.PollOnce(roleID)
		m.finishPollOp(op, res, perr)
	}()
	return nil
}

func (m *Manager) runPoll(roleID string) error {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return err
	}
	op := m.openPollOp(r)
	res, perr := m.PollOnce(roleID)
	m.finishPollOp(op, res, perr)
	return perr
}

func (m *Manager) openPollOp(r *model.Role) *OpItem {
	if r == nil {
		return nil
	}
	machineID, slot := "", 0
	if m.machines != nil {
		if bs := m.enabledBindings(r); len(bs) > 0 {
			machineID, slot = bs[0].MachineID, bs[0].Slot
		}
	}
	return m.BeginOp("poll", r, machineID, slot)
}

func (m *Manager) finishPollOp(op *OpItem, res PollResult, err error) {
	if op == nil {
		return
	}
	if err != nil {
		m.FinishOp(op.ID, err.Error(), "", 0)
		return
	}
	m.FinishOp(op.ID, "", res.Code, res.Count)
}

func (m *Manager) tryPollLock(roleID string) bool {
	m.pollMu.Lock()
	defer m.pollMu.Unlock()
	if m.pollBusy == nil {
		m.pollBusy = map[string]bool{}
	}
	if m.pollBusy[roleID] {
		return false
	}
	m.pollBusy[roleID] = true
	return true
}

func (m *Manager) unlockPoll(roleID string) {
	m.pollMu.Lock()
	delete(m.pollBusy, roleID)
	m.pollMu.Unlock()
}

func (m *Manager) scan() {
	// 共享 search monitor 生效时，其覆盖的用户全部停止轮询（由 webhook 推送）
	searchQuery, _ := m.st.GetSetting("socialdata_search_query")
	searchID, _ := m.st.GetSetting("socialdata_search_monitor_id")
	searchActive := searchID != "" && searchQuery != ""
	searchUsers := map[string]bool{}
	if searchActive {
		for _, u := range strings.Split(searchQuery, ",") {
			searchUsers[strings.ToLower(u)] = true
		}
	}
	for _, r := range m.roles.List() {
		if !m.effectivelyEnabled(r) {
			continue
		}
		// 自动随机播报：与「新推文到达」无关，只依赖本地缓存的勾选池，
		// 所以放在 dead / webhook / callback 这些「停止轮询」判断之前 —— 上游挂了也该能播缓存里的。
		m.maybeRandomMode(r.ID)
		m.maybeParodyMode(r.ID)
		// 上游账号级故障（欠费/Key 无效）已标记 dead：停止自动轮询，等用户修复后手动恢复
		if r.Dead {
			continue
		}
		// webhook 监控模式：由 socialdata.tools 反向推送，不再轮询
		if r.MonitorID != "" {
			continue
		}
		// 角色选择 callback 模式：交给反向通知（search monitor 覆盖或独立监控）
		if r.MonitorWay == "callback" {
			continue
		}
		// 共享 search monitor 只覆盖 twitter 用户：微博/小红书角色的同名账号不能被它跳过轮询
		if searchActive && normalizePlatform(r.Platform) == "twitter" && searchUsers[strings.ToLower(r.Username)] {
			continue
		}
		interval := time.Duration(r.Interval) * time.Second
		// 上游配置的刷新时间优先
		if _, up, err := m.providerForRole(r); err == nil && up != nil && up.RefreshSeconds > 0 {
			interval = time.Duration(up.RefreshSeconds) * time.Second
		}
		if interval <= 0 {
			interval = defaultInterval * time.Second
		}
		if !r.LastPollAt.IsZero() && time.Since(r.LastPollAt) < interval {
			continue
		}
		if err := m.PollTracked(r.ID); err != nil && !errors.Is(err, ErrPollBusy) {
			log.Printf("[watch] %s fetch failed: %v", r.Username, err)
		}
	}
}

func (m *Manager) saveRolePollState(id, lastTweetID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.roles.Get(id)
	if err != nil {
		return
	}
	r.LastTweetID = lastTweetID
	r.LastPollAt = time.Now()
	_ = m.roles.Upsert(r)
}

// markDead 上游账号级故障：置位 dead 并记录原因（幂等，只记一次时间）。
func (m *Manager) markDead(id, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.roles.Get(id)
	if err != nil {
		return
	}
	if r.Dead {
		return
	}
	r.Dead = true
	r.DeadReason = reason
	r.DeadAt = time.Now()
	_ = m.roles.Upsert(r)
	log.Printf("[watch] role %s(%s) marked dead: %s", r.Name, id, reason)
}

// clearDead 解除 dead 标记（上游恢复后）。
func (m *Manager) clearDead(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.roles.Get(id)
	if err != nil || !r.Dead {
		return
	}
	r.Dead = false
	r.DeadReason = ""
	r.DeadAt = time.Time{}
	_ = m.roles.Upsert(r)
	log.Printf("[watch] role %s(%s) dead cleared", r.Name, id)
}

// providerForRole 解析角色应使用的 Provider 与其上游（刷新时间等）。
// 新模型优先走 source.Resolver；旧角色（没有 FeedTarget/FeedChannelID/UpstreamID）回退旧解析。
func (m *Manager) providerForRole(r *model.Role) (twitter.Provider, *model.Upstream, error) {
	if m.channels != nil && m.registry != nil {
		p, up, err := m.sourceProviderForRole(r)
		if err == nil {
			return p, up, nil
		}
		if strings.TrimSpace(r.FeedTarget) != "" || strings.TrimSpace(r.FeedChannelID) != "" || strings.TrimSpace(r.UpstreamID) != "" {
			return nil, nil, err
		}
		log.Printf("[watch] role %s(%s) source resolve failed, fallback legacy: %v", r.Name, r.ID, err)
	}
	return m.legacyProviderForRole(r)
}

func (m *Manager) sourceProviderForRole(r *model.Role) (twitter.Provider, *model.Upstream, error) {
	target, err := roleFeedTarget(r)
	if err != nil {
		return nil, nil, err
	}
	channels := m.channels.ForDomain(source.DomainFeed, m.registry)
	res, err := m.registry.Resolve(source.ResolveRequest{
		Domain:    source.DomainFeed,
		Target:    target,
		ChannelID: firstNonEmpty(r.FeedChannelID, r.UpstreamID),
		Mode:      m.mode,
	}, channels)
	if err != nil {
		return nil, nil, err
	}
	fa, ok := source.AsFeed(res.Adapter)
	if !ok {
		return nil, nil, fmt.Errorf("适配器 %s 不支持 Feed", res.Channel.AdapterID)
	}
	w, err := fa.OpenFeed(res.Channel, target)
	if err != nil {
		return nil, nil, err
	}
	up := &model.Upstream{
		ID: res.Channel.ID, Name: res.Channel.Name, API: res.Channel.AdapterID,
		Platform: string(target.Platform), RefreshSeconds: res.Channel.RefreshSeconds,
	}
	return w, up, nil
}

func roleFeedTarget(r *model.Role) (source.Target, error) {
	if raw := strings.TrimSpace(r.FeedTarget); raw != "" {
		t, err := source.ParseFeed(raw, source.Platform(r.Platform))
		if err != nil {
			return source.Target{}, err
		}
		if err := source.ValidatePlatform(r.Platform, t); err != nil {
			return source.Target{}, err
		}
		return t, nil
	}
	username := strings.TrimSpace(r.Username)
	if username == "" {
		return source.Target{}, fmt.Errorf("角色未配置 Feed 目标（feed_target / username 为空）")
	}
	p := source.NormalizePlatform(r.Platform)
	if p == "" {
		p = source.PlatformX
	}
	kind := source.KindProfile
	if p == source.PlatformRSS {
		kind = source.KindFeedURL
	}
	return source.Target{
		Domain: source.DomainFeed, Platform: p, Kind: kind,
		Raw: username, ID: strings.TrimPrefix(username, "@"),
	}, nil
}

func (m *Manager) legacyProviderForRole(r *model.Role) (twitter.Provider, *model.Upstream, error) {
	fallback, _ := m.st.GetSetting("socialdata_api_key")
	if r.UpstreamID != "" {
		if u, err := m.upstreams.Get(r.UpstreamID); err == nil {
			p, err := upstream.ProviderFor(u, fallback)
			return p, u, err
		}
	}
	return m.upstreams.ProviderForRole(r.Platform, fallback)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
