package watch

import (
	"testing"
	"time"

	"twitter_read_play/internal/model"
)

// 平台口径：空值与历史值 "x" 都算 twitter。
func TestNormalizePlatform(t *testing.T) {
	cases := map[string]string{
		"":             "twitter",
		"  ":           "twitter",
		"x":            "twitter",
		"X":            "twitter",
		"Twitter":      "twitter",
		"weibo":        "weibo",
		" Xiaohongshu": "xiaohongshu",
		"rss":          "rss",
	}
	for in, want := range cases {
		if got := normalizePlatform(in); got != want {
			t.Errorf("normalizePlatform(%q) = %q, want %q", in, got, want)
		}
	}
}

// webhook 路由必须同时比 platform 与 username：微博/推特同名账号不能互相触发。
func TestRoleMatchesEvent(t *testing.T) {
	weibo := &model.Role{Platform: "weibo", Username: "elonmusk"}
	twitter := &model.Role{Platform: "twitter", Username: "elonmusk"}
	legacy := &model.Role{Username: "elonmusk"} // 老角色没有 platform = twitter

	cases := []struct {
		name     string
		role     *model.Role
		platform string
		username string
		want     bool
	}{
		{"微博事件命中微博角色", weibo, "weibo", "elonmusk", true},
		{"推特事件不命中微博角色", weibo, "twitter", "elonmusk", false},
		{"空平台事件不命中微博角色", weibo, "", "elonmusk", false},
		{"推特事件命中推特角色", twitter, "twitter", "elonmusk", true},
		{"微博事件不命中推特角色", twitter, "weibo", "elonmusk", false},
		{"空平台事件按推特命中老角色", legacy, "", "elonmusk", true},
		{"用户名大小写与 @ 归一", twitter, "twitter", "@ElonMusk", true},
		{"用户名不同不命中", twitter, "twitter", "musk", false},
	}
	for _, c := range cases {
		if got := roleMatchesEvent(c.role, c.platform, c.username); got != c.want {
			t.Errorf("%s: roleMatchesEvent = %v, want %v", c.name, got, c.want)
		}
	}
}

// parseTweetTime 宽口径：ISO / RFC1123 / Twitter 经典 / unix 秒毫秒 / 微博中文；解析不出来返回零值。
func TestParseTweetTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		in      string
		wantOK  bool
		wantY   int
		wantMon time.Month
		wantD   int
		wantH   int
		wantMin int
	}{
		{"2026-10-14T20:00:00+08:00", true, 2026, 10, 14, 20, 0},
		{"2026-10-14T20:00:00Z", true, 2026, 10, 14, 20, 0},
		{"2026-10-14 20:00:00", true, 2026, 10, 14, 20, 0},
		{"2026-10-14", true, 2026, 10, 14, 0, 0},
		{"Wed, 14 Oct 2026 20:00:00 +0800", true, 2026, 10, 14, 20, 0},
		{"Tue Oct 14 20:00:00 +0800 2026", true, 2026, 10, 14, 20, 0},
		{"1750000000", true, 2025, 6, 15, 15, 6}, // 10 位秒（只校验 unix 值，见下）
		{"今天 08:00", true, now.Year(), now.Month(), now.Day(), 8, 0},
		{"昨天 22:15", true, now.AddDate(0, 0, -1).Year(), now.AddDate(0, 0, -1).Month(), now.AddDate(0, 0, -1).Day(), 22, 15},
	}
	for _, c := range cases {
		got, ok := parseTweetTime(c.in)
		if ok != c.wantOK {
			t.Errorf("parseTweetTime(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		// 时间戳那条依赖本地时区，只校验能解析出来
		if c.in == "1750000000" {
			if got.Unix() != 1750000000 {
				t.Errorf("parseTweetTime(%q).Unix() = %d, want 1750000000", c.in, got.Unix())
			}
			continue
		}
		if got.Year() != c.wantY || got.Month() != c.wantMon || got.Day() != c.wantD || got.Hour() != c.wantH || got.Minute() != c.wantMin {
			t.Errorf("parseTweetTime(%q) = %v, want %d-%d-%d %02d:%02d", c.in, got, c.wantY, c.wantMon, c.wantD, c.wantH, c.wantMin)
		}
	}
}

// 微博中文日期（只有月日 → 当年；算出来比现在晚一天以上按去年）与 13 位毫秒；
// 解析不出来必须返回零值而不是 panic。
func TestParseTweetTimeChineseDateAndGarbage(t *testing.T) {
	got, ok := parseTweetTime("10月15日 09:54")
	if !ok {
		t.Fatal("10月15日 09:54 解析失败")
	}
	if got.Month() != 10 || got.Day() != 15 || got.Hour() != 9 || got.Minute() != 54 {
		t.Fatalf("10月15日 09:54 → %v", got)
	}
	now := time.Now()
	if y := got.Year(); y != now.Year() && y != now.Year()-1 {
		t.Fatalf("微博月日应落在今年或去年, got %d", y)
	}
	if y, ok := parseTweetTime("2026年10月15日 09:54:30"); !ok || y.Year() != 2026 || y.Second() != 30 {
		t.Fatalf("带年份的中文日期解析失败: %v %v", y, ok)
	}
	if m, ok := parseTweetTime("1750000000000"); !ok || m.UnixMilli() != 1750000000000 {
		t.Fatalf("13 位毫秒解析失败: %v %v", m, ok)
	}
	justNow, ok := parseTweetTime("刚刚")
	if !ok || time.Since(justNow) > time.Minute {
		t.Fatalf("刚刚 解析失败: %v %v", justNow, ok)
	}
	ago, ok := parseTweetTime("3分钟前")
	if !ok || time.Since(ago) < 2*time.Minute || time.Since(ago) > 4*time.Minute {
		t.Fatalf("3分钟前 解析失败: %v %v", ago, ok)
	}
	for _, bad := range []string{"", "   ", "昨天下午", "not a date", "13月45日"} {
		if got, ok := parseTweetTime(bad); ok {
			t.Errorf("parseTweetTime(%q) = %v, want 零值 + false", bad, got)
		}
	}
}
