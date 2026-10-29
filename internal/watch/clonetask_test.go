package watch

import (
	"errors"
	"testing"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/role"
	"twitter_read_play/internal/store"
)

func newTaskTestManager(t *testing.T) *Manager {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return &Manager{st: st}
}

// TestBeginCloneTaskMutex 同机互斥：第二个登记必须 ErrCloneBusy；
// reclone 的预登记任务在同角色/同绑定时可续用（StartReclone 后台 goroutine 取回）。
func TestBeginCloneTaskMutex(t *testing.T) {
	m := newTaskTestManager(t)
	r := &model.Role{ID: "role1", Name: "trump"}

	t1, err := m.beginCloneTask(r, "m1", 1, "reclone")
	if err != nil || t1.Status != "running" {
		t.Fatalf("第一次登记失败: task=%v err=%v", t1, err)
	}
	// 同机不同绑定 → 忙
	if _, err := m.beginCloneTask(r, "m1", 2, "reclone"); !errors.Is(err, ErrCloneBusy) {
		t.Fatalf("同机第二绑定应 ErrCloneBusy, got %v", err)
	}
	// 同机不同角色 → 忙
	if _, err := m.beginCloneTask(&model.Role{ID: "role2", Name: "musk"}, "m1", 1, "sync"); !errors.Is(err, ErrCloneBusy) {
		t.Fatalf("同机其它角色应 ErrCloneBusy, got %v", err)
	}
	// 同角色/同绑定/同触发 → 严格登记（StartReclone 路径）必须忙，绝不复用
	// （复用会又起一个克隆 goroutine，设备上并发克隆互踩）
	if _, err := m.beginCloneTask(r, "m1", 1, "reclone"); !errors.Is(err, ErrCloneBusy) {
		t.Fatalf("严格登记同绑定应 ErrCloneBusy, got %v", err)
	}
	// allowReuse=true（ensureVoiceT 克隆段取回预登记）→ 续用
	t2, err := m.beginCloneTaskOpt(r, "m1", 1, "reclone", true)
	if err != nil || t2.ID != t1.ID {
		t.Fatalf("allowReuse 应续用同任务, got task=%v err=%v", t2, err)
	}
	// 其它机器不受影响
	if _, err := m.beginCloneTask(r, "m2", 1, "sync"); err != nil {
		t.Fatalf("其它机器不应互斥: %v", err)
	}
}

// TestFinishCloneTaskIdempotent 收尾幂等：重复 finish / 收尾后再登记 不产生副作用。
func TestFinishCloneTaskIdempotent(t *testing.T) {
	m := newTaskTestManager(t)
	r := &model.Role{ID: "role1", Name: "trump"}
	t1, _ := m.beginCloneTask(r, "m1", 1, "reclone")

	m.finishCloneTask(t1.ID, "21", "grp", nil)
	m.finishCloneTask(t1.ID, "", "", errors.New("late")) // StartReclone 兜底再收 → no-op
	got := m.CloneTasks()[0]
	if got.Status != "done" || got.VoiceID != "21" || got.Error != "" {
		t.Fatalf("任务应收尾为 done/21, got %+v", got)
	}

	// 收尾后同机可再次登记
	if _, err := m.beginCloneTask(r, "m1", 1, "sync"); err != nil {
		t.Fatalf("收尾后应可再登记: %v", err)
	}
	f := m.CloneTasks()[0]
	m.finishCloneTask(f.ID, "", "", errors.New("502"))
	if f.Status != "failed" || f.Error != "502" {
		t.Fatalf("失败收尾错误, got %+v", f)
	}
}

// TestLoadCloneTasksRestartRecovery 重启恢复：持久化里残留的 running 必须标记为失败，
// 否则前端按钮会永久灰掉。
func TestLoadCloneTasksRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.New(dir)
	m := &Manager{st: st}
	r := &model.Role{ID: "role1", Name: "trump"}
	t1, _ := m.beginCloneTask(r, "m1", 1, "reclone") // 未收尾即“崩溃”

	m2 := &Manager{st: st}
	m2.loadCloneTasks()
	got := m2.CloneTasks()[0]
	if got.ID != t1.ID || got.Status != "failed" || got.Error == "" {
		t.Fatalf("重启后 running 应标记 failed, got %+v", got)
	}
}

