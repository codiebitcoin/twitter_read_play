package store

import "testing"

// TestTweetByID 手动播报旧推文依赖：缓存里任意 tid（不在上游最新一页内）都要能取到，
// 且引用/转发/媒体字段与中文翻译一并带回。
func TestTweetByID(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rows := []TweetRow{
		{ID: "100", User: "musk", Text: "old tweet", CreatedAt: "2026-09-27T03:56:23Z",
			QuotedText: "quoted", QuotedUser: "a", RetweetText: "rt body", RetweetUser: "b", Media: "photo"},
		{ID: "200", User: "musk", Text: "newer"},
	}
	if err := s.SaveTweets("musk", rows); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTranslation("musk", "100", "zh", "旧推文"); err != nil {
		t.Fatal(err)
	}

	got, ok := s.TweetByID("musk", "100")
	if !ok {
		t.Fatal("cached old tweet 100 not found")
	}
	if got.Text != "old tweet" || got.QuotedText != "quoted" || got.QuotedUser != "a" ||
		got.RetweetText != "rt body" || got.RetweetUser != "b" || got.Media != "photo" {
		t.Fatalf("field mismatch: %+v", got)
	}
	if got.Translation != "旧推文" {
		t.Fatalf("translation = %q, want 旧推文", got.Translation)
	}
	if _, ok := s.TweetByID("musk", "999"); ok {
		t.Fatal("unknown tid must not be found")
	}
	if _, ok := s.TweetByID("other", "100"); ok {
		t.Fatal("other role must not see this tweet")
	}
}
