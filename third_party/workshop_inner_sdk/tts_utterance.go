package clt01

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// 本文件实现固件 ≥1.5.3 的「独立文本播报（TTS ONLY）」原子接口：
//
//	POST   /data/user/tts/utterances                  提交朗读（不调 LLM、不写聊天记录）
//	GET    /data/user/tts/utterances/{utterance_id}   查询任务状态
//	POST   /data/user/tts/utterances/{utterance_id}/cancel  取消（空正文）
//
// 与 tts_audition（试听，任意固件，按 voice_id 直读）是两个概念：
// utterances 用当前槽位/角色的声音、语言和音量朗读任意文本，是 TTS 播报的正牌接口；
// <1.5.3 固件没有该端点，宿主需回退 tts_audition（见 TTSUtteranceAvailable）。
//
// 状态机：queued → synthesizing → playing → completed；失败 failed；取消 cancelled/cancelling。
// 202 仅表示受理；任务最长 180s；终态保留 30 分钟；不返回音频文件或音频流。

// ChannelTTSUtterance ≥1.5.3 独立文本播报通道（TTS ONLY）。
const ChannelTTSUtterance = "tts-utterances"

// TTSUtteranceResult 提交结果。UtteranceID 用于查询/取消与播放完成等待。
type TTSUtteranceResult struct {
	Channel     string
	UtteranceID string
}

// TTSUtteranceAvailable 设备固件是否具备独立文本播报端点（≥1.5.3）。
// 探测失败按 <1.5.3 保守处理（tts_audition 在两版本都可用）。
func (c *Client) TTSUtteranceAvailable() bool {
	return c.detectVersion().AtLeast(VerChatTurns)
}

// TTSUtterance 提交独立文本播报（设备直接外放，不调 LLM、不写聊天记录）。
//
//	text 去除首尾空白后须为 1–16000 字符；requestID 为空则自动生成 UUID。
//	请求体严格只含 request_id/slot/text（设备拒绝未知字段）。
func (c *Client) TTSUtterance(slot int, text, requestID string) (*TTSUtteranceResult, error) {
	return c.TTSUtteranceCtx(context.Background(), slot, text, requestID)
}

// TTSUtteranceCtx 同 TTSUtterance，带 context。
func (c *Client) TTSUtteranceCtx(ctx context.Context, slot int, text, requestID string) (*TTSUtteranceResult, error) {
	if requestID == "" {
		requestID = uuidOrDefault("")
	}
	body, err := json.Marshal(map[string]any{
		"request_id": requestID,
		"slot":       slot,
		"text":       text,
	})
	if err != nil {
		return nil, err
	}
	data, err := c.DoCtx(ctx, http.MethodPost, "/v1/api/data/user/tts/utterances", body, PayloadModeJSON)
	if err != nil {
		return nil, err
	}
	var shape struct {
		UtteranceID string `json:"utterance_id"`
		Data        struct {
			UtteranceID string `json:"utterance_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(data, &shape)
	id := shape.UtteranceID
	if id == "" {
		id = shape.Data.UtteranceID
	}
	if id == "" {
		return nil, fmt.Errorf("clt01: tts/utterances accepted but no utterance_id in response: %s", truncRawJSON(data))
	}
	return &TTSUtteranceResult{Channel: ChannelTTSUtterance, UtteranceID: id}, nil
}

// TTSUtteranceState 查询独立播报任务状态（原始 JSON，含 state 等字段）。
func (c *Client) TTSUtteranceState(ctx context.Context, utteranceID string) (json.RawMessage, error) {
	return c.GetRawCtx(ctx, "/v1/api/data/user/tts/utterances/"+utteranceID)
}

// TTSUtteranceCancelText 任务状态体里常用的两个字段（宿主解析用）。
type TTSUtteranceStatus struct {
	State       string `json:"state"`
	Error       string `json:"error"`
	UpdatedAtMs int64  `json:"updated_at_ms"`
}

// TTSUtteranceCancel 取消独立播报任务（空正文）。已完成的任务返回 409。
func (c *Client) TTSUtteranceCancel(ctx context.Context, utteranceID string) error {
	_, err := c.DoCtx(ctx, http.MethodPost,
		"/v1/api/data/user/tts/utterances/"+utteranceID+"/cancel", nil, PayloadModeEmpty)
	return err
}

// WaitTTSUtteranceDone 轮询直到任务到终态（原子等待，宿主也可自己轮询 State）。
//
//	返回最终状态字符串；ok=false 表示 failed/cancelled 或轮询出错/超时。
func (c *Client) WaitTTSUtteranceDone(ctx context.Context, utteranceID string, timeout time.Duration) (state string, ok bool) {
	deadline := time.Now().Add(timeout)
	interval := 500 * time.Millisecond
	for {
		raw, err := c.TTSUtteranceState(ctx, utteranceID)
		if err == nil {
			var st TTSUtteranceStatus
			if json.Unmarshal(raw, &st) == nil {
				state = strings.ToLower(strings.TrimSpace(st.State))
				switch state {
				case "completed":
					return state, true
				case "failed", "cancelled", "cancelling":
					return state, false
				}
			}
		}
		if time.Now().After(deadline) {
			return state, false
		}
		select {
		case <-ctx.Done():
			return state, false
		case <-time.After(interval):
		}
	}
}

// truncRawJSON 原始响应截断（日志用）。
func truncRawJSON(b json.RawMessage) string {
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
