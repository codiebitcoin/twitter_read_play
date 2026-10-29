package watch

import (
	"testing"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
)

func newPoolTestManager(t *testing.T) *Manager {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return &Manager{st: st}
}

// 每次同步条数：角色可配，缺省/越界回落到 20。
func TestFetchCountDefault(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, 20}, {-5, 20}, {1, 1}, {20, 20}, {50, 50}, {100, 100}, {101, 20}, {999, 20},
	}
	for _, c := range cases {
		if got := fetchCount(&model.Role{FetchCount: c.in}); got != c.want {
			t.Errorf("fetchCount(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// 播报池闸门：排除词 / 必须包含词 / 无正文开关。
func TestBroadcastableFilters(t *testing.T) {
	m := newPoolTestManager(t)
	cases := []struct {
		name string
		role model.Role
		tw   model.Tweet
		want bool
	}{
		{"plain text ok", model.Role{}, model.Tweet{ID: "1", Text: "hello"}, true},
		{"exclude word hits", model.Role{FilterWords: "hello"}, model.Tweet{ID: "1", Text: "hello world"}, false},
		{"exclude word misses", model.Role{FilterWords: "nope"}, model.Tweet{ID: "1", Text: "hello"}, true},
		{"require word hits", model.Role{RequireWords: "mars"}, model.Tweet{ID: "1", Text: "to mars!"}, true},
		{"require word misses", model.Role{RequireWords: "mars"}, model.Tweet{ID: "1", Text: "hello"}, false},
		{"require matches quoted", model.Role{RequireWords: "mars"}, model.Tweet{ID: "1", QuotedText: "mars now"}, true},
		{"require matches retweet", model.Role{RequireWords: "mars"}, model.Tweet{ID: "1", RetweetText: "mars now"}, true},
		{"media only skipped by default", model.Role{}, model.Tweet{ID: "1", Media: "video"}, false},
		{"media only allowed when opted in", model.Role{AllowEmpty: true}, model.Tweet{ID: "1", Media: "video"}, true},
		{"retweet body counts as text", model.Role{}, model.Tweet{ID: "1", RetweetText: "rt body"}, true},
		{"edited text counts", model.Role{}, model.Tweet{ID: "1", Edited: "user text"}, true},
	}
	for _, c := range cases {
		if got := m.broadcastable(&c.role, c.tw); got != c.want {
			t.Errorf("%s: broadcastable = %v, want %v", c.name, got, c.want)
		}
	}
}

// 随机播报池来自「已勾选」的本地缓存 —— 包括不在上游最新一页里的老推文。
func TestBroadcastPoolUsesSelectionAndCache(t *testing.T) {
	m := newPoolTestManager(t)
	if err := m.st.SaveTweets("r1", []store.TweetRow{
		{ID: "300", Text: "newest"},
		{ID: "200", Text: "middle"},
		{ID: "100", Text: "oldest"},
	}); err != nil {
		t.Fatal(err)
	}
	// 取消勾选最新两条 → 只剩最老的那条
	for _, tid := range []string{"300", "200"} {
		if err := m.st.SetTweetSelected("r1", tid, "random", false); err != nil {
			t.Fatal(err)
		}
	}
	r := &model.Role{ID: "r1"}
	pool := m.broadcastPool(r)
	if len(pool) != 1 || pool[0].ID != "100" {
		t.Fatalf("pool = %+v, want only checked oldest tweet 100", pool)
	}
	if pool[0].Text != "oldest" {
		t.Errorf("老推文应能进池，text = %q", pool[0].Text)
	}
	// 排除词把最后一条也筛掉 → 空池
	r.FilterWords = "oldest"
	if got := m.broadcastPool(r); len(got) != 0 {
		t.Fatalf("排除词命中后池应为空，got %d", len(got))
	}
	// 必须包含词命中才放行
	r = &model.Role{ID: "r1", RequireWords: "oldest"}
	if got := m.broadcastPool(r); len(got) != 1 {
		t.Fatalf("白名单命中应保留 1 条，got %d", len(got))
	}
}
