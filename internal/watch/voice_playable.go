package watch

import (
	"context"
	"encoding/json"
	"log"

	"twitter_read_play/internal/model"

	clt01 "workshop_inner_sdk"
)

// playableVoice 播报音色兜底。group_key / voice_id 是**每台机器自己的命名空间**
// （A 机是 sg_tts_en_group，B 机可能是 jp_tts_en_group；voice_id 编号也各不相同），
// 跨机器混用必然打在设备上不存在的组上 → 设备 TTS 无渠道 → 恒定数秒后 ERROR。
// 所以播报前必须按「当前这台设备」的分组表校验：
//  1. 绑定的 group_key 在这台设备上不存在/没有可用音色 → 重映射：
//     优先槽位当前音色（其组确实存在时）→ 组里登记了同号 voice 就沿用 → 任一可用组；
//  2. minimax 分组照旧不直接用（克隆不支持、tts 合成时好时坏）。
func (m *Manager) playableVoice(ctx context.Context, c *clt01.Client, r *model.Role, b model.Binding, voiceID, groupKey string) (string, string) {
	// 这台设备当前的实际分组表（拉不到 = nil，保持原样由 PlayChat 报错，不瞎猜）
	var groups []devVoiceGroup
	if raw, err := c.VoiceGroup(ctx); err == nil {
		var wrapped devVoiceGroups
		if json.Unmarshal(raw, &wrapped) == nil && len(wrapped.Groups) > 0 {
			groups = wrapped.Groups
		} else {
			var list []devVoiceGroup
			if json.Unmarshal(raw, &list) == nil {
				groups = list
			}
		}
	}
	usable := func(gk string) bool {
		for _, g := range groups {
			if g.GroupKey == gk && len(g.VoiceIDs) > 0 {
				return true
			}
		}
		return false
	}
	// 组内有没有登记这个 voice_id；有就沿用原编号，没有用该组第一个
	pick := func(g devVoiceGroup) string {
		for _, id := range g.VoiceIDs {
			if id == voiceID {
				return id
			}
		}
		return g.VoiceIDs[0]
	}

	if groups != nil && !usable(groupKey) {
		log.Printf("[watch] %s machine=%s slot=%d 绑定分组 %s 在该设备上不存在（group_key 每台机器独立，不能跨机混用），重映射",
			r.Username, b.MachineID, b.Slot, groupKey)
		// 【顺序很关键】先在可用分组里找**绑定自己的 voice_id**，找不到才退化成「槽位当前音色」。
		// 反过来的话会把漂移固化：槽位被别的角色播过（音色变成别人的编号）之后，
		// 每次重映射都把那个编号当成"本角色的音色"写回，永远纠不回来（实测 Trump 21 被鸣人
		// 的克隆 25 顶掉后，怎么 sync 都回到 25）。
		for _, preferNonMinimax := range []bool{true, false} {
			for _, g := range groups {
				if len(g.VoiceIDs) == 0 || g.minimax() != preferNonMinimax {
					continue
				}
				for _, id := range g.VoiceIDs {
					if id == voiceID && voiceID != "" {
						log.Printf("[watch] %s machine=%s slot=%d → 用该设备分组 %s 上的同名音色 %s", r.Username, b.MachineID, b.Slot, g.GroupKey, id)
						return id, g.GroupKey
					}
				}
			}
		}
		if ch, err := c.GetCharacterCtx(ctx, b.Slot); err == nil && ch.VoiceID != "" && ch.GroupKey != "" && usable(ch.GroupKey) {
			log.Printf("[watch] %s machine=%s slot=%d → 用该设备槽位音色 %s/%s", r.Username, b.MachineID, b.Slot, ch.VoiceID, ch.GroupKey)
			return ch.VoiceID, ch.GroupKey
		}
		// 任一可用分组：先非 minimax，再不限；组里登记了原 voice_id 就沿用
		for _, preferNonMinimax := range []bool{true, false} {
			for _, g := range groups {
				if len(g.VoiceIDs) == 0 || g.minimax() == preferNonMinimax {
					continue
				}
				v := pick(g)
				log.Printf("[watch] %s machine=%s slot=%d → 用该设备分组 %s 音色 %s", r.Username, b.MachineID, b.Slot, g.GroupKey, v)
				return v, g.GroupKey
			}
		}
		return voiceID, groupKey // 这台设备上也没有可用分组：保持原样，由 PlayChat 报错
	}

	if isMinimaxGroup(groupKey) {
		log.Printf("[watch] %s machine=%s slot=%d 绑定音色 %s/%s 属 minimax（克隆不支持、tts 合成时好时坏），改用可播分组",
			r.Username, b.MachineID, b.Slot, voiceID, groupKey)
		if ch, err := c.GetCharacterCtx(ctx, b.Slot); err == nil && ch.VoiceID != "" && !isMinimaxGroup(ch.GroupKey) {
			log.Printf("[watch] %s machine=%s slot=%d → 用设备槽位音色 %s/%s", r.Username, b.MachineID, b.Slot, ch.VoiceID, ch.GroupKey)
			return ch.VoiceID, ch.GroupKey
		}
		if groups != nil {
			for _, g := range groups {
				if g.minimax() || len(g.VoiceIDs) == 0 {
					continue
				}
				log.Printf("[watch] %s machine=%s slot=%d → 用分组 %s 音色 %s", r.Username, b.MachineID, b.Slot, g.GroupKey, g.VoiceIDs[0])
				return g.VoiceIDs[0], g.GroupKey
			}
		}
	}
	return voiceID, groupKey // 设备上也没有可用分组：保持原样，由 PlayChat 报错
}

