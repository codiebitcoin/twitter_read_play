package twitter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"twitter_read_play/internal/model"
)

// Bluesky 走公开 AppView API（public.api.bsky.app），读公开时间线/资料不需要登录。
// 私密内容、私信不在本适配器范围内。
type Bluesky struct {
	BaseURL string // 默认 https://public.api.bsky.app
}

// defaultBlueskyAPI 公开 AppView；自建 PDS 也能作为 appview 代理。
const defaultBlueskyAPI = "https://public.api.bsky.app"

func (b *Bluesky) Name() string { return "bluesky" }

func (b *Bluesky) apiBase() string {
	base := strings.TrimSpace(b.BaseURL)
	if base == "" {
		base = defaultBlueskyAPI
	}
	return strings.TrimSuffix(base, "/")
}

func (b *Bluesky) FetchLatest(ctx context.Context, actor string, count int) ([]model.Tweet, error) {
	actor = strings.TrimSpace(strings.TrimPrefix(actor, "@"))
	if actor == "" {
		return nil, fmt.Errorf("bluesky: 账号为空")
	}
	if count <= 0 || count > 100 {
		count = 20
	}
	// posts_no_replies：只要原创/转发，不带回复，播报更干净。
	u := b.apiBase() + "/xrpc/app.bsky.feed.getAuthorFeed?actor=" + url.QueryEscape(actor) +
		"&limit=" + strconv.Itoa(count) + "&filter=posts_no_replies"
	var out struct {
		Feed []struct {
			Post struct {
				URI    string `json:"uri"`
				Author struct {
					Handle      string `json:"handle"`
					DisplayName string `json:"displayName"`
				} `json:"author"`
				Record struct {
					Text      string `json:"text"`
					CreatedAt string `json:"createdAt"`
				} `json:"record"`
				LikeCount   int             `json:"likeCount"`
				ReplyCount  int             `json:"replyCount"`
				RepostCount int             `json:"repostCount"`
				Embed       json.RawMessage `json:"embed"`
			} `json:"post"`
		} `json:"feed"`
	}
	if err := b.getJSON(ctx, u, &out); err != nil {
		return nil, fmt.Errorf("bluesky: 拉取失败: %w", err)
	}

	tweets := make([]model.Tweet, 0, len(out.Feed))
	for _, it := range out.Feed {
		p := it.Post
		handle := strings.TrimSpace(strings.TrimPrefix(p.Author.Handle, "@"))
		text := strings.TrimSpace(p.Record.Text)
		images := blueskyImages(p.Embed)
		if text == "" && len(images) == 0 {
			continue
		}
		user := firstNonEmptyString(handle, p.Author.DisplayName, "bluesky")
		tw := model.Tweet{
			ID:           p.URI,
			User:         user,
			Text:         model.CleanText(text, 500),
			CreatedAt:    p.Record.CreatedAt,
			Likes:        p.LikeCount,
			CommentCount: p.ReplyCount,
			ShareCount:   p.RepostCount,
			Images:       images,
		}
		if rkey := blueskyRKey(p.URI); handle != "" && rkey != "" {
			tw.Link = "https://bsky.app/profile/" + handle + "/post/" + rkey
		}
		tweets = append(tweets, tw)
	}
	return tweets, nil
}

// Profile 读取账号公开资料（app.bsky.actor.getProfile）。
func (b *Bluesky) Profile(ctx context.Context, actor string) (UserProfile, error) {
	actor = strings.TrimSpace(strings.TrimPrefix(actor, "@"))
	if actor == "" {
		return UserProfile{}, fmt.Errorf("bluesky: 账号为空")
	}
	u := b.apiBase() + "/xrpc/app.bsky.actor.getProfile?actor=" + url.QueryEscape(actor)
	var p struct {
		DID            string `json:"did"`
		Handle         string `json:"handle"`
		DisplayName    string `json:"displayName"`
		Description    string `json:"description"`
		Avatar         string `json:"avatar"`
		FollowersCount int    `json:"followersCount"`
		FollowsCount   int    `json:"followsCount"`
		PostsCount     int    `json:"postsCount"`
	}
	if err := b.getJSON(ctx, u, &p); err != nil {
		return UserProfile{}, fmt.Errorf("bluesky: 账号资料读取失败: %w", err)
	}
	if p.DID == "" && p.Handle == "" {
		return UserProfile{}, fmt.Errorf("bluesky: 账号不存在")
	}
	handle := strings.TrimPrefix(p.Handle, "@")
	canonical := ""
	if handle != "" {
		canonical = "https://bsky.app/profile/" + handle
	}
	return UserProfile{
		ID:           p.DID,
		Handle:       handle,
		DisplayName:  firstNonEmptyString(p.DisplayName, handle),
		AvatarURL:    p.Avatar,
		Bio:          p.Description,
		Followers:    p.FollowersCount,
		Following:    p.FollowsCount,
		Posts:        p.PostsCount,
		CanonicalURL: canonical,
	}, nil
}

func (b *Bluesky) getJSON(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := sharedClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d（确认账号是否存在/是否被限流）", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// blueskyRKey 取 at://did:plc:xxx/app.bsky.feed.post/<rkey> 里的 rkey。
func blueskyRKey(uri string) string {
	i := strings.LastIndex(uri, "/")
	if i < 0 || i == len(uri)-1 {
		return ""
	}
	return uri[i+1:]
}

// blueskyImages 从 embed 里抽图：images / recordWithMedia / external 卡片 / video 封面。
func blueskyImages(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	var out []string
	addImages := func(v any) {
		mm, _ := v.(map[string]any)
		arr, _ := mm["images"].([]any)
		for _, it := range arr {
			im, _ := it.(map[string]any)
			if u, _ := im["fullsize"].(string); u != "" {
				out = append(out, u)
			}
		}
	}
	addImages(m)
	if media, ok := m["media"]; ok {
		addImages(media)
	}
	if ext, ok := m["external"].(map[string]any); ok {
		if u, _ := ext["thumb"].(string); u != "" {
			out = append(out, u)
		}
	}
	if th, _ := m["thumbnail"].(string); th != "" {
		out = append(out, th)
	}
	return out
}
