package clt01

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

// 本文件是 twitter_read_play 等业务服务使用的高层封装：
// 角色下发（character slot）、TTS 试听、音色分组、播报（chat/text）。

// CharacterSettings 对应设备 /data/user/character 的角色槽配置。
type CharacterSettings struct {
	SlotNumber             int    `json:"slot_number"`
	Name                   string `json:"name,omitempty"`
	Description            string `json:"description,omitempty"`
	CharacterPrompt        string `json:"character_prompt,omitempty"`
	VoiceID                string `json:"voice_id,omitempty"`
	GroupKey               string `json:"group_key,omitempty"`
	TTSVolume              int    `json:"tts_volume,omitempty"`
	SelectedCloth          int    `json:"selected_cloth,omitempty"`
	LinkOtherOriginalCloth string `json:"link_other_original_cloth,omitempty"`
	Sex                    int    `json:"sex,omitempty"`
	AnimationIntensity     int    `json:"animation_intensity,omitempty"`
	AvailableCloths        string `json:"available_cloths,omitempty"`
	CustomCloths           string `json:"custom_cloths,omitempty"`
}

// GetCharacter 读取角色槽配置。
func (c *Client) GetCharacter(slot int) (*CharacterSettings, error) {
	return c.GetCharacterCtx(context.Background(), slot)
}

