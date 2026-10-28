package source

import (
	"sort"
	"sync"
)

// Registry 适配器注册表。
type Registry struct {
	mu   sync.RWMutex
	list []Adapter
	byID map[string]Adapter
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{byID: map[string]Adapter{}}
}

// Register 注册适配器（ID 必须唯一）。
func (r *Registry) Register(a Adapter) error {
	if a == nil {
		return &DuplicateAdapterError{ID: "<nil>"}
	}
	info := a.Info()
	if info.ID == "" {
		return &DuplicateAdapterError{ID: "<empty>"}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[info.ID]; ok {
		return &DuplicateAdapterError{ID: info.ID}
	}
	r.byID[info.ID] = a
	r.list = append(r.list, a)
	return nil
}

// Get 按 ID 取适配器。
func (r *Registry) Get(id string) (Adapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.byID[id]
	return a, ok
}

// List 全部适配器（注册顺序）。
func (r *Registry) List() []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Adapter, len(r.list))
	copy(out, r.list)
	return out
}

// Infos 全部元信息（前端下拉用）。
func (r *Registry) Infos() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.list))
	for _, a := range r.list {
		out = append(out, a.Info())
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ForDomain 某域可用适配器。
func (r *Registry) ForDomain(d Domain) []Adapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Adapter, 0, len(r.list))
	for _, a := range r.list {
		if a.Info().SupportsDomain(d) {
			out = append(out, a)
		}
	}
	return out
}

// BaseMatch 通用能力匹配（目标→适配器）。适配器可在自己的 Match 里先调用它。
// 运行模式不在这里判断：模式由 CanChannelServe 统一检查。
func BaseMatch(info Info, t Target) Match {
	if !info.SupportsDomain(t.Domain) {
		return reject("domain 不匹配（适配器不支持 " + string(t.Domain) + "）")
	}
	if !info.SupportsPlatform(t.Platform) {
		return reject("平台不匹配（适配器不支持 " + string(t.Platform) + "）")
	}
	if !info.SupportsKind(t.Platform, t.Kind) {
		return reject("target kind 不支持（" + string(t.Platform) + " 需要 " + string(t.Kind) + "）")
	}
	return okMatch(100)
}

func modeList(modes []Mode) string {
	if len(modes) == 0 {
		return "全部"
	}
	out := ""
	for i, m := range modes {
		if i > 0 {
			out += " / "
		}
		out += string(m)
	}
	return out
}

// CanChannelServe 渠道→目标（凭据就绪），等价于 fetch 意图。
func CanChannelServe(ch Channel, a Adapter, t Target, mode Mode) Match {
	return CanChannelServeIntent(ch, a, t, mode, IntentFetch)
}

// CanChannelServeIntent 渠道→目标（凭据就绪 + 本次意图）：
//   - 意图能力：fetch 需要 Feed/Live Worker；inspect 需要 AccountInspector；
//   - 渠道启用；
//   - 适配器能力匹配（方向 A）；
//   - 必填凭据齐全（Needs AND）；
//   - 备选凭据至少满足一组（AnyOf OR）。
func CanChannelServeIntent(ch Channel, a Adapter, t Target, mode Mode, intent Intent) Match {
	if intent == IntentInspect {
		if _, ok := AsInspector(a); !ok {
			return reject("适配器不支持账号详情识别")
		}
	} else {
		switch t.Domain {
		case DomainFeed:
			if _, ok := AsFeed(a); !ok {
				return reject("适配器不支持 Feed 拉取")
			}
		case DomainLive:
			if _, ok := AsLive(a); !ok {
				return reject("适配器不支持 Live 拉取")
			}
		}
	}
	if !ch.Enabled {
		return reject("渠道已禁用")
	}
	m := a.Match(t)
	if !m.OK {
		return m
	}
	info := a.Info()
	if !info.SupportsMode(mode) {
		return reject("仅支持 " + modeList(info.Modes) + " 模式")
	}
	var missing []CredentialKey
	for _, need := range info.NeedsFor(t.Platform) {
		if !ch.Credentials.Has(need) {
			missing = append(missing, need)
		}
	}
	if len(missing) == 0 {
		groups := info.AnyOfFor(t.Platform)
		if len(groups) > 0 {
			satisfied := false
			var best []CredentialKey
			for _, group := range groups {
				var gm []CredentialKey
				for _, need := range group {
					if !ch.Credentials.Has(need) {
						gm = append(gm, need)
					}
				}
				if len(gm) == 0 {
					satisfied = true
					break
				}
				if best == nil || len(gm) < len(best) {
					best = gm
				}
			}
			if !satisfied {
				missing = best
			}
		}
	}
	if len(missing) > 0 {
		return rejectMissing("缺少凭据: "+joinCreds(missing), missing)
	}
	return Match{OK: true, Score: m.Score}
}

// CheckCredentials 检查渠道凭据是否齐全（UI 的「就绪」徽标用）。
// 对多平台适配器取并集：任一平台缺少的凭据都会列出（用于提示，不参与 Resolver）。
func CheckCredentials(ch Channel, a Adapter) []CredentialKey {
	info := a.Info()
	var missing []CredentialKey
	add := func(k CredentialKey) {
		for _, v := range missing {
			if v == k {
				return
			}
		}
		missing = append(missing, k)
	}
	for _, p := range info.Platforms {
		for _, need := range info.NeedsFor(p) {
			if !ch.Credentials.Has(need) {
				add(need)
			}
		}
		groups := info.AnyOfFor(p)
		if len(groups) == 0 {
			continue
		}
		ok := false
		for _, group := range groups {
			all := true
			for _, need := range group {
				if !ch.Credentials.Has(need) {
					all = false
					break
				}
			}
			if all {
				ok = true
				break
			}
		}
		if !ok {
			for _, need := range groups[0] {
				if !ch.Credentials.Has(need) {
					add(need)
				}
			}
		}
	}
	return missing
}

