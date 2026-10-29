package livechat

import "testing"

// 通用过滤器（livechat.Filter）的跨平台判定：
// 黑名单用户最优先 → 仅SC/过滤SC → 等级 → 黑名单词 → 必须词。
func TestFilterPassOrder(t *testing.T) {
	f := Filter{
		BlockUsers: []string{"spam"},
		BlockWords: []string{"广告"},
	}
	// 黑名单用户优先于一切（就算正文命中必须词也不播）
	if f.Pass(Message{User: "SPAM", Text: "来看广告", Kind: "superchat", Amount: "¥30"}) {
		t.Fatal("blacklisted user must be dropped first")
	}
	// 黑名单词命中（用户名+正文+金额拼接后子串匹配）
	if f.Pass(Message{User: "alice", Text: "最新广告上线"}) {
		t.Fatal("block word must drop the message")
	}
	if !f.Pass(Message{User: "alice", Text: "正常聊天"}) {
		t.Fatal("normal chat should pass")
	}
}

func TestFilterSpecialToggles(t *testing.T) {
	sc := Message{User: "a", Text: "谢谢", Kind: "superchat", Amount: "¥30"}
	chat := Message{User: "b", Text: "hello"}
	// 仅SC：普通弹幕滤掉，SC 放行
	only := Filter{OnlySpecial: true}
	if only.Pass(chat) {
		t.Fatal("OnlySpecial must drop plain chat")
	}
	if !only.Pass(sc) {
		t.Fatal("OnlySpecial must keep SC")
	}
	// 过滤SC：SC 滤掉，普通弹幕放行
	hide := Filter{HideSpecial: true}
	if hide.Pass(sc) {
		t.Fatal("HideSpecial must drop SC")
	}
	if !hide.Pass(chat) {
		t.Fatal("HideSpecial must keep plain chat")
	}
}

func TestFilterMinLevelSkipsUnknownLevel(t *testing.T) {
	f := Filter{MinLevel: 10}
	if f.Pass(Message{User: "low", Text: "hi", UserLevel: 3}) {
		t.Fatal("below min level must be dropped")
	}
	if !f.Pass(Message{User: "high", Text: "hi", UserLevel: 10}) {
		t.Fatal("at min level should pass")
	}
	// 平台不给等级（0）：等级门槛自动放行 —— 否则 Twitch/YouTube 会被整路滤没
	if !f.Pass(Message{User: "unknown", Text: "hi"}) {
		t.Fatal("unknown level (0) must pass the level gate")
	}
}

func TestFilterRequireWords(t *testing.T) {
	f := Filter{RequireWords: []string{"抽奖"}}
	if f.Pass(Message{User: "a", Text: "随便聊聊"}) {
		t.Fatal("miss on require words should drop")
	}
	if !f.Pass(Message{User: "a", Text: "参加抽奖啦"}) {
		t.Fatal("hit on require words should pass")
	}
	// 空正文且非打赏：滤掉（避免把系统行播出去）
	if f.Pass(Message{User: "a", Text: "  "}) {
		t.Fatal("empty text without special kind should drop")
	}
}

func TestParseWords(t *testing.T) {
	got := ParseWords("a, b，c;d；e\nf\tg a")
	want := []string{"a", "b", "c", "d", "e", "f", "g"}
	if len(got) != len(want) {
		t.Fatalf("ParseWords = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseWords = %v, want %v", got, want)
		}
	}
	if len(ParseWords(" , ，； ")) != 0 {
		t.Fatal("separators only → empty list")
	}
}

// 能力位：B 站链路带用户等级，其它平台没有（前端据此隐藏「最低等级」设置项）。
func TestCapabilitiesFor(t *testing.T) {
	if !CapabilitiesFor("bilibili_blivedm").UserLevel {
		t.Fatal("blivedm should report user_level")
	}
	if CapabilitiesFor("twitch_browser").UserLevel || CapabilitiesFor("youtube_api").UserLevel {
		t.Fatal("twitch/youtube must not report user_level")
	}
}
