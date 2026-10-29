// Package xapi 用登录 token 直接调 x.com 的网页 GraphQL，不再为每个用户打开 Chrome。
//
// 凭据来源（按顺序）：
//  1. 环境变量 TWITTER_AUTH_TOKEN / TWITTER_CT0
//  2. chrome-driverless 写下的 auth.json（profiles/<name>/auth.json 里的 auth_token、ct0）
//
// ct0 缺失时，带 auth_token 访问 x.com 首页，从 Set-Cookie 补上。
// queryId 从页面引用的前端脚本里解析并缓存，接口报 feature 缺失时补一次再重试。
package xapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/netcfg"
)

// 网页客户端内置的公开 Bearer，所有账号相同，不是用户密钥。
const webBearer = "AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"

const ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// Client 用 token 调 x.com。
type Client struct {
	DataDir   string
	Proxy     string
	AuthToken string
	CT0       string
	HTTP      *http.Client
	// Origin 测试时可指向 httptest；空则 https://x.com。
	Origin string

	mu       sync.Mutex
	loaded   bool
	qids     map[string]string
	qidAt    time.Time
	features map[string]bool
}

// Enabled 是否已经拿到 auth_token（没有就还应走原来的浏览器抓取）。
func (c *Client) Enabled() bool {
	if c == nil {
		return false
	}
	c.ensureCreds()
	return c.AuthToken != ""
}

// Exec 按 chrome 脚本的 CLI 参数执行，返回同样的单行 JSON 形状：
//
//	login-status → {ok, logged_in, detail}
//	user         → {ok, user}
//	tweets       → {ok, tweets, next_cursor}
//
// 未登录时 ok=false 且 need_login=true，error 为 nil（与脚本退出码 0 一致）。
func (c *Client) Exec(ctx context.Context, args ...string) (map[string]any, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("xapi: 没有 auth_token")
	}
	action, screen, count, after := parseArgs(args)
	switch action {
	case "login-status":
		return c.loginStatus(ctx)
	case "user":
		if screen == "" {
			return nil, fmt.Errorf("xapi: 缺少 screen")
		}
		user, err := c.userByScreen(ctx, screen)
		if err != nil {
			if nl, ok := err.(*needLogin); ok {
				return map[string]any{"ok": false, "need_login": true, "error": nl.Error()}, nil
			}
			return nil, err
		}
		return map[string]any{"ok": true, "user": user}, nil
	case "tweets":
		if screen == "" {
			return nil, fmt.Errorf("xapi: 缺少 screen")
		}
		if count <= 0 || count > 100 {
			count = 20
		}
		tweets, cursor, err := c.tweets(ctx, screen, count, after)
		if err != nil {
			if nl, ok := err.(*needLogin); ok {
				return map[string]any{"ok": false, "need_login": true, "error": nl.Error()}, nil
			}
			return nil, err
		}
		if len(tweets) == 0 {
			return nil, fmt.Errorf("xapi: %s 没有拉到推文", screen)
		}
		return map[string]any{"ok": true, "tweets": tweets, "next_cursor": cursor}, nil
	default:
		return nil, fmt.Errorf("xapi: 未知动作 %s", action)
	}
}

type needLogin struct{ msg string }

func (e *needLogin) Error() string {
	if e.msg == "" {
		return "需要登录 x.com（auth_token 无效或过期）"
	}
	return e.msg
}

func parseArgs(args []string) (action, screen string, count int, after string) {
	if len(args) == 0 {
		return "", "", 20, ""
	}
	action = args[0]
	count = 20
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--screen":
			if i+1 < len(args) {
				i++
				screen = strings.TrimPrefix(strings.ToLower(args[i]), "@")
			}
		case "--count":
			if i+1 < len(args) {
				i++
				fmt.Sscanf(args[i], "%d", &count)
			}
		case "--after":
			if i+1 < len(args) {
				i++
				after = strings.TrimPrefix(args[i], "@")
			}
		}
	}
	return action, screen, count, after
}

