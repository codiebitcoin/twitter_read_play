package source

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var ytVideoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

var reservedXPaths = map[string]bool{
	"i": true, "home": true, "explore": true, "search": true, "settings": true,
	"login": true, "logout": true, "signup": true, "messages": true,
	"notifications": true, "compose": true, "tos": true, "privacy": true,
	"intent": true, "share": true, "hashtag": true,
}

// Parse 解析 Role 输入为 Target。platform 可为空（URL 自带平台时）。
func Parse(domain Domain, raw string, platform Platform) (Target, error) {
	return parseTarget(domain, raw, platform)
}

// ParseFeed 解析 Feed 目标。
func ParseFeed(raw string, platform Platform) (Target, error) {
	return parseTarget(DomainFeed, raw, platform)
}

// ParseLive 解析 Live 目标。
func ParseLive(raw string, platform Platform) (Target, error) {
	return parseTarget(DomainLive, raw, platform)
}

func parseTarget(domain Domain, raw string, platform Platform) (Target, error) {
	raw = strings.TrimSpace(raw)
	p := NormalizePlatform(string(platform))
	if raw == "" {
		return Target{}, unsupported(domain, raw)
	}
	// Bluesky 的 handle 形如 alice.bsky.social，会被 looksLikeURL 当成域名。
	// 显式选了平台时按 handle 处理；真正的 URL 仍以 http(s):// 开头。
	if p == PlatformBluesky && domain != DomainLive && !strings.Contains(raw, "/") &&
		!strings.HasPrefix(strings.ToLower(raw), "http") {
		if id := strings.TrimPrefix(raw, "@"); id != "" {
			return finalize(domain, Target{Platform: PlatformBluesky, Kind: KindProfile, Raw: raw, ID: id})
		}
	}
	if looksLikeURL(raw) {
		return parseURL(domain, raw, p)
	}
	if strings.HasPrefix(raw, "@") {
		handle := strings.TrimSpace(strings.TrimPrefix(raw, "@"))
		if handle == "" {
			return Target{}, unsupported(domain, raw)
		}
		if p == "" {
			return Target{}, unsupported(domain, raw)
		}
		if domain == DomainLive {
			switch p {
			case PlatformYouTube, PlatformTwitch:
				return finalize(domain, Target{Platform: p, Kind: KindChannel, Raw: raw, ID: handle})
			default:
				return Target{}, unsupported(domain, raw)
			}
		}
		return finalize(domain, Target{Platform: p, Kind: KindProfile, Raw: raw, ID: handle})
	}

	switch p {
	case PlatformYouTube:
		if ytVideoIDRe.MatchString(raw) {
			kind := KindVideo
			if domain == DomainLive {
				kind = KindLiveVideo
			}
			return finalize(domain, Target{Platform: p, Kind: kind, Raw: raw, ID: raw})
		}
		return finalize(domain, Target{Platform: p, Kind: KindChannel, Raw: raw, ID: strings.TrimPrefix(raw, "@")})
	case PlatformTwitch:
		if domain == DomainFeed {
			return Target{}, unsupported(domain, raw)
		}
		return finalize(domain, Target{Platform: p, Kind: KindChannel, Raw: raw, ID: raw})
	case PlatformBilibili:
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if domain == DomainLive {
				return finalize(domain, Target{Platform: p, Kind: KindLiveRoom, Raw: raw, ID: raw})
			}
			return finalize(domain, Target{Platform: p, Kind: KindProfile, Raw: raw, ID: raw})
		}
		return Target{}, unsupported(domain, raw)
	case PlatformRSS:
		if domain != DomainFeed {
			return Target{}, unsupported(domain, raw)
		}
		return finalize(domain, Target{Platform: p, Kind: KindFeedURL, Raw: raw, ID: raw})
	case PlatformTruth:
		if domain == DomainLive {
			return Target{}, unsupported(domain, raw)
		}
		return finalize(domain, Target{Platform: p, Kind: KindProfile, Raw: raw, ID: raw})
	case PlatformX, PlatformWeibo, PlatformXHS, PlatformInstagram, PlatformBluesky:
		if domain == DomainLive {
			return Target{}, unsupported(domain, raw)
		}
		return finalize(domain, Target{Platform: p, Kind: KindProfile, Raw: raw, ID: raw})
	}
	return Target{}, unsupported(domain, raw)
}

// LooksLikeURL 供 API/UI 判断输入是 URL 还是裸 handle。
func LooksLikeURL(raw string) bool { return looksLikeURL(raw) }

