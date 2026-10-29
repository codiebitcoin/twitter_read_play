package machine

import (
	"testing"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
)

// 角色被删除后必须把它从机器的启用角色白名单里摘掉：白名单里只剩一个不存在的 id，
// 那台机器会判定「本角色未启用」而静默停止播报；全摘空则回到「未配置 = 全部启用」。
func TestDropEnabledRoles(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	ms := []*model.Machine{
		{ID: "m1", Name: "A", Addr: "10.0.0.1:8081", EnabledRoles: []string{"olddemo", "r-1"}},
		{ID: "m2", Name: "B", Addr: "10.0.0.2:8081", EnabledRoles: []string{"olddemo"}},
		{ID: "m3", Name: "C", Addr: "10.0.0.3:8081"},
	}
	if err := st.Save("machines", ms); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.DropEnabledRoles([]string{"olddemo"}); err != nil {
		t.Fatalf("DropEnabledRoles: %v", err)
	}
	if a, _ := m.Get("m1"); len(a.EnabledRoles) != 1 || a.EnabledRoles[0] != "r-1" {
		t.Errorf("m1 白名单 = %v, want [r-1]", a.EnabledRoles)
	}
	if b, _ := m.Get("m2"); len(b.EnabledRoles) != 0 {
		t.Errorf("m2 白名单应被摘空（回到全部启用）= %v", b.EnabledRoles)
	}
	if c, _ := m.Get("m3"); len(c.EnabledRoles) != 0 {
		t.Errorf("m3 本来就没配白名单，不该被改动 = %v", c.EnabledRoles)
	}

	// 落盘后重启也要保持
	m2, err := NewManager(st)
	if err != nil {
		t.Fatalf("重新 NewManager: %v", err)
	}
	if a, _ := m2.Get("m1"); len(a.EnabledRoles) != 1 || a.EnabledRoles[0] != "r-1" {
		t.Errorf("重启后 m1 白名单 = %v, want [r-1]", a.EnabledRoles)
	}
	if b, _ := m2.Get("m2"); len(b.EnabledRoles) != 0 {
		t.Errorf("重启后 m2 白名单 = %v, want 空", b.EnabledRoles)
	}
}
