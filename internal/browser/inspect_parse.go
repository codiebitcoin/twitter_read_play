package browser

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"time"

	"twitter_read_play/internal/source"
)

// ParseXUser 从拦截到的 UserByScreenName 响应里解析账号资料。
func ParseXUser(caps []Captured) (source.AccountProfile, bool) {
	for _, c := range caps {
		if len(c.JSON) == 0 {
			continue
		}
		var root any
		if err := json.Unmarshal(c.JSON, &root); err != nil {
			continue
		}
		result := getPathMap(root, "data", "user", "result")
		if result == nil {
			continue
		}
		legacy := getMap(result, "legacy")
		if legacy == nil {
			legacy = getPathMap(result, "tweet", "legacy")
		}
		if legacy == nil {
			continue
		}
		handle := firstNonEmpty(
			getStr(legacy, "screen_name"),
			getStr(getMap(result, "core"), "screen_name"),
			getStr(result, "screen_name"),
		)
		if handle == "" {
			continue
		}
		avatar := firstNonEmpty(
			getStr(legacy, "profile_image_url_https"),
			getStr(getMap(result, "avatar"), "image_url"),
		)
		verified := truthy(field(legacy, "verified")) || truthy(getPath(result, "verification", "verified"))
		return source.AccountProfile{
			Platform:    source.PlatformX,
			Kind:        source.KindProfile,
			ID:          firstNonEmpty(getStr(result, "rest_id"), getStr(legacy, "id_str"), getStr(legacy, "id")),
			Handle:      "@" + strings.TrimPrefix(handle, "@"),
			DisplayName: firstNonEmpty(getStr(legacy, "name"), getStr(getMap(result, "core"), "name"), handle),
			AvatarURL:   avatar,
			Bio:         getStr(legacy, "description"),
			Followers:   int64(floatValue(field(legacy, "followers_count"))),
			Following:   int64(floatValue(field(legacy, "friends_count"))),
			Posts:       int64(floatValue(field(legacy, "statuses_count"))),
			Verified:    verified,
			FetchedAt:   time.Now(),
		}, true
	}
	return source.AccountProfile{}, false
}

// ParseWeiboUser 从 container/getIndex 响应里解析账号资料。
func ParseWeiboUser(caps []Captured) (source.AccountProfile, bool) {
	for _, c := range caps {
		if len(c.JSON) == 0 {
			continue
		}
		var root any
		if err := json.Unmarshal(c.JSON, &root); err != nil {
			continue
		}
		info := getPathMap(root, "data", "userInfo")
		if info == nil {
			continue
		}
		name := getStr(info, "screen_name")
		if name == "" {
			continue
		}
		return source.AccountProfile{
			Platform:     source.PlatformWeibo,
			Kind:         source.KindProfile,
			ID:           firstNonEmpty(idString(field(info, "id")), getStr(info, "id")),
			Handle:       name,
			DisplayName:  name,
			AvatarURL:    firstNonEmpty(getStr(info, "profile_image_url"), getStr(info, "avatar_large"), getStr(info, "avatar_hd")),
			Bio:          getStr(info, "description"),
			Followers:    int64(countValue(field(info, "followers_count"))),
			Following:    int64(countValue(field(info, "friends_count"))),
			Posts:        int64(countValue(field(info, "statuses_count"))),
			Verified:     truthy(field(info, "verified")),
			VerifiedNote: getStr(info, "verified_reason"),
			FetchedAt:    time.Now(),
		}, true
	}
	return source.AccountProfile{}, false
}

// ParseXHSUser 从 user/otherinfo 响应里解析账号资料。
func ParseXHSUser(caps []Captured) (source.AccountProfile, bool) {
	for _, c := range caps {
		if len(c.JSON) == 0 {
			continue
		}
		var root any
		if err := json.Unmarshal(c.JSON, &root); err != nil {
			continue
		}
		if info := getPathMap(root, "data", "basic_info"); info != nil {
			if p, ok := parseXHSBasicInfo(info); ok {
				return p, true
			}
		}
	}
	return source.AccountProfile{}, false
}

