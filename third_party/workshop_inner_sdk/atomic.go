package clt01

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// atomic.go — 原子语义操作层（宿主唯一入口，版本差异收敛在 SDK）：
//
//	SetVrm             设置模型   ：检查不匹配才上传+应用；force=true 强制重传。实时返回
//	SetInfo            设角色信息 ：检查不匹配才 PUT character；force=true 强制下发
//	PlayChat           播报       ：≥1.5.3 → /data/user/chat/turns；<1.5.3 → tts_audition
//	CancelChat         取消播报
//	TTSAudition        试听（细粒度，单独调用）
//	TTSAuditionState   试听播放状态
//	ConversationState  会话播放状态（chat 通道）
//	VoiceGroup         音色分组查询
//	Feedback           反馈包采集（创建→轮询→提交→下载 一气呵成）
//
// 已移除：SetCloneVoice（克隆保留细粒度两步 VoiceCloneUpload/VoiceCloneConfirm）。

// ---- SetVrm ----

// SetVrm 设置角色模型（原子，实时返回）：
//
//	force=false（默认）：先检查设备当前状态 —— slot 的 selected_cloth 已是目标 cloth
//	                     且 uploaded-models / custom_cloths 中已有该模型 → 视为已匹配，
//	                     直接返回不上传（changed=false，不打断设备）；
//	                     不匹配才走完整上传+应用+分层落盘校验（≥1.5.3 分块 / 1.5.2 async）。
//	force=true        ：即使已匹配也重新上传+应用（同名文件内容更新的场景）。
//
// 返回的 VRMResult.Verify 标明落盘校验结果；changed=false 时 Verify 为 nil。
func (c *Client) SetVrm(ctx context.Context, cc *CallContext, filename string, data []byte, force bool, opt *VRMOptions) (*VRMResult, error) {
	cc = cc.ensure()
	if err := cc.validate(true); err != nil {
		return nil, err
	}
	if !force {
		if matched, err := c.vrmMatches(ctx, cc); err == nil && matched {
			if opt == nil {
				opt = &VRMOptions{}
			}
			opt.logf(true, fmt.Sprintf("%s VRM already set (%s) — skip upload", cc.prefix(), filename), "")
			return &VRMResult{Version: c.detectVersion()}, nil
		}
	}
	return c.uploadVRM(ctx, cc, filename, data, opt)
}

// vrmMatches 设备当前 slot/cloth 是否已经是目标模型（selected_cloth 命中且
// uploaded-models 或 custom_cloths 里有该模型证据）。
func (c *Client) vrmMatches(ctx context.Context, cc *CallContext) (bool, error) {
	ch, err := c.GetCharacterCtx(ctx, cc.Slot)
	if err != nil {
		return false, err
	}
	if cc.Cloth > 0 && ch.SelectedCloth != cc.Cloth {
		return false, nil
	}
	if ch.CustomCloths != "" {
		return true, nil
	}
	if models, merr := c.GetRawCtx(ctx, "/v1/api/data/user/cloth/uploaded-models"); merr == nil && uploadedModelPresentRaw(models, cc.Slot, cc.Cloth) {
		return true, nil
	}
	return false, nil
}

// ---- SetInfo ----

