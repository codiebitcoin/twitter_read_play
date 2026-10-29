package clt01

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
)

// 本文件按 Codie-HTTP-API-1.5.3 离线手册（workshop_api docs/1.5.3/openapi.json）
// 补全原子性接口：每个端点一个方法，一个方法只做一件事（提交/查询/取消分离），
// 返回统一 JSON 信封里的 data 原始字段（json.RawMessage），由调用方按需解析。
//
// 已在他处实现的不再重复：
//   - 配对/引导      pairing.go
//   - 角色/服装/槽位  codie.go / api_*.go（character get/put、outfit/apply、slot、cloth 上传链路）
//   - 播报三通道     atomic.go（chat/turns 提交）+ tts_utterance.go（独立文本播报）+ tts_audition
//   - 音色克隆       codie.go（upload/confirm）
//
// 手册要点：所有 /data/user/* 需配对签名；设备拒绝未知字段；202/200 常表示「已受理/已保存」
// 而非动作完成——异步任务请用配套的 status/轮询方法确认终态。

// ---- 通用薄封装 ----

func (c *Client) get153(ctx context.Context, path string, q url.Values) (json.RawMessage, error) {
	if len(q) > 0 {
		path = path + "?" + q.Encode()
	}
	return c.GetRawCtx(ctx, path)
}

func (c *Client) do153(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var buf []byte
	if body != nil {
		var err error
		if buf, err = json.Marshal(body); err != nil {
			return nil, err
		}
	} else {
		buf = nil // 空正文（PayloadModeEmpty）
	}
	return c.DoCtx(ctx, method, path, buf, PayloadModeJSON)
}

// ---- 1.5.3 对话轮次（chat/turns）配套 ----

// ChatTurn 查询指定对话轮次（state/assistant_text/error 等）。
func (c *Client) ChatTurn(ctx context.Context, turnID string) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/chat/turns/"+turnID, nil)
}

// CancelChatTurn 取消指定对话轮次（空正文）。
func (c *Client) CancelChatTurn(ctx context.Context, turnID string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/chat/turns/"+turnID+"/cancel", nil)
}

// CompleteChatTurnInput 结束指定轮次的设备录音输入（空正文）。
func (c *Client) CompleteChatTurnInput(ctx context.Context, turnID string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/chat/turns/"+turnID+"/input/complete", nil)
}

// ---- 聊天历史 ----

// ChatHistoryParams 历史查询参数（slot 必填，其余可选）。
type ChatHistoryParams struct {
	Slot      int
	Cursor    string
	Date      string // 单日 yyyy-mm-dd
	StartDate string // 区间起
	EndDate   string // 区间止
	Direction string // older/newer
	Limit     int
}

func (p ChatHistoryParams) values() url.Values {
	q := url.Values{}
	q.Set("slot", strconv.Itoa(p.Slot))
	setIf := func(k, v string) {
		if v != "" {
			q.Set(k, v)
		}
	}
	setIf("cursor", p.Cursor)
	setIf("date", p.Date)
	setIf("start_date", p.StartDate)
	setIf("end_date", p.EndDate)
	setIf("direction", p.Direction)
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	return q
}

// ChatHistory 按槽位、日期条件和游标读取聊天记录。
func (c *Client) ChatHistory(ctx context.Context, p ChatHistoryParams) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/chat/history", p.values())
}

// ChatHistoryDates 指定槽位有聊天记录的日期。
func (c *Client) ChatHistoryDates(ctx context.Context, slot int) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/chat/history/dates", url.Values{"slot": {strconv.Itoa(slot)}})
}

// ChatHistoryCount 指定槽位的聊天记录总数。
func (c *Client) ChatHistoryCount(ctx context.Context, slot int) (json.RawMessage, error) {
	return c.get153(ctx, fmt.Sprintf("/v1/api/data/user/chat/history/count/slot/%d", slot), nil)
}

// ClearChatHistory 清空当前用户的聊天记录及调试产物（全部槽位）。
func (c *Client) ClearChatHistory(ctx context.Context) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodDelete, "/v1/api/data/user/chat/history", nil)
}

