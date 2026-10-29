package watch

import (
	"context"
	"log"
	"strconv"
	clt01 "workshop_inner_sdk"

	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"twitter_read_play/internal/model"
)

// ApplyRolePackage 下发角色包到设备指定绑定（prompt / voice / 音量 / 槽位），同步入口与播报共用。
// sync/check 路径用强制下发；broadcast 路径先比对：配置与期望一致且槽位已激活时，
// 不重写 character、不重复触发 SwitchSlot（设备端对相同槽位也会重新走角色切换流程，会打断当前状态）。
func (m *Manager) ApplyRolePackage(r *model.Role, b model.Binding) error {
	return m.applyRolePackage(r, b, false)
}

func (m *Manager) applyRolePackage(r *model.Role, b model.Binding, skipIfMatch bool) error {
	c, err := m.machines.Client(b.MachineID)
	if err != nil {
		return err
	}
	// 原 slot 模式不接管设备音色：VoiceID/GroupKey 留空 → SetInfo 合并时保留设备现值。
	// 这样 sync / broadcast / book 任一入口都不会把设备原有声音换掉。
	custom := r.UsesCustomVoiceVrm()
	var voiceID, groupKey string
	if custom {
		voiceID, groupKey = m.voiceIDFor(r, b)
	}
	// 同一台设备上多个角色共用同一个克隆分组时，分组内的「当前音色」是**全局唯一**的：
	// 别的角色 apply 过就会把引擎音色切成它自己的，本角色即使槽位配置已匹配也必须强制重下发，
	// 否则会出现「配置对、出声是别人的声音」（实测：鸣人克隆出 25 后，D5 slot1 的 Trump 配置
	// 仍是 21，但设备实际用 25 出声）。单角色独占设备时保持原fast-path，不做无谓下发。
	// 原 slot 模式不写音色，也就没有这个串音问题。
	force := !skipIfMatch || (custom && m.sharesVoiceGroup(r, b))
	// 原子 SetInfo：检查不匹配才 PUT（skipIfMatch=false 即 force），合并/剥离 cloth 字段都在 SDK 内。
	// changed=这次真的改写了设备上的角色配置（用它决定要不要等音色生效）。
	changed, err := c.SetInfo(context.Background(), &clt01.CallContext{MachineID: b.MachineID, Slot: b.Slot, CharacterName: r.Name}, &clt01.CharacterSettings{
		Name:               r.Name,
		Description:        r.Description,
		CharacterPrompt:    r.Prompt,
		VoiceID:            voiceID,
		GroupKey:           groupKey,
		TTSVolume:          r.TTSVolume,
		Sex:                r.Sex,
		AnimationIntensity: r.AnimationIntensity,
	}, force)
	if err != nil {
		return fmt.Errorf("apply role package failed: %w", err)
	}
	// 保证激活槽位正确：当前槽位就是目标槽位则什么都不做，否则补一次 SwitchSlot（幂等）
	if cur, _, serr := c.CurrentSlot(); serr != nil || cur != b.Slot {
		if err := c.SwitchSlot(b.Slot); err != nil {
			return fmt.Errorf("switch slot failed: %w", err)
		}
		changed = true // 切槽位会重载该槽位的角色，音色同样要等生效
	}
	if changed {
		m.waitVoiceApplied(c, b.Slot, voiceID, groupKey)
	}
	return nil
}

// sharesVoiceGroup 判断本绑定所在分组是否被**同一台机器上的其它启用角色**也用着。
// 设备每个克隆分组只有一套「当前音色」，共用分组就必须每次强制下发，否则会串音。
func (m *Manager) sharesVoiceGroup(self *model.Role, b model.Binding) bool {
	// voiceIDFor 可能做跨机分组重映射，这里只看原始声明，避免副作用写入。
	if strings.TrimSpace(b.GroupKey) == "" {
		return false // 未声明分组：交给设备默认，无法可靠判定，不做强制
	}
	for _, other := range m.roles.List() {
		if other.ID == self.ID || !other.Enabled {
			continue
		}
		// 直接看原始绑定：enabledBindings 会再过一遍机器存在性，而这里判断的是「同一台机器」
		// （本绑定所在机器必然是有效机器），多那层过滤只会让判定在测试/边界下失准。
		for _, ob := range other.Bindings {
			if !ob.Enabled || ob.MachineID != b.MachineID {
				continue
			}
			if strings.TrimSpace(ob.GroupKey) == strings.TrimSpace(b.GroupKey) {
				return true
			}
		}
	}
	return false
}