// SetInfo 设置角色信息（原子，检查后更新，实时返回）：
//
//	want 中非零字段合并到设备现有配置（零值 = 沿用设备现值）；
//	合并结果与现值一致且 force=false → 跳过 PUT（changed=false，不打断设备）；
//	force=true：即使一致也 PUT。
//	cloth 相关字段（selected/available/custom/link）一律剥离 —— 模型走 SetVrm。
//
// 返回 changed（是否真的下发了 PUT）。
func (c *Client) SetInfo(ctx context.Context, cc *CallContext, want *CharacterSettings, force bool) (changed bool, err error) {
	cc = cc.ensure()
	if err := cc.validate(false); err != nil {
		return false, err
	}
	if want == nil {
		want = &CharacterSettings{}
	}
	ch, err := c.GetCharacterCtx(ctx, cc.Slot)
	if err != nil {
		return false, fmt.Errorf("clt01: get character: %w", err)
	}
	merged := *ch
	if want.Name != "" {
		merged.Name = want.Name
	}
	if want.Description != "" {
		merged.Description = want.Description
	}
	if want.CharacterPrompt != "" {
		merged.CharacterPrompt = want.CharacterPrompt
	}
	if want.VoiceID != "" {
		merged.VoiceID = want.VoiceID
	}
	if want.GroupKey != "" {
		merged.GroupKey = want.GroupKey
	}
	if want.TTSVolume > 0 {
		merged.TTSVolume = want.TTSVolume
	}
	if want.Sex > 0 {
		merged.Sex = want.Sex
	}
	if want.AnimationIntensity > 0 {
		merged.AnimationIntensity = want.AnimationIntensity
	}
	if !force && infoEqual(ch, &merged) {
		return false, nil
	}
	merged.SlotNumber = cc.Slot
	// cloth 字段剥离：PUT character 不接受变更 cloth，模型走 SetVrm
	merged.SelectedCloth = 0
	merged.AvailableCloths = ""
	merged.CustomCloths = ""
	if err := c.PutCharacter(&merged); err != nil {
		return false, fmt.Errorf("clt01: put character: %w", err)
	}
	return true, nil
}

// infoEqual 逐字段比较（只比 SetInfo 语义域；cloth 域不在比较范围）。
func infoEqual(a, b *CharacterSettings) bool {
	return a.Name == b.Name &&
		a.Description == b.Description &&
		a.CharacterPrompt == b.CharacterPrompt &&
		a.VoiceID == b.VoiceID &&
		a.GroupKey == b.GroupKey &&
		a.TTSVolume == b.TTSVolume &&
		a.Sex == b.Sex &&
		a.AnimationIntensity == b.AnimationIntensity
}

// ---- SetVrm 的仅应用形态（草稿复用） ----

// ApplyModel 仅应用已有模型（草稿已在设备上时，跳过上传），版本分发。
func (c *Client) ApplyModel(ctx context.Context, cc *CallContext, filename string, opt *VRMOptions) (*VRMResult, error) {
	cc = cc.ensure()
	if err := cc.validate(true); err != nil {
		return nil, err
	}
	if opt == nil {
		opt = &VRMOptions{Apply: true}
	}
	version := c.detectVersion()
	impl := implFor(version, c)
	verify, err := impl.ApplyVRM(ctx, cc, filename)
	res := &VRMResult{Version: version, Verify: verify}
	if err != nil {
		opt.logf(false, "", err.Error())
		return res, err
	}
	opt.logf(true, fmt.Sprintf("%s model applied (via %s, evidence: %s)", cc.prefix(), version, verify.Evidence), "")
	return res, nil
}

// ---- PlayChat / CancelChat ----

// VerChatTurns ≥1.5.3 播报走 chat/turns 通道（与分块上传同一版本分界）。
const VerChatTurns = VerChunkedUpload

// 播报通道名。
const (
	ChannelChatTurns = "chat-turns" // ≥1.5.3：POST /data/user/chat/turns
	ChannelTTS       = "tts"        // <1.5.3：POST /tts_audition（1.5.2 通道）
)

// PlayChatOptions 播报参数。
type PlayChatOptions struct {
	Language string // 语言（可空）
	VoiceID  string // tts 通道（<1.5.3）必需
	GroupKey string // tts 通道音色分组
	OnStep   func(ok bool, detail, errMsg string)
}

// PlayChatResult 播报结果。Channel 告知实际使用的通道，
// 宿主据此选择播放基线/等待方式（tts 与 chat 的状态接口不同）。
type PlayChatResult struct {
	Channel string
	ChatID  string // chat 通道返回的会话 ID（可用于 CancelChat）
}

