package clt01_test

import (
	. "workshop_inner_sdk"
	"context"
	"testing"
)

// SetInfo 真实变更往返：改 name → 回读验证 → 恢复（252 / slot 1）
func TestLiveSetInfoRealChange(t *testing.T) {
	c := liveClient(t)
	if c.Secret == "" {
		t.Skip("no secret")
	}
	ctx := context.Background()
	cc := &CallContext{MachineID: "252", Slot: 1}

	orig, err := c.GetCharacterCtx(ctx, 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	origName := orig.Name
	t.Logf("original: name=%q voice=%q selected=%d custom=%q", orig.Name, orig.VoiceID, orig.SelectedCloth, orig.CustomCloths)

	// 1) 改 name（内容真的变了，非 force 也应下发）
	changed, err := c.SetInfo(ctx, cc, &CharacterSettings{Name: origName + "-smoke"}, false)
	if err != nil {
		t.Fatalf("SetInfo(变更): %v", err)
	}
	if !changed {
		t.Fatal("内容已变更却跳过了 PUT")
	}
	ch, _ := c.GetCharacterCtx(ctx, 1)
	if ch.Name != origName+"-smoke" {
		t.Fatalf("回读 name=%q，期望 %q", ch.Name, origName+"-smoke")
	}
	t.Logf("changed=true, 回读 name=%q ✅", ch.Name)

	// 2) cloth 字段不能被动到
	if ch.SelectedCloth != orig.SelectedCloth {
		t.Errorf("selected_cloth 被改动: %d → %d", orig.SelectedCloth, ch.SelectedCloth)
	}

	// 3) 恢复原名
	changed, err = c.SetInfo(ctx, cc, &CharacterSettings{Name: origName}, false)
	if err != nil {
		t.Fatalf("SetInfo(恢复): %v", err)
	}
	if !changed {
		t.Fatal("恢复时未下发 PUT")
	}
	ch, _ = c.GetCharacterCtx(ctx, 1)
	if ch.Name != origName {
		t.Fatalf("恢复后 name=%q，期望 %q", ch.Name, origName)
	}
	t.Logf("恢复: name=%q ✅ selected=%d custom=%q", ch.Name, ch.SelectedCloth, ch.CustomCloths)
}