func looksLikeURL(raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return true
	}
	if strings.HasPrefix(s, "www.") {
		return true
	}
	first := s
	if i := strings.IndexAny(first, "/?#"); i >= 0 {
		first = first[:i]
	}
	return strings.Contains(first, ".") && !strings.Contains(first, " ")
}

func parseURL(domain Domain, raw string, hinted Platform) (Target, error) {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return Target{}, unsupported(domain, raw)
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	host = strings.TrimPrefix(host, "mobile.")

	switch {
	case host == "x.com" || host == "twitter.com":
		return parseXURL(domain, raw, u)
	case host == "weibo.com" || host == "weibo.cn":
		return parseWeiboURL(domain, raw, u)
	case host == "xiaohongshu.com" || host == "xhslink.com":
		return parseXHSURL(domain, raw, u)
	case host == "instagram.com" || host == "instagr.am":
		return parseInstagramURL(domain, raw, u)
	case host == "bsky.app":
		return parseBlueskyURL(domain, raw, u)
	case host == "bilibili.com" || strings.HasSuffix(host, ".bilibili.com") || host == "b23.tv":
		return parseBilibiliURL(domain, raw, u)
	case host == "youtube.com" || host == "youtu.be" || host == "youtube-nocookie.com":
		return parseYouTubeURL(domain, raw, u)
	case host == "twitch.tv" || strings.HasSuffix(host, ".twitch.tv"):
		return parseTwitchURL(domain, raw, u)
	case host == "truthsocial.com":
		return parseTruthURL(domain, raw, u)
	}

	// 没选平台时，alice.bsky.social 这种裸 handle 也能认出来。
	if domain == DomainFeed && strings.HasSuffix(host, ".bsky.social") && len(pathSegments(u.Path)) == 0 {
		return finalize(domain, Target{Platform: PlatformBluesky, Kind: KindProfile, Raw: raw, ID: host})
	}

	// 未知 host：Feed 域按 RSS/Atom 处理（由 adapter 实际测试校验）。
	if domain == DomainFeed && (hinted == PlatformRSS || hinted == "" || u.Scheme == "http" || u.Scheme == "https") {
		return finalize(domain, Target{Platform: PlatformRSS, Kind: KindFeedURL, Raw: raw, ID: raw})
	}
	return Target{}, unsupported(domain, raw)
}

func parseXURL(domain Domain, raw string, u *url.URL) (Target, error) {
	if domain == DomainLive {
		return Target{}, unsupported(domain, raw)
	}
	seg := pathSegments(u.Path)
	if len(seg) == 0 {
		return Target{}, unsupported(domain, raw)
	}
	if reservedXPaths[strings.ToLower(seg[0])] {
		return Target{}, unsupported(domain, raw)
	}
	return finalize(domain, Target{Platform: PlatformX, Kind: KindProfile, Raw: raw, ID: seg[0]})
}

func parseWeiboURL(domain Domain, raw string, u *url.URL) (Target, error) {
	if domain == DomainLive {
		return Target{}, unsupported(domain, raw)
	}
	seg := pathSegments(u.Path)
	if len(seg) == 0 {
		return Target{}, unsupported(domain, raw)
	}
	id := ""
	switch strings.ToLower(seg[0]) {
	case "u", "n", "profile":
		if len(seg) >= 2 {
			id = seg[1]
		}
	case "status", "detail", "ajax", "api", "p":
		return Target{}, unsupported(domain, raw)
	default:
		id = seg[0]
	}
	if id == "" {
		return Target{}, unsupported(domain, raw)
	}
	return finalize(domain, Target{Platform: PlatformWeibo, Kind: KindProfile, Raw: raw, ID: id})
}

func parseXHSURL(domain Domain, raw string, u *url.URL) (Target, error) {
	if domain == DomainLive {
		return Target{}, unsupported(domain, raw)
	}
	if strings.EqualFold(u.Hostname(), "xhslink.com") {
		// 短链必须在页面里展开；解析层不做网络请求。
		return Target{}, unsupported(domain, raw)
	}
	seg := pathSegments(u.Path)
	id := ""
	for i := 0; i < len(seg); i++ {
		if strings.EqualFold(seg[i], "profile") && i+1 < len(seg) {
			id = seg[i+1]
			break
		}
	}
	if id == "" {
		return Target{}, unsupported(domain, raw)
	}
	return finalize(domain, Target{Platform: PlatformXHS, Kind: KindProfile, Raw: raw, ID: id})
}

