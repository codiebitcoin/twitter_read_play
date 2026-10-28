package clt01_test

import (
	"context"
	"testing"
)

// 播报后核对 252 slot1 角色与播放状态
func TestVerify252AfterBroadcast(t *testing.T) {
	c := liveClient(t)
	if c.Secret == "" {
		t.Skip("no secret")
	}
	ctx := context.Background()
	if ch, err := c.GetCharacterCtx(ctx, 1); err == nil {
		t.Logf("252 slot1: name=%q voice=%q group=%q prompt=%d字节 selected=%d custom=%q",
			ch.Name, ch.VoiceID, ch.GroupKey, len(ch.CharacterPrompt), ch.SelectedCloth, ch.CustomCloths)
	} else {
		t.Fatalf("get char: %v", err)
	}
	if raw, err := c.ConversationState(ctx); err == nil {
		t.Logf("252 conversation state: %s", string(raw))
	}
	if raw, err := c.TTSAuditionState(ctx); err == nil {
		t.Logf("252 tts state: %s", string(raw))
	}
	if ch, err := c.Firmware(); err == nil {
		t.Logf("252 firmware: %s", ch.Version)
	}
	t.Logf("predicted playchat channel: %s", c.PredictPlayChatChannel())
}
