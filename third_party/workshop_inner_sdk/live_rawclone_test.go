package clt01

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"os"
	"testing"
	"time"
)

// 原始克隆调用：打印完整请求要素 + 设备原始响应（body/状态/耗时）
func TestRawCloneCall252(t *testing.T) {
	c := liveClient(t)
	if c.Secret == "" {
		t.Skip("no secret")
	}
	audio, err := os.ReadFile(envOr("CLT01_VOICE_SAMPLE"))
	if err != nil {
		t.Fatalf("sample: %v", err)
	}

	// 构造与 VoiceCloneUpload 完全一致的 multipart（file/group_key/voice_id/voice_name）
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("group_key", "sg_tts_en_group")
	_ = w.WriteField("voice_id", "21")
	_ = w.WriteField("voice_name", "raw-diag")
	part, _ := w.CreateFormFile("file", "trump_can_voice_clone.mp3")
	_, _ = part.Write(audio)
	_ = w.Close()

	path := "/v1/api/data/user/voice_clone/upload"
	ct := w.FormDataContentType()
	t.Logf("=== REQUEST ===")
	t.Logf("POST %s%s", c.BaseURL, path)
	t.Logf("Content-Type: %s", ct)
	t.Logf("fields: group_key=sg_tts_en_group voice_id=21 voice_name=raw-diag")
	t.Logf("file: trump_can_voice_clone.mp3 (%d bytes, %s)", len(audio), fileExt(audio))
	t.Logf("multipart body: %d bytes", buf.Len())

	start := time.Now()
	raw, err := c.doRawBytesCT(context.Background(), http.MethodPost, path, buf.Bytes(), PayloadModeMultipart, ct)
	elapsed := time.Since(start).Round(time.Millisecond)
	t.Logf("=== RESPONSE ===")
	t.Logf("elapsed: %s", elapsed)
	if ae, ok := err.(*APIError); ok {
		t.Logf("HTTP status: %d", ae.StatusCode)
		t.Logf("code: %q", ae.Code)
		t.Logf("message: %q", ae.Message)
	} else if err != nil {
		t.Logf("transport error: %v", err)
	}
	t.Logf("raw body: %q", string(raw))

}

func fileExt(b []byte) string {
	if len(b) > 2 && b[0] == 0x49 && b[1] == 0x44 && b[2] == 0x33 {
		return "mp3(ID3)"
	}
	if len(b) > 1 && b[0] == 0xFF && (b[1]&0xE0) == 0xE0 {
		return "mp3(frame)"
	}
	return "unknown"
}
