package clt01

import (
	"context"
	"testing"
)

// Trump 角色 voice 切到 qwen 默认（sg_tts_en_group / voice 1），试听验证
func TestLiveSetQwenVoice(t *testing.T) {
	c := liveClient(t)
	if c.Secret == "" {
		t.Skip("no secret")
	}
	ctx := context.Background()
	cc := &CallContext{MachineID: "252", Slot: 1}
	changed, err := c.SetInfo(ctx, cc, &CharacterSettings{
		VoiceID:  "1",
		GroupKey: "sg_tts_en_group",
	}, false)
	if err != nil {
		t.Fatalf("SetInfo: %v", err)
	}
	t.Logf("changed=%v", changed)
	ch, _ := c.GetCharacterCtx(ctx, 1)
	t.Logf("now: name=%q voice=%q group=%q selected=%d custom=%q", ch.Name, ch.VoiceID, ch.GroupKey, ch.SelectedCloth, ch.CustomCloths)
	if ch.VoiceID != "1" || ch.GroupKey != "sg_tts_en_group" {
		t.Fatal("voice/group 未生效")
	}
	if err := c.TTSAuditionCtx(ctx, "1", "hello, this is the qwen default voice test", "sg_tts_en_group"); err != nil {
		t.Fatalf("TTSAudition: %v", err)
	}
	t.Log("TTSAudition voice=1 (qwen) ok — 设备已出声 ✅")
}
