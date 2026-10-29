package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"twitter_read_play/internal/store"
)

// 池列表的游标分页与总数走 HTTP handler：
//
//	?checked=1&pool=random&limit=2 → 一页 2 条
//	?checked=1&pool=random&before=X → 往回翻
//	?checked=1&pool=random&count=1  → {"total":N}（不返回行数据）
//	?count=1&kw=…                   → 搜索结果总数
//
// 回归点：checked 列表原先忽略 before/limit（一次全量），前端只有 50 条封顶翻不动。
func TestRoleTweetsCheckedPaging(t *testing.T) {
	s := newQueueTestServer(t)
	roleID := "musk" // 空库初始化会插入内置角色 musk
	if err := s.Store.SaveTweets(roleID, []store.TweetRow{
		{ID: "100", User: "musk", Text: "apple"},
		{ID: "200", User: "musk", Text: "banana"},
		{ID: "300", User: "musk", Text: "cherry"},
	}); err != nil {
		t.Fatal(err)
	}
	// 新推文默认勾选；取消 100，random 池剩 200/300
	if err := s.Store.SetTweetSelected(roleID, "100", "random", false); err != nil {
		t.Fatal(err)
	}

	get := func(query string) []string {
		t.Helper()
		rec := httptest.NewRecorder()
		req := withPathValues(httptest.NewRequest(http.MethodGet, "/api/roles/musk/tweets?"+query, nil), "id", roleID)
		s.roleTweets(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", query, rec.Code, rec.Body.String())
		}
		var rows []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatalf("GET %s 非 JSON: %v (%s)", query, err, rec.Body.String())
		}
		ids := make([]string, 0, len(rows))
		for _, r := range rows {
			ids = append(ids, r.ID)
		}
		return ids
	}

	if ids := get("checked=1&pool=random&limit=2"); len(ids) != 2 || ids[0] != "300" || ids[1] != "200" {
		t.Fatalf("第一页 = %v, want [300 200]", ids)
	}
	if ids := get("checked=1&pool=random&before=200&limit=2"); len(ids) != 0 {
		t.Fatalf("第二页应为空（100 已摘出），got %v", ids)
	}
	// 不传 limit：保持旧行为（取全部），Feed 顶部池区靠它一次展示全量
	if ids := get("checked=1&pool=random"); len(ids) != 2 {
		t.Fatalf("不传 limit 应取全部，got %v", ids)
	}

	// count=1：只回总数，不回行数据
	total := func(query string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		req := withPathValues(httptest.NewRequest(http.MethodGet, "/api/roles/musk/tweets?"+query, nil), "id", roleID)
		s.roleTweets(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d", query, rec.Code)
		}
		var body struct {
			Total int `json:"total"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %s 非 JSON: %v (%s)", query, err, rec.Body.String())
		}
		return body.Total
	}
	if n := total("checked=1&pool=random&count=1"); n != 2 {
		t.Fatalf("random 池总数 = %d, want 2", n)
	}
	if n := total("count=1"); n != 3 {
		t.Fatalf("缓存推文总数 = %d, want 3", n)
	}
	if n := total("count=1&kw=anana"); n != 1 {
		t.Fatalf("kw=anana 命中数 = %d, want 1", n)
	}
}
