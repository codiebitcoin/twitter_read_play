package livechat

import "testing"

// Web 模式拿不到 YouTube 的流式聊天响应，只能读渲染后的 DOM；这里锁住解析口径。
func TestParseYouTubeChatDOM(t *testing.T) {
	raw := `[
	  {"id":"m1","kind":"yt-live-chat-text-message-renderer","author":"@alice","text":"hello","amount":""},
	  {"id":"m2","kind":"yt-live-chat-paid-message-renderer","author":"@bob","text":"","amount":"¥5.00"},
	  {"id":"","kind":"yt-live-chat-text-message-renderer","author":"","text":"","amount":""}
	]`
	msgs := parseYouTubeChatDOM(raw)
	if len(msgs) != 2 {
		t.Fatalf("want 2 messages, got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].ID != "m1" || msgs[0].User != "@alice" || msgs[0].Text != "hello" || msgs[0].Kind != "text" {
		t.Fatalf("text message parsed wrong: %+v", msgs[0])
	}
	if msgs[1].Kind != "superchat" || msgs[1].Amount != "¥5.00" || msgs[1].Text != "¥5.00" {
		t.Fatalf("superchat parsed wrong: %+v", msgs[1])
	}
	// 空行必须丢弃，否则每轮都会推一条空消息。
	for _, m := range msgs {
		if m.Text == "" {
			t.Fatalf("empty message leaked: %+v", m)
		}
	}
}
