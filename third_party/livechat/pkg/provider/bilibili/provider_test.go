package bilibili

import "testing"

func TestParseRoomID(t *testing.T) {
	cases := map[string]int{
		"6":                                 6,
		"  12345  ":                         12345,
		"https://live.bilibili.com/6":       6,
		"https://live.bilibili.com/6?spm=1": 6,
		"https://www.bilibili.com/video/1":  0,
		"not-a-room":                        0,
		"":                                  0,
	}
	for in, want := range cases {
		if got := ParseRoomID(in); got != want {
			t.Errorf("ParseRoomID(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestCookieOK(t *testing.T) {
	if CookieOK("") == nil {
		t.Fatal("空 Cookie 应报错")
	}
	if CookieOK("SESSDATA=abc") == nil {
		t.Fatal("缺 bili_jct 应报错")
	}
	if CookieOK("bili_jct=xyz") == nil {
		t.Fatal("缺 SESSDATA 应报错")
	}
	if err := CookieOK("SESSDATA=abc; bili_jct=xyz"); err != nil {
		t.Fatalf("完整 Cookie 不应报错: %v", err)
	}
}

func TestStreamAllowsEmptyCookie(t *testing.T) {
	p := New(Options{})
	msgs, _, err := p.Stream(t.Context(), "6")
	if err != nil {
		t.Fatalf("无 Cookie 也应能开始拉弹幕: %v", err)
	}
	if msgs == nil {
		t.Fatal("应返回消息通道")
	}
}