// ClearChatHistorySlot 清空指定槽位的聊天记录。
func (c *Client) ClearChatHistorySlot(ctx context.Context, slot int) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodDelete, fmt.Sprintf("/v1/api/data/user/chat/history/slot/%d", slot), nil)
}

// DeleteChatHistoryItems 删除选中的聊天记录。
func (c *Client) DeleteChatHistoryItems(ctx context.Context, slot int, chatIDs []string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodDelete, "/v1/api/data/user/chat/history/items",
		map[string]any{"slot_number": slot, "chat_ids": chatIDs})
}

// ---- 角色扩展（列表 / 加载状态 / 完整重置） ----

// CharacterList 列出所有槽位的角色属性。
func (c *Client) CharacterList(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/character/list", nil)
}

// CharacterLoadingState 获取角色模型加载状态。
func (c *Client) CharacterLoadingState(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/character/loading/state", nil)
}

// CharacterResetStart 启动当前角色的完整重置（body 原样透传，字段以手册为准）。
func (c *Client) CharacterResetStart(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/character/reset", body)
}

// CharacterResetAuthorize 验证当前用户并授权角色重置。
func (c *Client) CharacterResetAuthorize(ctx context.Context, username, password string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/character/reset/authorize",
		map[string]string{"username": username, "password": password})
}

// CharacterResetStatus 按 operation_id 查询重置任务状态。
func (c *Client) CharacterResetStatus(ctx context.Context, operationID string) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/character/reset/status",
		url.Values{"operation_id": {operationID}})
}

// CharacterResetActive 获取当前进行中的角色重置任务（无则空）。
func (c *Client) CharacterResetActive(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/character/reset/active", nil)
}

// ---- AI 语言 ----

// AILanguage 获取智能交互语言。
func (c *Client) AILanguage(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/ai-language", nil)
}

// SetAILanguage 更新智能交互语言（en/ja/fr/es/de）。
func (c *Client) SetAILanguage(ctx context.Context, lang string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPut, "/v1/api/data/user/ai-language", map[string]string{"ai_language": lang})
}

// ---- 设备与状态 ----

// DeviceState 息屏、锁屏及对话拦截状态。
func (c *Client) DeviceState(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/device/state", nil)
}

// SoundControl 控制声音播放或停止（action: play|stop）。
func (c *Client) SoundControl(ctx context.Context, action string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/sound/control", map[string]string{"action": action})
}

// ---- 闹钟 ----

// AlarmList 列出闹钟。
func (c *Client) AlarmList(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/alarm/list", nil)
}

// AlarmGet 闹钟详情。
func (c *Client) AlarmGet(ctx context.Context, alarmID string) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/alarm/"+alarmID, nil)
}

// AlarmCreate 创建闹钟。alarm 字段见手册：enabled/label/tag/hour/minute/repeat_mask/
// ringtone/snooze_minutes/snooze_enabled/snooze_max_count（全部必填）。
func (c *Client) AlarmCreate(ctx context.Context, alarm json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/alarm", map[string]json.RawMessage{"alarm": alarm})
}

// AlarmUpdate 更新闹钟。
func (c *Client) AlarmUpdate(ctx context.Context, alarmID string, alarm json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPut, "/v1/api/data/user/alarm/"+alarmID, map[string]json.RawMessage{"alarm": alarm})
}

// AlarmDelete 删除闹钟。
func (c *Client) AlarmDelete(ctx context.Context, alarmID string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodDelete, "/v1/api/data/user/alarm/"+alarmID, nil)
}

// ---- 备忘录 ----

// MemoList 列出备忘录。
func (c *Client) MemoList(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/memos", nil)
}

// MemoGet 备忘录详情。
func (c *Client) MemoGet(ctx context.Context, memoID string) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/memos/"+memoID, nil)
}

// MemoCreate 创建备忘录（memo 字段见手册，title/body/…/ringtone 全部必填）。
func (c *Client) MemoCreate(ctx context.Context, memo json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/memos", map[string]json.RawMessage{"memo": memo})
}

