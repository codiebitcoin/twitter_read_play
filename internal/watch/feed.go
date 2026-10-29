package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
	"twitter_read_play/internal/twitter"
)

// sharedFetch 带缓存的拉取：key = provider|upstream|platform|username，TTL 由调用方传入。
// 多角色监控同一用户时只产生一次上游调用。
//
// p 必须是**按角色解析出来的** provider（providerForRole）：早期版本这里直接用了
// Manager 上那个兜底的 socialdata provider，导致微博/小红书角色被当成 twitter 去拉
// （表现为「解析用户 … 失败 / socialdata.tools: HTTP 502」）。p 为 nil 时才退回兜底。
func (m *Manager) sharedFetch(ctx context.Context, p twitter.Provider, key string, ttl time.Duration, username string, count int) ([]model.Tweet, error) {
	if ttl <= 0 {
		ttl = defaultInterval * time.Second
	}
	m.fetchMu.Lock()
	if m.fetchPool == nil {
		m.fetchPool = map[string]cachedFetch{}
	}
	// 注意：fetchPoolMark 写入的是空推文占位（防后台刷新重复），不能当作有效结果返回，
	// 否则 PollOnce 会在 TTL 内拿到 0 条推文并静默返回（表现为「立即轮询」没反应）。
	if cf, ok := m.fetchPool[key]; ok && len(cf.tweets) > 0 && time.Since(cf.at) < ttl {
		m.fetchMu.Unlock()
		return cf.tweets, nil
	}
	m.fetchMu.Unlock()

	if p == nil {
		p = m.provider
	}
	tweets, err := p.FetchLatest(ctx, username, count)
	if err != nil {
		return nil, err
	}
	m.fetchMu.Lock()
	m.fetchPool[key] = cachedFetch{tweets: tweets, at: time.Now()}
	m.fetchMu.Unlock()
	return tweets, nil
}

// fetchCount 每次同步从上游拉多少条：角色可配（默认 20；上限 100，与 socialdata count 上限一致）。
func fetchCount(r *model.Role) int {
	if r.FetchCount <= 0 || r.FetchCount > 100 {
		return 20
	}
	return r.FetchCount
}

// tweetRowFromTweet 领域推文 → 缓存行（上游字段只在这里映射一次，
// 避免 fetchAndCache/loadOlder 各自手写时漏字段 —— Likes 就这么漏过）。
func tweetRowFromTweet(tw model.Tweet) store.TweetRow {
	return store.TweetRow{
		ID: tw.ID, User: tw.User, Text: tw.Text, CreatedAt: tw.CreatedAt,
		QuotedID: tw.QuotedID, QuotedUser: tw.QuotedUser, QuotedText: tw.QuotedText,
		RetweetID: tw.RetweetID, RetweetUser: tw.RetweetUser, RetweetText: tw.RetweetText,
		Media: tw.Media, Likes: tw.Likes,
		Images: tw.Images, Link: tw.Link,
		CommentCount: tw.CommentCount, ShareCount: tw.ShareCount, CollectCount: tw.CollectCount,
	}
}

// fetchAndCache 拉上游推文并落 SQLite（带索引），随后台补翻译。
func (m *Manager) fetchAndCache(r *model.Role) ([]model.Tweet, error) {
	provider, _, err := m.providerForRole(r)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	tweets, err := provider.FetchLatest(ctx, r.Username, fetchCount(r))
	if err != nil {
		return nil, err
	}
	rows := make([]store.TweetRow, 0, len(tweets))
	for _, tw := range tweets {
		row := tweetRowFromTweet(tw)
		row.Platform = normalizePlatform(r.Platform) // 接口要能看出这条来自哪个平台
		rows = append(rows, row)
	}
	_ = m.st.SaveTweets(r.ID, rows)
	go m.translateMissing(r.ID)
	return tweets, nil
}

