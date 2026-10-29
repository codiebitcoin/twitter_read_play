package clt01

import (
	"context"
	"os"
	"testing"
)

// 把 src 设备 slot 的角色信息整套 SetInfo 到 dst 设备 slot（cloth 不动）
func TestLiveCopyCharacterInfo(t *testing.T) {
	ctx := context.Background()
	srcAddr := envOr("CLT01_SRC_ADDR")
	srcSecret := envOr("CLT01_SRC_SECRET")
	if srcAddr == "" || srcSecret == "" {
		t.Skip("no CLT01_SRC_ADDR/CLT01_SRC_SECRET")
	}
	src, err0 := New(srcAddr)
	if err0 != nil {
		t.Fatalf("src new: %v", err0)
	}
	src.Apply(&Credentials{AuthScope: AuthScopePermanent, LocalSecret: srcSecret, DeviceAddr: srcAddr})
	dst := liveClient(t)

	trump, err := src.GetCharacterCtx(ctx, 1)
	if err != nil {
		t.Fatalf("src get: %v", err)
	}
	t.Logf("src(119 slot1): name=%q desc=%q prompt=%d字节 voice=%q group=%q vol=%d sex=%d anim=%d",
		trump.Name, trump.Description, len(trump.CharacterPrompt), trump.VoiceID, trump.GroupKey, trump.TTSVolume, trump.Sex, trump.AnimationIntensity)

	cc := &CallContext{MachineID: "252", Slot: 1}
	changed, err := dst.SetInfo(ctx, cc, &CharacterSettings{
		Name:               trump.Name,
		Description:        trump.Description,
		CharacterPrompt:    trump.CharacterPrompt,
		VoiceID:            trump.VoiceID,
		GroupKey:           trump.GroupKey,
		TTSVolume:          trump.TTSVolume,
		Sex:                trump.Sex,
		AnimationIntensity: trump.AnimationIntensity,
	}, false)
	if err != nil {
		t.Fatalf("dst SetInfo: %v", err)
	}
	t.Logf("changed=%v", changed)

	ch, err := dst.GetCharacterCtx(ctx, 1)
	if err != nil {
		t.Fatalf("dst get: %v", err)
	}
	t.Logf("dst(252 slot1): name=%q voice=%q group=%q prompt=%d字节 selected=%d custom=%q",
		ch.Name, ch.VoiceID, ch.GroupKey, len(ch.CharacterPrompt), ch.SelectedCloth, ch.CustomCloths)

	if ch.Name != trump.Name || ch.VoiceID != trump.VoiceID || ch.GroupKey != trump.GroupKey {
		t.Fatal("回读不一致")
	}
	if ch.SelectedCloth != 2 || ch.CustomCloths == "" {
		t.Errorf("模型状态被波及: selected=%d custom=%q", ch.SelectedCloth, ch.CustomCloths)
	}
	t.Log("✅ 整套角色信息同步完成，模型未受影响")
}

func envOr(k string) string { return os.Getenv(k) }
