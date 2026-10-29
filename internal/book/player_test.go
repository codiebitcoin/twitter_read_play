package book

import (
	"encoding/json"
	"testing"
)

// 播放状态按角色隔离：别的角色不能停当前角色的朗读。
func TestCancelForOnlyOwnRole(t *testing.T) {
	p := NewPlayer(nil)
	cancelled := false
	p.runs["m1"] = &PlayRun{
		ID: "run1", RoleID: "r1", MachineID: "m1", Status: "running",
		cancel: func() { cancelled = true },
	}

	if p.CancelFor("r2", "m1") {
		t.Fatal("r2 must not stop r1's run")
	}
	if cancelled {
		t.Fatal("run cancelled by non-owner")
	}
	if !p.CancelFor("r1", "m1") {
		t.Fatal("owner must be able to stop its run")
	}
	if !cancelled {
		t.Fatal("run not cancelled by owner")
	}
}

// 序列化里带 role_id/role_name，前端才能按角色过滤播放状态。
func TestPlayRunJSONHasRole(t *testing.T) {
	b, err := json.Marshal(&PlayRun{ID: "run1", RoleID: "r1", RoleName: "Elon", MachineID: "m1"})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		RoleID   string `json:"role_id"`
		RoleName string `json:"role_name"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.RoleID != "r1" || out.RoleName != "Elon" {
		t.Fatalf("json = %s", b)
	}
}