var reservedIGPaths = map[string]bool{
	"p": true, "reel": true, "reels": true, "explore": true, "accounts": true,
	"stories": true, "direct": true, "about": true, "developer": true, "legal": true,
	"api": true, "graphql": true, "web": true, "challenge": true, "emails": true,
	"session": true, "privacy": true, "terms": true, "tv": true, "topics": true,
}

func parseInstagramURL(domain Domain, raw string, u *url.URL) (Target, error) {
	if domain == DomainLive {
		return Target{}, unsupported(domain, raw)
	}
	seg := pathSegments(u.Path)
	if len(seg) == 0 {
		return Target{}, unsupported(domain, raw)
	}
	id := strings.TrimPrefix(seg[0], "@")
	if id == "" || reservedIGPaths[strings.ToLower(id)] {
		return Target{}, unsupported(domain, raw)
	}
	return finalize(domain, Target{Platform: PlatformInstagram, Kind: KindProfile, Raw: raw, ID: id})
}

func parseBlueskyURL(domain Domain, raw string, u *url.URL) (Target, error) {
	if domain == DomainLive {
		return Target{}, unsupported(domain, raw)
	}
	seg := pathSegments(u.Path)
	if len(seg) < 2 || !strings.EqualFold(seg[0], "profile") {
		return Target{}, unsupported(domain, raw)
	}
	id := strings.TrimPrefix(seg[1], "@")
	if id == "" {
		return Target{}, unsupported(domain, raw)
	}
	return finalize(domain, Target{Platform: PlatformBluesky, Kind: KindProfile, Raw: raw, ID: id})
}

func parseBilibiliURL(domain Domain, raw string, u *url.URL) (Target, error) {
	host := strings.ToLower(u.Hostname())
	seg := pathSegments(u.Path)

	if strings.HasPrefix(host, "live.") {
		if domain != DomainLive {
			return Target{}, unsupported(domain, raw)
		}
		room := firstNumeric(seg)
		if room == "" {
			return Target{}, unsupported(domain, raw)
		}
		return finalize(domain, Target{Platform: PlatformBilibili, Kind: KindLiveRoom, Raw: raw, ID: room})
	}

	if strings.HasPrefix(host, "space.") {
		if domain == DomainLive {
			return Target{}, unsupported(domain, raw)
		}
		mid := firstNumeric(seg)
		if mid == "" {
			return Target{}, unsupported(domain, raw)
		}
		return finalize(domain, Target{Platform: PlatformBilibili, Kind: KindProfile, Raw: raw, ID: mid})
	}

	if id := biliVideoID(seg); id != "" {
		if domain == DomainLive {
			return Target{}, unsupported(domain, raw)
		}
		return finalize(domain, Target{Platform: PlatformBilibili, Kind: KindVideo, Raw: raw, ID: id})
	}
	return Target{}, unsupported(domain, raw)
}

func parseYouTubeURL(domain Domain, raw string, u *url.URL) (Target, error) {
	host := strings.ToLower(u.Hostname())
	if host == "youtu.be" {
		seg := pathSegments(u.Path)
		if len(seg) == 0 || !ytVideoIDRe.MatchString(seg[0]) {
			return Target{}, unsupported(domain, raw)
		}
		kind := KindVideo
		if domain == DomainLive {
			kind = KindLiveVideo
		}
		return finalize(domain, Target{Platform: PlatformYouTube, Kind: kind, Raw: raw, ID: seg[0]})
	}

	seg := pathSegments(u.Path)
	if len(seg) == 0 {
		return Target{}, unsupported(domain, raw)
	}
	switch strings.ToLower(seg[0]) {
	case "watch":
		id := strings.TrimSpace(u.Query().Get("v"))
		if !ytVideoIDRe.MatchString(id) {
			return Target{}, unsupported(domain, raw)
		}
		kind := KindVideo
		if domain == DomainLive {
			kind = KindLiveVideo
		}
		return finalize(domain, Target{Platform: PlatformYouTube, Kind: kind, Raw: raw, ID: id})
	case "live", "embed", "shorts":
		if len(seg) < 2 || !ytVideoIDRe.MatchString(seg[1]) {
			return Target{}, unsupported(domain, raw)
		}
		kind := KindVideo
		if domain == DomainLive || strings.EqualFold(seg[0], "live") {
			kind = KindLiveVideo
		}
		return finalize(domain, Target{Platform: PlatformYouTube, Kind: kind, Raw: raw, ID: seg[1]})
	case "channel", "c", "user":
		if len(seg) < 2 {
			return Target{}, unsupported(domain, raw)
		}
		return finalize(domain, Target{Platform: PlatformYouTube, Kind: KindChannel, Raw: raw, ID: seg[1]})
	default:
		if strings.HasPrefix(seg[0], "@") {
			return finalize(domain, Target{
				Platform: PlatformYouTube, Kind: KindChannel, Raw: raw,
				ID: strings.TrimPrefix(seg[0], "@"),
			})
		}
	}
	return Target{}, unsupported(domain, raw)
}

