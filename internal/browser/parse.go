package browser

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/source"
)

// ---- 通用 map 工具 ----

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func field(m map[string]any, k string) any {
	if m == nil {
		return nil
	}
	return m[k]
}

func getMap(m map[string]any, k string) map[string]any { return asMap(field(m, k)) }

func getSlice(m map[string]any, k string) []any { return asSlice(field(m, k)) }

func getStr(m map[string]any, k string) string { return stringValue(field(m, k)) }

func getPath(root any, keys ...string) any {
	cur := root
	for _, k := range keys {
		m := asMap(cur)
		if m == nil {
			return nil
		}
		cur = m[k]
	}
	return cur
}

func getPathMap(root any, keys ...string) map[string]any { return asMap(getPath(root, keys...)) }

func idString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case json.Number:
		return x.String()
	}
	return stringValue(v)
}

// ---- X / Twitter ----

// ParseX 从拦截到的 GraphQL 响应里抽出推文（兼容新旧 user/timeline 结构）。
func ParseX(caps []Captured) []model.Tweet {
	out := make([]model.Tweet, 0)
	seen := map[string]bool{}
	for _, c := range caps {
		if len(c.JSON) == 0 {
			continue
		}
		var root any
		if err := json.Unmarshal(c.JSON, &root); err != nil {
			continue
		}
		parseXTimeline(root, &out, seen)
	}
	return out
}

func parseXTimeline(root any, out *[]model.Tweet, seen map[string]bool) {
	instructions := getSlice(getPathMap(root, "data", "user", "result", "timeline", "timeline"), "instructions")
	for _, raw := range instructions {
		ins := asMap(raw)
		entries := getSlice(ins, "entries")
		if e := getMap(ins, "entry"); e != nil {
			entries = []any{e}
		}
		for _, rawEntry := range entries {
			content := getMap(asMap(rawEntry), "content")
			switch getStr(content, "entryType") {
			case "TimelineTimelineItem":
				parseXItem(getMap(content, "itemContent"), out, seen)
			case "TimelineTimelineModule":
				for _, rawItem := range getSlice(content, "items") {
					parseXItem(getMap(getMap(asMap(rawItem), "item"), "itemContent"), out, seen)
				}
			}
			if ic := getMap(content, "itemContent"); ic != nil {
				parseXItem(ic, out, seen)
			}
		}
	}
}

func parseXItem(item map[string]any, out *[]model.Tweet, seen map[string]bool) {
	if item == nil || getStr(item, "itemType") != "TimelineTweet" {
		return
	}
	res := getPathMap(item, "tweet_results", "result")
	legacy := getMap(res, "legacy")
	if legacy == nil {
		legacy = getPathMap(res, "tweet", "legacy")
	}
	if legacy == nil {
		return
	}
	id := getStr(legacy, "id_str")
	if id == "" {
		id = getStr(legacy, "id")
	}
	if id == "" || seen[id] {
		return
	}
	seen[id] = true

	user := getPathMap(res, "core", "user_results", "result")
	screen := getStr(getMap(user, "legacy"), "screen_name")
	if screen == "" {
		screen = getStr(getMap(user, "core"), "screen_name")
	}
	if screen == "" {
		screen = getStr(user, "screen_name")
	}
	if screen == "" {
		screen = "X"
	}

	tw := model.Tweet{
		ID:        id,
		User:      screen,
		Text:      firstNonEmpty(getStr(legacy, "full_text"), getStr(legacy, "text")),
		CreatedAt: getStr(legacy, "created_at"),
		Likes:     int(floatValue(field(legacy, "favorite_count"))),
		Link:      "https://x.com/" + screen + "/status/" + id,
	}
	if entities := getMap(legacy, "entities"); entities != nil {
		for _, rawMedia := range getSlice(entities, "media") {
			if u := getStr(asMap(rawMedia), "media_url_https"); u != "" {
				tw.Images = append(tw.Images, u)
			}
		}
	}
	if q := getPathMap(res, "quoted_status_result", "result", "legacy"); q != nil {
		tw.QuotedID = getStr(q, "id_str")
		tw.QuotedText = firstNonEmpty(getStr(q, "full_text"), getStr(q, "text"))
	}
	if rt := getPathMap(res, "retweeted_status_result", "result", "legacy"); rt != nil {
		tw.RetweetID = getStr(rt, "id_str")
		tw.RetweetText = firstNonEmpty(getStr(rt, "full_text"), getStr(rt, "text"))
	}
	*out = append(*out, tw)
}