// ResolveRequest 解析请求。
type ResolveRequest struct {
	Domain    Domain
	Target    Target
	ChannelID string
	Mode      Mode
	Intent    Intent // 空 = IntentFetch
	// MaxCandidates 自动模式下最多返回多少个候选（0 = 不限制）。
	// 显式 ChannelID 时只可能返回 1 个。
	MaxCandidates int
}

// Candidates 返回按既有优先级排序的全部通过者，供识别降级链使用。
// 无候选时返回 *NoMatchError 与逐项拒绝原因。
func (r *Registry) Candidates(req ResolveRequest, channels []Channel) ([]Resolved, []Rejection, error) {
	if req.Target.IsZero() {
		return nil, nil, &UnsupportedTargetError{Domain: req.Domain}
	}
	if req.Domain != "" && req.Target.Domain != "" && req.Domain != req.Target.Domain {
		return nil, nil, &UnsupportedTargetError{
			Domain: req.Domain, Raw: req.Target.Raw,
			Supported: []string{"feed 目标不能用于 live，反之亦然"},
		}
	}
	intent := req.Intent
	if intent == "" {
		intent = IntentFetch
	}

	if id := req.ChannelID; id != "" {
		for _, ch := range channels {
			if ch.ID != id {
				continue
			}
			a, ok := r.Get(ch.AdapterID)
			if !ok {
				rej := Rejection{ChannelID: ch.ID, AdapterID: ch.AdapterID, Reason: "适配器不存在"}
				return nil, []Rejection{rej},
					&NoMatchError{Target: req.Target, Mode: req.Mode, Rejected: []Rejection{rej}}
			}
			m := CanChannelServeIntent(ch, a, req.Target, req.Mode, intent)
			if !m.OK {
				rej := Rejection{ChannelID: ch.ID, AdapterID: ch.AdapterID, Reason: m.Reason, Missing: m.Missing}
				return nil, []Rejection{rej},
					&NoMatchError{Target: req.Target, Mode: req.Mode, Rejected: []Rejection{rej}}
			}
			return []Resolved{{
				Channel: ch, Adapter: a, Target: req.Target, Score: m.Score,
			}}, nil, nil
		}
		return nil, nil, &ChannelNotFoundError{ID: id}
	}

	type candidate struct {
		ch Channel
		a  Adapter
		m  Match
	}
	var cands []candidate
	var rejected []Rejection
	for _, ch := range channels {
		a, ok := r.Get(ch.AdapterID)
		if !ok {
			rejected = append(rejected, Rejection{ChannelID: ch.ID, AdapterID: ch.AdapterID, Reason: "适配器不存在"})
			continue
		}
		m := CanChannelServeIntent(ch, a, req.Target, req.Mode, intent)
		if !m.OK {
			rejected = append(rejected, Rejection{ChannelID: ch.ID, AdapterID: ch.AdapterID, Reason: m.Reason, Missing: m.Missing})
			continue
		}
		cands = append(cands, candidate{ch: ch, a: a, m: m})
	}
	if len(cands) == 0 {
		return nil, rejected, &NoMatchError{Target: req.Target, Mode: req.Mode, Rejected: rejected}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		si, sj := candidateScore(cands[i].a, cands[i].ch, cands[i].m), candidateScore(cands[j].a, cands[j].ch, cands[j].m)
		if si != sj {
			return si > sj
		}
		if !cands[i].ch.CreatedAt.Equal(cands[j].ch.CreatedAt) {
			return cands[i].ch.CreatedAt.Before(cands[j].ch.CreatedAt)
		}
		return cands[i].ch.ID < cands[j].ch.ID
	})
	max := req.MaxCandidates
	if max > 0 && len(cands) > max {
		cands = cands[:max]
	}
	out := make([]Resolved, 0, len(cands))
	for _, c := range cands {
		out = append(out, Resolved{
			Channel: c.ch, Adapter: c.a, Target: req.Target,
			Score: c.m.Score, Rejected: rejected,
		})
	}
	return out, rejected, nil
}

// Resolve 选择渠道：
//  1. 显式 ChannelID：只检查它；
//  2. 自动：遍历启用渠道，A ∧ B（∧ 意图能力）全通过者进入候选；
//  3. 排序：适配器优先级 → 渠道 Sort → 创建时间 → ID；
//  4. 无候选：返回 *NoMatchError（带逐项拒绝原因）。
func (r *Registry) Resolve(req ResolveRequest, channels []Channel) (Resolved, error) {
	cands, rejected, err := r.Candidates(req, channels)
	if err != nil {
		return Resolved{Target: req.Target, Rejected: rejected}, err
	}
	return cands[0], nil
}

func candidateScore(a Adapter, ch Channel, m Match) int {
	p := a.Info().Priority
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	score := (100-p)*1000 - ch.Sort + m.Score
	// 内置浏览器登录渠道默认优先（产品约定：默认用内置浏览器登录方式拉取，
	// 免 API Key；桌面登录态/游客态即可）。显式指定渠道时走上面的直通分支，不受此加分影响。
	if ch.Credentials.Has(CredDesktopSession) {
		score += 20000
	}
	return score
}

// ValidatePlatform 校验 Role 平台与目标平台是否一致（空 = 不校验）。
func ValidatePlatform(rolePlatform string, t Target) error {
	rp := NormalizePlatform(rolePlatform)
	if rp == "" {
		return nil
	}
	if rp != NormalizePlatform(string(t.Platform)) {
		return &PlatformConflictError{Role: rp, Target: t.Platform}
	}
	return nil
}