// translateMissing 后台补齐缺失的中文翻译（免费 gtx 接口，尽力而为）。
func (m *Manager) translateMissing(roleID string) {
	for _, tw := range m.st.MissingTranslations(roleID, "zh", 20) {
		if zh, err := translateToZh(tw.Text); err == nil && zh != "" {
			_ = m.st.SaveTranslation(roleID, tw.ID, "zh", zh)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// rowToTweet 缓存行 → 领域推文（播报与上下文用；翻译字段不参与播报）。
func rowToTweet(row store.TweetRow) model.Tweet {
	return model.Tweet{ID: row.ID, User: row.User, Text: row.Text, CreatedAt: row.CreatedAt, Edited: row.Edited,
		QuotedID: row.QuotedID, QuotedUser: row.QuotedUser, QuotedText: row.QuotedText,
		RetweetID: row.RetweetID, RetweetUser: row.RetweetUser, RetweetText: row.RetweetText, Media: row.Media,
		Likes: row.Likes, Images: row.Images, Link: row.Link,
		CommentCount: row.CommentCount, ShareCount: row.ShareCount, CollectCount: row.CollectCount,
		Source: row.Source}
}

// RoleTweets 拉取角色绑定用户的最新推文（默认缓存优先，force=true 强制拉新并计费）。
func (m *Manager) RoleTweets(roleID string, force bool) ([]model.Tweet, error) {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return nil, err
	}
	if r.Username == "" {
		return nil, fmt.Errorf("role username not configured")
	}
	if force {
		return m.fetchAndCache(r)
	}
	// 列表类请求：SQLite 缓存秒开；超过 60s 未刷则后台刷新（不打断响应）
	if m.st.TweetCount(r.ID) > 0 {
		stale := true
		if m.fetchPoolFresh(m.fetchKey(r), 60*time.Second) {
			stale = false
		}
		if stale {
			m.fetchPoolMark(m.fetchKey(r)) // 占位防重
			go func() { _, _ = m.fetchAndCache(r) }()
		}
		rows := m.st.ListTweets(r.ID, "zh", "", "", 50)
		out := make([]model.Tweet, 0, len(rows))
		for _, tw := range rows {
			out = append(out, rowToTweet(tw))
		}
		return out, nil
	}
	// 首次：后台拉取，本次返回空（由调用方决定重试）
	if !m.fetchPoolFresh(m.fetchKey(r), time.Hour) {
		m.fetchPoolMark(m.fetchKey(r))
		go func() { _, _ = m.fetchAndCache(r) }()
	}
	return nil, nil
}

// RoleTweetRows API 用：带翻译字段的推文行。before/limit 游标分页；
// 只有第一页（before 为空）才触发后台刷新/翻译。
// more=true 时若缓存里已没有更早的推文，向上游翻历史页补齐（计费调用）。
func (m *Manager) RoleTweetRows(roleID string, force bool, before, kw string, limit int, more bool) []store.TweetRow {
	r, err := m.roles.Get(roleID)
	if err != nil || r.Username == "" {
		return nil
	}
	if before == "" {
		if force {
			// 手动刷新：同步拉新（用户明确点了按钮，愿意等）
			if _, err := m.fetchAndCache(r); err != nil {
				log.Printf("[feed] %s refresh failed: %v", r.Username, err)
			}
		}
		// 非 force：纯读本地缓存，绝不触发上游拉取。
		// 自动拉新由后端 scheduler 按角色 interval 负责（webhook/callback 模式由上游推送），
		// 前端打开 Feed 或补显翻译不应产生任何计费调用。
	} else if more {
		// 翻旧推文：缓存不够就向上游翻历史页
		m.loadOlder(r, before)
	}
	return m.st.ListTweets(r.ID, "zh", before, kw, limit)
}

// RoleCheckedTweets 某池勾选中的推文（pool: random|parody）。
// 游标分页：before 非空返回更早的；limit<=0=不分页（Feed 顶部池区一次展示全量）。
func (m *Manager) RoleCheckedTweets(roleID, pool, before string, limit int) []store.TweetRow {
	r, err := m.roles.Get(roleID)
	if err != nil || r.Username == "" {
		return nil
	}
	return m.st.ListCheckedTweets(r.ID, "zh", pool, before, limit)
}

// CheckedTweetCount 某池勾选中的推文总数（池列表分页显示「已加载 x / 共 N 条」）。
func (m *Manager) CheckedTweetCount(roleID, pool string) int {
	r, err := m.roles.Get(roleID)
	if err != nil || r.Username == "" {
		return 0
	}
	return m.st.CountCheckedTweets(r.ID, pool)
}

// TweetCount 缓存里匹配 kw 的推文总数（kw 为空=全部；搜索结果分页显示进度）。
func (m *Manager) TweetCount(roleID, kw string) int {
	r, err := m.roles.Get(roleID)
	if err != nil || r.Username == "" {
		return 0
	}
	return m.st.CountTweets(r.ID, kw)
}

// pager 支持游标翻页的上游（目前 socialdata.tools）。
type pager interface {
	FetchPage(ctx context.Context, screenName string, count int, cursor string) ([]model.Tweet, string, error)
}

// loadOlder 向上游翻历史页，直到拿到比 before 更早的推文或没有下一页。
// 每次最多翻 4 页（~80 条），控制计费；结果累积进缓存。
func (m *Manager) loadOlder(r *model.Role, before string) {
	provider, _, err := m.providerForRole(r)
	if err != nil {
		return
	}
	pg, ok := provider.(pager)
	if !ok {
		return // 该上游不支持翻页
	}
	// 缓存里已有更早的就不用翻
	if oldest := m.st.OldestTweetID(r.ID); oldest != "" && before != "" && gtID(before, oldest) {
		return
	}
	m.historyMu.Lock()
	busy := m.historyBusy[r.ID]
	m.historyBusy[r.ID] = true
	m.historyMu.Unlock()
	if busy {
		return
	}
	defer func() {
		m.historyMu.Lock()
		delete(m.historyBusy, r.ID)
		m.historyMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var collected []store.TweetRow
	cursor := ""
	for page := 0; page < 4; page++ {
		tweets, next, err := pg.FetchPage(ctx, r.Username, 50, cursor)
		if err != nil {
			log.Printf("[feed] %s history fetch failed: %v", r.Username, err)
			break
		}
		reached := false
		for _, tw := range tweets {
			if before != "" && tw.ID < before {
				reached = true
			}
			row := tweetRowFromTweet(tw)
			row.Platform = normalizePlatform(r.Platform)
			collected = append(collected, row)
		}
		if reached || next == "" || len(tweets) == 0 {
			break
		}
		cursor = next
		time.Sleep(300 * time.Millisecond) // 限速
	}
	if len(collected) > 0 {
		if err := m.st.SaveTweets(r.ID, collected); err != nil {
			log.Printf("[feed] %s history cache failed: %v", r.Username, err)
		} else {
			log.Printf("[feed] %s history +%d tweets cached", r.Username, len(collected))
		}
	}
}

// fetchKey 缓存键（与 sharedFetch 一致的口径）。
// 带上平台：同名账号在不同平台（twitter/weibo/elonmusk）不能互相命中缓存。
func (m *Manager) fetchKey(r *model.Role) string {
	provider, up, err := m.providerForRole(r)
	if err != nil {
		return "unknown|" + normalizePlatform(r.Platform) + "|" + r.Username
	}
	return fmt.Sprintf("%s|%s|%s|%s", provider.Name(), upIDorEmpty(up), normalizePlatform(r.Platform), strings.ToLower(r.Username))
}

// translateToZh 免费 Google translate gtx 接口（尽力而为，失败返回空）。
func translateToZh(text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", nil
	}
	req, _ := http.NewRequest(http.MethodGet,
		"https://translate.googleapis.com/translate_a/single?client=gtx&sl=auto&tl=zh-CN&dt=t&q="+url.QueryEscape(text), nil)
	resp, err := sharedHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("translate HTTP %d", resp.StatusCode)
	}
	var arr []any
	if err := json.NewDecoder(resp.Body).Decode(&arr); err != nil {
		return "", err
	}
	var sb strings.Builder
	if segs, ok := arr[0].([]any); ok {
		for _, seg := range segs {
			if pair, ok := seg.([]any); ok && len(pair) > 0 {
				if t, ok := pair[0].(string); ok {
					sb.WriteString(t)
				}
			}
		}
	}
	return sb.String(), nil
}

// fetchPoolMark 标记某 key 的拉取时间（懒初始化 map）。
func (m *Manager) fetchPoolMark(key string) {
	m.fetchMu.Lock()
	if m.fetchPool == nil {
		m.fetchPool = map[string]cachedFetch{}
	}
	m.fetchPool[key] = cachedFetch{at: time.Now()}
	m.fetchMu.Unlock()
}

// fetchPoolFresh key 是否在 ttl 内拉取过。
func (m *Manager) fetchPoolFresh(key string, ttl time.Duration) bool {
	m.fetchMu.Lock()
	defer m.fetchMu.Unlock()
	if m.fetchPool == nil {
		return false
	}
	cf, ok := m.fetchPool[key]
	return ok && time.Since(cf.at) < ttl
}

// splitWords 关键词表切分（逗号/空格/换行/顿号）。
func splitWords(s string) []string {
	return strings.FieldsFunc(s, func(rn rune) bool {
		return rn == ',' || rn == '\n' || rn == ' ' || rn == '\t' || rn == '，' || rn == '、'
	})
}

// filteredOut 排除词命中判断：对生效文本（编辑优先）与引用文本匹配。
func (m *Manager) filteredOut(r *model.Role, tw model.Tweet) bool {
	words := splitWords(r.FilterWords)
	if len(words) == 0 {
		return false
	}
	hay := strings.ToLower(tw.EffectiveText() + " " + tw.QuotedText)
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" && strings.Contains(hay, w) {
			return true
		}
	}
	return false
}