// ---- 微博 ----

var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// ParseWeibo 解析 container/getIndex 响应（也接受整页 JSON）。
func ParseWeibo(raw []byte) []model.Tweet {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	data := getMap(root, "data")
	out := make([]model.Tweet, 0)
	walkWeiboCards(getSlice(data, "cards"), &out)
	return out
}

func walkWeiboCards(cards []any, out *[]model.Tweet) {
	for _, raw := range cards {
		card := asMap(raw)
		if card == nil {
			continue
		}
		if mblog := getMap(card, "mblog"); mblog != nil {
			if tw, ok := weiboMblog(mblog); ok {
				*out = append(*out, tw)
			}
		}
		if group := getSlice(card, "card_group"); len(group) > 0 {
			walkWeiboCards(group, out)
		}
	}
}

func weiboMblog(mblog map[string]any) (model.Tweet, bool) {
	id := idString(field(mblog, "id"))
	text := stripHTML(getStr(mblog, "text"))
	if id == "" || text == "" {
		return model.Tweet{}, false
	}
	user := getMap(mblog, "user")
	tw := model.Tweet{
		ID:           id,
		User:         getStr(user, "screen_name"),
		Text:         text,
		CreatedAt:    getStr(mblog, "created_at"),
		Likes:        int(floatValue(field(mblog, "attitudes_count"))),
		CommentCount: int(floatValue(field(mblog, "comments_count"))),
		ShareCount:   int(floatValue(field(mblog, "reposts_count"))),
		Link:         "https://m.weibo.cn/detail/" + id,
	}
	if tw.User == "" {
		tw.User = "weibo"
	}
	if infos := getMap(mblog, "pic_infos"); infos != nil {
		for _, v := range infos {
			if u := getStr(getMap(asMap(v), "large"), "url"); u != "" {
				tw.Images = append(tw.Images, u)
			}
		}
	}
	for _, rawPic := range getSlice(mblog, "pics") {
		if u := getStr(getMap(asMap(rawPic), "large"), "url"); u != "" {
			tw.Images = append(tw.Images, u)
		}
	}
	return tw, true
}

// ---- 小红书 ----

// ParseXHS 解析 user_posted / otherinfo 响应。
func ParseXHS(raw []byte) []model.Tweet {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	notes := getSlice(getMap(root, "data"), "notes")
	out := make([]model.Tweet, 0, len(notes))
	for _, rawNote := range notes {
		note := asMap(rawNote)
		if note == nil {
			continue
		}
		id := firstNonEmpty(getStr(note, "note_id"), getStr(note, "id"))
		if id == "" {
			continue
		}
		title := getStr(note, "display_title")
		desc := getStr(note, "desc")
		text := strings.TrimSpace(strings.TrimSpace(title) + "\n" + strings.TrimSpace(desc))
		if text == "" {
			continue
		}
		user := getMap(note, "user")
		name := firstNonEmpty(getStr(user, "nickname"), getStr(user, "nick_name"))
		if name == "" {
			name = "xiaohongshu"
		}
		interact := getMap(note, "interact_info")
		tw := model.Tweet{
			ID:           id,
			User:         name,
			Text:         text,
			Likes:        parseCount(getStr(interact, "liked_count")),
			CommentCount: parseCount(getStr(interact, "comment_count")),
			CollectCount: parseCount(getStr(interact, "collected_count")),
			ShareCount:   parseCount(getStr(interact, "share_count")),
			Link:         "https://www.xiaohongshu.com/explore/" + id,
		}
		if ms := int64(floatValue(field(note, "time"))); ms > 0 {
			tw.CreatedAt = time.UnixMilli(ms).Format(time.RFC3339)
		}
		if cover := getMap(note, "cover"); cover != nil {
			if u := firstNonEmpty(getStr(cover, "url_default"), getStr(cover, "url")); u != "" {
				tw.Images = append(tw.Images, u)
			}
		}
		out = append(out, tw)
	}
	return out
}

