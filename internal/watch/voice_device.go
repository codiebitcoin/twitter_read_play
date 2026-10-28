package watch

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	clt01 "workshop_inner_sdk"
)

// devVoiceGroup 设备音色分组（group_key + provider + 全部可用 voice_ids + 克隆 voice_ids）。
type devVoiceGroup struct {
	GroupKey      string   `json:"group_key"`
	Provider      string   `json:"provider"`
	VoiceIDs      []string `json:"voice_ids"`
	CloneVoiceIDs []string `json:"clone_voice_ids"`
}

// devVoiceGroups VoiceGroups 被包成 {"groups":[…]} 时的外壳（有的固件直接返回裸数组）。
type devVoiceGroups struct {
	Groups []devVoiceGroup `json:"groups"`
}

// isMinimaxGroup minimax 分组：**一律不作为默认目标**（默认都是 qwen）。
// 依据（2026-10-15 真机实测）：
//   - 克隆：minimax 云端不支持 voice_clone，必然失败（用户明确「minmax 不能 voice_clone」）；
//   - 播报：1.5.2 的 tts 通道用 minimax 合成**时好时坏** —— 失败时设备 [E2E] stage TTS_AUDIO
//     status FINAL data: 0（空音频），15s 后 [TTSAudition] 超时置 FAILED，表现为「下发成功但没声音」；
//     同一绑定 22:31 FAILED(15.4s) / 22:32 ENDED(16.3s)，而 qwen 组 4~5s 稳定进 PLAYING。
//
// 按子串匹配（组名或 provider 含 "minimax" 即判真）：固件会把 provider 写成
// minimax_clone / minimax_tts 之类变体，精确匹配（== "minimax"）拦不住 → 又默认在用 minimax 克隆。
func isMinimaxGroup(groupKey string) bool {
	return strings.Contains(strings.ToLower(groupKey), "minimax")
}

// minimax 该分组是否 minimax（按组名或 provider 判定，均为子串匹配）。
func (g devVoiceGroup) minimax() bool {
	return isMinimaxGroup(g.GroupKey) || isMinimaxGroup(g.Provider)
}

// isQwenLike 该分组是否 qwen 系（默认目标）：provider 优先，组名兜底。
func (g devVoiceGroup) isQwenLike() bool {
	p := strings.ToLower(strings.TrimSpace(g.Provider))
	if p != "" {
		return strings.Contains(p, "qwen")
	}
	return strings.Contains(strings.ToLower(g.GroupKey), "qwen")
}

// VoiceExists 检查 voice_id 在指定机器上是否可用（给 API 用的设备侧校验）。
func (m *Manager) VoiceExists(machineID, voiceID string) (bool, error) {
	c, err := m.machines.Client(machineID)
	if err != nil {
		return false, err
	}
	exists, _, _ := m.deviceVoiceInfo(c, voiceID)
	return exists, nil
}

// deviceVoiceInfo 拉取设备音色信息；返回 (voiceID 是否存在, 克隆组 group_key, 该组克隆槽 voice_id)。
func (m *Manager) deviceVoiceInfo(c *clt01.Client, voiceID string) (bool, string, string) {
	raw, err := c.VoiceGroup(context.Background())
	if err != nil {
		return true, "", "" // 拉不到就别误杀绑定
	}
	var parsed devVoiceGroups
	if err := json.Unmarshal(raw, &parsed); err != nil {
		var list []devVoiceGroup
		if err := json.Unmarshal(raw, &list); err != nil {
			return true, "", ""
		}
		parsed.Groups = list
	}
	var exists bool
	var cloneGroup, cloneID string
	isCloneSlot := func(id string) bool {
		n, err := strconv.Atoi(strings.TrimSpace(id))
		return err == nil && n >= 21 && n <= 25
	}
	for _, g := range parsed.Groups {
		for _, id := range g.VoiceIDs {
			if id == voiceID {
				exists = true
			}
		}
		if cloneGroup != "" {
			continue
		}
		if g.minimax() {
			continue // minimax 组不支持克隆（且 tts 时好时坏），绝不作为默认克隆组
		}
		// 克隆能力判定：组的 voice_ids 里含 21-25（设备端 catalog 会把 provider 可用克隆槽合并进来）；
		// clone_voice_ids 只是历史克隆记录，不能作为能力依据
		preferred := ""
		capable := false
		for _, id := range g.VoiceIDs {
			if isCloneSlot(id) {
				capable = true
				if len(g.CloneVoiceIDs) > 0 {
					preferred = g.CloneVoiceIDs[0] // 优先沿用该组已用过的克隆槽
					break
				}
				if preferred == "" {
					preferred = id
				}
			}
		}
		if capable {
			cloneGroup = g.GroupKey
			cloneID = preferred
		}
	}
	return exists, cloneGroup, cloneID
}

// voiceOwner 返回同一台机器上占用该 voice_id 的其它角色（克隆槽是设备级资源，
// 两个角色绑定同一克隆槽时，后克隆的会覆盖先克隆的声音）。无占用返回空。
func (m *Manager) voiceOwner(machineID, voiceID, selfRoleID string) string {
	for _, other := range m.roles.List() {
		if other.ID == selfRoleID {
			continue
		}
		for _, ob := range other.Bindings {
			if ob.MachineID == machineID && ob.VoiceID == voiceID {
				return other.Name + "(" + other.ID + ")"
			}
		}
	}
	return ""
}
