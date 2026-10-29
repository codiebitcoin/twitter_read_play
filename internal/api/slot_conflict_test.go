package api

import (
	"strings"
	"testing"

	"twitter_read_play/internal/model"
)

// 默认不允许两个角色绑同一台机器的同一个 slot：
// 设备同一时刻只有一个激活槽位 + 一套角色/音色，抢同一槽位会互相覆盖（实测出现
// 「角色切了、声音还是别人的」）。显式 allow_slot_conflict=true 才放行。
func TestSlotBindingConflict(t *testing.T) {
	s := newQueueTestServer(t)
	if err := s.Roles.Upsert(&model.Role{Name: "Trump", Username: "trump", Enabled: true,
		Bindings: []model.Binding{{MachineID: "m1", Slot: 1, Enabled: true, VoiceID: "22"}}}); err != nil {
		t.Fatal(err)
	}
	// 同机同槽位 → 报冲突
	if got := s.slotBindingConflicts("", []model.Binding{{MachineID: "m1", Slot: 1, Enabled: true}}); len(got) != 1 {
		t.Fatalf("应检测到 1 个冲突，实际 %v", got)
	} else if !strings.Contains(got[0], "Trump") {
		t.Fatalf("冲突描述应带占用者角色名，实际 %q", got[0])
	}
	// 换个 slot / 换台机器 → 不冲突
	if got := s.slotBindingConflicts("", []model.Binding{{MachineID: "m1", Slot: 2, Enabled: true}}); len(got) != 0 {
		t.Fatalf("不同 slot 不该冲突：%v", got)
	}
	if got := s.slotBindingConflicts("", []model.Binding{{MachineID: "m2", Slot: 1, Enabled: true}}); len(got) != 0 {
		t.Fatalf("不同机器不该冲突：%v", got)
	}
	// 自己更新自己 → 不算冲突
	var r *model.Role
	for _, x := range s.Roles.List() {
		if x.Name == "Trump" {
			r = x
		}
	}
	if r == nil {
		t.Fatal("找不到测试角色")
	}
	if got := s.slotBindingConflicts(r.ID, []model.Binding{{MachineID: "m1", Slot: 1, Enabled: true}}); len(got) != 0 {
		t.Fatalf("自己改自己不该冲突：%v", got)
	}
	// 同一份提交里重复绑同一槽位 → 也要拦
	if got := s.slotBindingConflicts("", []model.Binding{
		{MachineID: "m1", Slot: 3, Enabled: true}, {MachineID: "m1", Slot: 3, Enabled: true}}); len(got) == 0 {
		t.Fatal("同一提交内重复绑定应报冲突")
	}
	// 未启用的旧绑定不参与冲突判定（停用角色不播报）
	if err := s.Roles.Upsert(&model.Role{Name: "Off", Username: "off", Enabled: false,
		Bindings: []model.Binding{{MachineID: "m3", Slot: 1, Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	if got := s.slotBindingConflicts("", []model.Binding{{MachineID: "m3", Slot: 1, Enabled: true}}); len(got) != 0 {
		t.Fatalf("停用角色不该算占用：%v", got)
	}
}