// GetCharacterCtx 同 GetCharacter，带 context。
func (c *Client) GetCharacterCtx(ctx context.Context, slot int) (*CharacterSettings, error) {
	data, err := c.GetRawCtx(ctx, fmt.Sprintf("/v1/api/data/user/character?slot=%d", slot))
	if err != nil {
		return nil, err
	}
	out := &CharacterSettings{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutCharacter 下发角色槽配置（system prompt / voice / 音量整体一个包）。
// 注意：设备端 PUT character 不接受变更 cloth —— 请求体里带 selected_cloth/available_cloths
// 会报 OUTFIT_APPLY_REQUIRED (HTTP 400)。这里统一剥离 cloth 字段；换装走 OutfitApply。
func (c *Client) PutCharacter(cs *CharacterSettings) error {
	stripped := *cs
	stripped.SelectedCloth = 0
	stripped.AvailableCloths = ""
	stripped.LinkOtherOriginalCloth = ""
	stripped.CustomCloths = ""
	body, err := json.Marshal(&stripped)
	if err != nil {
		return err
	}
	_, err = c.DoCtx(context.Background(), http.MethodPut, "/v1/api/data/user/character", body, PayloadModeJSON)
	return err
}

// OutfitApply 换装专用接口。
//
// Deprecated: clt01_embed_backend（1.5.2 / 1.5.3 分支均已核实）没有注册
// /character/outfit/apply 路由，该调用恒 404 —— 历史上 SelectCloth「outfit 优先」
// 实际都是靠 404 回退三件套 PUT 工作的。仅保留给极端旧构建兜底，勿在新代码使用。
func (c *Client) OutfitApply(slot, clothID int) error {
	ch, err := c.GetCharacter(slot)
	if err != nil {
		return err
	}
	available := ch.AvailableCloths
	if available == "" {
		available = "1,2,3"
	}
	if !clothsContain(available, clothID) {
		available = available + "," + itoa(clothID)
	}
	// slot_number/selected_cloth 必须是 JSON number（设备端 *int32），其余为 string
	body, err := json.Marshal(struct {
		SlotNumber             int    `json:"slot_number"`
		SelectedCloth          int    `json:"selected_cloth"`
		AvailableCloths        string `json:"available_cloths"`
		LinkOtherOriginalCloth string `json:"link_other_original_cloth"`
	}{
		SlotNumber:             slot,
		SelectedCloth:          clothID,
		AvailableCloths:        available,
		LinkOtherOriginalCloth: ch.LinkOtherOriginalCloth,
	})
	if err != nil {
		return err
	}
	_, err = c.Do(http.MethodPost, "/v1/api/data/user/character/outfit/apply", body, PayloadModeJSON)
	return err
}

func clothsContain(list string, id int) bool {
	for _, part := range strings.Split(list, ",") {
		if strings.TrimSpace(part) == itoa(id) {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// SelectClothLegacy 旧固件兜底：Get→改 SelectedCloth→Put。
func (c *Client) SelectClothLegacy(slot, clothID int) error {
	ch, err := c.GetCharacter(slot)
	if err != nil {
		return err
	}
	ch.SelectedCloth = clothID
	return c.PutCharacter(ch)
}

// SelectCloth 选中 cloth（只支持 1.5.3+ 固件）：
//   - 较新 1.5.3 构建：outfit/apply 是唯一应用通道（PUT 三件套会被 OUTFIT_APPLY_REQUIRED 拒）；
//   - 早期 1.5.3 构建：没有 /character/outfit/apply 端点（404），回退三件套 model-apply PUT
//     （isModelApply → commitDraftClothCache）。
func (c *Client) SelectCloth(slot, clothID int) error {
	return c.SelectClothCtx(context.Background(), slot, clothID)
}

// SelectClothCtx 同 SelectCloth，带 context。
func (c *Client) SelectClothCtx(ctx context.Context, slot, clothID int) error {
	return selectClothForV153(ctx, c, slot, clothID)
}

// SelectClothV15x 三件套 model-apply 通道（早期 1.5.3 构建的兜底路径）：
// 必须直接发 SelectedCloth+AvailableCloths+LinkOtherOriginalCloth 三件套，
// 设备端才会走 commitDraftClothCache 把刚上传的草稿转正式并换屏上模型。
// 返回响应里的 custom_cloths（包含刚提交的模型文件名，可作落盘证据）。
func (c *Client) SelectClothV15x(slot, clothID int) (customCloths string, err error) {
	ch, err := c.GetCharacter(slot)
	if err != nil {
		return "", err
	}
	available := ch.AvailableCloths
	if available == "" {
		available = "1,2,3"
	}
	if !clothsContain(available, clothID) {
		available += "," + strconv.Itoa(clothID)
	}
	// 三件套必须全部显式携带：link_other_original_cloth 即使是空串也要带 ——
	// 用 CharacterSettings(omitempty)序列化时空值会丢字段，固件端 *string 为 nil
	// → 判定非 model-apply → 不提交草稿，200 但 custom_cloths 恒空（codie-black 实测）。
	body, err := json.Marshal(struct {
		SlotNumber             int    `json:"slot_number"`
		SelectedCloth          int    `json:"selected_cloth"`
		AvailableCloths        string `json:"available_cloths"`
		LinkOtherOriginalCloth string `json:"link_other_original_cloth"`
	}{
		SlotNumber:             slot,
		SelectedCloth:          clothID,
		AvailableCloths:        available,
		LinkOtherOriginalCloth: ch.LinkOtherOriginalCloth,
	})
	if err != nil {
		return "", err
	}
	raw, err := c.Do(http.MethodPut, "/v1/api/data/user/character", body, PayloadModeJSON)
	if err != nil {
		return "", err
	}
	var resp struct {
		CustomCloths string `json:"custom_cloths"`
	}
	if uerr := json.Unmarshal(raw, &resp); uerr == nil {
		return resp.CustomCloths, nil
	}
	return "", nil
}

// ClothDraft 草稿模型记录（GET /cloth/drafts，固件 ≥1.5.2；未转正的上传文件）。
type ClothDraft struct {
	SlotNumber int    `json:"slot_number"`
	ClothID    int    `json:"cloth_id"`
	Filename   string `json:"filename"`
}

// ClothDrafts 列出某 slot 的草稿模型。
//
// 注意：clt01_embed_backend 1.5.2 / 1.5.3 分支均未注册 GET /cloth/drafts 路由
// （草稿扫描是内部函数 scanDraftClothCache，无 HTTP 出口），该调用当前恒 404，
// 调用方按无草稿处理。保留用于前向兼容（固件后续可能补该端点）。
func (c *Client) ClothDrafts(slot int) ([]ClothDraft, error) {
	raw, err := c.GetRaw(fmt.Sprintf("/v1/api/data/user/cloth/drafts?slot_number=%d", slot))
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []ClothDraft `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// ChatTextRequest 播报请求。
type ChatTextRequest struct {
	Text     string `json:"text"`
	Slot     int    `json:"slot"`
	Language string `json:"language,omitempty"`
}

// ChatTextResponse 播报响应（含 chat_id，可用于取消）。
type ChatTextResponse struct {
	ChatID string `json:"chat_id"`
}

// ChatText 让设备播报一段文本（走设备当前角色的 LLM+TTS 管线）。
func (c *Client) ChatText(text string, slot int, language string) (*ChatTextResponse, error) {
	body, err := json.Marshal(&ChatTextRequest{Text: text, Slot: slot, Language: language})
	if err != nil {
		return nil, err
	}
	data, err := c.Do(http.MethodPost, "/v1/api/data/user/chat/text", body, PayloadModeJSON)
	if err != nil {
		return nil, err
	}
	out := &ChatTextResponse{}
	if len(data) > 0 {
		_ = json.Unmarshal(data, out)
	}
	return out, nil
}

// CancelChat 取消一次播报。
func (c *Client) CancelChat(chatID string) error {
	body, _ := json.Marshal(map[string]string{"chat_id": chatID})
	_, err := c.Do(http.MethodPost, "/v1/api/data/user/chat/cancel", body, PayloadModeJSON)
	return err
}

// VoiceGroup 获取设备音色分组（含各分组下的 voice_id 列表）。
func (c *Client) VoiceGroups() (json.RawMessage, error) {
	return c.GetRaw("/v1/api/data/user/ability/group-keys")
}

// TTSAudition 指定音色试听一段文本。
func (c *Client) TTSAudition(voiceID, text, groupKey string) error {
	body, _ := json.Marshal(map[string]string{
		"voice_id":  voiceID,
		"text":      text,
		"group_key": groupKey,
	})
	_, err := c.Do(http.MethodPost, "/v1/api/data/user/tts_audition", body, PayloadModeJSON)
	return err
}

// TTSAuditionCtx 同 TTSAudition，带 context。
func (c *Client) TTSAuditionCtx(ctx context.Context, voiceID, text, groupKey string) error {
	body, _ := json.Marshal(map[string]string{
		"voice_id":  voiceID,
		"text":      text,
		"group_key": groupKey,
	})
	_, err := c.DoCtx(ctx, http.MethodPost, "/v1/api/data/user/tts_audition", body, PayloadModeJSON)
	return err
}

// CapabilityRoute AI 能力路由（model 选择，capability: llm/tts/asr/vlm）。
func (c *Client) SetCapabilityRoute(capability, profileID string) error {
	path := fmt.Sprintf("/v1/api/data/user/ai/capability-routes/%s", capability)
	body, _ := json.Marshal(map[string]string{"profile_id": profileID})
	_, err := c.Do(http.MethodPut, path, body, PayloadModeJSON)
	return err
}

// VoiceCloneResponse 音色克隆响应。
type VoiceCloneResponse struct {
	VoiceID string `json:"voice_id"`
	Status  string `json:"status,omitempty"`
}

// VoiceCloneUpload 上传音频克隆音色（multipart）。voiceID 可为空由设备生成。
func (c *Client) VoiceCloneUpload(audio []byte, filename, voiceID, groupKey, voiceName, text string, volume int) (*VoiceCloneResponse, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("group_key", groupKey)
	if voiceID != "" {
		_ = w.WriteField("voice_id", voiceID)
	}
	if voiceName != "" {
		_ = w.WriteField("voice_name", voiceName)
	}
	if text != "" {
		_ = w.WriteField("text", text)
	}
	if volume > 0 {
		_ = w.WriteField("volume", strconv.Itoa(volume))
	}
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(audio); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	data, err := c.doUnsignedCT(context.Background(), http.MethodPost, "/v1/api/data/user/voice_clone/upload", buf.Bytes(), PayloadMode("multipart"), w.FormDataContentType())
	if err != nil {
		return nil, err
	}
	out := &VoiceCloneResponse{}
	if len(data) > 0 {
		_ = json.Unmarshal(data, out)
	}
	return out, nil
}

// VoiceCloneConfirm 确认克隆并应用到角色。
func (c *Client) VoiceCloneConfirm(voiceID, groupKey string, apply bool) (*VoiceCloneResponse, error) {
	body, _ := json.Marshal(map[string]any{"voice_id": voiceID, "group_key": groupKey, "apply": apply})
	data, err := c.Do(http.MethodPost, "/v1/api/data/user/voice_clone/confirm", body, PayloadModeJSON)
	if err != nil {
		return nil, err
	}
	out := &VoiceCloneResponse{}
	if len(data) > 0 {
		_ = json.Unmarshal(data, out)
	}
	return out, nil
}

// CurrentSlot 读取设备当前激活的槽位与 cloth（GET /data/user/slot）。
func (c *Client) CurrentSlot() (slot, cloth int, err error) {
	data, err := c.GetRaw("/v1/api/data/user/slot")
	if err != nil {
		return 0, 0, err
	}
	var res struct {
		Success bool `json:"success"`
		Data    struct {
			SlotNumber    int    `json:"slot_number"`
			SelectedCloth int    `json:"selected_cloth"`
			Version       string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return 0, 0, err
	}
	if res.Data.SlotNumber == 0 && res.Data.Version != "" {
		// 兜底：从 "slot:cloth" version 字符串解析
		if _, err := fmt.Sscanf(res.Data.Version, "%d:%d", &slot, &cloth); err == nil {
			return slot, cloth, nil
		}
	}
	return res.Data.SlotNumber, res.Data.SelectedCloth, nil
}

// SwitchSlot 切换设备当前角色槽。
func (c *Client) SwitchSlot(slot int) error {
	body, _ := json.Marshal(map[string]any{"slot_number": slot})
	_, err := c.Do(http.MethodPut, "/v1/api/data/user/slot", body, PayloadModeJSON)
	return err
}

// ClothUploadJob 创建 VRM 上传任务。
func (c *Client) ClothUploadJob(slot int, clothID int, filename string, size int64) (jobID string, err error) {
	body, _ := json.Marshal(map[string]any{
		"slot_number": slot, "cloth_id": clothID, "filename": filename,
		"original_size_bytes": size, "transport_encoding": "identity",
	})
	data, err := c.Do(http.MethodPost, "/v1/api/data/user/cloth/upload/job", body, PayloadModeJSON)
	if err != nil {
		return "", err
	}
	var res struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", err
	}
	return res.JobID, nil
}

// ClothUploadJobFile 上传 VRM 文件本体（multipart）。
func (c *Client) ClothUploadJobFile(jobID string, file []byte, filename string) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(file); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	_, err = c.doUnsignedCT(context.Background(), http.MethodPost, "/v1/api/data/user/cloth/upload/job/"+jobID+"/file", buf.Bytes(), PayloadMode("multipart"), w.FormDataContentType())
	return err
}

// ClothUploadJobComplete 通知上传完成。
func (c *Client) ClothUploadJobComplete(jobID string) error {
	_, err := c.Do(http.MethodPost, "/v1/api/data/user/cloth/upload/job/"+jobID+"/complete", nil, PayloadModeEmpty)
	return err
}

// ClothUploadStatus 查询上传处理状态。
func (c *Client) ClothUploadStatus(jobID string) (status, errorCode string, err error) {
	data, err := c.GetRaw("/v1/api/data/user/cloth/upload/status?job_id=" + jobID)
	if err != nil {
		return "", "", err
	}
	var res struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", "", err
	}
	return res.Status, res.ErrorCode, nil
}

// UploadedClothModels 列出已上传的自定义 VRM。
func (c *Client) UploadedClothModels() (json.RawMessage, error) {
	return c.GetRaw("/v1/api/data/user/cloth/uploaded-models")
}

// 上传传输编码（clt01_embed_backend character.UploadTransport*）。
const (
	UploadTransportIdentity = "identity"
	UploadTransportGzip     = "gzip"
	UploadTransportZstd     = "zstd"
)

// ClothUploadJobCompressedV153 创建压缩分块上传任务（固件 ≥ 1.5.3 协议）。
//
// 1.5.3 起，gzip-stream/zstd-stream payload 模式正文 >8MiB 会被签名中间件直接拒绝
// （HTTP 413，code=MODEL_TOO_LARGE，正文需缓冲算哈希）；chunk 端点另有 8MiB
// MaxBytesReader。json/multipart 模式无 8MiB 限制，job/async 总上限 200MB
// （maxUploadSizeBytes）。大模型走 gzip/zstd 分块通道；transport 取 "gzip" 或 "zstd"。
// 返回 (jobID, nil)；旧固件（1.5.2）会忽略 transport_encoding 字段按 identity 建 job
// 成功 —— 判定是否 1.5.3 必须以 chunk 端点是否可用为准（见 Facade tryChunkedUpload）。
func (c *Client) ClothUploadJobCompressedV153(slot, clothID int, filename string, originalSize int64, transport string) (string, error) {
	if transport != UploadTransportGzip && transport != UploadTransportZstd {
		return "", fmt.Errorf("clt01: unsupported compressed transport %q (use gzip/zstd)", transport)
	}
	body, _ := json.Marshal(map[string]any{
		"slot_number": slot, "cloth_id": clothID, "filename": filename,
		"original_size_bytes": originalSize, "transport_encoding": transport,
	})
	data, err := c.Do(http.MethodPost, "/v1/api/data/user/cloth/upload/job", body, PayloadModeJSON)
	if err != nil {
		return "", err
	}
	var res struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return "", err
	}
	return res.JobID, nil
}

// ClothUploadChunkCompressedV153Ctx 同 ClothUploadChunkCompressedV153，带 context。
func (c *Client) ClothUploadChunkCompressedV153Ctx(ctx context.Context, jobID string, sequence int, frame []byte, transport string) error {
	mode := PayloadMode("gzip-stream")
	if transport == UploadTransportZstd {
		mode = "zstd-stream"
	}
	_, err := c.doUnsignedCT(ctx, http.MethodPost,
		"/v1/api/data/user/cloth/upload/job/"+jobID+"/chunk/"+strconv.Itoa(sequence),
		frame, mode, "application/octet-stream")
	return err
}

// ClothUploadChunkCompressedV153 上传一个压缩分块（每块必须是独立 gzip/zstd frame，≤8MiB）。
// sequence 从 0 严格递增；header 走 X-Local-Payload-Mode: gzip-stream/zstd-stream，
// Body-Hash 为该 frame 的 sha256（设备端会缓冲校验）。
func (c *Client) ClothUploadChunkCompressedV153(jobID string, sequence int, frame []byte, transport string) error {
	mode := PayloadMode("gzip-stream")
	if transport == UploadTransportZstd {
		mode = "zstd-stream"
	}
	_, err := c.doUnsignedCT(context.Background(), http.MethodPost,
		"/v1/api/data/user/cloth/upload/job/"+jobID+"/chunk/"+strconv.Itoa(sequence),
		frame, mode, "application/octet-stream")
	return err
}

// ClothUploadCompleteCompressedV153 通知压缩分块全部到齐；设备校验解压字节数与 VRM
// 结构后进入异步处理（之后用 ClothUploadStatus 轮询）。
func (c *Client) ClothUploadCompleteCompressedV153(jobID, transport string) error {
	return c.ClothUploadCompleteCompressedV153Ctx(context.Background(), jobID, transport)
}

// ClothUploadCompleteCompressedV153Ctx 同上，带 context。
// 注意：body 必须非空 —— 1.5.3+ 签名中间件对空 body 会把 payloadMode 降级为 empty，
// 与签名的 gzip-stream 不一致直接 401 PAYLOAD_MODE_MISMATCH（252 真机实测）。
func (c *Client) ClothUploadCompleteCompressedV153Ctx(ctx context.Context, jobID, transport string) error {
	mode := PayloadMode("gzip-stream")
	if transport == UploadTransportZstd {
		mode = "zstd-stream"
	}
	_, err := c.doUnsignedCT(ctx, http.MethodPost,
		"/v1/api/data/user/cloth/upload/job/"+jobID+"/complete", []byte("{}"), mode, "application/octet-stream")
	return err
}

// ClothUploadJobCancel 取消尚未完成的上传任务。
//
// 注意：固件未注册 DELETE /cloth/upload/job/:id（两版本同），该调用恒 404。
// 设备端的自愈机制是：同 slot/cloth 再建新 job 时自动取消旧任务
// （CreateClothUploadJob 返回 canceled_job_id）。SDK 内部所有 Cancel 调用均忽略错误。
func (c *Client) ClothUploadJobCancel(jobID string) error {
	_, err := c.Do(http.MethodDelete, "/v1/api/data/user/cloth/upload/job/"+jobID, nil, PayloadModeEmpty)
	return err
}

// ---- 设备上传通道（只支持 1.5.3+）----
//
// 1.5.3 起的压缩分块通道：job[transport=gzip] → chunk/:seq → complete
// （三条路由均为 1.5.3 新增）；8MiB 仅限 stream payload 模式（413 MODEL_TOO_LARGE）；
// uploaded-models 权威落盘校验。