func (c *Client) origin() string {
	if c.Origin != "" {
		return strings.TrimRight(c.Origin, "/")
	}
	return "https://x.com"
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{Proxy: netcfg.ProxyFunc}
	if c.Proxy != "" {
		if u, err := url.Parse(c.Proxy); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	c.HTTP = &http.Client{Timeout: 25 * time.Second, Jar: jar, Transport: tr}
	return c.HTTP
}

func (c *Client) ensureCreds() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return
	}
	c.loaded = true
	if c.AuthToken == "" {
		c.AuthToken = strings.TrimSpace(os.Getenv("TWITTER_AUTH_TOKEN"))
	}
	if c.CT0 == "" {
		c.CT0 = strings.TrimSpace(os.Getenv("TWITTER_CT0"))
	}
	if c.AuthToken != "" {
		return
	}
	tok, ct0 := loadAuthFile(c.DataDir)
	c.AuthToken, c.CT0 = tok, ct0
}

func loadAuthFile(dataDir string) (token, ct0 string) {
	if dataDir == "" {
		return "", ""
	}
	var paths []string
	if name := os.Getenv("PROFILE_NAME"); name != "" {
		paths = append(paths, filepath.Join(dataDir, "profiles", name, "auth.json"))
	}
	paths = append(paths, filepath.Join(dataDir, "auth.json"))
	matches, _ := filepath.Glob(filepath.Join(dataDir, "profiles", "*", "auth.json"))
	paths = append(paths, matches...)
	for _, p := range paths {
		tok, ct, err := readAuthJSON(p)
		if err == nil && tok != "" {
			return tok, ct
		}
	}
	return "", ""
}

func readAuthJSON(path string) (token, ct0 string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var doc struct {
		Cookies []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"cookies"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", "", err
	}
	for _, ck := range doc.Cookies {
		switch ck.Name {
		case "auth_token":
			token = ck.Value
		case "ct0":
			ct0 = ck.Value
		}
	}
	return token, ct0, nil
}

func (c *Client) loginStatus(ctx context.Context) (map[string]any, error) {
	if err := c.bootstrap(ctx); err != nil {
		return map[string]any{"ok": true, "logged_in": false, "detail": err.Error()}, nil
	}
	return map[string]any{"ok": true, "logged_in": true, "detail": "auth_token 可用（直接调接口，不打开浏览器）"}, nil
}

