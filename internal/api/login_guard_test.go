package api

import "testing"

func TestCredEq(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"admin", "admin", true},
		{"admin", "Admin", false},
		{"admin", "admin ", false},
		{"", "", true}, // 登录入口另行拒绝空配置，这里只管比较语义
		{"p@ss", "", false},
	}
	for _, c := range cases {
		if got := credEq(c.a, c.b); got != c.want {
			t.Errorf("credEq(%q,%q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestLoginGuardLimit(t *testing.T) {
	g := newLoginGuard()
	const ip = "1.2.3.4"
	for i := 0; i < loginMaxFails; i++ {
		if g.limited(ip) {
			t.Fatalf("第 %d 次失败就进入冷却，过早", i+1)
		}
		g.fail(ip)
	}
	if !g.limited(ip) {
		t.Fatal("达到阈值后应进入冷却")
	}
	if g.limited("5.6.7.8") {
		t.Fatal("其它 IP 不应被连带限速")
	}
	g.clear(ip)
	if g.limited(ip) {
		t.Fatal("clear 后应解除限速")
	}
}
