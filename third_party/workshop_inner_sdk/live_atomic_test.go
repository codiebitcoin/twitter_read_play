package clt01

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// codie-black 全功能在线冒烟（原子语义逐个调用，默认跳过）：
//
//	CLT01_LIVE_ADDR=192.168.1.10:8081 \
//	CLT01_LIVE_SECRET=... CLT01_LIVE_SESSION=... CLT01_LIVE_USER=... \
//	go test -run TestLiveAtomic -v .
//
// 可选开关：
//	CLT01_LIVE_VRM=<path.vrm>  才会执行 SetVrm force 真上传（重，默认跳过）
//	CLT01_LIVE_FEEDBACK=1      才会执行 Feedback 采集（设备会收集日志，重，默认跳过）
//
// 注意：TTSAudition / PlayChat 会让设备真的出声。

func liveCreds(t *testing.T) *Client {
	c := liveClient(t)
	if c.Secret == "" {
		t.Skip("未设置 CLT01_LIVE_SECRET，跳过")
	}
	return c
}

// TestLiveAtomicQueries 只读原子：版本/音色分组/两个播放状态/槽位/角色。
func TestLiveAtomicQueries(t *testing.T) {
	ctx := context.Background()
	c := liveCreds(t)

	fw, err := c.Firmware()
	if err != nil {
		t.Logf("firmware probe warning: %v", err)
	}
	if fw == nil {
		t.Fatal("Firmware() 返回 nil")
	}
	t.Logf("firmware: version=%s drafts=%v uploadedModels=%v chunkedProbed=%v",
		fw.Version, fw.HasDrafts, fw.HasUploadedModels, fw.ChunkedProbed)
	if !fw.Version.AtLeast(VerChunkedUpload) {
		t.Fatalf("固件 %s 低于 1.5.3", fw.Version)
	}

	vg, err := c.VoiceGroup(ctx)
	if err != nil {
		t.Fatalf("VoiceGroup: %v", err)
	}
	t.Logf("VoiceGroup: %d bytes", len(vg))

	if raw, err := c.TTSAuditionState(ctx); err != nil {
		t.Errorf("TTSAuditionState: %v", err)
	} else {
		t.Logf("TTSAuditionState: %s", truncRaw(raw))
	}
	if raw, err := c.ConversationState(ctx); err != nil {
		t.Logf("ConversationState（部分构建可能无此端点，仅记录）: %v", err)
	} else {
		t.Logf("ConversationState: %s", truncRaw(raw))
	}

	slot, cloth, err := c.CurrentSlot()
	if err != nil {
		t.Fatalf("CurrentSlot: %v", err)
	}
	if slot == 0 { // 部分固件当前无激活槽位时返回 0，但 GetCharacter 要求 1-5
		slot = 1
	}
	t.Logf("CurrentSlot: slot=%d cloth=%d", slot, cloth)

	ch, err := c.GetCharacter(slot)
	if err != nil {
		t.Fatalf("GetCharacter: %v", err)
	}
	t.Logf("Character: name=%q voice=%q group=%q custom_cloths=%q selected=%d",
		ch.Name, ch.VoiceID, ch.GroupKey, ch.CustomCloths, ch.SelectedCloth)
}

// TestLiveSetInfoCheckThenForce SetInfo 检查后更新：现值原样 → changed=false；force → changed=true。
func TestLiveSetInfoCheckThenForce(t *testing.T) {
	ctx := context.Background()
	c := liveCreds(t)
	slot, _, err := c.CurrentSlot()
	if err != nil {
		t.Fatalf("CurrentSlot: %v", err)
	}
	if slot == 0 {
		slot = 1
	}
	ch, err := c.GetCharacter(slot)
	if err != nil {
		t.Fatalf("GetCharacter: %v", err)
	}
	// 用设备现值作为 want：合并后必然一致 → 不 force 应跳过
	changed, err := c.SetInfo(ctx, &CallContext{MachineID: "live", Slot: slot}, &CharacterSettings{
		Name: ch.Name, Description: ch.Description, CharacterPrompt: ch.CharacterPrompt,
		VoiceID: ch.VoiceID, GroupKey: ch.GroupKey,
		TTSVolume: ch.TTSVolume, Sex: ch.Sex, AnimationIntensity: ch.AnimationIntensity,
	}, false)
	if err != nil {
		t.Fatalf("SetInfo(check): %v", err)
	}
	if changed {
		t.Error("SetInfo 现值一致却下发了 PUT（应跳过）")
	}
	t.Log("SetInfo check: skipped as expected")

	// force=true：一致也 PUT（同值回写，无副作用）
	changed, err = c.SetInfo(ctx, &CallContext{MachineID: "live", Slot: slot}, &CharacterSettings{
		Name: ch.Name, Description: ch.Description, CharacterPrompt: ch.CharacterPrompt,
		VoiceID: ch.VoiceID, GroupKey: ch.GroupKey,
		TTSVolume: ch.TTSVolume, Sex: ch.Sex, AnimationIntensity: ch.AnimationIntensity,
	}, true)
	if err != nil {
		t.Fatalf("SetInfo(force): %v", err)
	}
	if !changed {
		t.Error("SetInfo force=true 却未下发 PUT")
	}
	t.Log("SetInfo force: applied as expected")
}

