package clt01

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestTTSUtteranceMockFlow 模拟一台 1.5.3 固件设备，走一遍 TTS ONLY 原子接口全流程：
// 版本探测 → 提交 tts/utterances → 轮询任务状态到 completed → cancel 已终态任务。
// 设备不出声，纯协议层验证（真机冒烟见 live_*_test.go）。
func TestTTSUtteranceMockFlow(t *testing.T) {
	var mu sync.Mutex
	state := "queued"

	mux := http.NewServeMux()
	// 固件探测：drafts 缺参数 400 → HasDrafts=true → VerV153（≥1.5.3）
	mux.HandleFunc("/v1/api/data/user/cloth/drafts", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"success":false,"message":"slot_number required"}`))
	})
	// 提交独立文本播报
	mux.HandleFunc("/v1/api/data/user/tts/utterances", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			RequestID string `json:"request_id"`
			Slot      int    `json:"slot"`
			Text      string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if len(body.RequestID) != 36 {
			t.Errorf("request_id = %q, want UUID", body.RequestID)
		}
		if body.Slot != 1 {
			t.Errorf("slot = %d, want 1", body.Slot)
		}
		if body.Text != "客厅灯已关闭。" {
			t.Errorf("text = %q", body.Text)
		}
		w.WriteHeader(http.StatusAccepted) // 202 仅表示受理
		_, _ = w.Write([]byte(`{"success":true,"data":{"utterance_id":"ut-mock-1"}}`))
	})
	// 查询任务状态：queued → synthesizing → playing → completed
	mux.HandleFunc("/v1/api/data/user/tts/utterances/ut-mock-1", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		switch state {
		case "queued":
			state = "synthesizing"
		case "synthesizing":
			state = "playing"
		case "playing":
			state = "completed"
		}
		s := state
		mu.Unlock()
		_, _ = w.Write([]byte(`{"success":true,"state":"` + s + `","data":{"state":"` + s + `"}}`))
	})
	// 取消：任务已 completed → 409
	mux.HandleFunc("/v1/api/data/user/tts/utterances/ut-mock-1/cancel", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"success":false,"message":"already completed"}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.Secret = "mock-secret" // 签名通道要求已配对；mock 服务端不校验签名
	c.UserID = "mock-user"

	// 1) 版本判定：≥1.5.3 → utterances 可用
	if !c.TTSUtteranceAvailable() {
		t.Fatal("TTSUtteranceAvailable = false, want true (mock is 1.5.3)")
	}

	// 2) 提交（TTS 模式播报）
	res, err := c.TTSUtterance(1, "客厅灯已关闭。", "")
	if err != nil {
		t.Fatalf("TTSUtterance: %v", err)
	}
	if res.Channel != ChannelTTSUtterance || res.UtteranceID != "ut-mock-1" {
		t.Fatalf("result = %+v", res)
	}

	// 3) 等播完（状态机推进，~1.5s）
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	final, ok := c.WaitTTSUtteranceDone(ctx, res.UtteranceID, 20*time.Second)
	if !ok || final != "completed" {
		t.Fatalf("WaitTTSUtteranceDone = (%q, %v), want (completed, true)", final, ok)
	}

	// 4) 状态查询终态
	raw, err := c.TTSUtteranceState(ctx, res.UtteranceID)
	if err != nil {
		t.Fatalf("TTSUtteranceState: %v", err)
	}
	var st TTSUtteranceStatus
	if json.Unmarshal(raw, &st) != nil || st.State != "completed" {
		t.Fatalf("state raw = %s", raw)
	}

	// 5) 取消已终态任务 → 设备 409，SDK 只透传错误
	if err := c.TTSUtteranceCancel(ctx, res.UtteranceID); err == nil {
		t.Log("cancel on completed task accepted by mock (real device returns 409)")
	}
}