// TestBuildAutoCloneCandidatesTopToBottom 自动分组：qwen 优先、同档按组名，
// 然后逐组一次；minimax 跳过；未广播的已知 qwen 跟其它 qwen 排在一起。
func TestBuildAutoCloneCandidatesTopToBottom(t *testing.T) {
	groups := []devVoiceGroup{
		{GroupKey: "zz_last", Provider: "other_tts", VoiceIDs: []string{"1"}},
		{GroupKey: "jp_tts_en_group_minimax", Provider: "minimax", VoiceIDs: []string{"1", "21"}},
		{GroupKey: "aa_qwen", Provider: "qwen_audio", VoiceIDs: []string{"21", "22"}},
		{GroupKey: "aa_qwen", Provider: "qwen_audio", VoiceIDs: []string{"21"}}, // 重复组只试一次
		{GroupKey: "mid_plain", Provider: "something", VoiceIDs: []string{"3"}},
	}
	cands := buildAutoCloneCandidates(groups, map[string]string{"21": "Donald Trump"})
	want := []string{"aa_qwen", "sg_tts_en_group_qwen_audio", "mid_plain", "zz_last"}
	if len(cands) != len(want) {
		t.Fatalf("应按 qwen 优先再组名试 %v, got %v", want, cands)
	}
	for i, g := range want {
		if cands[i].group != g || cands[i].slot != "22" { // 21 已占用 → 首个空闲槽 22
			t.Fatalf("第 %d 个应为 %s/slot 22, got %+v", i, g, cands[i])
		}
	}
	for _, c := range cands {
		if isMinimaxGroup(c.group) {
			t.Fatalf("自动模式也必须跳过 minimax, got %v", c)
		}
	}

	allUsed := map[string]string{"21": "a", "22": "b", "23": "c", "24": "d", "25": "e"}
	if cands := buildAutoCloneCandidates(groups, allUsed); len(cands) != 0 {
		t.Fatalf("槽全满时不应再自动分配, got %v", cands)
	}

	onlyMinimax := []devVoiceGroup{{GroupKey: "jp_tts_en_group_minimax", Provider: "minimax", VoiceIDs: []string{"21"}}}
	cands = buildAutoCloneCandidates(onlyMinimax, nil)
	if len(cands) != 1 || cands[0].group != "sg_tts_en_group_qwen_audio" || cands[0].slot != "21" {
		t.Fatalf("只有 minimax 时应回退已知 qwen 一次(空槽占用表 → 槽 21), got %v", cands)
	}
}

// TestBuildCloneCandidatesQwenFirst 默认 qwen 且**过滤 minimax**：
// minimax 组克隆必失败（云端不支持）、1.5.2 的 tts 通道合成时好时坏（空音频 → 无声），
// 所以任何入口都不得产出 minimax 候选；设备未广播 qwen 组时已知 qwen 组仍排最前。
func TestBuildCloneCandidatesQwenFirst(t *testing.T) {
	minimax := devVoiceGroup{GroupKey: "jp_tts_en_group_minimax", Provider: "minimax",
		VoiceIDs: []string{"1", "21"}}
	plain := devVoiceGroup{GroupKey: "jp_tts_en_group", Provider: "qwen_audio", VoiceIDs: []string{"1", "2"}}
	used := map[string]string{"21": "Donald Trump"}

	// 1) minimax + 无克隆槽的 qwen 组 → 已知 qwen 组候选排最前，且不含 minimax
	cands := buildCloneCandidates([]devVoiceGroup{plain, minimax}, used, "")
	if len(cands) == 0 || cands[0].group != "sg_tts_en_group_qwen_audio" {
		t.Fatalf("未广播 qwen 时应默认已知 qwen 组最前, got %v", cands)
	}
	if cands[0].slot != "22" { // 21 已被占用 → 首个空闲槽，云端要求具体数值 voice_id
		t.Fatalf("首个候选应带具体空闲槽位号 22, got %v", cands[0])
	}
	// qwen 显式槽避开已占用的 21
	for _, c := range cands {
		if c.slot == "21" {
			t.Fatalf("候选不应使用已占用槽 21: %v", cands)
		}
	}
	// minimax 候选必须被过滤掉（用户要求：默认过滤 minimax）
	for _, c := range cands {
		if isMinimaxGroup(c.group) {
			t.Fatalf("minimax 候选必须被过滤, got %v", c)
		}
	}

	// 2) 广播了 qwen 组 → 广播组排最前，且不重复添加已知同名录
	qwenAdv := devVoiceGroup{GroupKey: "sg_tts_en_group_qwen_audio", Provider: "qwen_audio", VoiceIDs: []string{"21", "22"}}
	cands = buildCloneCandidates([]devVoiceGroup{qwenAdv, minimax}, used, "")
	if cands[0].group != qwenAdv.GroupKey {
		t.Fatalf("广播 qwen 应排最前, got %v", cands[0])
	}
	n := 0
	for _, c := range cands {
		if c.group == qwenAdv.GroupKey {
			n++
		}
	}
	if n != 4 { // 空闲显式槽 22/23/24/25（21 已被占用；不再发空槽自动分配）
		t.Fatalf("广播 qwen 候选应恰为 4(空闲槽)且不重复, got %d: %v", n, cands)
	}

	// 2b) 用户手工选的组排最前；但手工选 minimax 也照样被过滤
	cands = buildCloneCandidates([]devVoiceGroup{plain, minimax}, used, "jp_tts_en_group")
	if cands[0].group != "jp_tts_en_group" {
		t.Fatalf("用户选的组应排最前, got %v", cands[:3])
	}
	cands = buildCloneCandidates([]devVoiceGroup{qwenAdv, minimax}, used, "jp_tts_en_group_minimax")
	if len(cands) == 0 || cands[0].group != qwenAdv.GroupKey {
		t.Fatalf("显式 minimax 分组必须被忽略（默认走 qwen）, got %v", cands[:3])
	}
	for _, c := range cands {
		if isMinimaxGroup(c.group) {
			t.Fatalf("显式 minimax 也被过滤, got %v", c)
		}
	}

	// 3) 只有 minimax 组 → 候选里只剩「已知 qwen 组」（默认 qwen），绝不含 minimax
	allUsed := map[string]string{"21": "a", "22": "b", "23": "c", "24": "d", "25": "e"}
	if cands := buildCloneCandidates([]devVoiceGroup{minimax}, allUsed, ""); len(cands) != 0 {
		t.Fatalf("全占用时候选应为空(防 auto 覆盖), got %v", cands)
	}
	cands = buildCloneCandidates([]devVoiceGroup{minimax}, map[string]string{}, "")
	if len(cands) == 0 {
		t.Fatal("只有 minimax 时应回退到已知 qwen 组")
	}
	for _, c := range cands {
		if isMinimaxGroup(c.group) || c.group != "sg_tts_en_group_qwen_audio" {
			t.Fatalf("只有 minimax 时只应尝试已知 qwen 组, got %v", c)
		}
	}
}

