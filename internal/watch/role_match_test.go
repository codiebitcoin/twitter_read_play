package watch

import "testing"

// webhook 分发必须「平台 + 用户名」双匹配：微博与推特同名账号不能互相触发。
func TestRoleMatchesEventPlatformIsolation(t *testing.T) {
	cases := []struct {
		name             string
		rolePlat, roleU  string
		evtPlat, evtUser string
		want             bool
	}{
		{"同平台同名", "weibo", "李建秋的世界", "weibo", "李建秋的世界", true},
		{"平台不同（同名）", "twitter", "elonmusk", "weibo", "elonmusk", false},
		{"小红书不被推特事件触发", "xiaohongshu", "butterzhu", "twitter", "butterzhu", false},
		{"大小写与 @ 前缀", "weibo", "PeopleDaily", "weibo", "@peopledaily", true},
		{"空平台按 twitter", "twitter", "elonmusk", "", "elonmusk", true},
		{"空平台不匹配微博角色", "weibo", "elonmusk", "", "elonmusk", false},
		{"用户名不同", "weibo", "a", "weibo", "b", false},
		{"事件无用户名", "weibo", "a", "weibo", "", false},
		{"历史值 x 等价 twitter", "x", "elonmusk", "twitter", "elonmusk", true},
	}
	for _, c := range cases {
		r := newRoleStub(c.rolePlat, c.roleU)
		if got := roleMatchesEvent(r, c.evtPlat, c.evtUser); got != c.want {
			t.Errorf("%s: roleMatchesEvent(%q,%q | %q,%q) = %v, want %v",
				c.name, c.rolePlat, c.roleU, c.evtPlat, c.evtUser, got, c.want)
		}
	}
}

// 需要登录的失败要识别出来（用于退避，避免每 30s 起一次浏览器）。
func TestIsNeedLoginError(t *testing.T) {
	yes := []string{
		`social_read: HTTP 503 {"error":"xiaohongshu 无登录态，无法按昵称解析账号","need_login":true}`,
		"weibo 深翻页需要登录态（游客只能看首页约 10-20 条），请先在操作页登录",
	}
	for _, m := range yes {
		if !isNeedLoginError(errString(m)) {
			t.Errorf("应识别为需登录: %s", m)
		}
	}
	no := []string{"context deadline exceeded", "socialdata.tools: HTTP 502", "未截获帖子响应"}
	for _, m := range no {
		if isNeedLoginError(errString(m)) {
			t.Errorf("不该识别为需登录: %s", m)
		}
	}
	if isNeedLoginError(nil) {
		t.Error("nil 不该算需登录")
	}
}
