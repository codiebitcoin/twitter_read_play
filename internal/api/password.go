// Package api 登录口令：bcrypt 哈希存储 + 存量明文兼容升级（M3）。
// 历史上默认密码 admin/11111 内置、密码明文存 SQLite；现在：无默认密码
// （未配置时必须先走首次设置）、DB 里的口令一律 bcrypt 哈希（bcrypt: 前缀），
// 旧明文在登录成功时自动升级为哈希。
package api

import (
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptPrefix DB 里 auth_pass 的哈希标记。无前缀 = 存量明文（登录成功即升级）。
const bcryptPrefix = "bcrypt:"

// hashPassword 明文 → bcrypt 哈希存储值（带前缀）。bcrypt 只取前 72 字节，
// 超长输入在入口处拒绝（setupAuth / putSettings）。
func hashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return bcryptPrefix + string(h), nil
}

// verifyStoredPass 校验输入是否匹配 DB 存储值。返回 (是否匹配, 是否需要升级哈希)。
// storedInDB=false 时用 env AUTH_PASS（明文）比较——env 口令不落库、不升级，
// 否则后续改 env 会因 DB 优先级更高而失效。
func verifyStoredPass(stored string, storedInDB bool, input string) (ok bool, needsUpgrade bool) {
	if storedInDB && strings.HasPrefix(stored, bcryptPrefix) {
		return bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(stored, bcryptPrefix)), []byte(input)) == nil, false
	}
	// 存量明文（DB 或 env）：常数时间比较；DB 明文匹配后升级为哈希
	if credEq(input, stored) {
		return true, storedInDB
	}
	return false, false
}