// MemoUpdate 更新备忘录。
func (c *Client) MemoUpdate(ctx context.Context, memoID string, memo json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPut, "/v1/api/data/user/memos/"+memoID, map[string]json.RawMessage{"memo": memo})
}

// MemoDelete 删除备忘录。
func (c *Client) MemoDelete(ctx context.Context, memoID string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodDelete, "/v1/api/data/user/memos/"+memoID, nil)
}

// ---- 记忆与日记 ----

// DiaryEntries 分页读取角色日记（characterId/slot 必填；date/status/includeDeleted/page/pageSize 可选）。
func (c *Client) DiaryEntries(ctx context.Context, characterID string, slot int, q url.Values) (json.RawMessage, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("characterId", characterID)
	q.Set("slot", strconv.Itoa(slot))
	return c.get153(ctx, "/v1/api/data/user/diary/entries", q)
}

// DiaryGet 日记详情。
func (c *Client) DiaryGet(ctx context.Context, entryID string) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/diary/entries/"+entryID, nil)
}

// DiaryUpdate 更新日记内容（body 原样透传）。
func (c *Client) DiaryUpdate(ctx context.Context, entryID string, body json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPut, "/v1/api/data/user/diary/entries/"+entryID, body)
}

// DiaryDelete 删除日记。
func (c *Client) DiaryDelete(ctx context.Context, entryID string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodDelete, "/v1/api/data/user/diary/entries/"+entryID, nil)
}

// MemoryCards 读取本地记忆卡片列表（characterId 必填）。
func (c *Client) MemoryCards(ctx context.Context, characterID string, page, pageSize int) (json.RawMessage, error) {
	q := url.Values{"characterId": {characterID}}
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	if pageSize > 0 {
		q.Set("pageSize", strconv.Itoa(pageSize))
	}
	return c.get153(ctx, "/v1/api/data/user/memory/cards", q)
}

// MemoryCardGet 记忆卡片详情。
func (c *Client) MemoryCardGet(ctx context.Context, cardID string) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/memory/cards/"+cardID, nil)
}

// MemoryCardUpdate 更新记忆卡片（body 原样透传）。
func (c *Client) MemoryCardUpdate(ctx context.Context, cardID string, body json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPut, "/v1/api/data/user/memory/cards/"+cardID, body)
}

// MemoryCardDelete 删除记忆卡片。
func (c *Client) MemoryCardDelete(ctx context.Context, cardID string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodDelete, "/v1/api/data/user/memory/cards/"+cardID, nil)
}

// ---- 主动交互 ----

// ProactiveConfig 获取主动交互配置。
func (c *Client) ProactiveConfig(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/proactive/config", nil)
}

// SetProactiveConfig 更新主动交互配置（config 结构见手册：enabled/triggers/inhibitors/runtime，
// 建议先 ProactiveConfig 取回、改字段后原样回传）。
func (c *Client) SetProactiveConfig(ctx context.Context, config json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPut, "/v1/api/data/user/proactive/config",
		map[string]json.RawMessage{"config": config})
}

// ---- 灯光 ----

// LightMasterSwitch 获取灯光总开关状态。
func (c *Client) LightMasterSwitch(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/light-master-switch", nil)
}

// SetLightMasterSwitch 设置灯光总开关。
func (c *Client) SetLightMasterSwitch(ctx context.Context, enabled bool) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/light-master-switch", map[string]bool{"enabled": enabled})
}

// LightProjects 列出灯光作品。
func (c *Client) LightProjects(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/light-projects", nil)
}

// LightProject 灯光作品详情。
func (c *Client) LightProject(ctx context.Context, projectID string) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/light-projects/"+projectID, nil)
}

// CreateLightProject 创建灯光作品（body 原样透传）。
func (c *Client) CreateLightProject(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/light-projects", body)
}

// UpdateLightProject 更新灯光作品。
func (c *Client) UpdateLightProject(ctx context.Context, projectID string, body json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPut, "/v1/api/data/user/light-projects/"+projectID, body)
}

// DeleteLightProject 删除灯光作品。
func (c *Client) DeleteLightProject(ctx context.Context, projectID string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodDelete, "/v1/api/data/user/light-projects/"+projectID, nil)
}

