package browser

import "testing"

// 小红书主页在远程浏览器里是纯 SSR，只能从 __INITIAL_STATE__ 取笔记。
func TestParseXHSStateNotes(t *testing.T) {
	raw := `[
	  {"id":"","time":1792829393000,"title":"《多人接力转场》","desc":"","liked":"867","user":"33珊."},
	  {"id":"","time":1792911913000,"title":"《琵琶曲》","desc":"教程","liked":"1.2K","user":""},
	  {"id":"6970abc","time":1791000000000,"title":"带 id 的笔记","desc":"","liked":"5","user":"x"},
	  {"id":"","time":0,"title":"没有时间也没有 id","desc":"","liked":"","user":""},
	  {"id":"","time":1792000000000,"title":"","desc":"","liked":"","user":""}
	]`
	rows := ParseXHSStateNotes(raw)
	if len(rows) != 3 {
		t.Fatalf("want 3 rows (empty/time-less dropped), got %d: %+v", len(rows), rows)
	}
	if rows[0].ID != "1792829393000" {
		t.Fatalf("synthetic id should be zero-padded ms, got %q", rows[0].ID)
	}
	if len(rows[0].ID) != 13 || len(rows[1].ID) != 13 {
		t.Fatalf("synthetic ids must be fixed width for gtID ordering: %q %q", rows[0].ID, rows[1].ID)
	}
	if rows[1].Likes != 1200 {
		t.Fatalf("1.2K should parse to 1200, got %d", rows[1].Likes)
	}
	if rows[2].ID != "6970abc" || rows[2].Link == "" {
		t.Fatalf("real note id should be kept and linked: %+v", rows[2])
	}
	if rows[0].Text == "" || rows[1].Text != "《琵琶曲》\n教程" {
		t.Fatalf("text should join title and desc: %+v", rows[1])
	}
}
