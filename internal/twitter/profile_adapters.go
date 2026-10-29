package twitter

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"twitter_read_play/internal/xapi"
)

// ---- socialdata.tools ----

type sdProfile struct {
	IDStr                string `json:"id_str"`
	ID                   any    `json:"id"`
	ScreenName           string `json:"screen_name"`
	Name                 string `json:"name"`
	Description          string `json:"description"`
	ProfileImageURLHTTPS string `json:"profile_image_url_https"`
	FollowersCount       int    `json:"followers_count"`
	FriendsCount         int    `json:"friends_count"`
	StatusesCount        int    `json:"statuses_count"`
	Verified             bool   `json:"verified"`
}

func (s *Socialdata) Profile(ctx context.Context, screenName string) (UserProfile, error) {
	screenName = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(screenName), "@"))
	if screenName == "" {
		return UserProfile{}, fmt.Errorf("socialdata: 空用户名")
	}
	var u sdProfile
	if err := s.get(ctx, "/twitter/user/"+url.PathEscape(screenName), &u); err != nil {
		return UserProfile{}, err
	}
	id := u.IDStr
	if id == "" {
		id = anyString(u.ID)
	}
	if id == "" && u.ScreenName == "" {
		return UserProfile{}, fmt.Errorf("socialdata: 账号 %s 不存在", screenName)
	}
	handle := firstNonEmptyValue(u.ScreenName, screenName)
	return UserProfile{
		ID:          id,
		Handle:      handle,
		DisplayName: firstNonEmptyValue(u.Name, handle),
		AvatarURL:   u.ProfileImageURLHTTPS,
		Bio:         u.Description,
		Followers:   u.FollowersCount,
		Following:   u.FriendsCount,
		Posts:       u.StatusesCount,
		Verified:    u.Verified,
	}, nil
}

// ---- socialread（微博 / 小红书 / B 站）----

func (s *SocialRead) Profile(ctx context.Context, handle string) (UserProfile, error) {
	platform := s.platform()
	if platform == "" {
		return UserProfile{}, fmt.Errorf("social_read: 未配置平台（weibo/xiaohongshu/bilibili）")
	}
	handle = strings.TrimPrefix(strings.TrimSpace(handle), "@")
	if handle == "" {
		return UserProfile{}, fmt.Errorf("social_read: 空用户名")
	}
	var u srUser
	if err := s.get(ctx, "/"+platform+"/user/"+url.PathEscape(handle), &u); err != nil {
		return UserProfile{}, err
	}
	if u.ID == "" && u.Handle == "" && u.Name == "" {
		return UserProfile{}, fmt.Errorf("social_read: 账号 %s 不存在", handle)
	}
	return UserProfile{
		ID:           u.ID,
		Handle:       firstNonEmptyValue(u.Handle, handle),
		DisplayName:  firstNonEmptyValue(u.Name, u.Handle, handle),
		AvatarURL:    u.Avatar,
		Bio:          u.Bio,
		Followers:    u.FollowersCount,
		Following:    u.FollowingCount,
		Posts:        u.PostsCount,
		Verified:     u.Verified,
		CanonicalURL: u.WebURL,
	}, nil
}

// ---- X 登录 token / 官方 API ----

func (x *XToken) Profile(ctx context.Context, screenName string) (UserProfile, error) {
	token, ct0 := ParseXCreds(x.AuthToken, x.CT0)
	if token == "" {
		return UserProfile{}, fmt.Errorf("xtoken: 未配置 auth_token")
	}
	c := &xapi.Client{AuthToken: token, CT0: ct0, Proxy: x.Proxy}
	u, err := c.UserProfile(ctx, screenName)
	if err != nil {
		return UserProfile{}, err
	}
	if len(u) == 0 {
		return UserProfile{}, fmt.Errorf("xtoken: 账号 %s 不存在", screenName)
	}
	handle := firstNonEmptyValue(strOf(u["screen_name"]), strings.TrimPrefix(screenName, "@"))
	return UserProfile{
		ID:          strOf(u["id_str"]),
		Handle:      handle,
		DisplayName: firstNonEmptyValue(strOf(u["name"]), handle),
		AvatarURL:   strOf(u["profile_image_url_https"]),
		Bio:         strOf(u["description"]),
		Followers:   anyInt(u["followers_count"]),
		Following:   anyInt(u["friends_count"]),
		Posts:       anyInt(u["statuses_count"]),
		Verified:    boolValue(u["verified"]),
	}, nil
}

