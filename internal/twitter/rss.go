package twitter

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"twitter_read_play/internal/model"
)

// RSSFeed 标准 RSS/Atom 源（推荐自建 RSSHub）。
// 地址解析（类似 Tweet：上游是适配器，角色 username 是具体 feed）：
//  1. username 是完整 URL → 直接用
//  2. 上游 BaseURL + username 路径（RSSHub：base=https://rsshub.app，username=twitter/user/elonmusk）
//  3. 上游 BaseURL / Extra 本身就是完整 feed
type RSSFeed struct {
	BaseURL     string
	FallbackURL string // Extra 备用地址
}

func (r *RSSFeed) Name() string { return "rss" }

type rssDoc struct {
	Channel struct {
		Title string    `xml:"title"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
	Title   string     `xml:"title"`
	Entries []rssEntry `xml:"entry"`
}
type rssItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	Desc    string `xml:"description"`
	GUID    string `xml:"guid"`
	PubDate string `xml:"pubDate"`
	Updated string `xml:"updated"`
	Content string `xml:"http://purl.org/rss/1.0/modules/content/ encoded"`
}
type rssEntry struct {
	Title string `xml:"title"`
	Link  []struct {
		Href string `xml:"href,attr"`
	} `xml:"link"`
	Content   string `xml:"content"`
	Summary   string `xml:"summary"`
	ID        string `xml:"id"`
	Updated   string `xml:"updated"`
	Published string `xml:"published"`
}

var tagRe = regexp.MustCompile(`<[^>]+>`)

// resolveURL 角色 username（feed）+ 上游 BaseURL → 最终请求地址。
func (r *RSSFeed) resolveURL(feed string) (string, error) {
	feed = strings.TrimSpace(feed)
	base := strings.TrimSpace(r.BaseURL)
	if base == "" {
		base = strings.TrimSpace(r.FallbackURL)
	}
	if strings.Contains(feed, "://") {
		return feed, nil
	}
	if base == "" {
		if feed == "" {
			return "", fmt.Errorf("rss: 未配置 feed 地址")
		}
		return "", fmt.Errorf("rss: 无效地址 %q（需要完整 URL，或在上游填写 RSSHub base_url）", feed)
	}
	if !strings.Contains(base, "://") {
		return "", fmt.Errorf("rss: 无效地址 %q", base)
	}
	if feed == "" {
		return base, nil
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(feed, "/"), nil
}

func (r *RSSFeed) FetchLatest(ctx context.Context, feed string, count int) ([]model.Tweet, error) {
	target, err := r.resolveURL(feed)
	if err != nil {
		return nil, err
	}
	doc, err := fetchRSSDoc(ctx, target)
	if err != nil {
		return nil, err
	}
	feedUser := firstNonEmpty(strings.TrimSpace(doc.Channel.Title), strings.TrimSpace(doc.Title), "rss")
	type entry struct{ id, text, at, link string }
	list := make([]entry, 0)
	for _, it := range doc.Channel.Items {
		list = append(list, entry{
			id:   firstNonEmpty(it.GUID, it.Link, it.Title),
			text: firstNonEmpty(it.Content, it.Desc, it.Title),
			at:   firstNonEmpty(it.PubDate, it.Updated),
			link: it.Link,
		})
	}
	for _, e := range doc.Entries {
		link := ""
		if len(e.Link) > 0 {
			link = e.Link[0].Href
		}
		list = append(list, entry{
			id:   firstNonEmpty(e.ID, link, e.Title),
			text: firstNonEmpty(e.Content, e.Summary, e.Title),
			at:   firstNonEmpty(e.Updated, e.Published),
			link: link,
		})
	}
	if count <= 0 || count > 50 {
		count = 20
	}
	out := make([]model.Tweet, 0, count)
	for i, e := range list {
		if i >= count {
			break
		}
		id := strings.TrimSpace(e.id)
		if id == "" {
			id = fmt.Sprintf("rss-%d", i)
		}
		text := tagRe.ReplaceAllString(e.text, "")
		text = strings.Join(strings.Fields(text), " ")
		out = append(out, model.Tweet{
			ID:        id,
			User:      feedUser,
			Text:      model.CleanText(text, 300),
			CreatedAt: e.at,
			Link:      e.link,
		})
	}
	return out, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
