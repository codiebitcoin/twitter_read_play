package role

import (
	"os"
	"testing"

	"twitter_read_play/internal/assets"
	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
)

// 空库初始化：插入一条普通角色（SeedRoleID），并把 go:embed 的资源拷到数据目录。
// 该角色没有任何特殊标记 —— 与用户自建角色完全一致。
func TestEmptyDBSeedsInitialRoleAndAssets(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	m, err := NewManager(st)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	r, err := m.Get(assets.SeedRoleID)
	if err != nil {
		t.Fatalf("空库没有插入初始角色: %v", err)
	}
	if r.Name == "" || r.Username == "" {
		t.Errorf("初始角色记录不完整: %+v", r)
	}
	if r.LocalVrm != "roles/"+assets.SeedRoleID+"/model.vrm" || r.LocalVoice == "" {
		t.Errorf("内置资源没登记: vrm=%q voice=%q", r.LocalVrm, r.LocalVoice)
	}
	assertSize(t, dir+"/roles/"+assets.SeedRoleID+"/model.vrm", len(assets.SeedVRM))
	assertSize(t, dir+"/roles/"+assets.SeedRoleID+"/voice.mp3", len(assets.SeedVoice))
	// 内置角色必须默认启用：缺省 false 会让它一出生就显示「停用」且不参与自动播报
	if !r.Enabled {
		t.Errorf("内置角色应默认启用(enabled=true), got %+v", r)
	}

	// 幂等：二次初始化不重复插入、不覆盖
	m2, err := NewManager(st)
	if err != nil {
		t.Fatalf("NewManager(2): %v", err)
	}
	if _, err := m2.Get(assets.SeedRoleID); err != nil {
		t.Fatalf("二次初始化后初始角色丢了: %v", err)
	}
	assertSize(t, dir+"/roles/"+assets.SeedRoleID+"/model.vrm", len(assets.SeedVRM))
}