// requireWordsOK 必须包含词（白名单）：为空=不限制；否则正文/引用/转发需命中其中之一。
func requireWordsOK(r *model.Role, tw model.Tweet) bool {
	words := splitWords(r.RequireWords)
	if len(words) == 0 {
		return true
	}
	hay := strings.ToLower(tw.EffectiveText() + " " + tw.QuotedText + " " + tw.RetweetText)
	for _, w := range words {
		w = strings.ToLower(strings.TrimSpace(w))
		if w != "" && strings.Contains(hay, w) {
			return true
		}
	}
	return false
}

// hasReadableText 有可播报正文（含引用/转发目标）：纯媒体推文正文为空。
func hasReadableText(tw model.Tweet) bool {
	return strings.TrimSpace(tw.EffectiveText()) != "" ||
		strings.TrimSpace(tw.RetweetText) != "" ||
		strings.TrimSpace(tw.QuotedText) != ""
}

// broadcastable 一条推文能否进入随机/自动播报池：排除词、必须包含词、无正文开关。
// 注意：用户在 Feed 里显式点「播报这条」不走这里 —— 他明确要播就不拦。
func (m *Manager) broadcastable(r *model.Role, tw model.Tweet) bool {
	if m.filteredOut(r, tw) || !requireWordsOK(r, tw) {
		return false
	}
	return r.AllowEmpty || hasReadableText(tw)
}

