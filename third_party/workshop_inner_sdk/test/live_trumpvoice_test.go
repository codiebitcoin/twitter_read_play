package clt01_test

import (
	"context"
	"encoding/json"
	"testing"
)

// 252 上 voice 21 / jp_tts_en_group_minimax 是否存在 + 用该 voice 试听
func TestLive252TrumpVoice(t *testing.T) {
	c := liveClient(t)
	if c.Secret == "" {
		t.Skip("no secret")
	}
	raw, err := c.VoiceGroup(context.Background())
	if err != nil {
		t.Fatalf("VoiceGroup: %v", err)
	}
	var groups []struct {
		GroupKey string   `json:"group_key"`
		VoiceIDs []string `json:"voice_ids"`
	}
	_ = json.Unmarshal(raw, &groups)
	found := false
	for _, g := range groups {
		mark := ""
		for _, v := range g.VoiceIDs {
			if v == "21" && g.GroupKey == "jp_tts_en_group_minimax" {
				found = true
				mark = " ← 目标"
			}
		}
		t.Logf("group=%s voices=%v%s", g.GroupKey, g.VoiceIDs, mark)
	}
	if !found {
		t.Errorf("252 上不存在 voice 21 / jp_tts_en_group_minimax，Trump 的 TTS 会失败")
		return
	}
	if err := c.TTSAuditionCtx(context.Background(), "21", "this is trump voice test", "jp_tts_en_group_minimax"); err != nil {
		t.Fatalf("TTSAudition: %v", err)
	}
	t.Log("TTSAudition voice=21 ok — 设备已出声")
}
