package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
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

// knownQwenCloneGroups 已知支持克隆的 qwen 组：设备未广播时也默认优先尝试。
// 依据：2026-10-12 e2e 实测 codie-black/neo.h 上 sg_tts_en_group_qwen_audio 克隆成功
// （voice 21/22）；固件升级后设备不再广播该组，但克隆接口按 group_key 直接转发云端、
// 不校验是否在广播列表里 → 显式按已知组名先试。
var knownQwenCloneGroups = []string{"sg_tts_en_group_qwen_audio"}

// maxVoiceSampleBytes 克隆样本上传上限（留足余量：设备端 10 MiB 上限，云端按秒计费）。
const maxVoiceSampleBytes = 6 << 20

// trimVoiceSample 从 MP3 中截出前 limit 字节（按 MPEG 帧边界找齐，避免半个帧造成杂音）。
// 不重编码、不依赖 ffmpeg：ID3 头之后逐帧跳，截到最后不超过 limit 的帧结束位置。
func trimVoiceSample(data []byte, limit int) []byte {
	if len(data) <= limit {
		return data
	}
	// 跳过 ID3v2 头（"ID3" + 版本 + flags + 4 字节 synchsafe 长度）
	i := 0
	if len(data) > 10 && string(data[:3]) == "ID3" {
		n := int(data[6])<<21 | int(data[7])<<14 | int(data[8])<<7 | int(data[9])
		i = 10 + n
	}
	// 找第一个帧同步
	for i+4 < len(data) && !(data[i] == 0xFF && data[i+1]&0xE0 == 0xE0) {
		i++
	}
	end := i
	for i+4 < len(data) {
		if data[i] != 0xFF || data[i+1]&0xE0 != 0xE0 {
			i++
			continue
		}
		// 帧长：MPEG1/2/2.5 Layer III 的常见位率表
		br := int(data[i+2]>>4) & 0x0F
		sr := int(data[i+2]>>2) & 0x03
		pad := int(data[i+2]>>1) & 0x01
		if br == 0 || br == 0x0F || sr == 3 {
			i++
			continue
		}
		mpeg1 := data[i+1]&0x18 == 0x18
		isLayer3 := data[i+1]&0x06 == 0x02
		var frame int
		if mpeg1 && isLayer3 {
			bitrates := [...]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
			srates := [...]int{44100, 48000, 32000}
			frame = 144000*bitrates[br]/srates[sr] + pad
		} else if isLayer3 {
			bitrates := [...]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
			srates := [...]int{22050, 24000, 16000}
			frame = 72000*bitrates[br]/srates[sr] + pad
		}
		if frame <= 0 {
			i++
			continue
		}
		if i+frame > limit {
			break
		}
		i += frame
		end = i
	}
	if end == 0 {
		return data[:limit]
	}
	return data[:end]
}

// cloneCand 一个克隆候选（组/槽位）。slot 一律是具体数值：云端已不接受空 voice_id
// （INVALID_VOICE_SLOT: voice_id must be numeric），不再让设备「自动分配」。
type cloneCand struct{ group, slot string }

func slotLabel(slot string) string {
	if slot == "" {
		return "auto"
	}
	return slot
}

// freeCloneSlots 本机未被其它绑定占用的克隆槽（21-25，升序）。克隆上传必须带具体数值
// voice_id：云端已拒绝空/auto 槽位，所以由本端挑定空闲槽再下发。
func freeCloneSlots(used map[string]string) []string {
	var out []string
	for n := 21; n <= 25; n++ {
		slot := strconv.Itoa(n)
		if _, taken := used[slot]; !taken {
			out = append(out, slot)
		}
	}
	return out
}

