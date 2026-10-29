package watch

import (
	"strings"
	"time"

	"twitter_read_play/internal/livechat"
	"twitter_read_play/internal/model"
)

// scanLive 角色 Stream 页：按各自间隔消费绑定的弹幕源。
// 与 Feed Watch 完全独立 —— 不看 r.Enabled、Username、Interval、FilterWords、RepeatCount。
func (m *Manager) scanLive() {
	if m.live == nil || m.roles == nil {
		return
	}
	for _, r := range m.roles.List() {
		if !r.LiveEnabled || strings.TrimSpace(r.LiveSourceID) == "" {
			continue
		}
		if len(m.enabledBindings(r)) == 0 {
			continue
		}
		iv := r.LiveInterval
		if iv <= 0 {
			iv = 1
		}
		if !r.LiveLastAt.IsZero() && time.Since(r.LiveLastAt) < time.Duration(iv)*time.Second {
			continue
		}
		m.consumeLive(r)
	}
}

func (m *Manager) consumeLive(r *model.Role) {
	srcID := strings.TrimSpace(r.LiveSourceID)
	if srcID == "" || m.live == nil {
		return
	}
	// 刚打开或刚换源（LiveLastAt 为零）：丢掉积压，从当前最新接着听。
	if r.LiveLastAt.IsZero() {
		m.saveLiveCursor(r.ID, m.live.LatestSeq(srcID))
		return
	}
	msgs, _ := m.live.Messages(srcID, r.LiveLastSeq, 0, 0)
	latest := r.LiveLastSeq
	var pick *livechat.Message
	for i := range msgs {
		if msgs[i].Seq > latest {
			latest = msgs[i].Seq
		}
		if livePass(r, msgs[i]) {
			pick = &msgs[i]
		}
	}
	m.saveLiveCursor(r.ID, latest)
	if pick == nil {
		return
	}
	tw := model.NewLiveTweet(pick.User, pick.Text, pick.Amount, pick.Source, pick.Seq)
	m.enqueueItems(r, tw, m.enabledBindings(r), "live")
}

func (m *Manager) saveLiveCursor(id string, seq int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.roles.Get(id)
	if err != nil {
		return
	}
	r.LiveLastSeq = seq
	r.LiveLastAt = time.Now()
	_ = m.roles.Upsert(r)
}

// liveFilter 角色的 Stream 过滤配置 → 通用 livechat.Filter。
// 判定全部收敛在 livechat.Filter（跨平台同一套规则），这里只做字段映射。
func liveFilter(r *model.Role) livechat.Filter {
	return livechat.Filter{
		BlockUsers:   livechat.ParseWords(r.LiveBlockUsers),
		BlockWords:   livechat.ParseWords(r.LiveFilterWords),
		RequireWords: livechat.ParseWords(r.LiveRequireWords),
		OnlySpecial:  r.LiveOnlySC,
		HideSpecial:  r.LiveNoSC,
		MinLevel:     r.LiveMinLevel,
	}
}

// livePass Stream 自己的闸门：黑名单用户/黑名单词/必须词/仅SC/过滤SC/最低等级。
// 不用 Role.FilterWords / RequireWords —— 那是 Feed / Watch 的。
func livePass(r *model.Role, msg livechat.Message) bool {
	return liveFilter(r).Pass(msg)
}
