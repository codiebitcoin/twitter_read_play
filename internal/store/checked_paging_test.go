package store

import "testing"

func checkedIDs(rows []TweetRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// 池列表分页 + 总数：前端 PoolPicker 用「before 游标 + limit」一页页往回翻缓存，
// 标题的「已加载 x / 共 N 条」靠 CountCheckedTweets / CountTweets。
// 回归点：checked 列表原先不分页（一次全量拉回），搜索结果原先硬截断在 50 条且翻不动。
func TestCheckedTweetsPagingAndCount(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTweets("r1", []TweetRow{
		{ID: "100", User: "u", Text: "apple"},
		{ID: "200", User: "u", Text: "banana"},
		{ID: "300", User: "u", Text: "cherry"},
		{ID: "400", User: "u", Text: "durian"},
		{ID: "500", User: "u", Text: "elderberry"},
		{ID: "900", User: "u", Text: "parody text", Source: "parody"},
	}); err != nil {
		t.Fatal(err)
	}
	// 新推文默认勾选：取消 300/500，random 池只剩 100/200/400
	for _, id := range []string{"300", "500"} {
		if err := s.SetTweetSelected("r1", id, "random", false); err != nil {
			t.Fatal(err)
		}
	}
	// parody 背景池只勾 300（与 random 池隔离）
	if err := s.SetTweetSelected("r1", "300", "parody", true); err != nil {
		t.Fatal(err)
	}

	if n := s.CountCheckedTweets("r1", "random"); n != 3 {
		t.Fatalf("random 池总数 = %d, want 3", n)
	}
	if n := s.CountCheckedTweets("r1", "parody"); n != 1 {
		t.Fatalf("parody 池总数 = %d, want 1", n)
	}

	// 第一页：新→旧 2 条
	page1 := s.ListCheckedTweets("r1", "", "random", "", 2)
	if ids := checkedIDs(page1); len(ids) != 2 || ids[0] != "400" || ids[1] != "200" {
		t.Fatalf("random 池第一页 = %v, want [400 200]", checkedIDs(page1))
	}
	// 第二页：before = 第一页最后一条的 tid
	page2 := s.ListCheckedTweets("r1", "", "random", page1[len(page1)-1].ID, 2)
	if ids := checkedIDs(page2); len(ids) != 1 || ids[0] != "100" {
		t.Fatalf("random 池第二页 = %v, want [100]", checkedIDs(page2))
	}
	// 翻到底之后继续翻必须返回空（前端据此显示「没有更多了」）
	if rest := s.ListCheckedTweets("r1", "", "random", page2[0].ID, 2); len(rest) != 0 {
		t.Fatalf("翻到底后应返回空，got %v", checkedIDs(rest))
	}
	// limit<=0 = 不分页（Feed 顶部池区一次展示全量的旧行为）
	if all := s.ListCheckedTweets("r1", "", "random", "", 0); len(all) != 3 {
		t.Fatalf("limit<=0 应取全部，got %v", checkedIDs(all))
	}
	// 两池数据隔离
	if bg := s.ListCheckedTweets("r1", "", "parody", "", 50); len(bg) != 1 || bg[0].ID != "300" {
		t.Fatalf("parody 池 = %v, want [300]", checkedIDs(bg))
	}
	// 仿写推文（source=parody）不进池
	if n := s.CountTweets("r1", ""); n != 5 {
		t.Fatalf("缓存真实推文总数 = %d, want 5（仿写 900 不计）", n)
	}

	// 搜索结果分页：kw 在 SQL 侧过滤，游标口径与 ListTweets 一致
	if n := s.CountTweets("r1", "anana"); n != 1 {
		t.Fatalf("kw=anana 命中数 = %d, want 1（banana）", n)
	}
	search1 := s.ListTweets("r1", "", "", "e", 2) // 命中 500/300/100
	if ids := checkedIDs(search1); len(ids) != 2 || ids[0] != "500" || ids[1] != "300" {
		t.Fatalf("搜索第一页 = %v, want [500 300]", checkedIDs(search1))
	}
	search2 := s.ListTweets("r1", "", search1[len(search1)-1].ID, "e", 2)
	if ids := checkedIDs(search2); len(ids) != 1 || ids[0] != "100" {
		t.Fatalf("搜索第二页 = %v, want [100]", checkedIDs(search2))
	}
	if n := s.CountTweets("r1", "e"); n != 3 {
		t.Fatalf("kw=e 命中数 = %d, want 3（与分页结果条数一致）", n)
	}
}