func (c *Client) bootstrap(ctx context.Context) error {
	if c.CT0 != "" && len(c.qids) > 0 && time.Since(c.qidAt) < 6*time.Hour {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.origin()+"/", nil)
	if err != nil {
		return err
	}
	c.decorate(req, false)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	resp.Body.Close()
	c.absorbCT0(resp)
	if c.CT0 == "" {
		if m := regexp.MustCompile(`ct0=([a-zA-Z0-9_-]+)`).FindStringSubmatch(string(body)); len(m) == 2 {
			c.CT0 = m[1]
		}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if c.CT0 == "" {
			return &needLogin{msg: fmt.Sprintf("x.com HTTP %d，auth_token 无效", resp.StatusCode)}
		}
	}
	if c.CT0 == "" {
		return &needLogin{msg: "拿到了 auth_token，但没有 ct0（请重新登录 x.com）"}
	}
	if len(c.qids) == 0 || time.Since(c.qidAt) >= 6*time.Hour {
		if err := c.discover(ctx, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) absorbCT0(resp *http.Response) {
	if c.CT0 != "" || resp == nil {
		return
	}
	for _, ck := range resp.Cookies() {
		if ck.Name == "ct0" && ck.Value != "" {
			c.CT0 = ck.Value
			return
		}
	}
	if c.HTTP != nil && c.HTTP.Jar != nil {
		u, _ := url.Parse(c.origin() + "/")
		for _, ck := range c.HTTP.Jar.Cookies(u) {
			if ck.Name == "ct0" && ck.Value != "" {
				c.CT0 = ck.Value
				return
			}
		}
	}
}

var (
	scriptRe = regexp.MustCompile(`https://abs\.twimg\.com/responsive-web/client-web/[A-Za-z0-9._/-]+\.js`)
	qidRe    = regexp.MustCompile(`queryId:"([A-Za-z0-9_-]{8,})",operationName:"(UserByScreenName|UserTweets|UserOriginalsTimeline|UserTweetsAndReplies)"`)
	qidRe2   = regexp.MustCompile(`operationName:"(UserByScreenName|UserTweets|UserOriginalsTimeline|UserTweetsAndReplies)",queryId:"([A-Za-z0-9_-]{8,})"`)
)

func (c *Client) discover(ctx context.Context, html string) error {
	if c.qids == nil {
		c.qids = map[string]string{}
	}
	apply := func(js string) {
		for _, m := range qidRe.FindAllStringSubmatch(js, -1) {
			if c.qids[m[2]] == "" {
				c.qids[m[2]] = m[1]
			}
		}
		for _, m := range qidRe2.FindAllStringSubmatch(js, -1) {
			if c.qids[m[1]] == "" {
				c.qids[m[1]] = m[2]
			}
		}
	}
	apply(html)
	seen := map[string]bool{}
	for _, src := range scriptRe.FindAllString(html, 8) {
		if seen[src] {
			continue
		}
		seen[src] = true
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", ua)
		resp, err := c.httpClient().Do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		resp.Body.Close()
		apply(string(b))
		if c.qids["UserByScreenName"] != "" && (c.qids["UserTweets"] != "" || c.qids["UserOriginalsTimeline"] != "") {
			break
		}
	}
	if c.qids["UserByScreenName"] == "" {
		return fmt.Errorf("xapi: 没从 x.com 前端脚本解析到 UserByScreenName queryId")
	}
	c.qidAt = time.Now()
	if c.features == nil {
		c.features = defaultFeatures()
	}
	return nil
}

// UserProfile 返回 UserByScreenName 的归一化用户对象（供账号识别使用）。
func (c *Client) UserProfile(ctx context.Context, screen string) (map[string]any, error) {
	screen = strings.TrimPrefix(strings.TrimSpace(screen), "@")
	if screen == "" {
		return nil, fmt.Errorf("xapi: 空用户名")
	}
	return c.userByScreen(ctx, screen)
}

func (c *Client) userByScreen(ctx context.Context, screen string) (map[string]any, error) {
	if err := c.bootstrap(ctx); err != nil {
		return nil, err
	}
	vars := map[string]any{"screen_name": screen, "withSafetyModeUserFields": true}
	raw, err := c.gql(ctx, "UserByScreenName", vars, nil)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Data struct {
			User struct {
				Result map[string]any `json:"result"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	user := normalizeUser(doc.Data.User.Result)
	if user == nil || str(user["id_str"]) == "" {
		return nil, fmt.Errorf("xapi: 解析不到用户 %s", screen)
	}
	return user, nil
}

func (c *Client) tweets(ctx context.Context, screen string, count int, after string) ([]any, string, error) {
	user, err := c.userByScreen(ctx, screen)
	if err != nil {
		return nil, "", err
	}
	uid := str(user["id_str"])
	op := "UserTweets"
	if c.qids[op] == "" {
		op = "UserOriginalsTimeline"
	}
	var (
		all    []any
		cursor string
		seen   = map[string]bool{}
	)
	for page := 0; page < 4 && len(all) < count; page++ {
		vars := map[string]any{
			"userId": uid, "count": count,
			"includePromotedContent": false, "withQuickPromoteEligibilityTweetFields": true,
			"withVoice": true, "withV2Timeline": true,
		}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		raw, err := c.gql(ctx, op, vars, map[string]any{"withArticlePlainText": false})
		if err != nil {
			return nil, "", err
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, "", err
		}
		batch, next := extractTweets(doc)
		cursor = next
		added := 0
		for _, tw := range batch {
			id := str(tw["id_str"])
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			all = append(all, tw)
			added++
		}
		if after != "" && added > 0 {
			oldest := str(all[len(all)-1].(map[string]any)["id_str"])
			if oldest != "" && ltID(oldest, after) {
				break
			}
		}
		if cursor == "" || added == 0 {
			break
		}
		if after == "" {
			break
		}
	}
	if len(all) > count && after == "" {
		all = all[:count]
	}
	return all, cursor, nil
}

func (c *Client) gql(ctx context.Context, op string, vars, toggles map[string]any) ([]byte, error) {
	qid := c.qids[op]
	if qid == "" {
		return nil, fmt.Errorf("xapi: 没有 %s 的 queryId", op)
	}
	raw, status, err := c.gqlOnce(ctx, op, qid, vars, toggles, c.features)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, &needLogin{msg: fmt.Sprintf("x.com %s HTTP %d", op, status)}
	}
	if missing := missingFeatures(raw); len(missing) > 0 {
		if c.features == nil {
			c.features = defaultFeatures()
		}
		for _, name := range missing {
			c.features[name] = false
		}
		raw, status, err = c.gqlOnce(ctx, op, qid, vars, toggles, c.features)
		if err != nil {
			return nil, err
		}
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("xapi: %s HTTP %d %s", op, status, clip(raw))
	}
	if msg := graphqlError(raw); msg != "" {
		return nil, fmt.Errorf("xapi: %s", msg)
	}
	return raw, nil
}

func (c *Client) gqlOnce(ctx context.Context, op, qid string, vars, toggles map[string]any, features map[string]bool) ([]byte, int, error) {
	vraw, _ := json.Marshal(vars)
	fraw, _ := json.Marshal(features)
	q := url.Values{}
	q.Set("variables", string(vraw))
	q.Set("features", string(fraw))
	if len(toggles) > 0 {
		traw, _ := json.Marshal(toggles)
		q.Set("fieldToggles", string(traw))
	}
	u := c.origin() + "/i/api/graphql/" + qid + "/" + op + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	c.decorate(req, true)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	c.absorbCT0(resp)
	return b, resp.StatusCode, nil
}

func (c *Client) decorate(req *http.Request, api bool) {
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", c.origin()+"/")
	cookie := "auth_token=" + c.AuthToken
	if c.CT0 != "" {
		cookie += "; ct0=" + c.CT0
	}
	req.Header.Set("Cookie", cookie)
	if !api {
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		return
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+webBearer)
	if c.CT0 != "" {
		req.Header.Set("x-csrf-token", c.CT0)
		req.Header.Set("x-twitter-auth-type", "OAuth2Session")
		req.Header.Set("x-twitter-active-user", "yes")
		req.Header.Set("x-twitter-client-language", "en")
	}
}

var missingFeatRe = regexp.MustCompile(`The following features cannot be null: ([^\n"]+)`)

func missingFeatures(raw []byte) []string {
	m := missingFeatRe.FindStringSubmatch(string(raw))
	if len(m) != 2 {
		return nil
	}
	var out []string
	for _, p := range strings.Split(m[1], ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func graphqlError(raw []byte) string {
	var doc struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &doc) != nil || len(doc.Errors) == 0 {
		return ""
	}
	if strings.Contains(doc.Errors[0].Message, "features cannot be null") {
		return ""
	}
	return doc.Errors[0].Message
}

func clip(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 180 {
		return s[:180]
	}
	return s
}

func defaultFeatures() map[string]bool {
	return map[string]bool{
		"creator_subscriptions_tweet_preview_api_enabled":                         true,
		"communities_web_enable_tweet_community_results_fetch":                    true,
		"c9s_tweet_anatomy_moderator_badge_enabled":                               true,
		"articles_preview_enabled":                                                true,
		"tweetypie_unmention_optimization_enabled":                                true,
		"responsive_web_edit_tweet_api_enabled":                                   true,
		"graphql_is_translatable_rweb_tweet_is_translatable_enabled":              true,
		"view_counts_everywhere_api_enabled":                                      true,
		"longform_notetweets_consumption_enabled":                                 true,
		"responsive_web_twitter_article_tweet_consumption_enabled":                true,
		"tweet_awards_web_tipping_enabled":                                        false,
		"creator_subscriptions_quote_tweet_preview_enabled":                       false,
		"freedom_of_speech_not_reach_fetch_enabled":                               true,
		"standardized_nudges_misinfo":                                             true,
		"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled": true,
		"tweet_with_visibility_results_prefer_gql_media_interstitial_enabled":     true,
		"rweb_video_timestamps_enabled":                                           true,
		"longform_notetweets_rich_text_read_enabled":                              true,
		"longform_notetweets_inline_media_enabled":                                true,
		"rweb_tipjar_consumption_enabled":                                         true,
		"responsive_web_graphql_exclude_directive_enabled":                        true,
		"verified_phone_label_enabled":                                            false,
		"responsive_web_graphql_skip_user_profile_image_extensions_enabled":       false,
		"responsive_web_graphql_timeline_navigation_enabled":                      true,
		"responsive_web_enhance_cards_enabled":                                    false,
		"responsive_web_media_download_video_enabled":                             false,
		"hidden_profile_likes_enabled":                                            true,
		"hidden_profile_subscriptions_enabled":                                    true,
		"subscriptions_verification_info_is_identity_verified_enabled":            true,
		"subscriptions_verification_info_verified_since_enabled":                  true,
		"highlights_tweets_tab_ui_enabled":                                        true,
		"responsive_web_twitter_article_notes_tab_enabled":                        false,
		"premium_content_api_read_enabled":                                        false,
		"profile_label_improvements_pcf_label_in_post_enabled":                    false,
	}
}