// TestLiveSetVrmSkipCheck SetVrm 检查路径：当前 slot/cloth 已有模型证据时应跳过上传。
// （不满足证据时本测试只记录，不强行上传垃圾数据。）
func TestLiveSetVrmSkipCheck(t *testing.T) {
	ctx := context.Background()
	c := liveCreds(t)
	slot, _, err := c.CurrentSlot()
	if err != nil {
		t.Fatalf("CurrentSlot: %v", err)
	}
	if slot == 0 { // 部分固件无激活槽位时返回 0
		slot = 1
	}
	ch, _ := c.GetCharacter(slot)
	if ch == nil || ch.SelectedCloth == 0 || strings.TrimSpace(ch.CustomCloths) == "" {
		t.Skipf("slot %d 当前无已应用的自定义模型证据，跳过 SetVrm 检查路径", slot)
	}
	cloth := ch.SelectedCloth
	res, err := c.SetVrm(ctx, &CallContext{Slot: slot, Cloth: cloth}, strings.TrimSpace(ch.CustomCloths), []byte{0}, false, &VRMOptions{})
	if err != nil {
		t.Fatalf("SetVrm(check): %v", err)
	}
	if res.Verify != nil {
		t.Error("SetVrm 检查命中时不应执行上传/校验")
	}
	t.Logf("SetVrm check: skipped (version=%s)", res.Version)
}

// TestLiveSetVrmForceUpload SetVrm force 真上传（需 CLT01_LIVE_VRM 指向本地 .vrm）。
func TestLiveSetVrmForceUpload(t *testing.T) {
	ctx := context.Background()
	c := liveCreds(t)
	path := os.Getenv("CLT01_LIVE_VRM")
	if path == "" {
		t.Skip("未设置 CLT01_LIVE_VRM，跳过真上传")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vrm: %v", err)
	}
	// 目标槽位必须显式指定（force 会覆盖该槽已有模型——曾因硬编码 slot 1 覆盖过 Trump）
	slot, err := strconv.Atoi(os.Getenv("CLT01_LIVE_SLOT"))
	if err != nil || slot < 1 || slot > 5 {
		t.Skip("未设置 CLT01_LIVE_SLOT（1-5），跳过真上传（force 会覆盖该槽模型）")
	}
	_, cloth, err := c.CurrentSlot()
	if err != nil || cloth == 0 {
		cloth = 2
	}
	c.SetHTTPTimeout(15 * time.Minute)
	res, err := c.SetVrm(ctx, &CallContext{Slot: slot, Cloth: cloth}, baseFilename(path), data, true, &VRMOptions{
		Apply: true,
		OnStep: func(ok bool, detail, errMsg string) {
			t.Logf("[%v] %s%s", ok, detail, errMsg)
		},
	})
	if err != nil {
		t.Fatalf("SetVrm(force): %v", err)
	}
	if res.Verify == nil || !res.Verify.Persisted {
		t.Fatalf("SetVrm force 落盘校验未通过: %+v", res.Verify)
	}
	t.Logf("SetVrm force: version=%s evidence=%s", res.Version, res.Verify.Evidence)
}

