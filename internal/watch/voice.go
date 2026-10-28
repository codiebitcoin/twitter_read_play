package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"twitter_read_play/internal/model"

	clt01 "workshop_inner_sdk"
)

// voiceIDFor 解析一个绑定（机器/槽位）的 voice 配置。每台机器/槽位的值不一样，不做跨机器默认赋值：
// 只认 binding 自身 voice_id/group_key（machine/slot 1:1，clone 后记录）。
// 没有记录 → 返回空，由 ensureVoice 触发该机器自己的 clone 并回填。
func (m *Manager) voiceIDFor(r *model.Role, b model.Binding) (voiceID, groupKey string) {
	if vb := bindingOf(r, b.MachineID, b.Slot); vb != nil {
		return vb.VoiceID, vb.GroupKey
	}
	return "", ""
}

// bindingOf 返回角色上 machine/slot 绑定的指针（找不到返回 nil）。
func bindingOf(r *model.Role, machineID string, slot int) *model.Binding {
	for i := range r.Bindings {
		if r.Bindings[i].MachineID == machineID && r.Bindings[i].Slot == slot {
			return &r.Bindings[i]
		}
	}
	return nil
}

// ensureVoice 确认绑定机器的 voice 可用：
// 绑定的 voice_id 已被同机器上其它角色的绑定占用 → 作废映射（克隆槽冲突，重 clone 到空闲槽）；
// 绑定的 voice_id 记录仍在 → probe=true 时先试听验证真的能播：能播就沿用（绝不重复克隆）；
// 本地无有效 voice_clone_id 记录 → 用激活的本地样本触发 voice_clone 并登记；
// 样本未设置则默认不克隆（设备保留槽位现有音色；sync/check 跳过不报错，reclone 仍报错）。
//
// probe=true 的额外作用：设备不再登记该克隆槽（固件/换机导致 deviceVoiceInfo 判定「不存在」）时，
// 先试听确认；能播就保留记录，避免为了一个其实好用的克隆白白重克隆几分钟。
//
// 真正开始克隆（无有效记录需补克隆）时登记后台任务（trigger 标记来源 sync/check/reclone）：
// 克隆期间前端按 /api/clone/tasks 灰掉 re-clone 并显示「克隆中」；同机已有克隆则 ErrCloneBusy。
func (m *Manager) ensureVoice(r *model.Role, b model.Binding, probe bool) (voiceID, groupKey string, err error) {
	return m.ensureVoiceT(r, b, probe, "sync")
}

