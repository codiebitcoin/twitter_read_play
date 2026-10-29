// Package llm OpenAI 兼容的 Chat Completions 客户端。
// parody 模式用它按 prompt 生成仿写推文；base_url/api_key/model 由 Settings 页配置，
// 所以 OpenAI / DeepSeek / 通义 / Ollama 等只要兼容 /chat/completions 都能直接用。
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config 一次调用的连接配置（来自全局 Settings）。
type Config struct {
	BaseURL string // 如 https://api.openai.com/v1（末尾斜杠可省）
	APIKey  string
	Model   string // 如 gpt-4o-mini / deepseek-chat
}

var httpClient = &http.Client{Timeout: 90 * time.Second}

// Ready 配置是否齐全。
func (c Config) Ready() bool { return c.BaseURL != "" && c.APIKey != "" && c.Model != "" }

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Chat 发一次 chat/completions，返回回复正文。
func Chat(ctx context.Context, cfg Config, system, user string) (string, error) {
	if !cfg.Ready() {
		return "", fmt.Errorf("LLM API 未配置完整（需要 base_url / api_key / model，在 Settings 页填）")
	}
	body, err := json.Marshal(chatRequest{
		Model:       cfg.Model,
		Temperature: 0.9, // 仿写要有点随机性
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", err
	}
	url := strings.TrimSuffix(cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("LLM 返回非 JSON（HTTP %d）: %s", resp.StatusCode, snippet(raw))
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", fmt.Errorf("LLM 报错: %s", out.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, snippet(raw))
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("LLM 没有返回任何候选: %s", snippet(raw))
	}
	text := strings.TrimSpace(out.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("LLM 返回空文本")
	}
	return text, nil
}

// snippet 截断错误体，避免把整页 HTML 灌进日志/前端。
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	r := []rune(s)
	if len(r) > 200 {
		return string(r[:200]) + "…"
	}
	return s
}