// ApplyLightProject 应用灯光作品。
func (c *Client) ApplyLightProject(ctx context.Context, projectID string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/light-projects/"+projectID+"/apply", nil)
}

// CompileLightProject 编译灯光作品。
func (c *Client) CompileLightProject(ctx context.Context, projectID string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/light-projects/"+projectID+"/compile", nil)
}

// AppliedLightProject 获取当前应用的灯光作品。
func (c *Client) AppliedLightProject(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/light-projects/applied", nil)
}

// LightPreview 预览已保存的灯光作品或草稿（body 原样透传）。
func (c *Client) LightPreview(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/light-projects/preview", body)
}

// LightPreviewStop 停止灯光预览。
func (c *Client) LightPreviewStop(ctx context.Context) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/light-projects/preview/stop", nil)
}

// ---- 音乐 ----

// MusicControl 发送音乐控制命令。action: PLAY/PAUSE/PREVIOUS/NEXT/SELECT_TRACK/QUERY_STATE/
// SEEK/SET_VOLUME/SET_PLAY_MODE/SET_SOUND_EFFECT；SELECT_TRACK 需 track_id，SET_VOLUME 需
// volume_level，SET_PLAY_MODE 需 play_mode，SET_SOUND_EFFECT 需 sound_effect，
// SEEK 需 progress_ms 或 progress_ratio（extra 里带上对应字段）。
func (c *Client) MusicControl(ctx context.Context, action string, extra map[string]any) (json.RawMessage, error) {
	body := map[string]any{"action": action, "source": "LOCAL_TF"}
	for k, v := range extra {
		body[k] = v
	}
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/music/control", body)
}

// MusicSource 获取当前音乐来源。
func (c *Client) MusicSource(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/music/source", nil)
}

// SetMusicSource 切换音乐来源。
func (c *Client) SetMusicSource(ctx context.Context, source string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/music/source", map[string]any{"source": source})
}

// MusicPlayback 获取音乐播放状态。
func (c *Client) MusicPlayback(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/music/playback", nil)
}

// MusicAudioRoute 获取音乐音频输出路由。
func (c *Client) MusicAudioRoute(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/music/audio-route", nil)
}

// MusicBluetooth 获取蓝牙连接状态。
func (c *Client) MusicBluetooth(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/music/bluetooth", nil)
}

// MusicForgetDevice 移除已配对的蓝牙设备（body 原样透传，字段以手册为准）。
func (c *Client) MusicForgetDevice(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/music/bluetooth/devices/forget", body)
}

// MusicPage 获取设备音乐页面状态。
func (c *Client) MusicPage(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/music/page", nil)
}

// MusicStorage 获取存储卡状态及音乐文件列表。
func (c *Client) MusicStorage(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/music/storage", nil)
}

// MusicDeleteFile 删除存储卡上的音乐文件（path 为存储卡上的文件路径）。
func (c *Client) MusicDeleteFile(ctx context.Context, path string) (json.RawMessage, error) {
	return c.DoCtx(ctx, http.MethodDelete,
		"/v1/api/data/user/music/file?path="+url.QueryEscape(path), nil, PayloadModeEmpty)
}

// MusicUpload 上传音乐到存储卡（multipart，字段 file）。
func (c *Client) MusicUpload(ctx context.Context, filename string, data []byte) (json.RawMessage, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("filename", filename); err != nil {
		return nil, err
	}
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err = fw.Write(data); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	return c.doUnsignedCT(ctx, http.MethodPost, "/v1/api/data/user/music/upload",
		buf.Bytes(), PayloadMode("multipart"), w.FormDataContentType())
}

// ---- 模型导出 / 删除（cloth download） ----

// ClothDownloadAsync 异步导出已应用的自定义模型。
func (c *Client) ClothDownloadAsync(ctx context.Context, slot, clothID int) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/cloth/download/async",
		map[string]int{"slot_number": slot, "cloth_id": clothID})
}

