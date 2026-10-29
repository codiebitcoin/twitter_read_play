package twitter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// UserProfile 渠道读取到的账号公开资料（与 source.AccountProfile 解耦，
// 避免 twitter 包反向依赖 source）。
type UserProfile struct {
	ID           string
	Handle       string
	DisplayName  string
	AvatarURL    string
	Bio          string
	Followers    int
	Following    int
	Posts        int
	Verified     bool
	VerifiedNote string
	CanonicalURL string
	Partial      bool
}

// ProfileProvider 可选能力：读取账号资料。
// 由 upstream 适配器在实现 source.AccountInspector 时做类型断言。
type ProfileProvider interface {
	Profile(ctx context.Context, handle string) (UserProfile, error)
}

// anyInt 兼容 JSON 数字、数字字符串、以及微博/小红书常见的 "1.2万" / "3.4亿" / "5k"。
func anyInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case string:
		s := strings.TrimSpace(strings.ReplaceAll(n, ",", ""))
		if s == "" {
			return 0
		}
		mult := 1.0
		switch {
		case strings.HasSuffix(s, "万"):
			mult, s = 10000, strings.TrimSuffix(s, "万")
		case strings.HasSuffix(s, "亿"):
			mult, s = 1e8, strings.TrimSuffix(s, "亿")
		case strings.HasSuffix(s, "w"), strings.HasSuffix(s, "W"):
			mult, s = 10000, s[:len(s)-1]
		case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "K"):
			mult, s = 1000, s[:len(s)-1]
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return 0
		}
		return int(f * mult)
	}
	return 0
}

// anyString 兼容 JSON 字符串/数字（微博 userInfo.id 常见为数字）。
func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

func boolValue(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(b, "true") || b == "1"
	case float64:
		return b != 0
	}
	return false
}

func firstNonEmptyValue(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
