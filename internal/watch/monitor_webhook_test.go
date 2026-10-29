package watch

import (
	"strings"
	"testing"

	"twitter_read_play/internal/store"
)

func newMonitorTestManager(t *testing.T) *Manager {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return &Manager{st: st}
}

// 公网地址没有默认值：未配置时必须报错，而不是拼出相对路径。
func TestWebhookURLRequiresConfiguredBase(t *testing.T) {
	m := newMonitorTestManager(t)
	if DefaultWebhookBase != "" {
		t.Fatalf("DefaultWebhookBase should be empty, got %q", DefaultWebhookBase)
	}
	if _, err := m.webhookURL(); err == nil {
		t.Fatal("webhookURL() should fail when the public base URL is not configured")
	}
	// 只有空白也不行
	_ = m.st.SetSetting("socialdata_webhook_base", "   ")
	if _, err := m.webhookURL(); err == nil {
		t.Fatal("webhookURL() should fail on blank base URL")
	}
}

func TestWebhookURLJoinsPathAndTrimsSlash(t *testing.T) {
	m := newMonitorTestManager(t)
	_ = m.st.SetSetting("socialdata_webhook_base", "https://trp.example.com/")
	got, err := m.webhookURL()
	if err != nil {
		t.Fatalf("webhookURL: %v", err)
	}
	if want := "https://trp.example.com/api/webhook/socialdata"; got != want {
		t.Fatalf("webhookURL() = %q, want %q", got, want)
	}
}

// 不带 scheme 的地址会被 socialdata.tools 拒绝，提前拦下并给出可读错误。
func TestWebhookURLRejectsMissingScheme(t *testing.T) {
	m := newMonitorTestManager(t)
	_ = m.st.SetSetting("socialdata_webhook_base", "trp.example.com")
	_, err := m.webhookURL()
	if err == nil {
		t.Fatal("webhookURL() should reject a base URL without scheme")
	}
	if !strings.Contains(err.Error(), "http") {
		t.Fatalf("error should mention http/https, got %v", err)
	}
}
