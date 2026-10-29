package clt01

import (
	"context"
	"testing"
	"time"
)

// 252 音色试听：用 slot1 角色当前音色（qwen）试听 + 状态跟踪，另试两个 qwen stock voice
func TestAudition252(t *testing.T) {
	c := liveClient(t)
	if c.Secret == "" {
		t.Skip("no secret")
	}
	ctx := context.Background()

	ch, err := c.GetCharacterCtx(ctx, 1)
	if err != nil {
		t.Fatalf("get char: %v", err)
	}
	t.Logf("slot1 当前音色: voice=%q group=%q (name=%q)", ch.VoiceID, ch.GroupKey, ch.Name)

	// 1) 角色当前音色试听
	if err := c.TTSAuditionCtx(ctx, ch.VoiceID, "试听测试，这是二百五十二号设备的当前角色音色", ch.GroupKey); err != nil {
		t.Fatalf("audition(当前音色 %s/%s): %v", ch.VoiceID, ch.GroupKey, err)
	}
	t.Logf("✅ 当前音色试听下发成功 (%s/%s)", ch.VoiceID, ch.GroupKey)
	for i := 0; i < 4; i++ {
		time.Sleep(1 * time.Second)
		if raw, err := c.TTSAuditionState(ctx); err == nil {
			t.Logf("  [%ds] tts state: %s", i+1, string(raw))
		}
	}

	// 2) qwen 组其它 stock voice 抽查（3、7）
	for _, v := range []string{"3", "7"} {
		if err := c.TTSAuditionCtx(ctx, v, "试听 "+v+" 号音色", "sg_tts_en_group"); err != nil {
			t.Errorf("audition(voice=%s): %v", v, err)
			continue
		}
		t.Logf("✅ qwen stock voice %s 试听下发成功", v)
		time.Sleep(2 * time.Second)
	}
}
