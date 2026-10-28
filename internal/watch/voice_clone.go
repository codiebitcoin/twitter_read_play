package watch

import (
	"sort"
	"strconv"
	"strings"
)

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