// TestLoadCloneTasksRestoresPrevVoice 重启打断的重克隆：按任务快照把原 voice 记录补回，
// 避免一次失败的重克隆（或发版重启）永久丢掉可用音色。
func TestLoadCloneTasksRestoresPrevVoice(t *testing.T) {
	st, _ := store.New(t.TempDir())
	rm, err := role.NewManager(st)
	if err != nil {
		t.Fatalf("role.NewManager: %v", err)
	}
	r := &model.Role{ID: "musk", Name: "Elon Musk",
		Bindings: []model.Binding{{MachineID: "m1", Slot: 2, Enabled: true, VoiceID: "22", GroupKey: "jp_tts_en_group"}}}
	if err := rm.Upsert(r); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	m := &Manager{st: st, roles: rm}
	// 模拟 reclone 开始：清 voice、快照进任务、进程被杀
	task, _ := m.beginCloneTask(r, "m1", 2, "reclone")
	m.setCloneTaskPrev(task.ID, "22", "jp_tts_en_group")
	if err := rm.SetBindingVoice("musk", "m1", 2, "", "jp_tts_en_group"); err != nil {
		t.Fatalf("clear: %v", err)
	}

	m2 := &Manager{st: st, roles: rm}
	m2.loadCloneTasks()
	got := rm.List()[0].Bindings[0]
	if got.VoiceID != "22" || got.GroupKey != "jp_tts_en_group" {
		t.Fatalf("重启后应恢复 voice 22/jp_tts_en_group, got %q/%q", got.VoiceID, got.GroupKey)
	}
	gt := m2.CloneTasks()[0]
	if gt.Status != "failed" || gt.VoiceID != "22" {
		t.Fatalf("任务应 failed 且带保留音色, got %+v", gt)
	}
}

func TestPollOpStatus(t *testing.T) {
	m := &Manager{}
	r := &model.Role{ID: "r1", Name: "Musk"}
	op := m.openPollOp(r)
	if op == nil || op.Kind != "poll" || op.Status != "running" {
		t.Fatalf("应登记 running 轮询, got %+v", op)
	}
	m.finishPollOp(op, PollResult{Code: "none"}, nil)
	got := m.OpQueue()
	if len(got) != 1 || got[0].Status != "done" || got[0].Detail != "none" {
		t.Fatalf("完成后应是 none, got %+v", got)
	}
	op2 := m.openPollOp(r)
	m.finishPollOp(op2, PollResult{Code: "queued", Count: 2}, nil)
	got = m.OpQueue()
	if len(got) != 1 || got[0].ID != op2.ID || got[0].Detail != "queued" || got[0].Count != 2 {
		t.Fatalf("同一角色只保留最新一轮轮询, got %+v", got)
	}
	if !m.tryPollLock("r1") {
		t.Fatal("第一次应能占住轮询")
	}
	if m.tryPollLock("r1") {
		t.Fatal("进行中的轮询不应再开一轮")
	}
	m.unlockPoll("r1")
}