// ParseXHSPageState 从 __INITIAL_STATE__.user.userPageData 里解析账号资料。
func ParseXHSPageState(raw any) (source.AccountProfile, bool) {
	var page map[string]any
	switch v := raw.(type) {
	case string:
		if err := json.Unmarshal([]byte(v), &page); err != nil {
			return source.AccountProfile{}, false
		}
	case map[string]any:
		page = v
	default:
		return source.AccountProfile{}, false
	}
	if info := getMap(page, "basic_info"); info != nil {
		return parseXHSBasicInfo(info)
	}
	if info := getMap(page, "basicInfo"); info != nil {
		return parseXHSBasicInfo(info)
	}
	return source.AccountProfile{}, false
}

func parseXHSBasicInfo(info map[string]any) (source.AccountProfile, bool) {
	nickname := firstNonEmpty(getStr(info, "nickname"), getStr(info, "nick_name"))
	if nickname == "" {
		return source.AccountProfile{}, false
	}
	return source.AccountProfile{
		Platform:    source.PlatformXHS,
		Kind:        source.KindProfile,
		ID:          getStrAny(info, "user_id", "userId", "red_id", "redId", "id"),
		Handle:      nickname,
		DisplayName: nickname,
		AvatarURL:   firstNonEmpty(getStr(info, "images"), getStr(info, "image"), getStr(info, "avatar")),
		Bio:         getStr(info, "desc"),
		Followers:   int64(countValue(field(info, "fans"))),
		Following:   int64(countValue(field(info, "follows"))),
		Posts:       int64(countValue(firstValue(info, "note_num", "noteNum"))),
		Verified:    truthy(field(info, "verified")),
		FetchedAt:   time.Now(),
	}, true
}

// ParseInstagramUser 从拦截到的 web_profile_info / ProfilePage GraphQL 响应里解析账号资料。
func ParseInstagramUser(caps []Captured) (source.AccountProfile, bool) {
	for _, c := range caps {
		if len(c.JSON) == 0 {
			continue
		}
		if p, ok := ParseInstagramUserJSON(c.JSON); ok {
			return p, true
		}
	}
	return source.AccountProfile{}, false
}

// ParseInstagramUserJSON 支持 data.user（web_profile_info）与 ProfilePage 的嵌套结构。
func ParseInstagramUserJSON(raw []byte) (source.AccountProfile, bool) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return source.AccountProfile{}, false
	}
	user := getPathMap(root, "data", "user")
	if user == nil {
		user = getMap(root, "user")
	}
	if user == nil {
		return source.AccountProfile{}, false
	}
	username := getStr(user, "username")
	if username == "" {
		return source.AccountProfile{}, false
	}
	bio := getStr(user, "biography")
	if bio == "" {
		bio = getStr(getMap(user, "biography_with_entities"), "raw_text")
	}
	followers := int64(floatValue(field(getMap(user, "edge_followed_by"), "count")))
	if followers == 0 {
		followers = int64(floatValue(field(user, "follower_count")))
	}
	following := int64(floatValue(field(getMap(user, "edge_follow"), "count")))
	if following == 0 {
		following = int64(floatValue(field(user, "following_count")))
	}
	posts := int64(floatValue(field(getMap(user, "edge_owner_to_timeline_media"), "count")))
	if posts == 0 {
		posts = int64(floatValue(field(user, "media_count")))
	}
	return source.AccountProfile{
		Platform:    source.PlatformInstagram,
		Kind:        source.KindProfile,
		ID:          firstNonEmpty(getStr(user, "id"), getStr(user, "pk")),
		Handle:      "@" + username,
		DisplayName: firstNonEmpty(getStr(user, "full_name"), username),
		AvatarURL:   firstNonEmpty(getStr(user, "profile_pic_url_hd"), getStr(user, "profile_pic_url")),
		Bio:         bio,
		Followers:   followers,
		Following:   following,
		Posts:       posts,
		Verified:    truthy(field(user, "is_verified")) || truthy(field(user, "verified")),
		FetchedAt:   time.Now(),
	}, true
}

func getStrAny(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := getStr(m, k); v != "" {
			return v
		}
	}
	return ""
}

