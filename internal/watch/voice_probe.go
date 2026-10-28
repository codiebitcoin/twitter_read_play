package watch

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	clt01 "workshop_inner_sdk"
)

// ---- 试听探测：先确认已记录的克隆「能不能播」，再决定要不要重克隆 ----

// auditionProbeText 探测用的试听文本（越短越省时间；会被设备外放出来）。
const auditionProbeText = "Voice check."

// auditionState 设备试听状态机（与设备控制台同一套）：WAITING → PLAYING → ENDED / FAILED。
type auditionState struct {
	State       string `json:"state"`
	UpdatedAtMs int64  `json:"updated_at_ms"`
}

// auditionVerdict 由「上一次状态快照 + 当前状态」判断本次试听结果（纯函数，便于单测）：
//
//	done=true, ok=true   本次试听真的播出来了
//	done=true, ok=false  设备明确报失败（voice_id/group_key 组合无效、克隆已丢失等）
//	done=false           还没出结果，调用方继续轮询
//
// ENDED 必须伴随 updated_at_ms 前进才算「本次」结果，否则会把上一次试听的 ENDED 误判成本次成功。
func auditionVerdict(prevMs int64, st auditionState) (done, ok bool) {
	switch strings.ToUpper(strings.TrimSpace(st.State)) {
	case "FAILED", "ERROR", "FAILED_PLAYBACK":
		return true, false
	case "ENDED", "SUCCESS", "DONE":
		if st.UpdatedAtMs != prevMs {
			return true, true
		}
	}
	return false, false
}

// probeVoicePlayable 试听探测：让设备用 (voiceID, groupKey) 合成并播放一小段，轮询试听状态机判断能否真正播放。
// 返回 (可播放, 说明)。fail-open 原则：探测请求失败 / 状态超时都返回 true ——
// 宁可沿用已有克隆记录，也不因为「测不出来」而误触发一次分钟级的重克隆。
func (m *Manager) probeVoicePlayable(c *clt01.Client, voiceID, groupKey string) (bool, string) {
	if voiceID == "" {
		return false, "empty voice_id"
	}
	var prev auditionState
	if raw, err := c.TTSAuditionState(context.Background()); err == nil {
		_ = json.Unmarshal(raw, &prev)
	}
	if err := c.TTSAudition(voiceID, auditionProbeText, groupKey); err != nil {
		return true, "audition request failed, treat as playable: " + err.Error()
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		raw, err := c.TTSAuditionState(context.Background())
		if err != nil {
			continue
		}
		var st auditionState
		if json.Unmarshal(raw, &st) != nil {
			continue
		}
		if done, ok := auditionVerdict(prev.UpdatedAtMs, st); done {
			if ok {
				return true, "playable (audition " + strings.ToUpper(st.State) + ")"
			}
			return false, "audition FAILED (voice " + voiceID + "/" + groupKey + " not playable)"
		}
	}
	return true, "audition state timeout, treat as playable"
}

// recordedVoiceKeep 决定「已记录的克隆 voice_id」是沿用还是作废重克隆（纯函数，便于单测）。
//
//	probe=false：只能靠设备是否登记该 voice 判断（exists），沿用条件 = exists；
//	probe=true ：以试听实测为准 —— 能播就沿用（exists=false 只说明设备没登记，不代表声音不可用），
//	             播不出来才作废重克隆。这一条是「尽量避免少克隆」的核心：
//	             一次 clone 分钟级且占用/可能覆盖克隆槽，绝不因为「查不到登记」就盲目重克隆。
func recordedVoiceKeep(probe, exists, playable bool) bool {
	if !probe {
		return exists
	}
	return playable
}