func (t *TwitterOfficial) Profile(ctx context.Context, screenName string) (UserProfile, error) {
	if t.Bearer == "" {
		return UserProfile{}, fmt.Errorf("twitter_official: 未配置 Bearer Token")
	}
	base := strings.TrimSuffix(strings.TrimSpace(t.BaseURL), "/")
	if base == "" {
		base = "https://api.twitter.com"
	}
	screenName = strings.TrimPrefix(strings.TrimSpace(screenName), "@")
	u := base + "/2/users/by/username/" + url.PathEscape(screenName) +
		"?user.fields=profile_image_url,description,public_metrics,verified"
	hc := *sharedClient
	hc.Timeout = 20 * time.Second
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return UserProfile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+t.Bearer)
	resp, err := hc.Do(req)
	if err != nil {
		return UserProfile{}, err
	}
	defer resp.Body.Close()
	var res struct {
		Data struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			Username        string `json:"username"`
			ProfileImageURL string `json:"profile_image_url"`
			Description     string `json:"description"`
			Verified        bool   `json:"verified"`
			PublicMetrics   struct {
				FollowersCount int `json:"followers_count"`
				FollowingCount int `json:"following_count"`
				TweetCount     int `json:"tweet_count"`
			} `json:"public_metrics"`
		} `json:"data"`
		Errors []struct {
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return UserProfile{}, err
	}
	if res.Data.ID == "" {
		detail := "账号不存在"
		if len(res.Errors) > 0 && res.Errors[0].Detail != "" {
			detail = res.Errors[0].Detail
		}
		return UserProfile{}, fmt.Errorf("twitter_official: %s", detail)
	}
	return UserProfile{
		ID:          res.Data.ID,
		Handle:      firstNonEmptyValue(res.Data.Username, screenName),
		DisplayName: firstNonEmptyValue(res.Data.Name, res.Data.Username),
		AvatarURL:   res.Data.ProfileImageURL,
		Bio:         res.Data.Description,
		Followers:   res.Data.PublicMetrics.FollowersCount,
		Following:   res.Data.PublicMetrics.FollowingCount,
		Posts:       res.Data.PublicMetrics.TweetCount,
		Verified:    res.Data.Verified,
	}, nil
}

// ---- 微博 Cookie ----