func parseTwitchURL(domain Domain, raw string, u *url.URL) (Target, error) {
	if domain == DomainFeed {
		return Target{}, unsupported(domain, raw)
	}
	seg := pathSegments(u.Path)
	if len(seg) == 0 {
		return Target{}, unsupported(domain, raw)
	}
	switch strings.ToLower(seg[0]) {
	case "videos", "clip", "directory", "downloads", "settings", "subscriptions":
		return Target{}, unsupported(domain, raw)
	}
	return finalize(domain, Target{Platform: PlatformTwitch, Kind: KindChannel, Raw: raw, ID: seg[0]})
}

func parseTruthURL(domain Domain, raw string, u *url.URL) (Target, error) {
	if domain == DomainLive {
		return Target{}, unsupported(domain, raw)
	}
	seg := pathSegments(u.Path)
	if len(seg) == 0 {
		return Target{}, unsupported(domain, raw)
	}
	id := strings.TrimPrefix(seg[0], "@")
	if id == "" {
		return Target{}, unsupported(domain, raw)
	}
	return finalize(domain, Target{Platform: PlatformTruth, Kind: KindProfile, Raw: raw, ID: id})
}

func finalize(domain Domain, t Target) (Target, error) {
	t.Domain = domain
	if t.ID == "" {
		t.ID = t.Raw
	}
	if t.Display == "" {
		t.Display = defaultDisplay(t)
	}
	if !kindAllowed(domain, t.Kind) {
		return Target{}, unsupported(domain, t.Raw)
	}
	return t, nil
}

func kindAllowed(domain Domain, k TargetKind) bool {
	switch domain {
	case DomainFeed:
		return k == KindProfile || k == KindFeedURL || k == KindChannel || k == KindVideo
	case DomainLive:
		return k == KindLiveVideo || k == KindLiveRoom || k == KindChannel
	}
	return false
}

func defaultDisplay(t Target) string {
	switch t.Kind {
	case KindProfile:
		return "@" + t.ID
	case KindFeedURL, KindVideo:
		return t.ID
	case KindChannel:
		return string(t.Platform) + " 频道 · " + t.ID
	case KindLiveRoom:
		return "Bilibili 直播 · " + t.ID
	case KindLiveVideo:
		return "YouTube 直播 · " + t.ID
	}
	return t.ID
}

func unsupported(domain Domain, raw string) error {
	return &UnsupportedTargetError{
		Domain: domain, Raw: raw,
		Supported: supportedList(domain),
		Examples:  examples(domain),
	}
}

func supportedList(domain Domain) []string {
	if domain == DomainLive {
		return []string{
			"YouTube 直播/视频 URL（youtube.com/live/…、watch?v=…、youtu.be/…）",
			"Bilibili 直播间 URL 或房间号（live.bilibili.com/123）",
			"Twitch 频道 URL 或频道名（twitch.tv/name）",
		}
	}
	return []string{
		"X / 微博 / 小红书 / Instagram / Bilibili 空间主页 URL",
		"YouTube 频道 URL（@handle、/channel/…）",
		"RSS/Atom URL 或 RSSHub 路径",
		"@handle（需先选择平台）",
	}
}

func examples(domain Domain) []string {
	if domain == DomainLive {
		return []string{
			"https://www.youtube.com/live/abcdEFGHijk",
			"https://live.bilibili.com/123",
			"https://www.twitch.tv/somechannel",
		}
	}
	return []string{
		"https://x.com/elonmusk",
		"https://weibo.com/u/1234567890",
		"https://www.youtube.com/@channel",
		"https://example.com/feed.xml",
	}
}

func pathSegments(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	raw := strings.Split(p, "/")
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if dec, err := url.PathUnescape(s); err == nil {
			s = dec
		}
		out = append(out, s)
	}
	return out
}

func firstNumeric(seg []string) string {
	for _, s := range seg {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return s
		}
	}
	return ""
}

func biliVideoID(seg []string) string {
	for _, s := range seg {
		u := strings.ToUpper(s)
		if strings.HasPrefix(u, "BV") && len(s) == 12 {
			return s
		}
		if strings.HasPrefix(strings.ToLower(s), "av") {
			if _, err := strconv.Atoi(s[2:]); err == nil {
				return s
			}
		}
	}
	return ""
}
