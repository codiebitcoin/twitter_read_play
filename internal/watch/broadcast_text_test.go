package watch

import (
	"strings"
	"testing"

	"twitter_read_play/internal/model"
)

// 纯媒体推文（只有 t.co 占位链接、正文为空）必须有兜底播报文本，
// 否则 TTS 读空串设备报 400（realDonaldTrump 大量视频推文即此情况）。
func TestPlaybackTextMediaFallback(t *testing.T) {
	cases := []struct {
		name string
		tw   model.Tweet
		want string
	}{
		{"video", model.Tweet{ID: "1", Text: "", Media: "video"}, "Just posted a new video. Check it out."},
		{"gif", model.Tweet{ID: "2", Text: "", Media: "animated_gif"}, "Just posted a new video. Check it out."},
		{"photo", model.Tweet{ID: "3", Text: "", Media: "photo"}, "Just posted a new photo. Check it out."},
		{"text wins", model.Tweet{ID: "4", Text: "hello world", Media: "video"}, "hello world"},
		{"no media no text", model.Tweet{ID: "5"}, ""},
		{"quoted media uses quote", model.Tweet{ID: "6", QuotedUser: "x", QuotedText: "quoted body", Media: "video"}, "Quoting @x: quoted body"},
	}
	for _, c := range cases {
		if got := playbackText(c.tw); got != c.want {
			t.Errorf("%s: playbackText = %q, want %q", c.name, got, c.want)
		}
	}
}

// 兜底文本必须是可朗读的（speakableText 清洗后不为空）。
func TestMediaFallbackSpeakable(t *testing.T) {
	tw := model.Tweet{ID: "1", Media: "video"}
	if got := speakableText(playbackText(tw)); strings.TrimSpace(got) == "" {
		t.Fatalf("speakableText(playbackText(media tweet)) = empty, would hit device 400")
	}
}

// 播报模板按通道取字段（SpeakTemplateFor）：chat / tts 各自独立，互不回退。
// Feed（poll）/ 手动 / 随机 / Stream 弹幕（live）/ Parody / Lab ChatRoom
// 全部汇入 broadcast() → applySpeakTransform 这一条路，统一吃这份配置。
func TestSpeakTemplatePerMode(t *testing.T) {
	m := newPoolTestManager(t)
	tw := model.Tweet{ID: "1", Text: "hello"}
	r := &model.Role{
		SpeakTemplateChat: "CHAT<{{TWEET}}>",
		SpeakTemplateTTS:  "TTS<{{TWEET}}>",
	}
	if got := m.applySpeakTransform(r, tw, "hello", "chat"); got != "CHAT<hello>" {
		t.Errorf("chat 通道模板 = %q, want CHAT<hello>", got)
	}
	if got := m.applySpeakTransform(r, tw, "hello", "tts"); got != "TTS<hello>" {
		t.Errorf("tts 通道模板 = %q, want TTS<hello>", got)
	}
	// 只配了 chat：tts 通道不回退 chat 字段，原文直出（与新默认 {{TWEET}} 等效）
	onlyChat := &model.Role{SpeakTemplateChat: "CHAT<{{TWEET}}>"}
	if got := m.applySpeakTransform(onlyChat, tw, "hello", "tts"); got != "hello" {
		t.Errorf("tts 未配模板 = %q, want hello（不回退 chat 字段）", got)
	}
	// 空模式串按 chat 处理（bookWrap 走这条路）
	if got := onlyChat.SpeakTemplateFor(""); got != "CHAT<{{TWEET}}>" {
		t.Errorf(`SpeakTemplateFor("") = %q, want CHAT<{{TWEET}}>`, got)
	}
}