// PlayChat 播报一段文本（原子操作，按固件版本自动选通道）：
//
//	≥1.5.3：POST /data/user/chat/turns（body: text/slot_number/language），
//	        走设备当前角色 LLM+TTS 管线，返回 chat_id；
//	<1.5.3：POST /tts_audition（1.5.2 通道，需要 VoiceID/GroupKey），
//	        按请求参数直读合成、稳定出声。
//
// 版本判定失败（探测异常）时按 <1.5.3 保守处理（tts 通道在两版本都可用）。
func (c *Client) PlayChat(ctx context.Context, cc *CallContext, text string, opt *PlayChatOptions) (*PlayChatResult, error) {
	if opt == nil {
		opt = &PlayChatOptions{}
	}
	cc = cc.ensure()
	logf := func(ok bool, detail, errMsg string) {
		if opt.OnStep != nil {
			opt.OnStep(ok, detail, errMsg)
		}
	}
	version := c.detectVersion()
	if version.AtLeast(VerChatTurns) {
		res, err := c.playChatTurns(ctx, cc, text, opt)
		if err != nil {
			logf(false, "", "chat-turns: "+err.Error())
			return nil, err
		}
		logf(true, fmt.Sprintf("%s playchat via chat/turns (chat_id=%s)", cc.prefix(), res.ChatID), "")
		return res, nil
	}
	// <1.5.3：tts 通道
	if opt.VoiceID == "" {
		return nil, fmt.Errorf("clt01: firmware %s uses tts channel for playchat — VoiceID/GroupKey required", version)
	}
	if err := c.TTSAuditionCtx(ctx, opt.VoiceID, text, opt.GroupKey); err != nil {
		logf(false, "", "playchat tts: "+err.Error())
		return nil, err
	}
	logf(true, fmt.Sprintf("%s playchat via tts_audition (voice %s, firmware %s)", cc.prefix(), opt.VoiceID, version), "")
	return &PlayChatResult{Channel: ChannelTTS}, nil
}

// PredictPlayChatChannel 预判 PlayChat 将使用的通道（不产生副作用），
// 宿主可据此提前取播放基线/等待方式。
func (c *Client) PredictPlayChatChannel() string {
	if c.detectVersion().AtLeast(VerChatTurns) {
		return ChannelChatTurns
	}
	return ChannelTTS
}

