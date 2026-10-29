package watch

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"twitter_read_play/internal/model"
)

// fakeLLM 起一个假的 OpenAI 兼容端点，返回固定回复。
func fakeLLM(t *testing.T, reply string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("路径 = %s，want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + jsonQuote(reply) + `}}]}`))
		} else {
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jsonQuote(s string) string {
	b := strings.ReplaceAll(s, `\`, `\\`)
	b = strings.ReplaceAll(b, `"`, `\"`)
	b = strings.ReplaceAll(b, "\n", `\n`)
	return `"` + b + `"`
}

// tts 口语化链路：配好 LLM 后，rewriteForSpeech 走 LLM 并按 SpeakPrompt 清洗结果。
func TestRewriteForSpeech(t *testing.T) {
	srv := fakeLLM(t, "Post: \"Hello at elon, check this out\"", http.StatusOK)
	m := newPoolTestManager(t)
	for k, v := range map[string]string{
		"llm_base_url": srv.URL, "llm_api_key": "test-key", "llm_model": "gpt-test",
	} {
		if err := m.st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if !m.LLMReady() {
		t.Fatal("配好三项后 LLMReady 应为 true")
	}
	got, err := m.rewriteForSpeech(&model.Role{Name: "Elon", SpeakPrompt: "make it spoken"}, "Hello @elon, check this out https://t.co/x")
	if err != nil {
		t.Fatalf("rewriteForSpeech: %v", err)
	}
	if got != "Hello at elon, check this out" {
		t.Errorf("清洗后 = %q，want %q", got, "Hello at elon, check this out")
	}
}

// 没配 LLM：明确报错（前端据此不显示开关），而不是静默读原文。
func TestRewriteForSpeechNotConfigured(t *testing.T) {
	m := newPoolTestManager(t)
	if m.LLMReady() {
		t.Fatal("没配 LLM 时 LLMReady 应为 false")
	}
	if _, err := m.rewriteForSpeech(&model.Role{}, "hi"); err == nil {
		t.Fatal("没配 LLM 应返回错误")
	}
}

// LLM 报错时也要返回错误，让调用方回退原文朗读。
func TestRewriteForSpeechLLMError(t *testing.T) {
	srv := fakeLLM(t, "", http.StatusInternalServerError)
	m := newPoolTestManager(t)
	for k, v := range map[string]string{
		"llm_base_url": srv.URL, "llm_api_key": "test-key", "llm_model": "gpt-test",
	} {
		if err := m.st.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.rewriteForSpeech(&model.Role{}, "hi"); err == nil {
		t.Fatal("LLM 500 应返回错误")
	}
}
