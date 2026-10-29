package source

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var ytChannelIDRe = regexp.MustCompile(`^UC[A-Za-z0-9_-]{22}$`)

// ViewURL 由 Target 推导唯一可信的主页/内容地址。
// 只返回 http/https；无法安全推导时 ok=false。
//
// 前端不允许自己拼 URL：查看窗口 / 新标签都只接受这里的返回值。
func (t Target) ViewURL() (string, bool) {
	id := strings.TrimSpace(strings.TrimPrefix(t.ID, "@"))
	if id == "" {
		return "", false
	}
	switch t.Platform {
	case PlatformX:
		if t.Kind != KindProfile || reservedXPaths[strings.ToLower(id)] {
			return "", false
		}
		return pathURL("https://x.com/", id), true
	case PlatformWeibo:
		if t.Kind != KindProfile {
			return "", false
		}
		if isNumericID(id) {
			return pathURL("https://weibo.com/u/", id), true
		}
		return pathURL("https://weibo.com/n/", id), true
	case PlatformXHS:
		if t.Kind != KindProfile {
			return "", false
		}
		return pathURL("https://www.xiaohongshu.com/user/profile/", id), true
	case PlatformInstagram:
		if t.Kind != KindProfile {
			return "", false
		}
		return "https://www.instagram.com/" + url.PathEscape(id) + "/", true
	case PlatformBluesky:
		if t.Kind != KindProfile {
			return "", false
		}
		return "https://bsky.app/profile/" + url.PathEscape(id), true
	case PlatformBilibili:
		switch t.Kind {
		case KindProfile:
			return pathURL("https://space.bilibili.com/", id), true
		case KindLiveRoom:
			return pathURL("https://live.bilibili.com/", id), true
		case KindVideo:
			return pathURL("https://www.bilibili.com/video/", id), true
		}
	case PlatformYouTube:
		switch t.Kind {
		case KindChannel:
			if ytChannelIDRe.MatchString(id) {
				return pathURL("https://www.youtube.com/channel/", id), true
			}
			return pathURL("https://www.youtube.com/@", id), true
		case KindVideo:
			return "https://www.youtube.com/watch?v=" + url.QueryEscape(id), true
		case KindLiveVideo:
			return pathURL("https://www.youtube.com/live/", id), true
		}
	case PlatformTwitch:
		if t.Kind != KindChannel {
			return "", false
		}
		return pathURL("https://www.twitch.tv/", id), true
	case PlatformTruth:
		if t.Kind != KindProfile {
			return "", false
		}
		return pathURL("https://truthsocial.com/@", id), true
	case PlatformRSS:
		if t.Kind != KindFeedURL {
			return "", false
		}
		return httpURL(t.ID)
	}
	return "", false
}

func pathURL(prefix, id string) string {
	return prefix + url.PathEscape(id)
}

func httpURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	return u.String(), true
}

func isNumericID(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}