// ParseXHSStateNotes 解析页面 SSR 状态里的笔记列表（__INITIAL_STATE__.user.notes）。
//
// 小红书主页在远程浏览器里常常整页由 SSR 渲染，页面不再发 user_posted 请求，
// 网络捕获拿不到任何东西；纯 user/profile/<id> 链接还会被要求带 xsec_token。
// SSR 里的 noteId 在新版页面里常为空，此时用笔记时间戳补一个定宽 ID：
// gtID 先比长度再比字典序，13 位毫秒时间戳的长度与字典序都等于时间顺序。
func ParseXHSStateNotes(raw any) []model.Tweet {
	var rows []struct {
		ID    string  `json:"id"`
		Time  float64 `json:"time"`
		Title string  `json:"title"`
		Desc  string  `json:"desc"`
		Liked string  `json:"liked"`
		Type  string  `json:"type"`
		User  string  `json:"user"`
	}
	var data []byte
	switch v := raw.(type) {
	case string:
		data = []byte(v)
	default:
		data, _ = json.Marshal(v)
	}
	if len(data) == 0 || json.Unmarshal(data, &rows) != nil {
		return nil
	}
	out := make([]model.Tweet, 0, len(rows))
	for _, r := range rows {
		text := strings.TrimSpace(strings.TrimSpace(r.Title) + "\n" + strings.TrimSpace(r.Desc))
		if text == "" {
			continue
		}
		id := strings.TrimSpace(r.ID)
		if id == "" && r.Time > 0 {
			id = fmt.Sprintf("%013d", int64(r.Time))
		}
		if id == "" {
			continue
		}
		tw := model.Tweet{
			ID:    id,
			User:  firstNonEmpty(r.User, "xiaohongshu"),
			Text:  text,
			Likes: parseCount(r.Liked),
		}
		if r.Time > 0 {
			tw.CreatedAt = time.UnixMilli(int64(r.Time)).Format(time.RFC3339)
		}
		if strings.TrimSpace(r.ID) != "" {
			tw.Link = "https://www.xiaohongshu.com/explore/" + r.ID
		}
		out = append(out, tw)
	}
	return out
}

// ---- Instagram ----

// ParseInstagram 解析 Instagram 的三种时间线响应：
//   - web_profile_info / 旧版 profile GraphQL：data.user.edge_owner_to_timeline_media.edges
//   - 新版 GraphQL：data.xdt_api__v1__feed__user_timeline_graphql_connection.edges
//   - REST：items[]
func ParseInstagram(raw []byte) []model.Tweet {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	data := getMap(root, "data")
	out := make([]model.Tweet, 0)
	seen := map[string]bool{}

	if user := getMap(data, "user"); user != nil {
		out = appendInstagramTimeline(out, getMap(user, "edge_owner_to_timeline_media"), seen)
	}
	if conn := getMap(data, "xdt_api__v1__feed__user_timeline_graphql_connection"); conn != nil {
		out = appendInstagramEdges(out, getSlice(conn, "edges"), seen)
	}
	for _, rawItem := range getSlice(root, "items") {
		if tw, ok := instagramNode(asMap(rawItem)); ok && !seen[tw.ID] {
			seen[tw.ID] = true
			out = append(out, tw)
		}
	}
	return out
}