func (m *Manager) ensureVoiceT(r *model.Role, b model.Binding, probe bool, trigger string) (voiceID, groupKey string, err error) {
	voiceID, groupKey = m.voiceIDFor(r, b)
	c, cerr := m.machines.Client(b.MachineID)
	if cerr != nil {
		return "", "", cerr
	}
	if voiceID != "" {
		if owner := m.voiceOwner(b.MachineID, voiceID, r.ID); owner != "" {
			// 克隆槽被其它角色占用：设备侧无法区分样本归属，继续用会串声音。
			// 作废本绑定记录，下面的 clone 流程会用 usedCloneVoices 避开已占用槽。
			// 只作废 voice_id，保留用户手工选的 group_key
			_ = m.roles.SetBindingVoice(r.ID, b.MachineID, b.Slot, "", groupKey)
			m.syncLog(r, b.MachineID, true, "",
				"voice "+voiceID+" on this device is claimed by "+owner+"; invalidating binding and re-cloning to a free slot")
			voiceID = ""
		} else {
			exists, devGroup, _ := m.deviceVoiceInfo(c, voiceID)
			// 试听用的 group：优先绑定里记录的（就是下发到设备的那一个），缺失时退回设备当前可用克隆组
			gk := groupKey
			if gk == "" {
				gk = devGroup
			}
			playable, detail := true, "not probed"
			if probe {
				playable, detail = m.probeVoicePlayable(c, voiceID, gk)
			}
			if recordedVoiceKeep(probe, exists, playable) {
				if probe {
					if exists {
						m.syncLog(r, b.MachineID, true, "",
							"voice "+voiceID+" verified playable on device ("+detail+"); keeping existing clone, no re-clone")
					} else {
						m.syncLog(r, b.MachineID, true, "",
							"voice "+voiceID+" not advertised by device but "+detail+"; keeping existing clone, no re-clone")
					}
				}
				return voiceID, groupKey, nil
			}
			// 作废该 machine/slot 记录并重克隆：探测明确播不出来，或（未探测时）设备上没有这个 voice
			// 只作废 voice_id，保留用户手工选的 group_key
			_ = m.roles.SetBindingVoice(r.ID, b.MachineID, b.Slot, "", groupKey)
			if probe {
				m.syncLog(r, b.MachineID, true, "",
					"voice "+voiceID+" is recorded but not playable ("+detail+"); invalidating binding and re-cloning")
			} else {
				m.syncLog(r, b.MachineID, true, "", "voice "+voiceID+" not present on device; invalidating binding and re-cloning")
			}
			voiceID = ""
		}
	}
	// 无记录：需要 clone（登记后台任务：克隆期间前端灰掉 re-clone 并显示「克隆中」）
	sample := m.roles.AssetPath(r.LocalVoice)
	if sample == "" {
		if trigger == "reclone" { // 用户显式点「重新克隆」：必须给出失败原因
			return "", "", fmt.Errorf("no voice_id and no local voice sample (upload/select a sample in Persona first)")
		}
		// 音色样本未设置：默认不克隆，设备保留槽位现有音色，不算同步/检查失败
		m.syncLog(r, b.MachineID, true, "", "no voice sample set; skip voice clone (device keeps its current voice)")
		return "", "", nil
	}
	data, err := os.ReadFile(sample)
	if err != nil {
		return "", "", fmt.Errorf("read local voice sample failed: %w", err)
	}
	// 克隆样本有大小上限（设备/云端一次只收一小段）：本地样本可能是整段讲经（几十 MB），
	// 直接上传必失败且报错含混。这里只要前 60 秒左右就够了。
	if len(data) > maxVoiceSampleBytes {
		data = trimVoiceSample(data, maxVoiceSampleBytes)
		m.syncLog(r, b.MachineID, true, "",
			fmt.Sprintf("voice sample trimmed to %d KB for cloning", len(data)>>10))
	}
	// 机器级互斥：同一设备克隆槽 21-25 是全局资源，同时只跑一个克隆
	// allowReuse=true：reclone 流程取回 StartReclone 预登记的同一任务
	task, terr := m.beginCloneTaskOpt(r, b.MachineID, b.Slot, trigger, true)
	if terr != nil {
		return "", "", terr
	}
	// 候选（组,槽位）依次试探。云端要求具体数值 voice_id（空槽位会被拒：
	// INVALID_VOICE_SLOT: voice_id must be numeric），所以每个候选都带具体槽位号，
	// 且只允许未被本机其它绑定占用的槽（21-25）；全部占用则明确报错，绝不静默覆盖。
	// 本机已被其它绑定占用的克隆槽（voice_id → 角色名）
	used := m.usedCloneVoices(b.MachineID)
	var groups []devVoiceGroup
	if raw, err := c.VoiceGroup(context.Background()); err == nil {
		_ = json.Unmarshal(raw, &groups) // 裸数组
		if len(groups) == 0 {
			var wrapped devVoiceGroups
			if json.Unmarshal(raw, &wrapped) == nil {
				groups = wrapped.Groups
			}
		}
	}
	// group_key 为空 = 界面「自动」。force_clone 与第一次克隆：qwen 优先，再从上到下逐组试。
	// 手工指定了 group_key 时仍优先该组（buildCloneCandidates）。
	autoGroup := strings.TrimSpace(groupKey) == ""
	var cands []cloneCand
	if autoGroup {
		cands = buildAutoCloneCandidates(groups, used)
	} else {
		cands = buildCloneCandidates(groups, used, groupKey)
	}
	if len(cands) == 0 {
		taken := make([]string, 0, len(used))
		for id, role := range used {
			taken = append(taken, id+"("+role+")")
		}
		err := fmt.Errorf("本机克隆槽已全部被其它角色占用（21-25 共 5 个：%s），请先解绑或清除不需要的角色声音", strings.Join(taken, ", "))
		m.finishCloneTask(task.ID, "", "", err)
		return "", "", err
	}
	if groupKey == "" && len(cands) > 0 {
		groupKey = cands[0].group
	}
	if autoGroup {
		names := make([]string, len(cands))
		for i, cd := range cands {
			names[i] = cd.group
		}
		m.syncLog(r, b.MachineID, true, "",
			fmt.Sprintf("voice_clone auto: try %d groups on free slot %s, qwen first: %s", len(cands), cands[0].slot, strings.Join(names, " → ")))
	}

	var up *clt01.VoiceCloneResponse
	var confirmErr error
	cloned := false
	var attemptErrs []string
	for i, cd := range cands {
		var candErr error
		for cycle := 0; cycle < 2; cycle++ {
			if cycle > 0 {
				time.Sleep(2 * time.Second)
			}
			var uerr error
			up, uerr = c.VoiceCloneUpload(data, "voice.mp3", cd.slot, cd.group, r.Name+"-clone", "", 0)
			if uerr != nil {
				candErr = fmt.Errorf("voice_clone upload failed (%s/%s): %w", cd.group, slotLabel(cd.slot), uerr)
				break
			}
			time.Sleep(3 * time.Second)
			confirmErr = nil
			for k := 0; k < 2; k++ {
				if _, confirmErr = c.VoiceCloneConfirm(up.VoiceID, cd.group, true); confirmErr == nil {
					break
				}
				time.Sleep(2 * time.Second)
			}
			if confirmErr == nil {
				groupKey = cd.group
				cloned = true
				break
			}
			candErr = fmt.Errorf("voice_clone confirm failed (%s/%s): %w", cd.group, slotLabel(cd.slot), confirmErr)
		}
		if cloned {
			break
		}
		if candErr != nil {
			attemptErrs = append(attemptErrs, candErr.Error())
			log.Printf("[watch] %s machine=%s slot=%d %v (%d/%d)", r.Username, b.MachineID, b.Slot, candErr, i+1, len(cands))
		}
		if i+1 < len(cands) {
			m.syncLog(r, b.MachineID, true, "",
				fmt.Sprintf("voice_clone group %s failed, trying next (%d/%d)", cd.group, i+1, len(cands)))
		}
	}
	if !cloned {
		err := fmt.Errorf("voice_clone failed (all %d candidates): %s", len(cands), strings.Join(attemptErrs, "; "))
		m.finishCloneTask(task.ID, "", "", err)
		return "", "", err
	}
	_ = m.roles.SetBindingVoice(r.ID, b.MachineID, b.Slot, up.VoiceID, groupKey)
	m.syncLog(r, b.MachineID, true, "", "voice_clone done voice_id="+up.VoiceID+" ("+b.MachineID+"/slot "+strconv.Itoa(b.Slot)+")")
	m.finishCloneTask(task.ID, up.VoiceID, groupKey, nil)
	return up.VoiceID, groupKey, nil
}

