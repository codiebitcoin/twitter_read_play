package store

import "testing"

// TestSaveTweetsUpsertKeepsUserData 回归：同一 tid 再次同步（上游刷新）时，
// 去重必须生效，且用户的勾选（selected）与编辑（edited）不能被重置。
// 原实现 INSERT OR REPLACE 是「先删后插」，会把这两列打回默认值。
func TestSaveTweetsUpsertKeepsUserData(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTweets("musk", []TweetRow{{ID: "100", User: "musk", Text: "hello"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTweetSelected("musk", "100", "random", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTweetEdit("musk", "100", "user edited"); err != nil {
		t.Fatal(err)
	}

	// 下一轮同步：同一条 + 一条新的
	if err := s.SaveTweets("musk", []TweetRow{
		{ID: "100", User: "musk", Text: "hello (upstream)"},
		{ID: "200", User: "musk", Text: "newer"},
	}); err != nil {
		t.Fatal(err)
	}

	if n := s.TweetCount("musk"); n != 2 {
		t.Fatalf("去重失败：条数 = %d，want 2", n)
	}
	got, ok := s.TweetByID("musk", "100")
	if !ok {
		t.Fatal("tweet 100 missing")
	}
	if got.Selected {
		t.Error("selected 被上游刷新重置成勾选了")
	}
	if got.Edited != "user edited" {
		t.Errorf("edited 被上游刷新清掉了：%q", got.Edited)
	}
	if got.Text != "hello (upstream)" {
		t.Errorf("上游原文应更新：%q", got.Text)
	}
	// 新推文默认勾选
	if nw, _ := s.TweetByID("musk", "200"); !nw.Selected {
		t.Error("新推文应默认勾选")
	}
}

// 点赞数「有就覆盖」：上游刷新时取较大值（点赞只会涨，不因某次返回 0 而清掉）。
func TestSaveTweetsLikesOverwrite(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	save := func(likes int) {
		t.Helper()
		if err := s.SaveTweets("r1", []TweetRow{{ID: "100", Text: "hi", Likes: likes}}); err != nil {
			t.Fatal(err)
		}
	}
	save(10)
	if got, _ := s.TweetByID("r1", "100"); got.Likes != 10 {
		t.Fatalf("likes = %d, want 10", got.Likes)
	}
	save(5) // 上游这次返回更小的值：不应把已有的 10 覆盖掉
	if got, _ := s.TweetByID("r1", "100"); got.Likes != 10 {
		t.Fatalf("likes = %d, want 10（不应被更小的值覆盖）", got.Likes)
	}
	save(99) // 涨了：覆盖
	if got, _ := s.TweetByID("r1", "100"); got.Likes != 99 {
		t.Fatalf("likes = %d, want 99", got.Likes)
	}
}

// 三种模式数据独立：broadcast/random 只看 source=real，parody 只看 source=parody。
func TestTweetSourcesAreSeparate(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTweets("r1", []TweetRow{
		{ID: "900", Text: "real tweet", Source: "real", Likes: 100},
		{ID: "800", Text: "parody tweet", Source: "parody"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.ListTweets("r1", "", "", "", 50); len(got) != 1 || got[0].ID != "900" {
		t.Fatalf("ListTweets（broadcast/random 用）应只含真实推文，got %+v", got)
	}
	if got := s.FilterTweets("r1", "parody", false, 0, 50); len(got) != 1 || got[0].ID != "800" {
		t.Fatalf("FilterTweets(source=parody) 应只含仿写，got %+v", got)
	}
	// 来源为空 = 不限
	if got := s.FilterTweets("r1", "", false, 0, 50); len(got) != 2 {
		t.Fatalf("source 为空应返回全部，got %d", len(got))
	}
	// 点赞下限
	if got := s.FilterTweets("r1", "", false, 50, 50); len(got) != 1 || got[0].ID != "900" {
		t.Fatalf("点赞≥50 应只剩 900，got %+v", got)
	}
	// 只看勾选
	if err := s.SetTweetSelected("r1", "900", "random", false); err != nil {
		t.Fatal(err)
	}
	if got := s.FilterTweets("r1", "", true, 0, 50); len(got) != 1 || got[0].ID != "800" {
		t.Fatalf("只看勾选应只剩 800，got %+v", got)
	}
	// 删除（parody 手动删除用）
	if err := s.DeleteTweet("r1", "800"); err != nil {
		t.Fatal(err)
	}
	if got := s.FilterTweets("r1", "parody", false, 0, 50); len(got) != 0 {
		t.Fatalf("删除后应为空，got %+v", got)
	}
}

// 新列（images/link/comment_count/share_count/collect_count）必须与 tweetCols/scanTweet 列数对齐：
// 写入后逐字段读回，任何一列错位都会在这里暴露。
func TestSaveTweetsRoundTripsSocialFields(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in := TweetRow{
		ID: "65f0a1b2c3d4e5f60718293a", User: "peopledaily", Text: "微博正文",
		CreatedAt: "2026-10-14T20:00:00+08:00",
		Images:    []string{"https://img.example/a.jpg", "https://img.example/b.jpg"},
		Link:      "https://weibo.com/1234/5021234567890123",
		Likes:     42, CommentCount: 7, ShareCount: 3, CollectCount: 1,
		Media: "photo", Source: "real",
	}
	if err := s.SaveTweets("r1", []TweetRow{in}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.TweetByID("r1", in.ID)
	if !ok {
		t.Fatal("刚写入的推文读不回来")
	}
	if len(got.Images) != 2 || got.Images[1] != "https://img.example/b.jpg" {
		t.Errorf("Images = %v", got.Images)
	}
	if got.Link != in.Link {
		t.Errorf("Link = %q, want %q", got.Link, in.Link)
	}
	if got.CommentCount != 7 || got.ShareCount != 3 || got.CollectCount != 1 || got.Likes != 42 {
		t.Errorf("计数列错位: likes=%d comment=%d share=%d collect=%d", got.Likes, got.CommentCount, got.ShareCount, got.CollectCount)
	}
	if got.Media != "photo" || got.User != "peopledaily" || got.CreatedAt != in.CreatedAt {
		t.Errorf("其余列错位: %+v", got)
	}
	// 列表路径（ListTweets 走同一份 tweetCols）也要一致
	rows := s.ListTweets("r1", "", "", "", 50)
	if len(rows) != 1 || len(rows[0].Images) != 2 || rows[0].CommentCount != 7 {
		t.Fatalf("ListTweets 读回的字段不对: %+v", rows)
	}
	// 空载荷（webhook 精简数据）不应把已存的配图/链接清掉，计数也只增不减
	if err := s.SaveTweets("r1", []TweetRow{{ID: in.ID, Text: "微博正文（更新）"}}); err != nil {
		t.Fatal(err)
	}
	got2, _ := s.TweetByID("r1", in.ID)
	if len(got2.Images) != 2 || got2.Link != in.Link {
		t.Errorf("空 images/link 覆盖掉了已有值: %+v", got2)
	}
	if got2.CommentCount != 7 || got2.ShareCount != 3 || got2.CollectCount != 1 {
		t.Errorf("计数不应被更小的值覆盖: %+v", got2)
	}
	if got2.Text != "微博正文（更新）" {
		t.Errorf("正文应更新: %q", got2.Text)
	}
}

// platform 列：接口要能区分这条帖子来自哪个上游（微博/小红书/twitter）。
func TestSaveTweetsPlatformRoundTrip(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rows := []TweetRow{
		{ID: "1", User: "a", Text: "微博内容", Platform: "weibo"},
		{ID: "2", User: "b", Text: "小红书内容", Platform: "xiaohongshu"},
	}
	if err := st.SaveTweets("r1", rows); err != nil {
		t.Fatal(err)
	}
	got := st.ListTweets("r1", "", "", "", 0)
	if len(got) != 2 {
		t.Fatalf("应有 2 行，实际 %d", len(got))
	}
	byID := map[string]string{}
	for _, r := range got {
		byID[r.ID] = r.Platform
	}
	if byID["1"] != "weibo" || byID["2"] != "xiaohongshu" {
		t.Fatalf("platform 落库/读出不对: %v", byID)
	}
	// 后续 webhook 精简载荷（platform 为空）不能把已存的平台冲掉
	if err := st.SaveTweets("r1", []TweetRow{{ID: "1", User: "a", Text: "更新", Platform: ""}}); err != nil {
		t.Fatal(err)
	}
	for _, r := range st.ListTweets("r1", "", "", "", 0) {
		if r.ID == "1" && r.Platform != "weibo" {
			t.Fatalf("空 platform 不应覆盖旧值，实际 %q", r.Platform)
		}
	}
}