func appendInstagramTimeline(out []model.Tweet, media map[string]any, seen map[string]bool) []model.Tweet {
	return appendInstagramEdges(out, getSlice(media, "edges"), seen)
}

func appendInstagramEdges(out []model.Tweet, edges []any, seen map[string]bool) []model.Tweet {
	for _, rawEdge := range edges {
		node := getMap(asMap(rawEdge), "node")
		tw, ok := instagramNode(node)
		if !ok || seen[tw.ID] {
			continue
		}
		seen[tw.ID] = true
		out = append(out, tw)
	}
	return out
}

// instagramNode 兼容 web_profile_info 与 GraphQL/REST 的字段命名差异。
func instagramNode(node map[string]any) (model.Tweet, bool) {
	if node == nil {
		return model.Tweet{}, false
	}
	code := firstNonEmpty(getStr(node, "shortcode"), getStr(node, "code"))
	text := instagramCaption(node)
	if code == "" && text == "" {
		return model.Tweet{}, false
	}
	id := firstNonEmpty(getStr(node, "id"), getStr(node, "pk"), code)
	user := getMap(node, "user")
	if user == nil {
		user = getMap(node, "owner")
	}
	name := firstNonEmpty(getStr(user, "username"), getStr(user, "full_name"))
	if name == "" {
		name = "instagram"
	}
	tw := model.Tweet{
		ID:           id,
		User:         name,
		Text:         text,
		Likes:        int(floatValue(field(node, "like_count"))),
		CommentCount: int(floatValue(field(node, "comment_count"))),
		Link:         "https://www.instagram.com/p/" + code + "/",
	}
	if tw.Likes == 0 {
		tw.Likes = int(floatValue(field(getMap(node, "edge_liked_by"), "count")))
	}
	if tw.CommentCount == 0 {
		tw.CommentCount = int(floatValue(field(getMap(node, "edge_media_to_comment"), "count")))
	}
	// 时间戳：新接口是 unix 秒 taken_at，旧接口是 taken_at_timestamp
	if ts := int64(floatValue(field(node, "taken_at"))); ts > 0 {
		tw.CreatedAt = time.Unix(ts, 0).Format(time.RFC3339)
	} else if ts := int64(floatValue(field(node, "taken_at_timestamp"))); ts > 0 {
		tw.CreatedAt = time.Unix(ts, 0).Format(time.RFC3339)
	}
	if u := firstNonEmpty(getStr(node, "display_url"), getStr(node, "thumbnail_src")); u != "" {
		tw.Images = append(tw.Images, u)
	} else if versions := getMap(node, "image_versions2"); versions != nil {
		for _, rawCand := range getSlice(versions, "candidates") {
			if u := getStr(asMap(rawCand), "url"); u != "" {
				tw.Images = append(tw.Images, u)
				break
			}
		}
	}
	if code == "" {
		tw.Link = ""
	}
	return tw, true
}

func instagramCaption(node map[string]any) string {
	if c := getMap(node, "caption"); c != nil {
		if text := getStr(c, "text"); text != "" {
			return text
		}
	}
	if caps := getMap(node, "edge_media_to_caption"); caps != nil {
		for _, rawEdge := range getSlice(caps, "edges") {
			if text := getStr(getMap(asMap(rawEdge), "node"), "text"); text != "" {
				return text
			}
		}
	}
	return ""
}

// ---- Truth Social（Mastodon /api/v1）----

