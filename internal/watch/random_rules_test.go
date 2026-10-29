package watch

import (
	"testing"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
)

func TestParseTweetTimeFormats(t *testing.T) {
	cases := []struct {
		in   string
		want string // 期望解析出的 UTC 日期（YYYY-MM-DD），空=应解析失败
	}{
		{"2026-09-27T03:56:23Z", "2026-09-27"},
		{"2026-09-27T03:56:23+00:00", "2026-09-27"},
		{"2026-09-27T03:56:23", "2026-09-27"},
		{"2026-09-27 03:56:23", "2026-09-27"},
		{"Sat Sep 27 03:56:23 +0000 2026", "2026-09-27"}, // Twitter 经典格式
		{"2026-09-27", "2026-09-27"},
		{"", ""},
		{"not a time", ""},
	}
	for _, c := range cases {
		got, ok := parseTweetTime(c.in)
		if c.want == "" {
			if ok {
				t.Errorf("parseTweetTime(%q) 应失败，got %v", c.in, got)
			}
			continue
		}
		if !ok {
			t.Errorf("parseTweetTime(%q) 解析失败", c.in)
			continue
		}
		if d := got.UTC().Format("2006-01-02"); d != c.want {
			t.Errorf("parseTweetTime(%q) = %s, want %s", c.in, d, c.want)
		}
	}
}

func TestInDateRange(t *testing.T) {
	cases := []struct {
		name      string
		createdAt string
		from, to  string
		want      bool
	}{
		{"no range", "2026-09-27T03:56:23Z", "", "", true},
		{"inside", "2026-09-27T03:56:23Z", "2026-09-01", "2026-09-30", true},
		{"before from", "2026-08-31T23:59:59Z", "2026-09-01", "", false},
		{"after to", "2026-10-01T00:00:01Z", "", "2026-09-30", false},
		{"to is inclusive (same day)", "2026-09-30T23:59:59Z", "", "2026-09-30", true},
		{"from is inclusive (same day)", "2026-09-01T00:00:01Z", "2026-09-01", "", true},
		{"unparseable time excluded", "??", "2026-09-01", "2026-09-30", false},
	}
	for _, c := range cases {
		if got := inDateRange(c.createdAt, c.from, c.to); got != c.want {
			t.Errorf("%s: inDateRange = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRandomRulesOK(t *testing.T) {
	cases := []struct {
		name string
		role model.Role
		tw   model.Tweet
		want bool
	}{
		{"no rules", model.Role{}, model.Tweet{Text: "hello"}, true},
		{"require hit", model.Role{RandomRequireWords: "mars"}, model.Tweet{Text: "to mars"}, true},
		{"require miss", model.Role{RandomRequireWords: "mars"}, model.Tweet{Text: "hello"}, false},
		{"require any-of", model.Role{RandomRequireWords: "mars, moon"}, model.Tweet{Text: "the moon"}, true},
		{"exclude hit", model.Role{RandomExcludeWords: "crypto"}, model.Tweet{Text: "buy crypto now"}, false},
		{"exclude miss", model.Role{RandomExcludeWords: "crypto"}, model.Tweet{Text: "to mars"}, true},
		{"require in quoted", model.Role{RandomRequireWords: "mars"}, model.Tweet{QuotedText: "mars soon"}, true},
		{"date out of range", model.Role{RandomFrom: "2026-09-01", RandomTo: "2026-09-30"},
			model.Tweet{Text: "hi", CreatedAt: "2026-08-01T00:00:00Z"}, false},
		{"date in range", model.Role{RandomFrom: "2026-09-01", RandomTo: "2026-09-30"},
			model.Tweet{Text: "hi", CreatedAt: "2026-09-15T00:00:00Z"}, true},
	}
	for _, c := range cases {
		if got := randomRulesOK(&c.role, c.tw); got != c.want {
			t.Errorf("%s: randomRulesOK = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCleanParodyText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"hello world", "hello world"},
		{"```\nhello world\n```", "hello world"},
		{"\"hello world\"", "hello world"},
		{"Post: hello world", "hello world"},
		{"「hello world」", "hello world"},
		{"  spaced  ", "spaced"},
	}
	for _, c := range cases {
		if got := cleanLLMText(c.in); got != c.want {
			t.Errorf("cleanLLMText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// random 模式池：SQL 侧按来源/勾选/点赞过滤，Go 侧再按关键词与日期过滤。
func TestRandomModePoolFilters(t *testing.T) {
	m := newPoolTestManager(t)
	const created = "2026-09-15T00:00:00Z" // 固定发布日期，便于测日期规则
	rows := []struct {
		id      string
		text    string
		likes   int
		checked bool
		source  string
	}{
		{"900", "to mars and back", 100, true, "real"},
		{"800", "buy crypto now", 500, true, "real"},
		{"700", "to mars again", 5, true, "real"},
		{"600", "unchecked mars post", 999, false, "real"},
		{"500", "a generated parody", 0, true, "parody"},
	}
	for _, r := range rows {
		if err := m.st.SaveTweets("r1", []store.TweetRow{{
			ID: r.id, Text: r.text, Likes: r.likes, Source: r.source, CreatedAt: created,
		}}); err != nil {
			t.Fatal(err)
		}
		if !r.checked {
			if err := m.st.SetTweetSelected("r1", r.id, "random", false); err != nil {
				t.Fatal(err)
			}
		}
	}
	role := &model.Role{ID: "r1"}
	if got := len(m.randomModePool(role)); got != 4 {
		t.Errorf("无规则时应命中 4 条真实推文（parody 不算），got %d", got)
	}
	role.RandomRequireWords = "mars"
	if got := len(m.randomModePool(role)); got != 3 {
		t.Errorf("必须含 mars 应命中 3 条，got %d", got)
	}
	role.RandomExcludeWords = "crypto"
	if got := len(m.randomModePool(role)); got != 3 {
		t.Errorf("排除 crypto 后仍是 3 条，got %d", got)
	}
	role.RandomMinLikes = 50 // 900(100) 与 600(999) 达标，700(5) 不达标
	if got := len(m.randomModePool(role)); got != 2 {
		t.Errorf("点赞≥50 且含 mars 应剩 2 条，got %d", got)
	}
	role.RandomMinLikes = 0
	role.RandomOnlyChecked = true
	if got := len(m.randomModePool(role)); got != 2 {
		t.Errorf("只看勾选（600 未勾选）应剩 2 条，got %d", got)
	}
	role.RandomOnlyChecked = false
	role.RandomFrom = "2099-01-01" // 未来日期：一条都不该命中
	if got := len(m.randomModePool(role)); got != 0 {
		t.Errorf("日期区间在未来应 0 条，got %d", got)
	}
}
