package watch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"twitter_read_play/internal/llm"
	"twitter_read_play/internal/model"
)

// tts 模式的口语化链路：tweet → LLM(SpeakPrompt) → tts_audition。
// 设备 chat 入口的流式 TTS 对部分音色分组不产音频（见 docs/chat_qwen_audio_bug.md），
// 所以改成「我们自己用 LLM 口语化，再走稳定的 tts_audition 直读」。
// 需要 Settings 里配好 LLM；没配就走不了这条路（前端连开关都不显示）。

// defaultSpeakRewritePrompt 角色没配 SpeakPrompt 时的默认口语化指令。
const defaultSpeakRewritePrompt = `Rewrite the following post as natural spoken language for a text-to-speech voice.
Expand @mentions into spoken names, drop URLs, keep the original meaning and language, keep it short and conversational.
Output ONLY the rewritten text — no quotes, no labels, no explanation.`

// rewriteForSpeech 用配置的 LLM 按角色 SpeakPrompt 把推文改写成口语化文本（tts 模式专用）。
// 没配 LLM 或调用失败时返回错误，调用方回退到原文朗读 —— 「能播出来」优先于「播得漂亮」。
func (m *Manager) rewriteForSpeech(r *model.Role, raw string) (string, error) {
	cfg := m.llmConfig()
	if !cfg.Ready() {
		return "", fmt.Errorf("LLM API 未配置完整（base_url / api_key / model，在 Settings 页填）")
	}
	system := strings.TrimSpace(r.SpeakPrompt)
	if system == "" {
		system = defaultSpeakRewritePrompt
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := llm.Chat(ctx, cfg, system, strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return cleanLLMText(out), nil
}

// LLMReady 供接口层判断「有没有 LLM」——前端据此决定要不要显示 tts 口语化开关。
func (m *Manager) LLMReady() bool { return m.llmConfig().Ready() }