// 关键回归：库里有数据时，NewManager 绝不允许删除/改动任何角色 ——
// 历史事故：曾按「内置 demo 标记」在启动时清理角色，用户真在用的角色被连
// 绑定 voice_id/group_key、本地 VRM/语音样本、推文缓存一起删掉。
func TestExistingRolesAreNeverDeleted(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	legacy := []*model.Role{
		{
			ID: "trump", Name: "Donald Trump", Username: "realDonaldTrump",
			Prompt: "You are Trump.",
			Bindings: []model.Binding{{
				MachineID: "m1", Slot: 1, Enabled: true,
				VoiceID: "21", GroupKey: "jp_tts_en_group_minimax",
			}},
			LocalVrm:   "roles/trump/model.vrm",
			LocalVoice: "roles/trump/voice.mp3",
			LocalVrms:  []model.VrmAsset{{File: "roles/trump/model.vrm", ClothID: 2}},
		},
		{ID: assets.SeedRoleID, Name: "Elon Musk", Username: "elonmusk"},
		{ID: "r-1", Name: "My Role"},
	}
	if err := st.Save("roles", legacy); err != nil {
		t.Fatalf("写入老数据: %v", err)
	}
	assetDir := dir + "/roles/trump"
	if err := os.MkdirAll(assetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assetDir+"/model.vrm", []byte("trump-vrm"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assetDir+"/voice.mp3", []byte("trump-voice"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveTweets("trump", []store.TweetRow{{ID: "1", Text: "hi"}}); err != nil {
		t.Fatal(err)
	}

	m, err := NewManager(st)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	for _, id := range []string{"trump", assets.SeedRoleID, "r-1"} {
		if _, err := m.Get(id); err != nil {
			t.Errorf("角色 %q 被删了: %v", id, err)
		}
	}
	r, err := m.Get("trump")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Bindings) != 1 || r.Bindings[0].VoiceID != "21" || r.Bindings[0].GroupKey != "jp_tts_en_group_minimax" {
		t.Errorf("绑定数据被改动: %+v", r.Bindings)
	}
	if r.LocalVrm == "" || r.LocalVoice == "" {
		t.Errorf("本地资产指针被清掉: vrm=%q voice=%q", r.LocalVrm, r.LocalVoice)
	}
	if b, err := os.ReadFile(assetDir + "/model.vrm"); err != nil || string(b) != "trump-vrm" {
		t.Errorf("资产文件被覆盖/删除: %v %q", err, b)
	}
	if n := st.TweetCount("trump"); n != 1 {
		t.Errorf("推文缓存被清掉: %d", n)
	}
	if _, err := os.Stat(dir + "/roles/" + assets.SeedRoleID + "/model.vrm"); err == nil {
		t.Error("已有数据的库不该再拷内置资源")
	}
}

// 角色上不存在任何特殊标记：初始角色可以被正常改名/绑定/删除。
func TestRolesHaveNoSpecialFlags(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	m, err := NewManager(st)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	r, err := m.Get(assets.SeedRoleID)
	if err != nil {
		t.Fatal(err)
	}
	r.Name = "Renamed"
	r.Bindings = []model.Binding{{MachineID: "m1", Slot: 3, Enabled: true}}
	if err := m.Upsert(r); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := m.Get(assets.SeedRoleID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Renamed" || len(got.Bindings) != 1 {
		t.Errorf("初始角色不能像普通角色一样修改: %+v", got)
	}
	// 库非空时删掉初始角色：不会再自动插回来
	if err := m.Upsert(&model.Role{ID: "other", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(assets.SeedRoleID); err != nil {
		t.Fatalf("初始角色不能删除: %v", err)
	}
	if _, err := m.Get(assets.SeedRoleID); err == nil {
		t.Error("初始角色删除后仍然存在")
	}
	m2, err := NewManager(st)
	if err != nil {
		t.Fatalf("NewManager(2): %v", err)
	}
	if _, err := m2.Get(assets.SeedRoleID); err == nil {
		t.Error("库非空时不该重新插入初始角色")
	}
	if _, err := m2.Get("other"); err != nil {
		t.Errorf("其它角色被影响: %v", err)
	}
}

// 同一台机器的同一槽位只能绑一个角色（否则互相覆盖 character/VRM/voice）。
func TestSlotConflictRejected(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	m, err := NewManager(st)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Upsert(&model.Role{
		ID: "a", Name: "A",
		Bindings: []model.Binding{{MachineID: "m1", Slot: 1, Enabled: true}},
	}); err != nil {
		t.Fatal(err)
	}
	err = m.Upsert(&model.Role{
		ID: "b", Name: "B",
		Bindings: []model.Binding{{MachineID: "m1", Slot: 1, Enabled: true}},
	})
	if err == nil {
		t.Fatal("同槽位绑定没有被拒绝")
	}
	if err := m.Upsert(&model.Role{
		ID: "b", Name: "B",
		Bindings: []model.Binding{{MachineID: "m1", Slot: 2, Enabled: true}},
	}); err != nil {
		t.Fatalf("不同槽位不该被拒绝: %v", err)
	}
}

func assertSize(t *testing.T, path string, want int) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if st.Size() != int64(want) {
		t.Errorf("%s 大小 = %d, want %d（内置资源没拷对）", path, st.Size(), want)
	}
}

// Get 返回快照：调用方改返回值（或改写它并发读到的旧对象）不能影响角色库。
// 历史事故：GET 与 PUT 并发时，前端读到的旧对象被 Upsert 后，下一次保存把新字段冲回旧值。
func TestGetReturnsSnapshot(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Get("musk")
	if err != nil {
		t.Fatal(err)
	}
	r.Prompt = "MUTATED"
	if _, ok := r.Bindings, true; ok {
		r.Bindings = append(r.Bindings, model.Binding{MachineID: "x", Slot: 9})
	}
	again, _ := m.Get("musk")
	if again.Prompt == "MUTATED" || len(again.Bindings) != 0 {
		t.Fatalf("Get must return a snapshot, got prompt=%q bindings=%v", again.Prompt, again.Bindings)
	}
}