// broadcastPool broadcast 模式的候选池：本地缓存里「已勾选」且通过过滤的推文。
// 用缓存而不是上游最新一页 —— 勾选是用户的显式选择，旧推文同样应该有机会被播。
func (m *Manager) broadcastPool(r *model.Role) []model.Tweet {
	rows := m.st.SelectedTweets(r.ID, "", 0)
	out := make([]model.Tweet, 0, len(rows))
	for _, row := range rows {
		tw := rowToTweet(row)
		if m.broadcastable(r, tw) {
			out = append(out, tw)
		}
	}
	return out
}

// ---- random 模式：按规则从缓存里挑 ----

// randomModePool random 模式的候选池 = 缓存真实推文 ∩ 规则。
// 规则：必须含词 / 排除词 / 推文发布日期区间 / 点赞≥N；RandomOnlyChecked 打开则只从勾选里挑。
// 点赞/勾选/来源在 SQL 侧过滤，关键词与日期在 Go 侧判（created_at 格式各上游不一，字符串比较不可靠）。
func (m *Manager) randomModePool(r *model.Role) []model.Tweet {
	rows := m.st.FilterTweets(r.ID, "real", r.RandomOnlyChecked, r.RandomMinLikes, 0)
	out := make([]model.Tweet, 0, len(rows))
	for _, row := range rows {
		tw := rowToTweet(row)
		if m.broadcastable(r, tw) && randomRulesOK(r, tw) {
			out = append(out, tw)
		}
	}
	return out
}

