package livechat

import (
	"strings"
)

// Filter 弹幕通用过滤器：黑名单用户 / 黑名单词 / 必须包含词 / 仅SC / 过滤SC / 最低用户等级。
// 跨平台通用 —— 所有弹幕来源（twitch / youtube / bilibili / browser 适配）共用这一套判定，
// 调用方（Stream Watch 播报、前端预览）各自用自己的配置构造 Filter。
//
// 等级条件只在消息真的带等级时生效（UserLevel>0）：不给等级的平台（Twitch/YouTube）
// 弹幕 UserLevel 恒为 0，此时等级条件自动放行，绝不会把整路弹幕滤没。
type Filter struct {
	BlockUsers   []string // 黑名单用户：用户名精确匹配（忽略大小写）
	BlockWords   []string // 黑名单词：用户名+正文+金额任一命中即滤掉（子串，忽略大小写）
	RequireWords []string // 必须包含词：非空时命中任一才放行
	OnlySpecial  bool     // 仅 SC/礼物/舰长（打赏类）
	HideSpecial  bool     // 过滤 SC/礼物/舰长（与 OnlySpecial 相对；都开 = 全滤，调用方应避免）
	MinLevel     int      // 最低用户等级（仅对带等级的平台生效）
}

// Pass 单条弹幕是否放行。
func (f Filter) Pass(msg Message) bool {
	// 1) 黑名单用户（最优先：拉黑的人说什么都不播）
	u := strings.ToLower(strings.TrimSpace(msg.User))
	for _, b := range f.BlockUsers {
		if b = strings.ToLower(strings.TrimSpace(b)); b != "" && b == u {
			return false
		}
	}
	// 2) 仅SC / 过滤SC
	special := isSpecial(msg)
	if f.OnlySpecial && !special {
		return false
	}
	if f.HideSpecial && special {
		return false
	}
	// 3) 最低用户等级（0 = 平台不提供 → 放行）
	if f.MinLevel > 0 && msg.UserLevel > 0 && msg.UserLevel < f.MinLevel {
		return false
	}
	hay := strings.ToLower(msg.User + " " + msg.Text + " " + msg.Amount)
	// 4) 黑名单词
	for _, w := range f.BlockWords {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" && strings.Contains(hay, w) {
			return false
		}
	}
	// 5) 必须包含词（非空时命中任一才放行；空正文且非打赏的直接滤掉）
	if len(f.RequireWords) == 0 {
		return strings.TrimSpace(msg.Text) != "" || special
	}
	for _, w := range f.RequireWords {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" && strings.Contains(hay, w) {
			return true
		}
	}
	return false
}

// isSpecial 打赏类消息（SC / 礼物 / 舰长 / cheer / sub / 订阅）。
func isSpecial(msg Message) bool {
	if strings.TrimSpace(msg.Amount) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(msg.Kind)) {
	case "superchat", "cheer", "gift", "sub", "subscription", "member":
		return true
	}
	return false
}

// ParseWords 分词：逗号 / 空白 / 换行分隔，去空去重（保留原大小写，匹配时再统一小写）。
func ParseWords(s string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '，' || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ';' || r == '；'
	}) {
		w := strings.TrimSpace(raw)
		if w == "" || seen[strings.ToLower(w)] {
			continue
		}
		seen[strings.ToLower(w)] = true
		out = append(out, w)
	}
	return out
}

// Capabilities 一类弹幕来源的能力（前端据此显示/隐藏对应的过滤设置项：
// 平台不支持的就不展示，例如 Twitch/YouTube 没有用户等级）。
type Capabilities struct {
	UserLevel bool `json:"user_level"` // 消息带用户等级（bilibili）
}

// CapabilitiesFor 按 kind / adapter id 判能力。kind 或 adapter id 含 "bilibili"
// 即认为带用户等级（blivedm 与内置浏览器两条 B 站链路都透传 UL 等级）。
func CapabilitiesFor(kindOrAdapter string) Capabilities {
	k := strings.ToLower(kindOrAdapter)
	return Capabilities{UserLevel: strings.Contains(k, "bilibili")}
}
