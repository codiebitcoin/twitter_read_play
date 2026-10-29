package watch

import (
	"testing"

	"twitter_read_play/internal/machine"
	"twitter_read_play/internal/model"
	"twitter_read_play/internal/role"
	"twitter_read_play/internal/store"
)

// 同一台设备多个角色共用同一克隆分组时必须强制下发：设备每个分组只有一套「当前音色」，
// 只比对槽位配置会漏掉「配置对、出声是别人的声音」（实测 Trump slot1 被鸣人的 25 顶掉）。
func TestSharesVoiceGroup(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rm, err := role.NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	mm, err := machine.NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{st: st, roles: rm, machines: mm}
	self := &model.Role{ID: "r1", Name: "Naruto", Enabled: true, Bindings: []model.Binding{
		{MachineID: "m1", Slot: 3, Enabled: true, VoiceID: "25", GroupKey: "sg_tts_en_group"},
	}}
	other := &model.Role{ID: "r2", Name: "Trump", Enabled: true, Bindings: []model.Binding{
		{MachineID: "m1", Slot: 1, Enabled: true, VoiceID: "21", GroupKey: "sg_tts_en_group"},
	}}
	if err := m.roles.Upsert(self); err != nil {
		t.Fatal(err)
	}
	if err := m.roles.Upsert(other); err != nil {
		t.Fatal(err)
	}
	b := self.Bindings[0]
	if !m.sharesVoiceGroup(self, b) {
		t.Fatal("同机同分组应判定为共用 → 需要强制下发")
	}
	// 换台机器 → 不共用
	if m.sharesVoiceGroup(self, model.Binding{MachineID: "m2", Slot: 3, Enabled: true, GroupKey: "sg_tts_en_group"}) {
		t.Fatal("不同机器不该判定共用")
	}
	// 同机不同分组 → 不共用
	if m.sharesVoiceGroup(self, model.Binding{MachineID: "m1", Slot: 5, Enabled: true, GroupKey: "jp_tts_en_group"}) {
		t.Fatal("不同分组不该判定共用")
	}
	// 对方停用 → 不共用
	other.Enabled = false
	if err := m.roles.Upsert(other); err != nil {
		t.Fatal(err)
	}
	if m.sharesVoiceGroup(self, b) {
		t.Fatal("对方停用后不该再判定共用")
	}
}