// randomRulesOK random 模式的关键词与日期规则（点赞/勾选已在 SQL 侧过滤）。
func randomRulesOK(r *model.Role, tw model.Tweet) bool {
	hay := strings.ToLower(tw.EffectiveText() + " " + tw.QuotedText + " " + tw.RetweetText)
	for _, w := range splitWords(r.RandomExcludeWords) {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" && strings.Contains(hay, w) {
			return false
		}
	}
	req := splitWords(r.RandomRequireWords)
	if len(req) > 0 {
		hit := false
		for _, w := range req {
			if w = strings.ToLower(strings.TrimSpace(w)); w != "" && strings.Contains(hay, w) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return inDateRange(tw.CreatedAt, r.RandomFrom, r.RandomTo)
}

// inDateRange 推文发布日期是否落在 [from, to]（含边界；YYYY-MM-DD，空=不限）。
func inDateRange(createdAt, from, to string) bool {
	if from == "" && to == "" {
		return true
	}
	t, ok := parseTweetTime(createdAt)
	if !ok {
		return false // 时间解析不出来就不参与日期规则（宁可少播，也不误播）
	}
	if from != "" {
		if f, err := time.Parse("2006-01-02", from); err == nil && t.Before(f) {
			return false
		}
	}
	if to != "" {
		if e, err := time.Parse("2006-01-02", to); err == nil && t.After(e.Add(24*time.Hour)) {
			return false
		}
	}
	return true
}

// ---- 时间解析 ----

var (
	// 微博相对时间："3分钟前" / "2小时前" / "5秒前" / "1天前"
	cnAgoRe = regexp.MustCompile(`^(\d+)\s*(秒|分钟|小时|天)前$`)
	// 微博日期："10月15日 09:54" / "2026年10月15日 09:54:30" / "10月15日"
	cnDateRe = regexp.MustCompile(`^(?:(\d{4})年)?(\d{1,2})月(\d{1,2})日(?:\s*(\d{1,2}):(\d{2})(?::(\d{2}))?)?$`)
	// 时刻："08:00" / "8:00:30"
	cnClockRe = regexp.MustCompile(`^(\d{1,2}):(\d{2})(?::(\d{2}))?$`)
)

// parseTweetTime 解析各上游的 created_at：
//   - ISO8601/RFC3339/RFC1123/RFC822 与 Twitter 经典格式 "Mon Jan 02 15:04:05 -0700 2006"
//   - unix 时间戳（10 位秒 / 13 位毫秒）
//   - 微博中文时间："刚刚" / "3分钟前" / "今天 08:00" / "昨天 22:15" / "10月15日 09:54"
//
// 解析不出来返回零值 + false（调用方降级为原文，绝不 panic）。
func parseTweetTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		time.RFC1123,
		time.RFC1123Z,
		time.RFC822,
		time.RFC822Z,
		time.RubyDate,
		time.UnixDate,
		"Mon Jan 02 15:04:05 -0700 2006",
		"Mon Jan 2 15:04:05 -0700 2006",
		"Mon Jan 02 15:04:05 MST 2006",
		"Mon Jan 2 15:04:05 MST 2006",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	// 纯数字：10 位 unix 秒 / 13 位毫秒
	if len(s) == 10 || len(s) == 13 {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			if len(s) == 13 {
				return time.UnixMilli(n), true
			}
			return time.Unix(n, 0), true
		}
	}
	return parseCNTime(s)
}