// ClothDownloadStatus 按 job_id 查询模型导出任务状态。
func (c *Client) ClothDownloadStatus(ctx context.Context, jobID string) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/cloth/download/status", url.Values{"job_id": {jobID}})
}

// ClothDownloadResult 下载模型导出压缩包（二进制，调用方自行写盘）。
func (c *Client) ClothDownloadResult(ctx context.Context, jobID string) ([]byte, error) {
	return c.DoRawBytesCtx(ctx, http.MethodGet,
		"/v1/api/data/user/cloth/download/result?job_id="+url.QueryEscape(jobID), nil, PayloadModeEmpty)
}

// DeleteClothModel 删除用户模型及关联资源。
func (c *Client) DeleteClothModel(ctx context.Context, slot, clothID int) (json.RawMessage, error) {
	q := url.Values{"slot_number": {strconv.Itoa(slot)}, "cloth_id": {strconv.Itoa(clothID)}}
	return c.DoCtx(ctx, http.MethodDelete, "/v1/api/data/user/cloth/model?"+q.Encode(), nil, PayloadModeEmpty)
}

// ClothUploadStatusRaw 按 job_id 查询模型上传任务状态（原始 JSON；已解析版本见 ClothUploadStatus）。
func (c *Client) ClothUploadStatusRaw(ctx context.Context, jobID string) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/cloth/upload/status", url.Values{"job_id": {jobID}})
}

// CancelClothUploadJob 取消模型上传任务。
func (c *Client) CancelClothUploadJob(ctx context.Context, jobID string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodDelete, "/v1/api/data/user/cloth/upload/job/"+jobID, nil)
}

// ---- 音色克隆补充 ----

// VoiceCloneDelete 删除克隆音色（voice_id: 21–25；需带 group_key）。
func (c *Client) VoiceCloneDelete(ctx context.Context, voiceID, groupKey string) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/voice_clone/delete",
		map[string]string{"voice_id": voiceID, "group_key": groupKey})
}

// VoiceCloneDeviceRecordStart 开始设备端参考音频录制。
func (c *Client) VoiceCloneDeviceRecordStart(ctx context.Context) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/voice_clone/device_record/start", nil)
}

// VoiceCloneDeviceRecordState 获取设备端参考录音状态。
func (c *Client) VoiceCloneDeviceRecordState(ctx context.Context) (json.RawMessage, error) {
	return c.get153(ctx, "/v1/api/data/user/voice_clone/device_record/state", nil)
}

// VoiceCloneDeviceRecordStop 停止设备录音（返回音频，调用方自行处理/回传 upload）。
func (c *Client) VoiceCloneDeviceRecordStop(ctx context.Context) (json.RawMessage, error) {
	return c.do153(ctx, http.MethodPost, "/v1/api/data/user/voice_clone/device_record/stop", nil)
}

// ---- 只读资源（/resources，无需配对签名） ----

// SystemPortraitManifest 获取系统内置角色头像清单。
func (c *Client) SystemPortraitManifest(ctx context.Context) ([]byte, error) {
	return c.doUnsigned(ctx, http.MethodGet, "/v1/api/resources/system/original-portraits/manifest", nil, "")
}

// SystemPortraitAsset 读取系统内置角色头像（filepath 为清单里的相对路径）。
func (c *Client) SystemPortraitAsset(ctx context.Context, filepath string) ([]byte, error) {
	return c.doUnsigned(ctx, http.MethodGet,
		"/v1/api/resources/system/assets/original-portraits/"+filepath, nil, "")
}

// UserThumbnail 读取模型缩略图。
func (c *Client) UserThumbnail(ctx context.Context, filepath string) ([]byte, error) {
	return c.doUnsigned(ctx, http.MethodGet, "/v1/api/resources/user/assets/thumbnails/"+filepath, nil, "")
}

// ChatImage 读取当前用户的聊天图片（uuid 必填）。
func (c *Client) ChatImage(ctx context.Context, uuid string) ([]byte, error) {
	return c.doUnsigned(ctx, http.MethodGet,
		"/v1/api/resources/user/chat-images?uuid="+url.QueryEscape(uuid), nil, "")
}