func firstValue(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v := field(m, k); v != nil {
			return v
		}
	}
	return nil
}

func countValue(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case string:
		return parseCount(x)
	}
	return 0
}

// xUserDOM X 主页 DOM 兜底读取的原始字段（见 inspectXDOMJS）。
type xUserDOM struct {
	Href      string `json:"href"`
	Handle    string `json:"handle"`
	Name      string `json:"name"`
	Bio       string `json:"bio"`
	Avatar    string `json:"avatar"`
	Verified  bool   `json:"verified"`
	Followers string `json:"followers"`
	Following string `json:"following"`
	Body      string `json:"body"`
}

// ParseXUserDOM 从渲染后的主页 DOM 解析账号资料。
// chrome-driverless 模式是「导航加载完才注入捕获钩子」，加载期的 UserByScreenName
// 抓不到，capture 为空时走这里兜底（名称/简介/头像/关注数，没有 rest_id）。
// want 非空时校验页面确实落在该账号主页上 —— 导航静默失败时读到的会是上一页。
func ParseXUserDOM(v any, want string) (source.AccountProfile, bool) {
	var raw string
	switch x := v.(type) {
	case string:
		raw = x
	case json.RawMessage:
		raw = string(x)
	default:
		return source.AccountProfile{}, false
	}
	var d xUserDOM
	if json.Unmarshal([]byte(raw), &d) != nil {
		return source.AccountProfile{}, false
	}
	handle := strings.TrimPrefix(strings.TrimSpace(d.Handle), "@")
	if handle == "" {
		return source.AccountProfile{}, false
	}
	if want = strings.TrimPrefix(strings.TrimSpace(want), "@"); want != "" {
		if !strings.EqualFold(handle, want) || !xProfileHrefOK(d.Href, want) {
			return source.AccountProfile{}, false
		}
	}
	return source.AccountProfile{
		Platform:    source.PlatformX,
		Kind:        source.KindProfile,
		Handle:      "@" + handle,
		DisplayName: firstNonEmpty(strings.TrimSpace(d.Name), handle),
		AvatarURL:   strings.TrimSpace(d.Avatar),
		Bio:         strings.TrimSpace(d.Bio),
		Followers:   labelCount(d.Followers),
		Following:   labelCount(d.Following),
		Posts:       xPostsCount(d.Body),
		Verified:    d.Verified,
		FetchedAt:   time.Now(),
		Partial:     true, // DOM 里没有 rest_id，计数取页面展示值（可能是万/亿缩写）
	}, true
}

// xProfileHrefOK 当前地址是否就是 x.com/<want> 主页（忽略尾斜杠与查询串）。
func xProfileHrefOK(href, want string) bool {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "x.com", "www.x.com", "twitter.com", "www.twitter.com":
	default:
		return false
	}
	return strings.EqualFold(strings.Trim(strings.TrimSpace(u.Path), "/"), want)
}

// xCountPrefix 取标签文本开头的计数部分（"563.8万 关注者" → "563.8万"）。
var xCountPrefix = regexp.MustCompile(`^[0-9][0-9.,\x{202f}\x{00a0}' ]*[kKmMbB万亿萬千]?`)

// labelCount 解析「计数 + 文字标签」里的计数，支持 1,234 / 1.2K / 563.8万。
func labelCount(s string) int64 {
	m := xCountPrefix.FindString(strings.TrimSpace(s))
	if m == "" {
		return 0
	}
	m = strings.NewReplacer(",", "", " ", "", "\u202f", "", "\u00a0", "", "'", "").Replace(m)
	return int64(parseCount(m))
}

// xPostsLabel 主页头部的帖子数（"7,636 帖子" / "7,636 Posts"）。
var xPostsLabel = regexp.MustCompile(`([0-9][0-9.,\x{202f}\x{00a0}' ]*[kKmMbB万亿萬千]?)\s*(?:帖子|推文|posts?|ポスト)`)

func xPostsCount(body string) int64 {
	m := xPostsLabel.FindStringSubmatch(body)
	if len(m) < 2 {
		return 0
	}
	return labelCount(m[1])
}
