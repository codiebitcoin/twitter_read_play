package clt01_test

import (
	"context"
	"os"
	"testing"
)

// 在 252 上用 Trump 样本克隆为 voice 21（jp_tts_en_group_minimax），与 119 对齐
func TestLiveCloneTrumpVoice21(t *testing.T) {
	c := liveClient(t)
	if c.Secret == "" {
		t.Skip("no secret")
	}
	audio, err := os.ReadFile(envOr("CLT01_VOICE_SAMPLE"))
	if err != nil {
		t.Fatalf("read sample: %v", err)
	}
	up, err := c.VoiceCloneUpload(audio, "trump_can_voice_clone.mp3", "21", "sg_tts_en_group", "Donald Trump", "", 0)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	t.Logf("uploaded voice_id=%s status=%s", up.VoiceID, up.Status)
	if _, err := c.VoiceCloneConfirm(up.VoiceID, "sg_tts_en_group", true); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	t.Log("confirmed & applied ✅")

	if err := c.TTSAuditionCtx(context.Background(), up.VoiceID, "this is trump voice on the new device", "sg_tts_en_group"); err != nil {
		t.Fatalf("audition: %v", err)
	}
	t.Log("TTSAudition voice=21 ok — 设备已出声 ✅")
}
