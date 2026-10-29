package source

import "testing"

func TestViewURL(t *testing.T) {
	cases := []struct {
		name string
		t    Target
		want string
		ok   bool
	}{
		{"x profile", Target{Platform: PlatformX, Kind: KindProfile, ID: "elonmusk"}, "https://x.com/elonmusk", true},
		{"x handle at", Target{Platform: PlatformX, Kind: KindProfile, ID: "@elonmusk"}, "https://x.com/elonmusk", true},
		{"x reserved", Target{Platform: PlatformX, Kind: KindProfile, ID: "home"}, "", false},
		{"weibo uid", Target{Platform: PlatformWeibo, Kind: KindProfile, ID: "123456"}, "https://weibo.com/u/123456", true},
		{"weibo name", Target{Platform: PlatformWeibo, Kind: KindProfile, ID: "人民日报"}, "https://weibo.com/n/%E4%BA%BA%E6%B0%91%E6%97%A5%E6%8A%A5", true},
		{"xhs", Target{Platform: PlatformXHS, Kind: KindProfile, ID: "abc123"}, "https://www.xiaohongshu.com/user/profile/abc123", true},
		{"bili space", Target{Platform: PlatformBilibili, Kind: KindProfile, ID: "2"}, "https://space.bilibili.com/2", true},
		{"bili live", Target{Platform: PlatformBilibili, Kind: KindLiveRoom, ID: "123"}, "https://live.bilibili.com/123", true},
		{"bili video", Target{Platform: PlatformBilibili, Kind: KindVideo, ID: "BV1xx411c7mD"}, "https://www.bilibili.com/video/BV1xx411c7mD", true},
		{"youtube handle", Target{Platform: PlatformYouTube, Kind: KindChannel, ID: "channel"}, "https://www.youtube.com/@channel", true},
		{"youtube channel id", Target{Platform: PlatformYouTube, Kind: KindChannel, ID: "UC1234567890123456789012"}, "https://www.youtube.com/channel/UC1234567890123456789012", true},
		{"youtube video", Target{Platform: PlatformYouTube, Kind: KindVideo, ID: "abcdEFGHijk"}, "https://www.youtube.com/watch?v=abcdEFGHijk", true},
		{"youtube live", Target{Platform: PlatformYouTube, Kind: KindLiveVideo, ID: "abcdEFGHijk"}, "https://www.youtube.com/live/abcdEFGHijk", true},
		{"twitch", Target{Platform: PlatformTwitch, Kind: KindChannel, ID: "somechannel"}, "https://www.twitch.tv/somechannel", true},
		{"truth", Target{Platform: PlatformTruth, Kind: KindProfile, ID: "@user"}, "https://truthsocial.com/@user", true},
		{"rss", Target{Platform: PlatformRSS, Kind: KindFeedURL, ID: "https://example.com/feed.xml"}, "https://example.com/feed.xml", true},
		{"rss bad scheme", Target{Platform: PlatformRSS, Kind: KindFeedURL, ID: "javascript:alert(1)"}, "", false},
		{"rss no host", Target{Platform: PlatformRSS, Kind: KindFeedURL, ID: "https:///feed.xml"}, "", false},
		{"empty", Target{Platform: PlatformX, Kind: KindProfile, ID: "  "}, "", false},
		{"wrong kind", Target{Platform: PlatformX, Kind: KindVideo, ID: "abc"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := c.t.ViewURL()
			if ok != c.ok || got != c.want {
				t.Fatalf("ViewURL() = (%q, %v), want (%q, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}