// playChatTurns ≥1.5.3 播报通道。
// 请求 schema（252 真机逐字段探测确认，严格绑定 unknown-field 拒绝）：
//
//	{
//	  "request_id": "<UUID>",            // 必填，UUID
//	  "slot": 1,                          // 必填，1-5（注意不是 slot_number）
//	  "input":  {"text": "…", "type": "text"},
//	  "output": {"speech": ""}            // 必填占位，设备回填语音结果
//	}
func (c *Client) playChatTurns(ctx context.Context, cc *CallContext, text string, opt *PlayChatOptions) (*PlayChatResult, error) {
	body, err := json.Marshal(map[string]any{
		"request_id": uuidOrDefault(cc.RequestID),
		"slot":       cc.Slot,
		"input":      map[string]any{"text": text, "type": "text"},
		"output":     map[string]any{"speech": "device"}, // 设备端出声（可选 none/device）
	})
	if err != nil {
		return nil, err
	}
	data, err := c.DoCtx(ctx, http.MethodPost, "/v1/api/data/user/chat/turns", body, PayloadModeJSON)
	if err != nil {
		return nil, err
	}
	var shape struct {
		ChatID string `json:"chat_id"`
		TurnID string `json:"turn_id"`
		Data   struct {
			ChatID string `json:"chat_id"`
			TurnID string `json:"turn_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(data, &shape)
	id := shape.ChatID
	if id == "" {
		id = shape.Data.ChatID
	}
	if id == "" {
		id = shape.TurnID
	}
	if id == "" {
		id = shape.Data.TurnID
	}
	return &PlayChatResult{Channel: ChannelChatTurns, ChatID: id}, nil
}

// uuidOrDefault RequestID 非法/为空时生成随机 UUID（设备要求 UUID 格式）。
func uuidOrDefault(s string) string {
	if len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' {
		return s
	}
	b := make([]byte, 16)
	if _, err := randRead(b); err != nil {
		return "00000000-0000-4000-8000-000000000000" // v4 占位
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---- 播放状态原子（通道 → 状态端点的映射归 SDK） ----

// TTSAuditionState 试听播放状态（tts 通道）：GET /data/user/tts_audition/state。
func (c *Client) TTSAuditionState(ctx context.Context) (json.RawMessage, error) {
	return c.GetRawCtx(ctx, "/v1/api/data/user/tts_audition/state")
}

// ConversationState 会话播放状态（chat 通道）：GET /data/user/conversation/state。
func (c *Client) ConversationState(ctx context.Context) (json.RawMessage, error) {
	return c.GetRawCtx(ctx, "/v1/api/data/user/conversation/state")
}

// StateForChannel 按播报通道取对应播放状态原始 JSON。
func (c *Client) StateForChannel(ctx context.Context, channel string) (json.RawMessage, error) {
	if channel == ChannelChatTurns {
		return c.ConversationState(ctx)
	}
	return c.TTSAuditionState(ctx)
}

// ---- VoiceGroup ----

// VoiceGroup 音色分组查询（含各分组 voice_id 列表）。
func (c *Client) VoiceGroup(ctx context.Context) (json.RawMessage, error) {
	return c.GetRawCtx(ctx, "/v1/api/data/user/ability/group-keys")
}

// ---- Feedback ----

// FeedbackOptions 反馈包采集参数。
type FeedbackOptions struct {
	Title             string
	Description       string
	IncludeBasicLogs  bool
	IncludeSystemLogs bool
	Timeout           time.Duration // 生成等待上限（0 = 默认 100s）
}

// FeedbackResult 反馈包结果。
type FeedbackResult struct {
	ReportID string
	Archive  []byte // 打包好的日志压缩档（写入文件即可）
}

// Feedback 反馈包采集（原子）：创建任务 → 轮询生成状态 → 提交 → 下载归档。
func (c *Client) Feedback(ctx context.Context, opt *FeedbackOptions) (*FeedbackResult, error) {
	if opt == nil {
		opt = &FeedbackOptions{}
	}
	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 100 * time.Second
	}
	body, _ := json.Marshal(map[string]any{
		"title":               opt.Title,
		"description":         opt.Description,
		"include_basic_logs":  opt.IncludeBasicLogs,
		"include_system_logs": opt.IncludeSystemLogs,
	})
	data, err := c.DoCtx(ctx, http.MethodPost, "/v1/api/data/user/feedback/jobs", body, PayloadModeJSON)
	if err != nil {
		return nil, fmt.Errorf("clt01: feedback create: %w", err)
	}
	var created struct {
		ReportID string `json:"report_id"`
		ID       string `json:"id"`
	}
	_ = json.Unmarshal(data, &created)
	id := created.ReportID
	if id == "" {
		id = created.ID
	}
	if id == "" {
		return nil, fmt.Errorf("clt01: feedback create: no report id in response")
	}

	// 轮询生成状态：ready / completed / done 任一即就绪
	ready := func() bool {
		st, err := c.DoCtx(ctx, http.MethodGet, "/v1/api/data/user/feedback/jobs/"+id, nil, PayloadModeEmpty)
		if err != nil {
			return false
		}
		s := strings.ToLower(string(st))
		return strings.Contains(s, "ready") || strings.Contains(s, "completed") || strings.Contains(s, "done")
	}
	for waited := 3 * time.Second; waited <= timeout; waited += 3 * time.Second {
		if ready() {
			break
		}
		time.Sleep(3 * time.Second)
	}
	if !ready() {
		return &FeedbackResult{ReportID: id}, fmt.Errorf("clt01: feedback %s not ready within %s", id, timeout)
	}

	// 下载必须在 submit 之前：Status==Ready 时才有 BundlePath，
	// submit 会把包上传到社区并清空 BundlePath（之后 download 恒 BUNDLE_NOT_READY）。
	dlDeadline := time.Now().Add(timeout)
	var archive []byte
	for {
		var dlErr error
		archive, dlErr = c.DoRawBytesCtx(ctx, http.MethodGet, "/v1/api/data/user/feedback/jobs/"+id+"/download", nil, PayloadModeEmpty)
		if dlErr == nil {
			break
		}
		if ae, ok := dlErr.(*APIError); ok && strings.Contains(ae.Message+ae.Code, "BUNDLE_NOT_READY") && time.Now().Before(dlDeadline) {
			time.Sleep(3 * time.Second)
			continue
		}
		return &FeedbackResult{ReportID: id}, fmt.Errorf("clt01: feedback download: %w", dlErr)
	}
	// 提交上报（上传到社区，设备端清空本地包）；失败不影响已拿到的归档
	if _, err := c.DoCtx(ctx, http.MethodPost, "/v1/api/data/user/feedback/jobs/"+id+"/submit", []byte("{}"), PayloadModeJSON); err != nil {
		return &FeedbackResult{ReportID: id, Archive: archive}, fmt.Errorf("clt01: feedback submit: %w", err)
	}
	return &FeedbackResult{ReportID: id, Archive: archive}, nil
}

// detectVersion 当前固件版本（本 SDK 只支持 1.5.3+）。
func (c *Client) detectVersion() FirmwareVersion {
	if fw, _ := c.Firmware(); fw != nil {
		return fw.Version
	}
	return VerV153
}

// randRead 供 uuidOrDefault 使用（crypto/rand 薄封装）。
func randRead(b []byte) (int, error) { return rand.Read(b) }

// ---- VoiceClone 原子（只走克隆可用 provider：minimax 不支持克隆，SDK 直接过滤） ----

// VoiceCloneGroup 克隆可用的音色分组。
type VoiceCloneGroup struct {
	GroupKey    string
	DisplayName string
	Provider    string
	VoiceIDs    []string
}

// CloneVoiceGroups 返回**支持克隆**的音色分组：过滤掉 minimax（实测不支持 voice_clone，
// 对其发起克隆必然失败）与无 provider 的空组。默认克隆目标取其中第一个。
// minimax 判定为子串匹配：组名或 provider 任一含 "minimax" 即过滤 —— 固件会把
// provider 写成 minimax_clone / minimax_tts 之类变体，精确匹配拦不住。
func (c *Client) CloneVoiceGroups(ctx context.Context) ([]VoiceCloneGroup, error) {
	raw, err := c.VoiceGroup(ctx)
	if err != nil {
		return nil, err
	}
	var all []struct {
		GroupKey    string   `json:"group_key"`
		DisplayName string   `json:"display_name"`
		Provider    string   `json:"provider"`
		VoiceIDs    []string `json:"voice_ids"`
	}
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("clt01: parse voice groups: %w", err)
	}
	out := make([]VoiceCloneGroup, 0, len(all))
	for _, g := range all {
		if isMinimaxKey(g.GroupKey) || !CloneCapableProvider(g.Provider) {
			continue // 组名含 minimax / provider 为 minimax 变体或空：不支持克隆，跳过
		}
		out = append(out, VoiceCloneGroup{
			GroupKey: g.GroupKey, DisplayName: g.DisplayName,
			Provider: g.Provider, VoiceIDs: g.VoiceIDs,
		})
	}
	return out, nil
}

// CloneCapableProvider 该 provider 是否支持 voice_clone（minimax 及其变体不支持，见实测结论）。
// 子串匹配：minimax / minimax_clone / minimax_tts 等一律判否。
func CloneCapableProvider(provider string) bool {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" || isMinimaxKey(p) {
		return false
	}
	return true
}

// isMinimaxKey 字符串（组名 / provider）是否含 minimax（大小写不敏感的子串匹配）。
func isMinimaxKey(s string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(s)), "minimax")
}

// ErrCloneGroupUnsupported 目标分组不支持克隆（minimax 等）。
var ErrCloneGroupUnsupported = fmt.Errorf("clt01: 该音色分组 provider 不支持 voice_clone（minimax 不支持）")

// CloneVoiceOptions 克隆参数。
type CloneVoiceOptions struct {
	GroupKey       string // 目标分组（留空 = 自动选第一个支持克隆的分组）
	VoiceID        string // 克隆音色槽位号（留空 = 自动选该分组内第一个空槽）
	VoiceName      string
	Text           string // 预览文本（留空 = 设备默认）
	Volume         int
	Apply          bool // true = 克隆后把该音色应用到 cc.Slot
	ConfirmRetries int  // confirm 重试次数（0 = 默认 6）
}

// CloneVoiceResult 克隆结果。
type CloneVoiceResult struct {
	VoiceID  string
	GroupKey string
	Applied  bool
}

// SetCloneVoice 克隆音色（原子）：
//  1. 解析目标分组：显式指定则校验 provider 支持克隆（minimax 直接拒绝）；
//     未指定则自动选第一个支持克隆的分组（如 qwen_audio），不再误用 minimax。
//  2. upload 克隆（长耗时，内部 20 分钟超时）。
//  3. confirm 轮询重试（设备的目录刷新有延迟，"preview not found" 属可重试错误）。
//  4. Apply=true 时用 SetInfo 把音色**显式应用到 cc.Slot**（固件自带的 apply 只作用于
//     设备当前选中槽，不可控——这里绕过它，保证落到调用方指定的槽位）。
func (c *Client) SetCloneVoice(ctx context.Context, cc *CallContext, audio []byte, filename string, opt *CloneVoiceOptions) (*CloneVoiceResult, error) {
	if opt == nil {
		opt = &CloneVoiceOptions{Apply: true}
	}
	cc = cc.ensure()
	group, err := c.resolveCloneGroup(ctx, opt.GroupKey)
	if err != nil {
		return nil, err
	}
	voiceID := strings.TrimSpace(opt.VoiceID)
	if voiceID == "" {
		voiceID = firstFreeVoiceID(group.VoiceIDs)
	}
	c.SetHTTPTimeout(20 * time.Minute)
	up, err := c.VoiceCloneUpload(audio, filename, voiceID, group.GroupKey, opt.VoiceName, opt.Text, opt.Volume)
	if err != nil {
		return nil, fmt.Errorf("clt01: clone upload (group=%s provider=%s): %w", group.GroupKey, group.Provider, err)
	}
	res := &CloneVoiceResult{VoiceID: up.VoiceID, GroupKey: group.GroupKey}
	if res.VoiceID == "" {
		res.VoiceID = voiceID
	}

	retries := opt.ConfirmRetries
	if retries <= 0 {
		retries = 6
	}
	var cerr error
	for i := 0; i < retries; i++ {
		_, cerr = c.VoiceCloneConfirm(res.VoiceID, group.GroupKey, false) // 不 apply：槽位由 SetInfo 控制
		if cerr == nil {
			break
		}
		if !isRetryableConfirm(cerr) {
			return res, fmt.Errorf("clt01: clone confirm: %w", cerr)
		}
		select {
		case <-ctx.Done():
			return res, fmt.Errorf("clt01: canceled: %w", ctx.Err())
		case <-time.After(5 * time.Second):
		}
	}
	if cerr != nil {
		return res, fmt.Errorf("clt01: clone confirm after %d retries: %w", retries, cerr)
	}

	if opt.Apply {
		changed, serr := c.SetInfo(ctx, cc, &CharacterSettings{
			VoiceID: res.VoiceID, GroupKey: group.GroupKey,
		}, false)
		if serr != nil {
			return res, fmt.Errorf("clt01: apply cloned voice to slot %d: %w", cc.Slot, serr)
		}
		res.Applied = true
		_ = changed
	}
	return res, nil
}

// resolveCloneGroup 选定克隆分组并校验 provider 可克隆。
func (c *Client) resolveCloneGroup(ctx context.Context, want string) (*VoiceCloneGroup, error) {
	if isMinimaxKey(want) {
		// 显式点名 minimax（含变体组名）：直接拒绝，绝不回退到其它组静默克隆
		return nil, fmt.Errorf("%w (group=%s)", ErrCloneGroupUnsupported, want)
	}
	groups, err := c.CloneVoiceGroups(ctx)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("clt01: 设备没有支持 voice_clone 的音色分组")
	}
	want = strings.TrimSpace(want)
	if want == "" {
		return &groups[0], nil
	}
	for i := range groups {
		if groups[i].GroupKey == want {
			return &groups[i], nil
		}
	}
	// 显式指定的分组不在可克隆列表里（例如 minimax）
	return nil, fmt.Errorf("%w (group=%s)", ErrCloneGroupUnsupported, want)
}

// firstFreeVoiceID 在 [1..30] 里挑第一个不在现有列表里的槽位号。
func firstFreeVoiceID(existing []string) string {
	used := map[string]bool{}
	for _, v := range existing {
		used[v] = true
	}
	for i := 1; i <= 30; i++ {
		s := strconv.Itoa(i)
		if !used[s] {
			return s
		}
	}
	return "30"
}

// isRetryableConfirm 目录刷新延迟导致的 confirm 失败可重试。
func isRetryableConfirm(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	if ae.Code == "VOICE_CLONE_CONFIRM_FAILED" || ae.Code == "VOICE_CLONE_APPLY_FAILED" {
		return true
	}
	return strings.Contains(ae.Message, "not found") || strings.Contains(ae.Message, "expired")
}