// buildAutoCloneCandidates 自动分组（绑定 group_key 为空：界面「自动」）。
// force_clone 与第一次克隆走这条：qwen 排前面，同档按组名排序，然后从上到下每组只试一次。
// 每个候选都带首个空闲克隆槽（具体数值，云端要求）；minimax 跳过（云端不支持克隆）。
// 未广播的已知 qwen 组跟其它 qwen 一起排。没有空闲槽或全部分组都失败时返回空/由调用方报错。
func buildAutoCloneCandidates(groups []devVoiceGroup, used map[string]string) []cloneCand {
	free := freeCloneSlots(used)
	if len(free) == 0 {
		return nil // 没有可下发的空闲槽位号：交给调用方报错
	}
	type item struct {
		key  string
		qwen bool
	}
	var items []item
	seen := map[string]bool{}
	add := func(key string, qwen bool) {
		key = strings.TrimSpace(key)
		if key == "" || isMinimaxGroup(key) || seen[key] {
			return
		}
		seen[key] = true
		items = append(items, item{key, qwen})
	}
	for _, g := range groups {
		if g.minimax() {
			continue
		}
		add(g.GroupKey, g.isQwenLike())
	}
	for _, q := range knownQwenCloneGroups {
		add(q, true)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].qwen != items[j].qwen {
			return items[i].qwen
		}
		return items[i].key < items[j].key
	})
	cands := make([]cloneCand, len(items))
	for i, it := range items {
		cands[i] = cloneCand{group: it.key, slot: free[0]}
	}
	return cands
}

// buildCloneCandidates 生成克隆候选列表：**默认 qwen，minimax 一律过滤掉**，
// 用户显式选的可克隆组排最前：
//  0. preferGroup：绑定上手工选的 group_key（minimax 直接丢弃，见 isMinimaxGroup）
//  1. 设备广播的 qwen 组（provider=qwen_audio / 组名含 qwen，且含 21-25 克隆槽）
//  2. 未广播但已知的 qwen 组（knownQwenCloneGroups）
//  3. 其余非 minimax 的可克隆广播组
//
// 每组按未被本机其它绑定占用的克隆槽 21-25 升序各出一个候选（云端要求具体数值
// voice_id，不再发空槽让设备自选）。全部槽位被占用时候选为空，由调用方报错。
func buildCloneCandidates(groups []devVoiceGroup, used map[string]string, preferGroup string) []cloneCand {
	capable := func(g devVoiceGroup) bool {
		for _, id := range g.VoiceIDs {
			if n, err := strconv.Atoi(strings.TrimSpace(id)); err == nil && n >= 21 && n <= 25 {
				return true
			}
		}
		return false
	}
	sort.Slice(groups, func(i, j int) bool { // 广播组内 qwen 优先，再按组名稳定排序
		if a, b := groups[i].isQwenLike(), groups[j].isQwenLike(); a != b {
			return a
		}
		return groups[i].GroupKey < groups[j].GroupKey
	})
	freeSlot := func(slot string) bool { _, taken := used[slot]; return !taken }
	advertised := map[string]bool{}
	for _, g := range groups {
		advertised[g.GroupKey] = true
	}

	var cands []cloneCand
	seen := map[string]bool{}
	add := func(group, slot string) {
		if group == "" || isMinimaxGroup(group) { // 双保险：任何入口都不给 minimax 候选
			return
		}
		k := group + "/" + slot
		if !seen[k] {
			seen[k] = true
			cands = append(cands, cloneCand{group, slot})
		}
	}
	addGroup := func(key string) {
		for n := 21; n <= 25; n++ {
			if slot := strconv.Itoa(n); freeSlot(slot) {
				add(key, slot)
			}
		}
	}
	// 0) 用户手工选的组：最先试（即使设备没广播、即使不含克隆槽 ——
	//    克隆接口按 group_key 直接转发云端，用户的选择必须生效；minimax 例外，直接过滤）
	if preferGroup != "" {
		addGroup(preferGroup)
	}
	for _, g := range groups { // 1) 广播的 qwen 组
		if capable(g) && !g.minimax() && g.isQwenLike() {
			addGroup(g.GroupKey)
		}
	}
	for _, q := range knownQwenCloneGroups { // 2) 未广播的已知 qwen 组
		if !advertised[q] {
			addGroup(q)
		}
	}
	for _, g := range groups { // 3) 其余非 minimax 的可克隆广播组
		if capable(g) && !g.minimax() && !g.isQwenLike() {
			addGroup(g.GroupKey)
		}
	}
	return cands
}

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