// slotConflicts 找出「同一个 machine+slot 被别的启用角色也绑着」的情况。
// 返回 machineID#slot → 其它角色名。
func (m *Manager) slotConflicts(self *model.Role) map[string][]string {
	out := map[string][]string{}
	roles := m.roles.List()
	for _, r := range roles {
		if r.ID == self.ID || !r.Enabled {
			continue
		}
		for _, b := range m.enabledBindings(r) {
			out[b.MachineID+"#"+strconv.Itoa(b.Slot)] = append(out[b.MachineID+"#"+strconv.Itoa(b.Slot)], r.Name)
		}
	}
	return out
}

// waitVRMPersisted 轮询等待「设备端 uploaded-models 里出现该槽位的模型」。
// 上传/commit 是异步的，check 必须等它落盘再判定，否则会误报固件管线损坏。
func (m *Manager) waitVRMPersisted(machineID string, slot, cloth int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if persisted, err := m.VRMModelPersisted(machineID, slot, cloth); err == nil && persisted {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Second)
	}
}

// waitVoiceApplied 回读槽位，确认设备已经用上目标音色再往下走。
// 设备侧「换音色」比「换角色/模型」生效慢：SetInfo 返回 200、屏幕上角色和模型都换了，
// 但紧接着下发的第一条 TTS 仍在用旧音色（用户实测：开始那次只切了角色和模型、声音没切）。
// 这里轮询回读，命中即返回（音色本来就对时零等待），最多等 6 秒并把超时写进日志。
func (m *Manager) waitVoiceApplied(c *clt01.Client, slot int, voiceID, groupKey string) {
	deadline := time.Now().Add(6 * time.Second)
	for {
		ch, err := c.GetCharacter(slot)
		if err == nil && ch != nil {
			voiceOK := voiceID == "" || ch.VoiceID == voiceID
			groupOK := groupKey == "" || ch.GroupKey == groupKey
			if voiceOK && groupOK {
				return
			}
			if time.Now().After(deadline) {
				log.Printf("[playback] slot %d 音色等待超时：期望 %s/%s，设备仍是 %s/%s（继续播报，可能出现一次旧音色）",
					slot, voiceID, groupKey, ch.VoiceID, ch.GroupKey)
				return
			}
		} else if time.Now().After(deadline) {
			log.Printf("[playback] slot %d 音色等待超时（回读失败: %v）", slot, err)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// syncLog 记录 sync/check 步骤日志（系统日志 tab + 失败原因展示）。
func (m *Manager) syncLog(r *model.Role, machineID string, ok bool, msg, detail string) {
	m.appendLog(&model.BroadcastLog{
		Time: time.Now(), Kind: "sync", Platform: normalizePlatform(r.Platform), RoleID: r.ID, MachineID: machineID,
		OK: ok, Error: msg, Detail: detail,
	})
}

// SyncLogByID 供 API 层把后台任务（如 VRM 异步上传）的结果写进同步日志面板，
// 否则失败只落在服务日志里，用户会以为「上传了但没生效」。
func (m *Manager) SyncLogByID(roleID, machineID string, ok bool, msg, detail string) {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return
	}
	m.syncLog(r, machineID, ok, msg, detail)
}

// SyncRole 一键同步角色包到 Codie 设备：同步全部启用绑定（或指定 machine/slot 的那一个）。
// VRM 有本地文件时后台补传（耗时任务不阻塞响应）。
// probeVoice：对已记录的克隆先试听验证能播才沿用（避免为可用的克隆白克隆一次）。
func (m *Manager) SyncRole(roleID string, onlyMachineID string, onlySlot int, probeVoice bool) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	r, err := m.roles.Get(roleID)
	if err != nil {
		return err
	}
	bindings := m.enabledBindings(r)
	if onlyMachineID != "" || onlySlot > 0 {
		var filtered []model.Binding
		for _, b := range r.Bindings {
			if (onlyMachineID == "" || b.MachineID == onlyMachineID) && (onlySlot <= 0 || b.Slot == onlySlot) {
				filtered = append(filtered, b)
			}
		}
		bindings = filtered
	}
	if len(bindings) == 0 {
		return ErrNeedBinding
	}
	var errs []string
	for _, b := range bindings {
		errsBefore := len(errs)
		custom := r.UsesCustomVoiceVrm()
		// 1) model（VRM）最前：同步上传+选中（内部逐步 syncLog，失败不中断后续步骤）。
		// 原 slot 模式明确不上传 VRM，设备保留自己的模型。
		if custom && r.LocalVrm != "" {
			if verr := m.uploadLocalVrm(r.ID, b); verr != nil {
				errs = append(errs, fmt.Sprintf("slot %d model: %v", b.Slot, verr))
			}
		}
		// 2) 下发角色包（prompt/name/音量/槽位；原 slot 模式不含 voice）
		if rf, ferr := m.roles.Get(roleID); ferr == nil {
			r = rf
		}
		if err := m.ApplyRolePackage(r, b); err != nil {
			m.syncLog(r, b.MachineID, false, err.Error(), "")
			errs = append(errs, fmt.Sprintf("slot %d role package: %v", b.Slot, err))
			// 不中断：继续 voice
		}
		// 3) voice 最后：需要克隆时是分钟级操作；失败也不中断。原 slot 模式跳过克隆。
		if custom {
			voiceID, _, verr := m.ensureVoice(r, b, probeVoice)
			if verr != nil {
				m.syncLog(r, b.MachineID, false, "voice: "+verr.Error(), "")
				errs = append(errs, fmt.Sprintf("slot %d voice: %v", b.Slot, verr))
			} else {
				// ensureVoice 可能已回填 binding，重读后重新下发让 character 带上 voice，并回读校验
				if rf, ferr := m.roles.Get(roleID); ferr == nil {
					r = rf
				}
				if pkgErr := m.ApplyRolePackage(r, b); pkgErr != nil {
					m.syncLog(r, b.MachineID, false, pkgErr.Error(), "")
					errs = append(errs, fmt.Sprintf("slot %d role package: %v", b.Slot, pkgErr))
				} else if c, cerr := m.machines.Client(b.MachineID); cerr == nil {
					if ch, gerr := c.GetCharacter(b.Slot); gerr == nil && voiceID != "" && ch.VoiceID != voiceID {
						time.Sleep(time.Second)
						_ = m.ApplyRolePackage(r, b)
						if ch2, g2 := c.GetCharacter(b.Slot); g2 == nil && ch2.VoiceID != voiceID {
							m.syncLog(r, b.MachineID, false, "voice not effective: device="+ch2.VoiceID+" expected="+voiceID, "")
							errs = append(errs, fmt.Sprintf("slot %d voice not effective (device=%s)", b.Slot, ch2.VoiceID))
						}
					}
				}
			}
		}
		if len(errs) == errsBefore {
			// 本绑定没有任何新增错误 → 记成功
			if custom {
				m.syncLog(r, b.MachineID, true, "", "sync done (model/prompt/voice, slot "+fmt.Sprint(b.Slot)+")")
			} else {
				m.syncLog(r, b.MachineID, true, "", "sync done (config only; original slot VRM/voice kept, slot "+fmt.Sprint(b.Slot)+")")
			}
		}
	}
	if len(errs) > 0 {
		// 全部步骤都跑完才汇总报错
		return fmt.Errorf("sync finished with errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) uploadLocalVrm(roleID string, b model.Binding) error {
	r, err := m.roles.Get(roleID)
	if err != nil || r.LocalVrm == "" {
		return nil
	}
	data, err := os.ReadFile(m.roles.AssetPath(r.LocalVrm))
	if err != nil {
		m.syncLog(r, b.MachineID, false, "read local VRM failed: "+err.Error(), "")
		return fmt.Errorf("read local VRM: %w", err)
	}
	c, err := m.machines.Client(b.MachineID)
	if err != nil {
		m.syncLog(r, b.MachineID, false, err.Error(), "")
		return err
	}
	clothID := r.ClothID
	if clothID <= 0 {
		clothID = 2
	}
	logf := func(ok bool, detail, errMsg string) { m.syncLog(r, b.MachineID, ok, errMsg, detail) }
	// 草稿复用：Persona「仅上传」已在设备留下同名草稿时跳过整份重传，直接应用
	if applied, err := ApplyDraftVRM(c, b.MachineID, b.Slot, clothID, filepath.Base(r.LocalVrm), logf); applied {
		return err
	}
	return UploadVRMData(c, b.MachineID, b.Slot, clothID, filepath.Base(r.LocalVrm), data, logf)
}

// CheckResult 单个绑定的设备状态核对结果。
type CheckResult struct {
	MachineID string   `json:"machine_id"`
	Slot      int      `json:"slot"`
	OK        bool     `json:"ok"`
	Issues    []string `json:"issues,omitempty"`
	Fixed     []string `json:"fixed,omitempty"`
}

// CheckOpts check/修复的可选项。
type CheckOpts struct {
	// ProbeVoice 对已记录的克隆做试听探测（设备会外放一小段），确认能播才沿用、播不出来才重克隆。
	// 目的：尽量避免重复克隆（一次 clone 分钟级，还会占用/覆盖克隆槽）。
	ProbeVoice bool
}

// filterBindings check/sync/独立按钮共用的绑定筛选:默认全部启用绑定,指定 machine/slot 只查那一个。
func (m *Manager) filterBindings(r *model.Role, onlyMachineID string, onlySlot int) []model.Binding {
	bindings := m.enabledBindings(r)
	if onlyMachineID != "" || onlySlot > 0 {
		var filtered []model.Binding
		for _, b := range r.Bindings {
			if (onlyMachineID == "" || b.MachineID == onlyMachineID) && (onlySlot <= 0 || b.Slot == onlySlot) {
				filtered = append(filtered, b)
			}
		}
		bindings = filtered
	}
	return bindings
}

// ApplyConfig 仅 PATCH 基础角色配置(prompt/name/desc/sex/intensity + 已记录的 voice):
// POST /api/roles/{id}/apply(PATCH设置按钮)与 check&fix 阶段1 共用,不等 VRM/克隆。
// 每个绑定一条 op 登记进全局 MessageQueue（同步登记：请求返回前就能被轮询看到）。
func (m *Manager) ApplyConfig(roleID string, onlyMachineID string, onlySlot int) {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return
	}
	ops := m.beginOps("patch_config", r, onlyMachineID, onlySlot)
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	for _, b := range m.filterBindings(r, onlyMachineID, onlySlot) {
		var errMsg string
		if err := m.ApplyRolePackage(r, b); err != nil {
			errMsg = "config patch failed: " + err.Error()
			m.syncLog(r, b.MachineID, false, errMsg, "")
		} else {
			m.syncLog(r, b.MachineID, true, "", fmt.Sprintf("config patch applied (slot %d)", b.Slot))
		}
		endBindingOp(m, ops, b.MachineID, b.Slot, errMsg)
	}
}

// PushVRM 仅上传激活的本地 VRM(上传路径完成时自动 apply:SelectCloth/三件套):
// POST /api/roles/{id}/vrm/push(VRM上传按钮)与 check&fix 阶段2 共用。
// 每个绑定一条 op 登记进全局 MessageQueue。
func (m *Manager) PushVRM(roleID string, onlyMachineID string, onlySlot int) {
	r, err := m.roles.Get(roleID)
	if err != nil || r.LocalVrm == "" {
		return
	}
	ops := m.beginOps("upload_vrm", r, onlyMachineID, onlySlot)
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	for _, b := range m.filterBindings(r, onlyMachineID, onlySlot) {
		var errMsg string
		if err := m.uploadLocalVrm(roleID, b); err != nil {
			errMsg = "VRM push failed: " + err.Error()
			m.syncLog(r, b.MachineID, false, errMsg, "")
		} else {
			m.syncLog(r, b.MachineID, true, "", fmt.Sprintf("VRM pushed & applied (slot %d)", b.Slot))
		}
		endBindingOp(m, ops, b.MachineID, b.Slot, errMsg)
	}
}

// CheckRole 核对设备实际状态与角色配置：voice_id / model(cloth) / prompt 是否一致，不一致自动修复。
// 分段推进(用户要求:每段完成立即 PATCH 设备,不要等全部结束才下发):
//
//	1/3 基础配置先 PATCH(name/desc/prompt/sex/intensity,带上已记录 voice)
//	2/3 VRM 上传,完成时上传路径自行 apply
//	3/3 voice 试听验证/必要时克隆,克隆回填后单独 PATCH
//
// 最终结论("check done")只在全部阶段结束后给出 —— 前端以此为准展示 ok。
func (m *Manager) CheckRole(roleID string, onlyMachineID string, onlySlot int, opts CheckOpts) []CheckResult {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	r, err := m.roles.Get(roleID)
	if err != nil {
		return nil
	}
	bindings := m.filterBindings(r, onlyMachineID, onlySlot)
	// 同机同槽位冲突检查：设备一台机器同时只有一个「激活槽位 + 一套角色/音色」，
	// 两个角色绑到同一个 machine+slot 时，谁最后 sync 谁生效，另一个播报就会用错角色/音色
	// （实测：dev slot1 同时绑了 Donald Trump 和 Paimon，voice 22 vs 21，来回抢）。
	conflicts := m.slotConflicts(r)
	var out []CheckResult
	for _, b := range bindings {
		res := CheckResult{MachineID: b.MachineID, Slot: b.Slot, OK: true}
		// 每个绑定一条 op 登记进全局 MessageQueue（check 分钟级，队列里全程可见）
		op := m.BeginOp("check_fix", r, b.MachineID, b.Slot)
		if others := conflicts[b.MachineID+"#"+strconv.Itoa(b.Slot)]; len(others) > 0 {
			res.Issues = append(res.Issues, fmt.Sprintf("同一槽位被多个角色绑定：本角色与 %s 都绑定 %s slot %d —— 设备同时只有一个激活槽位，会互相抢（表现为角色/声音来回切换或播报用错音色）；请给每个角色分配不同机器或不同 slot",
				strings.Join(others, "、"), b.MachineID, b.Slot))
		}
		c, err := m.machines.Client(b.MachineID)
		if err != nil {
			res.OK = false
			res.Issues = append(res.Issues, err.Error())
			m.EndOp(op.ID, err.Error())
			out = append(out, res)
			continue
		}
		// ---- 阶段 0/3:固件版本重探(强制,不看 24h TTL) ----
		// 设备刷机降级(如 1.5.3→1.5.2)后旧记录会让播报走错通道(chat/turns 打到 1.5.2 必 ERROR)。
		prevFw := ""
		if mc, gerr := m.machines.Get(b.MachineID); gerr == nil {
			prevFw = mc.Firmware
		}
		if fw, perr := m.machines.ProbeFirmware(b.MachineID); perr == nil && fw != "" {
			if prevFw != "" && prevFw != fw {
				res.Issues = append(res.Issues, fmt.Sprintf("firmware changed: recorded=%s actual=%s", prevFw, fw))
				res.Fixed = append(res.Fixed, "firmware re-probed: "+fw)
				m.syncLog(r, b.MachineID, true, "", fmt.Sprintf("check[0/3] firmware re-probed: %s -> %s (playback channel follows)", prevFw, fw))
			} else {
				m.syncLog(r, b.MachineID, true, "", fmt.Sprintf("check[0/3] firmware confirmed: %s", fw))
			}
		} else if perr != nil {
			res.Issues = append(res.Issues, "firmware probe failed: "+perr.Error())
		}
		// ---- 阶段 0.5:槽位音色本机化(group_key/voice_id 每台机器独立,不能跨机混用) ----
		// 槽位角色里若被写入了别台机器的 group_key,chat 管线找不到 TTS 渠道 → 恒定 ERROR。
		// 修复只由 Check&Fix 触发(播报路径不擅自改写设备配置)。
		// 原 slot 模式不接管设备音色，这一阶段也必须跳过（否则会把设备原有声音改掉）。
		custom := r.UsesCustomVoiceVrm()
		if custom && m.ensureSlotVoicePlayable(context.Background(), c, r, b) {
			res.Issues = append(res.Issues, fmt.Sprintf("slot voice/group not from this machine: slot=%d", b.Slot))
			res.Fixed = append(res.Fixed, "slot voice/group rewritten to this machine's group")
			m.syncLog(r, b.MachineID, true, "", fmt.Sprintf("check[0.5] slot %d voice/group rewritten to a local group (was cross-machine)", b.Slot))
		}
		ch, err := c.GetCharacter(b.Slot)
		if err != nil {
			res.OK = false
			res.Issues = append(res.Issues, "read character failed: "+err.Error())
			m.EndOp(op.ID, err.Error())
			out = append(out, res)
			continue
		}
		// ---- 阶段 1/3:基础配置先 PATCH(不等 VRM 上传和克隆,设备先拿到正确人设) ----
		if r.Name != "" && ch.Name != r.Name {
			res.Issues = append(res.Issues, "name mismatch: device="+ch.Name+" expected="+r.Name)
		}
		if r.Description != "" && ch.Description != r.Description {
			res.Issues = append(res.Issues, "description mismatch")
		}
		cfgMismatch := (r.Prompt != "" && ch.CharacterPrompt != r.Prompt) ||
			(r.Name != "" && ch.Name != r.Name) ||
			(r.Description != "" && ch.Description != r.Description) ||
			(r.Sex > 0 && ch.Sex != r.Sex) ||
			(r.AnimationIntensity > 0 && ch.AnimationIntensity != r.AnimationIntensity)
		if cfgMismatch {
			if r.Prompt != "" && ch.CharacterPrompt != r.Prompt {
				res.Issues = append(res.Issues, "prompt mismatch")
			}
			if r.Sex > 0 && ch.Sex != r.Sex {
				res.Issues = append(res.Issues, fmt.Sprintf("sex mismatch: device=%d expected=%d", ch.Sex, r.Sex))
			}
			if r.AnimationIntensity > 0 && ch.AnimationIntensity != r.AnimationIntensity {
				res.Issues = append(res.Issues, fmt.Sprintf("animation intensity mismatch: device=%d expected=%d", ch.AnimationIntensity, r.AnimationIntensity))
			}
			if err := m.ApplyRolePackage(r, b); err == nil {
				res.Fixed = append(res.Fixed, "config patched first")
				m.syncLog(r, b.MachineID, true, "", fmt.Sprintf("check[1/3] config patched (slot %d)", b.Slot))
				// 回读确认：已对齐的 issue 不能留在结论里，否则「check done」永远 FAIL，
				// 用户看到的是「报错了但其实已经修好」（实测：vrm/声音都生效了，总结还是 FAIL）。
				if fresh, ferr := c.GetCharacter(b.Slot); ferr == nil {
					ch = fresh
					if r.Name == "" || ch.Name == r.Name {
						res.clearIssue("name mismatch")
					}
					if r.Description == "" || ch.Description == r.Description {
						res.clearIssue("description mismatch")
					}
					if r.Prompt == "" || ch.CharacterPrompt == r.Prompt {
						res.clearIssue("prompt mismatch")
					}
					if r.Sex == 0 || ch.Sex == r.Sex {
						res.clearIssue("sex mismatch")
					}
					if r.AnimationIntensity == 0 || ch.AnimationIntensity == r.AnimationIntensity {
						res.clearIssue("animation intensity mismatch")
					}
				}
			} else {
				res.Fixed = append(res.Fixed, "config patch failed: "+err.Error())
			}
		}
		// ---- 阶段 2/3:VRM 上传(完成时上传路径自行 apply:SelectCloth / 1.5.x 三件套) ----
		// 原 slot 模式不接管形象：既不上传模型，也不比较/切换 cloth，设备保持原样。
		if custom {
			wantCloth := r.ClothID
			if wantCloth <= 0 {
				wantCloth = 2
			}
			// 设备端是否真的存着该模型(防固件假 SUCCESS:cloth 显示已选中但 uploaded-models 为空)。
			// 仅当角色有本地 VRM(期望设备上就是这个自定义模型)时才判定,否则设备内置模型是正常的。
			if r.LocalVrm != "" {
				if persisted, perr := m.VRMModelPersisted(b.MachineID, b.Slot, wantCloth); perr == nil && !persisted {
					res.Issues = append(res.Issues, fmt.Sprintf("model not persisted on device: no uploaded model for cloth %d (firmware VRM pipeline broken)", wantCloth))
					res.Fixed = append(res.Fixed, "uploading active local VRM")
					m.syncLog(r, b.MachineID, true, "", fmt.Sprintf("check[2/3] VRM uploading (slot %d)", b.Slot))
					_ = m.uploadLocalVrm(r.ID, b)
					// 上传（含设备端 commit 草稿模型）是异步的：这里必须等它落盘再下结论，
					// 否则会在几秒内报「no uploaded model ... firmware VRM pipeline broken」这种
					// 吓人的假失败（实测：同一秒 sync 已报 uploaded-models 证据，check 却说没落盘；
					// 手动重跑 check 立刻变成 all consistent）。等到落盘或 40s 超时为止。
					if m.waitVRMPersisted(b.MachineID, b.Slot, wantCloth, 40*time.Second) {
						res.Issues = nil
						res.OK = true
						res.Fixed = append(res.Fixed, "VRM persisted after upload")
					}
				}
			}
			if ch.SelectedCloth != 0 && ch.SelectedCloth != wantCloth {
				res.Issues = append(res.Issues, fmt.Sprintf("model mismatch: device cloth=%d expected=%d", ch.SelectedCloth, wantCloth))
				if err := c.SelectCloth(b.Slot, wantCloth); err != nil {
					res.Fixed = append(res.Fixed, "cloth switch failed: "+err.Error())
				} else {
					// 回读：切了但没生效 = 该设备上没有这个 cloth 的模型 → 上传激活的本地 VRM
					time.Sleep(2 * time.Second)
					if ch2, gerr := c.GetCharacter(b.Slot); gerr == nil && ch2.SelectedCloth != wantCloth {
						res.Fixed = append(res.Fixed, "cloth not effective on device — uploading active local VRM")
						if r.LocalVrm != "" {
							m.syncLog(r, b.MachineID, true, "", fmt.Sprintf("check[2/3] VRM uploading (slot %d)", b.Slot))
							_ = m.uploadLocalVrm(r.ID, b) // 同步上传+选中（check 已是异步，可等待）
						} else {
							res.Fixed = append(res.Fixed, "no local VRM to upload")
						}
					} else {
						res.Fixed = append(res.Fixed, "cloth switched")
					}
				}
			}
		}
		// ---- 阶段 3/3:voice 最后(试听验证/必要时克隆,分钟级);克隆回填后单独 PATCH ----
		// config PATCH / VRM apply 可能已改设备状态,voice 比对用新鲜读数。
		// 原 slot 模式不克隆、不比对音色，设备原有声音原样保留。
		if custom {
			if ch2, gerr := c.GetCharacter(b.Slot); gerr == nil {
				ch = ch2
			}
			voiceID, _, verr := m.ensureVoiceT(r, b, opts.ProbeVoice, "check")
			if verr != nil {
				res.Issues = append(res.Issues, "voice resolve failed: "+verr.Error())
			}
			// ensureVoice 可能已回填 binding，重读最新角色再下发
			if rf, ferr := m.roles.Get(roleID); ferr == nil {
				r = rf
			}
			if voiceID != "" && ch.VoiceID != voiceID {
				voiceIssue := "voice mismatch: device=" + ch.VoiceID + " expected=" + voiceID
				res.Issues = append(res.Issues, voiceIssue)
				if _, _, verr := m.ensureVoiceT(r, b, opts.ProbeVoice, "check"); verr != nil {
					res.Fixed = append(res.Fixed, "voice fix failed: "+verr.Error())
				} else {
					fresh, ferr := m.roles.Get(roleID)
					if ferr != nil {
						res.Fixed = append(res.Fixed, "voice fix failed: "+ferr.Error())
					} else {
						fv, _ := m.voiceIDFor(fresh, b)
						if err := m.ApplyRolePackage(fresh, b); err == nil {
							if ch2, g2 := c.GetCharacter(b.Slot); g2 == nil && ch2.VoiceID == fv {
								res.Fixed = append(res.Fixed, "voice applied and verified ("+fv+")")
								res.clearIssue(voiceIssue) // 已生效就不再算问题
							} else if g2 == nil {
								res.Fixed = append(res.Fixed, "voice still not effective: device="+ch2.VoiceID)
							} else {
								res.Fixed = append(res.Fixed, "voice applied (read-back failed: "+g2.Error()+")")
							}
						} else {
							res.Fixed = append(res.Fixed, "voice re-apply failed: "+err.Error())
						}
					}
				}
			}
		}
		// ---- 全部阶段结束才给最终结论(前端以 "check done" 判定完成、展示 ok) ----
		if len(res.Issues) > 0 {
			res.OK = false
			m.syncLog(r, b.MachineID, false, "check done: "+strings.Join(res.Issues, "; "), strings.Join(res.Fixed, "; "))
			m.EndOp(op.ID, strings.Join(res.Issues, "; "))
		} else {
			if custom {
				m.syncLog(r, b.MachineID, true, "", fmt.Sprintf("check done: prompt/voice/model all consistent (slot %d)", b.Slot))
			} else {
				m.syncLog(r, b.MachineID, true, "", fmt.Sprintf("check done: config consistent; original slot VRM/voice untouched (slot %d)", b.Slot))
			}
			m.EndOp(op.ID, "")
		}
		out = append(out, res)
	}
	return out
}

// VRMModelPersisted 设备端是否真的存下了该 slot/cloth 的 VRM 模型（防假 SUCCESS）。
// 按固件分层判定（不同版本行为不同）：
//   - ≥1.5.3：uploaded-models 与实际存储根一致，恒空即管线损坏 → 返回 false；
//   - 1.5.x / legacy：部分构建的 uploaded-models 扫描硬编码根路径，可能与实际存储
//     目录不一致而恒空 —— 恒空不能证明失败，返回 true（模型以 SUCCESS+SelectCloth
//     回读为准，屏幕实况为准），避免 check 陷入无意义的反复补传。
func (m *Manager) VRMModelPersisted(machineID string, slot, clothID int) (bool, error) {
	c, err := m.machines.Client(machineID)
	if err != nil {
		return false, err
	}
	// 分层判定下沉 SDK（≥1.5.3 权威 / 低层宽松），见 facade.VerifyVRMPersisted
	return c.VerifyVRMPersisted(context.Background(), &clt01.CallContext{MachineID: machineID, Slot: slot, Cloth: clothID})
}

// clearIssue 从结论里去掉一条已经修好的 issue（前缀匹配，带具体值的也算）。
// 存在原因：check 先记录问题再修复，若不清掉，最终 "check done" 会永远 FAIL。
func (r *CheckResult) clearIssue(prefix string) {
	out := r.Issues[:0]
	for _, x := range r.Issues {
		if !strings.HasPrefix(x, prefix) {
			out = append(out, x)
		}
	}
	r.Issues = out
}
