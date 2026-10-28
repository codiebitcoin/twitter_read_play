package clt01_test

import (
	. "workshop_inner_sdk"
	"context"
	"os"
	"testing"
)

// 克隆原子：分组过滤（minimax 排除）+ 克隆 + confirm 重试 + 显式应用到指定槽位
func TestLiveSetCloneVoiceAtomic(t *testing.T) {
	c := liveClient(t)
	if c.Secret == "" {
		t.Skip("no secret")
	}
	ctx := context.Background()

	groups, err := c.CloneVoiceGroups(ctx)
	if err != nil {
		t.Fatalf("CloneVoiceGroups: %v", err)
	}
	for _, g := range groups {
		t.Logf("可克隆分组: key=%s provider=%s voices=%d", g.GroupKey, g.Provider, len(g.VoiceIDs))
	}
	if len(groups) == 0 {
		t.Fatal("没有可克隆分组")
	}
	// minimax 必须被过滤掉
	if groups[0].Provider == "minimax" {
		t.Fatal("minimax 未被过滤")
	}
	// 显式指定 minimax 应被拒绝
	if _, err := c.SetCloneVoice(ctx, &CallContext{Slot: 1}, nil, "x.mp3", &CloneVoiceOptions{GroupKey: "sg_tts_en_group_minimax"}); err == nil {
		t.Error("显式 minimax 分组未被拒绝")
	} else {
		t.Logf("显式 minimax 分组已拒绝: %v", err)
	}

	if os.Getenv("CLT01_DO_CLONE") != "1" {
		t.Skip("未设置 CLT01_DO_CLONE=1，跳过真实克隆")
	}
	audio, err := os.ReadFile(envOr("CLT01_VOICE_SAMPLE"))
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	slot := 2
	res, err := c.SetCloneVoice(ctx, &CallContext{MachineID: "live", Slot: slot}, audio, "trump_clone_atomic.mp3", &CloneVoiceOptions{
		VoiceName: "atomic-clone", Apply: true,
	})
	if err != nil {
		t.Fatalf("SetCloneVoice: %v", err)
	}
	t.Logf("克隆成功: voice=%s group=%s applied=%v", res.VoiceID, res.GroupKey, res.Applied)
	ch, err := c.GetCharacterCtx(ctx, slot)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	t.Logf("slot%d 回读: voice=%q group=%q", slot, ch.VoiceID, ch.GroupKey)
	if ch.VoiceID != res.VoiceID || ch.GroupKey != res.GroupKey {
		t.Fatal("应用到槽位未生效")
	}
}