// usedCloneVoices 本机已被其它绑定占用的克隆槽（voice_id → 角色名）。
func (m *Manager) usedCloneVoices(machineID string) map[string]string {
	used := map[string]string{}
	for _, other := range m.roles.List() {
		for _, ob := range other.Bindings {
			if ob.MachineID == machineID && ob.VoiceID != "" {
				used[ob.VoiceID] = other.Name
			}
		}
	}
	return used
}

// StartReclone 异步发起重新克隆（修复历史互相覆盖的声音）。用户显式要求，不试听探测。
// 先预登记任务占住机器（同机忙 → ErrCloneBusy，API 层 409），goroutine 里走 ensureVoiceT 补克隆；
// ensureVoiceT 克隆段会续用该预登记任务并在结束时收尾（finishCloneTask 幂等，这里兜底再收一次）。
// 返回任务记录，前端轮询 /api/clone/tasks 展示「克隆中」并在完成后回填 voice_id。
func (m *Manager) StartReclone(roleID, machineID string, slot int) (*CloneTask, error) {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return nil, err
	}
	b := bindingOf(r, machineID, slot)
	if b == nil {
		return nil, fmt.Errorf("角色未绑定该机器/槽位")
	}
	task, err := m.beginCloneTask(r, machineID, slot, "reclone")
	if err != nil {
		return nil, err
	}
	// 快照原记录：清空只是「强制重克隆」的手段，克隆失败必须恢复，
	// 否则一次失败的重克隆会把本来可用的音色记录弄丢（面板显示「未记录」、播报报 voice_id 为空）。
	prevVoice, prevGroup := b.VoiceID, b.GroupKey
	m.setCloneTaskPrev(task.ID, prevVoice, prevGroup) // 快照进任务记录，供失败/重启恢复
	go func() {
		// 只清 voice_id，保留用户手工选的 group_key —— reclone 必须尊重它，
		// 否则候选组被重置成设备广播列表，用户选的组永远不生效。
		_ = m.roles.SetBindingVoice(roleID, machineID, slot, "", prevGroup)
		voiceID, groupKey, err := m.ensureVoiceT(r, *b, false, "reclone")
		if err != nil {
			if prevVoice != "" {
				_ = m.roles.SetBindingVoice(roleID, machineID, slot, prevVoice, prevGroup)
				m.syncLog(r, machineID, false, "reclone: "+err.Error(), "kept previous voice "+prevVoice+" (reclone failed)")
				voiceID, groupKey = prevVoice, prevGroup // 任务记录里体现保留下来的音色
			} else {
				// 失败也落同步日志，否则面板看不到手动重克隆为什么没结果
				m.syncLog(r, machineID, false, "reclone: "+err.Error(), "")
			}
		}
		m.finishCloneTask(task.ID, voiceID, groupKey, err)
	}()
	return task, nil
}
