package watch

import "twitter_read_play/internal/model"

// newRoleStub 造一个只带平台/用户名的角色（这些纯函数测试不需要 store）。
func newRoleStub(platform, username string) *model.Role {
	return &model.Role{ID: "r-test", Name: "test", Platform: platform, Username: username, Enabled: true}
}

type errString string

func (e errString) Error() string { return string(e) }