// ParseTruth 解析 /api/v1/accounts/<id>/statuses 返回的状态数组。
func ParseTruth(raw []byte) []model.Tweet {
	var arr []map[string]any
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]model.Tweet, 0, len(arr))
	for _, st := range arr {
		// 转推：内容在 reblog 里，账号取原作者的。
		target := st
		if rb := getMap(st, "reblog"); rb != nil {
			target = rb
		}
		text := stripHTML(getStr(target, "content"))
		if text == "" && len(getSlice(target, "media_attachments")) == 0 {
			continue
		}
		id := firstNonEmpty(getStr(target, "id"), getStr(st, "id"))
		if id == "" {
			continue
		}
		account := getMap(target, "account")
		user := firstNonEmpty(getStr(account, "username"), getStr(account, "acct"))
		if user == "" {
			user = "truth"
		}
		tw := model.Tweet{
			ID:           id,
			User:         user,
			Text:         model.CleanText(text, 500),
			CreatedAt:    getStr(target, "created_at"),
			Likes:        int(floatValue(field(target, "favourites_count"))),
			CommentCount: int(floatValue(field(target, "replies_count"))),
			ShareCount:   int(floatValue(field(target, "reblogs_count"))),
		}
		if u := getStr(st, "url"); u != "" {
			tw.Link = u
		} else if user != "" {
			tw.Link = "https://truthsocial.com/@" + user
		}
		for _, rawMedia := range getSlice(target, "media_attachments") {
			m := asMap(rawMedia)
			if u := firstNonEmpty(getStr(m, "url"), getStr(m, "preview_url")); u != "" {
				tw.Images = append(tw.Images, u)
			}
		}
		out = append(out, tw)
	}
	return out
}

// ParseTruthUserJSON 解析 /api/v1/accounts/<id> 或 lookup 的账号资料。
func ParseTruthUserJSON(raw []byte) (source.AccountProfile, bool) {
	var u map[string]any
	if err := json.Unmarshal(raw, &u); err != nil {
		return source.AccountProfile{}, false
	}
	username := getStr(u, "username")
	if username == "" {
		return source.AccountProfile{}, false
	}
	return source.AccountProfile{
		Platform:     source.PlatformTruth,
		Kind:         source.KindProfile,
		ID:           firstNonEmpty(getStr(u, "id"), username),
		Handle:       "@" + username,
		DisplayName:  firstNonEmpty(getStr(u, "display_name"), username),
		AvatarURL:    firstNonEmpty(getStr(u, "avatar"), getStr(u, "avatar_static")),
		Bio:          stripHTML(getStr(u, "note")),
		Followers:    int64(floatValue(field(u, "followers_count"))),
		Following:    int64(floatValue(field(u, "following_count"))),
		Posts:        int64(floatValue(field(u, "statuses_count"))),
		Verified:     truthy(field(u, "verified")) || truthy(field(u, "is_verified")),
		VerifiedNote: getStr(u, "verified_reason"),
		CanonicalURL: "https://truthsocial.com/@" + username,
		FetchedAt:    time.Now(),
	}, true
}

func stripHTML(s string) string {
	s = htmlTagRe.ReplaceAllString(s, "")
	replacer := strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'",
	)
	return strings.TrimSpace(replacer.Replace(s))
}

func parseCount(s string) int {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "万"), strings.HasSuffix(s, "萬"):
		mult = 10000
		s = s[:len(s)-len("万")]
	case strings.HasSuffix(s, "w"), strings.HasSuffix(s, "W"):
		mult = 10000
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "亿"), strings.HasSuffix(s, "億"):
		mult = 1e8
		s = s[:len(s)-len("亿")]
	case strings.HasSuffix(s, "千"):
		mult = 1000
		s = strings.TrimSuffix(s, "千")
	case strings.HasSuffix(s, "k"), strings.HasSuffix(s, "K"):
		mult = 1000
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "m"), strings.HasSuffix(s, "M"):
		mult = 1e6
		s = s[:len(s)-1]
	case strings.HasSuffix(s, "b"), strings.HasSuffix(s, "B"):
		mult = 1e9
		s = s[:len(s)-1]
	}
	f, _ := strconv.ParseFloat(s, 64)
	return int(f * mult)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