// parseCNTime 微博中文时间（"刚刚"/"N分钟前"/"今天 08:00"/"昨天 22:15"/"10月15日 09:54"）。
func parseCNTime(s string) (time.Time, bool) {
	now := time.Now()
	loc := now.Location()
	if s == "刚刚" || s == "刚才" || s == "刚刚发布" {
		return now, true
	}
	if m := cnAgoRe.FindStringSubmatch(s); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return time.Time{}, false
		}
		switch m[2] {
		case "秒":
			return now.Add(-time.Duration(n) * time.Second), true
		case "分钟":
			return now.Add(-time.Duration(n) * time.Minute), true
		case "小时":
			return now.Add(-time.Duration(n) * time.Hour), true
		case "天":
			return now.AddDate(0, 0, -n), true
		}
	}
	// "今天 08:00" / "昨天 22:15" / "前天 09:00"：没带时刻就按当天 00:00
	day := now
	rest := ""
	dayPrefix := false
	switch {
	case strings.HasPrefix(s, "今天"):
		dayPrefix, rest = true, strings.TrimSpace(strings.TrimPrefix(s, "今天"))
	case strings.HasPrefix(s, "昨天"):
		dayPrefix, day = true, now.AddDate(0, 0, -1)
		rest = strings.TrimSpace(strings.TrimPrefix(s, "昨天"))
	case strings.HasPrefix(s, "前天"):
		dayPrefix, day = true, now.AddDate(0, 0, -2)
		rest = strings.TrimSpace(strings.TrimPrefix(s, "前天"))
	}
	if dayPrefix {
		h, mi, sec := 0, 0, 0
		if rest != "" {
			mm := cnClockRe.FindStringSubmatch(rest)
			if mm == nil {
				return time.Time{}, false
			}
			h, _ = strconv.Atoi(mm[1])
			mi, _ = strconv.Atoi(mm[2])
			if mm[3] != "" {
				sec, _ = strconv.Atoi(mm[3])
			}
		}
		if h > 23 || mi > 59 || sec > 59 {
			return time.Time{}, false
		}
		return time.Date(day.Year(), day.Month(), day.Day(), h, mi, sec, 0, loc), true
	}
	// "10月15日 09:54" / "2026年10月15日"
	if m := cnDateRe.FindStringSubmatch(s); m != nil {
		year := now.Year()
		if m[1] != "" {
			year, _ = strconv.Atoi(m[1])
		}
		mon, _ := strconv.Atoi(m[2])
		dayN, _ := strconv.Atoi(m[3])
		if mon < 1 || mon > 12 || dayN < 1 || dayN > 31 {
			return time.Time{}, false
		}
		h, mi, sec := 0, 0, 0
		if m[4] != "" {
			h, _ = strconv.Atoi(m[4])
		}
		if m[5] != "" {
			mi, _ = strconv.Atoi(m[5])
		}
		if m[6] != "" {
			sec, _ = strconv.Atoi(m[6])
		}
		if h > 23 || mi > 59 || sec > 59 {
			return time.Time{}, false
		}
		t := time.Date(year, time.Month(mon), dayN, h, mi, sec, 0, loc)
		// 微博只写月日：算出来比现在晚一天以上 → 其实是去年的
		if m[1] == "" && t.After(now.Add(24*time.Hour)) {
			t = t.AddDate(-1, 0, 0)
		}
		return t, true
	}
	return time.Time{}, false
}