// TestLivePlayChatAndCancel PlayChat（按版本选通道，设备会出声）+ TTSAuditionState。
func TestLivePlayChatAndCancel(t *testing.T) {
	ctx := context.Background()
	c := liveCreds(t)
	channel := c.PredictPlayChatChannel()
	t.Logf("predicted channel: %s", channel)

	var voiceID, groupKey string
	if channel == ChannelTTS {
		vg, err := c.VoiceGroup(ctx)
		if err != nil {
			t.Fatalf("VoiceGroup: %v", err)
		}
		voiceID, groupKey = firstVoiceID(t, vg)
		t.Logf("tts channel voice: %s (group %s)", voiceID, groupKey)
	}
	res, err := c.PlayChat(ctx, &CallContext{MachineID: "live", Slot: 1}, "Hello, this is a live smoke test.", &PlayChatOptions{
		Language: "en", VoiceID: voiceID, GroupKey: groupKey,
	})
	if err != nil {
		t.Fatalf("PlayChat: %v", err)
	}
	if res.Channel == "" {
		t.Fatal("PlayChat 未返回通道")
	}
	t.Logf("PlayChat: channel=%s chatID=%s", res.Channel, res.ChatID)

	time.Sleep(2 * time.Second)
	if raw, err := c.StateForChannel(ctx, res.Channel); err != nil {
		t.Errorf("StateForChannel(%s): %v", res.Channel, err)
	} else {
		t.Logf("StateForChannel: %s", truncRaw(raw))
	}

	if res.ChatID != "" {
		if err := c.CancelChat(res.ChatID); err != nil {
			t.Logf("CancelChat（可能已播完，仅记录）: %v", err)
		} else {
			t.Log("CancelChat: ok")
		}
	}
}

// TestLiveTTSAudition 试听（设备出声）。
func TestLiveTTSAudition(t *testing.T) {
	ctx := context.Background()
	c := liveCreds(t)
	vg, err := c.VoiceGroup(ctx)
	if err != nil {
		t.Fatalf("VoiceGroup: %v", err)
	}
	voiceID, groupKey := firstVoiceID(t, vg)
	if err := c.TTSAuditionCtx(ctx, voiceID, "audition smoke test", groupKey); err != nil {
		t.Fatalf("TTSAudition: %v", err)
	}
	t.Logf("TTSAudition: ok (voice %s)", voiceID)
	time.Sleep(2 * time.Second)
	if raw, err := c.TTSAuditionState(ctx); err != nil {
		t.Errorf("TTSAuditionState after audition: %v", err)
	} else {
		t.Logf("TTSAuditionState: %s", truncRaw(raw))
	}
}

// TestLiveFeedback 反馈包采集（重，设备会收集日志；需显式开 CLT01_LIVE_FEEDBACK=1）。
func TestLiveFeedback(t *testing.T) {
	ctx := context.Background()
	if os.Getenv("CLT01_LIVE_FEEDBACK") != "1" {
		t.Skip("未设置 CLT01_LIVE_FEEDBACK=1，跳过反馈包采集")
	}
	c := liveCreds(t)
	res, err := c.Feedback(ctx, &FeedbackOptions{
		Title:             "sdk live smoke",
		Description:       "automated atomic feedback test",
		IncludeBasicLogs:  true,
		IncludeSystemLogs: true,
		Timeout:           6 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Feedback: %v", err)
	}
	t.Logf("Feedback: report=%s archive=%d bytes", res.ReportID, len(res.Archive))
	if len(res.Archive) == 0 {
		t.Fatal("归档为空")
	}
}

func truncRaw(raw []byte) string {
	s := string(raw)
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// firstVoiceID 从 VoiceGroup 响应里取第一个可用 voice_id + group_key。
func firstVoiceID(t *testing.T, raw []byte) (voiceID, groupKey string) {
	t.Helper()
	var groups []struct {
		GroupKey string   `json:"group_key"`
		VoiceIDs []string `json:"voice_ids"`
	}
	if err := json.Unmarshal(raw, &groups); err == nil && len(groups) > 0 {
		for _, g := range groups {
			if len(g.VoiceIDs) > 0 {
				return g.VoiceIDs[0], g.GroupKey
			}
		}
	}
	var wrapped struct {
		Data []struct {
			GroupKey string   `json:"group_key"`
			VoiceIDs []string `json:"voice_ids"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		for _, g := range wrapped.Data {
			if len(g.VoiceIDs) > 0 {
				return g.VoiceIDs[0], g.GroupKey
			}
		}
	}
	t.Skipf("VoiceGroup 响应中未找到可用 voice: %s", truncRaw(raw))
	return "", ""
}

func baseFilename(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
