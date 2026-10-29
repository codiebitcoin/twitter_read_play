package twitter

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/xapi"
)

// XToken 用 x.com 登录 token（auth_token + ct0）直接调网页 GraphQL，不经过浏览器。
// AuthToken 可以是裸 token，也可以是整段 Cookie（含 auth_token= / ct0=）。
type XToken struct {
	AuthToken string
	CT0       string
	Proxy     string
}

func (x *XToken) Name() string { return "xtoken" }

// ParseXCreds 从 key / cookie 两个字段抽出 auth_token 与 ct0。
func ParseXCreds(key, cookie string) (token, ct0 string) {
	token, ct0 = pick(key)
	if ctok, cct := pick(cookie); ctok != "" || cct != "" {
		if token == "" {
			token = ctok
		}
		if ct0 == "" {
			ct0 = cct
		}
	}
	if token == "" {
		token = strings.TrimSpace(key)
	}
	if ct0 == "" && !strings.Contains(cookie, "auth_token=") {
		ct0 = strings.TrimSpace(cookie)
	}
	return token, ct0
}

func pick(s string) (token, ct0 string) {
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "auth_token":
			token = strings.TrimSpace(v)
		case "ct0":
			ct0 = strings.TrimSpace(v)
		}
	}
	return token, ct0
}

func (x *XToken) FetchLatest(ctx context.Context, screenName string, count int) ([]model.Tweet, error) {
	token, ct0 := ParseXCreds(x.AuthToken, x.CT0)
	if token == "" {
		return nil, fmt.Errorf("xtoken: 未配置 auth_token")
	}
	if count <= 0 || count > 100 {
		count = 20
	}
	c := &xapi.Client{AuthToken: token, CT0: ct0, Proxy: x.Proxy}
	res, err := c.Exec(ctx, "tweets", "--screen", screenName, "--count", strconv.Itoa(count))
	if err != nil {
		return nil, err
	}
	if res["need_login"] == true {
		return nil, fmt.Errorf("xtoken: %s", strOf(res["error"]))
	}
	arr, _ := res["tweets"].([]any)
	out := make([]model.Tweet, 0, len(arr))
	for _, it := range arr {
		m, _ := it.(map[string]any)
		if m == nil {
			continue
		}
		user, _ := m["user"].(map[string]any)
		out = append(out, model.Tweet{
			ID:        strOf(m["id_str"]),
			User:      strings.ToLower(strOf(user["screen_name"])),
			Text:      model.CleanText(strOf(m["full_text"]), 300),
			CreatedAt: strOf(m["created_at"]),
			Likes:     asInt(m["favorite_count"]),
			Source:    "real",
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("xtoken: %s 没有拉到推文", screenName)
	}
	return out, nil
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

func asInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}
