package livechat

import (
	"strings"
	"testing"
)

// H2 回归：chat_downloader 的解释器路径历史上来自页面保存的 python_path，
// 直接传 exec.CommandContext，可执行任意程序（如 /bin/sh）。修复后：
// 页面字段一律剥离/拒绝，解释器只认 TRP_CHATDL_PYTHON 环境变量。
func TestValidateRejectsExecFields(t *testing.T) {
	base := SourceConfig{Kind: KindChatDownloader, Channel: "https://youtube.com/live/x"}
	if err := base.Validate(); err != nil {
		t.Fatalf("baseline config should validate: %v", err)
	}
	for field, mutate := range map[string]func(*SourceConfig){
		"python_path": func(c *SourceConfig) { c.PythonPath = "/bin/sh" },
		"extra_args":  func(c *SourceConfig) { c.ExtraArgs = "--start_time 1; rm -rf /" },
	} {
		c := base
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s set: Validate must reject", field)
		} else if !strings.Contains(err.Error(), "TRP_CHATDL_PYTHON") {
			t.Errorf("%s set: error should point at TRP_CHATDL_PYTHON, got %v", field, err)
		}
	}
}

func TestSaveConfigStripsExecFields(t *testing.T) {
	m, _ := newTestManager(t)
	saved, err := m.SaveConfig(SourceConfig{
		Kind:       KindChatDownloader,
		Channel:    "https://youtube.com/live/x",
		PythonPath: "/bin/sh",
		ExtraArgs:  "-c evil",
	})
	if err != nil {
		t.Fatalf("SaveConfig with legacy exec fields must strip silently, got: %v", err)
	}
	if saved.PythonPath != "" || saved.ExtraArgs != "" {
		t.Errorf("exec fields must be stripped on save, got python_path=%q extra_args=%q", saved.PythonPath, saved.ExtraArgs)
	}
}

func TestChatdlPythonFromEnv(t *testing.T) {
	t.Setenv("TRP_CHATDL_PYTHON", "/usr/local/bin/python3.12")
	if got := chatdlPython(); got != "/usr/local/bin/python3.12" {
		t.Errorf("chatdlPython = %q, want env value", got)
	}
	t.Setenv("TRP_CHATDL_PYTHON", "   ")
	if got := chatdlPython(); got != "python3" {
		t.Errorf("blank env must fall back to python3, got %q", got)
	}
}