// ensureSlotVoicePlayable 槽位角色音色的本机化校验（**由 Check&Fix 触发**，播报路径不自动改写）：
// chat/turns 只发 text/slot/language，设备用的是**槽位角色配置里存的 voice/group**
// （不是本次播报参数）。同步链路若把别台机器的 group_key 写进了这台设备的槽位
// （group_key/voice_id 每台机器独立），chat 管线找不到 TTS 渠道 → 恒定数秒后 ERROR。
// 这里校验不一致时就地改写槽位角色为本机可用的 voice/group。返回是否做了修正。
func (m *Manager) ensureSlotVoicePlayable(ctx context.Context, c *clt01.Client, r *model.Role, b model.Binding) bool {
	ch, err := c.GetCharacterCtx(ctx, b.Slot)
	if err != nil {
		return false
	}
	if ch.GroupKey == "" && ch.VoiceID == "" {
		return false
	}
	var groups []devVoiceGroup
	if raw, err := c.VoiceGroup(ctx); err == nil {
		var wrapped devVoiceGroups
		if json.Unmarshal(raw, &wrapped) == nil && len(wrapped.Groups) > 0 {
			groups = wrapped.Groups
		} else {
			var list []devVoiceGroup
			if json.Unmarshal(raw, &list) == nil {
				groups = list
			}
		}
	}
	usable := func(gk string) bool {
		for _, g := range groups {
			if g.GroupKey == gk && len(g.VoiceIDs) > 0 {
				return true
			}
		}
		return false
	}
	if groups == nil || usable(ch.GroupKey) {
		return false // 拉不到分组表不动它；槽位配置本来就是本机的也不动
	}
	voiceID, groupKey := m.playableVoice(ctx, c, r, b, ch.VoiceID, ch.GroupKey)
	if groupKey == ch.GroupKey && voiceID == ch.VoiceID {
		return false
	}
	log.Printf("[watch] %s machine=%s slot=%d 槽位角色音色 %s/%s 非本机分组（chat 管线必 ERROR），改写为 %s/%s",
		r.Username, b.MachineID, b.Slot, ch.VoiceID, ch.GroupKey, voiceID, groupKey)
	ch.VoiceID, ch.GroupKey = voiceID, groupKey
	if _, err := c.SetInfo(ctx, &clt01.CallContext{MachineID: b.MachineID, Slot: b.Slot, CharacterName: r.Name}, ch, true); err != nil {
		log.Printf("[watch] %s machine=%s slot=%d 槽位音色改写失败: %v", r.Username, b.MachineID, b.Slot, err)
		return false
	}
	return true
}