func (w *Weibo) Profile(ctx context.Context, id string) (UserProfile, error) {
	id = strings.TrimPrefix(strings.TrimSpace(id), "@")
	if id == "" {
		return UserProfile{}, fmt.Errorf("weibo: 空用户名")
	}
	if _, err := strconv.Atoi(id); err != nil {
		return UserProfile{}, fmt.Errorf("weibo: 详情识别需要数字 UID（请使用 https://weibo.com/u/<uid>）")
	}
	base := strings.TrimSuffix(strings.TrimSpace(w.BaseURL), "/")
	if base == "" {
		base = "https://m.weibo.cn"
	}
	u := fmt.Sprintf("%s/api/container/getIndex?type=uid&value=%s", base, url.QueryEscape(id))
	hc := *sharedClient
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return UserProfile{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15")
	req.Header.Set("Referer", base+"/u/"+id)
	if w.Cookie != "" {
		req.Header.Set("Cookie", w.Cookie)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return UserProfile{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == 432 {
		return UserProfile{}, fmt.Errorf("weibo: 被反爬拦截(%d)，请在上游配置登录 Cookie", resp.StatusCode)
	}
	var res struct {
		OK   int `json:"ok"`
		Data struct {
			UserInfo struct {
				ID              any    `json:"id"`
				ScreenName      string `json:"screen_name"`
				ProfileImageURL string `json:"profile_image_url"`
				Description     string `json:"description"`
				FollowersCount  any    `json:"followers_count"`
				FriendsCount    any    `json:"friends_count"`
				StatusesCount   any    `json:"statuses_count"`
				Verified        bool   `json:"verified"`
				VerifiedReason  string `json:"verified_reason"`
			} `json:"userInfo"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return UserProfile{}, err
	}
	info := res.Data.UserInfo
	if info.ScreenName == "" {
		return UserProfile{}, fmt.Errorf("weibo: 账号 %s 不存在或未返回资料", id)
	}
	return UserProfile{
		ID:           firstNonEmptyValue(anyString(info.ID), id),
		Handle:       info.ScreenName,
		DisplayName:  info.ScreenName,
		AvatarURL:    info.ProfileImageURL,
		Bio:          info.Description,
		Followers:    anyInt(info.FollowersCount),
		Following:    anyInt(info.FriendsCount),
		Posts:        anyInt(info.StatusesCount),
		Verified:     info.Verified,
		VerifiedNote: info.VerifiedReason,
	}, nil
}

// ---- RSS ----

func (r *RSSFeed) Profile(ctx context.Context, feed string) (UserProfile, error) {
	u, err := r.resolveURL(feed)
	if err != nil {
		return UserProfile{}, err
	}
	doc, err := fetchRSSDoc(ctx, u)
	if err != nil {
		return UserProfile{}, err
	}
	title := firstNonEmptyValue(strings.TrimSpace(doc.Channel.Title), strings.TrimSpace(doc.Title), u)
	count := len(doc.Channel.Items) + len(doc.Entries)
	return UserProfile{
		ID:           feed,
		Handle:       title,
		DisplayName:  title,
		Bio:          u,
		Posts:        count,
		CanonicalURL: u,
		Partial:      true, // RSS 没有头像/粉丝数
	}, nil
}

// ---- Truth Social ----

func (t *TruthSocial) Profile(ctx context.Context, acct string) (UserProfile, error) {
	base := strings.TrimSuffix(strings.TrimSpace(t.BaseURL), "/")
	if base == "" {
		base = "https://truthsocial.com"
	}
	acct = strings.TrimPrefix(strings.TrimSpace(acct), "@")
	if acct == "" {
		return UserProfile{}, fmt.Errorf("truth: 空用户名")
	}
	u := base + "/api/v1/accounts/lookup?acct=" + url.QueryEscape(acct)
	hc := *sharedClient
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return UserProfile{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return UserProfile{}, fmt.Errorf("truth: lookup 失败（可能需要反代 base_url）: %w", err)
	}
	defer resp.Body.Close()
	var acc struct {
		ID             string `json:"id"`
		Username       string `json:"username"`
		DisplayName    string `json:"display_name"`
		Avatar         string `json:"avatar"`
		Note           string `json:"note"`
		FollowersCount int    `json:"followers_count"`
		FollowingCount int    `json:"following_count"`
		StatusesCount  int    `json:"statuses_count"`
		Verified       bool   `json:"verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&acc); err != nil || acc.Username == "" {
		return UserProfile{}, fmt.Errorf("truth: 账号 %s 不存在", acct)
	}
	return UserProfile{
		ID:          acc.ID,
		Handle:      acc.Username,
		DisplayName: firstNonEmptyValue(acc.DisplayName, acc.Username),
		AvatarURL:   acc.Avatar,
		Bio:         acc.Note,
		Followers:   acc.FollowersCount,
		Following:   acc.FollowingCount,
		Posts:       acc.StatusesCount,
		Verified:    acc.Verified,
	}, nil
}

// ---- 共享 ----

func fetchRSSDoc(ctx context.Context, u string) (*rssDoc, error) {
	hc := *sharedClient
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; trp/1.0)")
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml, */*")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("rss: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	var doc rssDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("rss: 解析失败: %w", err)
	}
	return &doc, nil
}
