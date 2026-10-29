package source

import "testing"

func TestParseFeed(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		platform Platform
		want     Target
		wantErr  bool
	}{
		{"x profile", "https://x.com/elonmusk", "", Target{Domain: DomainFeed, Platform: PlatformX, Kind: KindProfile, ID: "elonmusk"}, false},
		{"twitter status", "https://twitter.com/elonmusk/status/123", "", Target{Domain: DomainFeed, Platform: PlatformX, Kind: KindProfile, ID: "elonmusk"}, false},
		{"bare handle", "@elonmusk", Platform("twitter"), Target{Domain: DomainFeed, Platform: PlatformX, Kind: KindProfile, ID: "elonmusk"}, false},
		{"x reserved", "https://x.com/i/flow/login", "", Target{}, true},
		{"weibo u", "https://weibo.com/u/123456", "", Target{Domain: DomainFeed, Platform: PlatformWeibo, Kind: KindProfile, ID: "123456"}, false},
		{"weibo name", "https://m.weibo.cn/n/%E6%9D%A5%E5%8E%BB%E4%B9%8B%E9%97%B4", "", Target{Domain: DomainFeed, Platform: PlatformWeibo, Kind: KindProfile, ID: "来去之间"}, false},
		{"xhs profile", "https://www.xiaohongshu.com/user/profile/abc123", "", Target{Domain: DomainFeed, Platform: PlatformXHS, Kind: KindProfile, ID: "abc123"}, false},
		{"instagram profile", "https://www.instagram.com/ig_user/", "", Target{Domain: DomainFeed, Platform: PlatformInstagram, Kind: KindProfile, ID: "ig_user"}, false},
		{"instagram reserved", "https://www.instagram.com/p/ABC123/", "", Target{}, true},
		{"instagram bare", "@ig_user", Platform("ig"), Target{Domain: DomainFeed, Platform: PlatformInstagram, Kind: KindProfile, ID: "ig_user"}, false},
		{"bluesky url", "https://bsky.app/profile/alice.bsky.social", "", Target{Domain: DomainFeed, Platform: PlatformBluesky, Kind: KindProfile, ID: "alice.bsky.social"}, false},
		{"bluesky bare handle", "alice.bsky.social", Platform("bsky"), Target{Domain: DomainFeed, Platform: PlatformBluesky, Kind: KindProfile, ID: "alice.bsky.social"}, false},
		{"bluesky handle no hint", "alice.bsky.social", "", Target{Domain: DomainFeed, Platform: PlatformBluesky, Kind: KindProfile, ID: "alice.bsky.social"}, false},
		{"truth url", "https://truthsocial.com/@alice", "", Target{Domain: DomainFeed, Platform: PlatformTruth, Kind: KindProfile, ID: "alice"}, false},
		{"truth bare", "alice", Platform("truth"), Target{Domain: DomainFeed, Platform: PlatformTruth, Kind: KindProfile, ID: "alice"}, false},
		{"bili space", "https://space.bilibili.com/123", "", Target{Domain: DomainFeed, Platform: PlatformBilibili, Kind: KindProfile, ID: "123"}, false},
		{"yt handle", "https://www.youtube.com/@channel", "", Target{Domain: DomainFeed, Platform: PlatformYouTube, Kind: KindChannel, ID: "channel"}, false},
		{"yt channel", "https://www.youtube.com/channel/UCabc", "", Target{Domain: DomainFeed, Platform: PlatformYouTube, Kind: KindChannel, ID: "UCabc"}, false},
		{"yt watch", "https://www.youtube.com/watch?v=abcdEFGHijk", "", Target{Domain: DomainFeed, Platform: PlatformYouTube, Kind: KindVideo, ID: "abcdEFGHijk"}, false},
		{"yt short", "https://youtu.be/abcdEFGHijk", "", Target{Domain: DomainFeed, Platform: PlatformYouTube, Kind: KindVideo, ID: "abcdEFGHijk"}, false},
		{"rss", "https://example.com/feed.xml", "", Target{Domain: DomainFeed, Platform: PlatformRSS, Kind: KindFeedURL, ID: "https://example.com/feed.xml"}, false},
		{"rss unknown host", "https://rsshub.app/twitter/user/elonmusk", "", Target{Domain: DomainFeed, Platform: PlatformRSS, Kind: KindFeedURL, ID: "https://rsshub.app/twitter/user/elonmusk"}, false},
		{"live url in feed", "https://live.bilibili.com/123", "", Target{}, true},
		{"empty", "", "", Target{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseFeed(tc.raw, tc.platform)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Domain != tc.want.Domain || got.Platform != tc.want.Platform || got.Kind != tc.want.Kind || got.ID != tc.want.ID {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseLive(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		platform Platform
		want     Target
		wantErr  bool
	}{
		{"yt live", "https://www.youtube.com/live/abcdEFGHijk", "", Target{Domain: DomainLive, Platform: PlatformYouTube, Kind: KindLiveVideo, ID: "abcdEFGHijk"}, false},
		{"yt watch", "https://www.youtube.com/watch?v=abcdEFGHijk", "", Target{Domain: DomainLive, Platform: PlatformYouTube, Kind: KindLiveVideo, ID: "abcdEFGHijk"}, false},
		{"yt short", "https://youtu.be/abcdEFGHijk", "", Target{Domain: DomainLive, Platform: PlatformYouTube, Kind: KindLiveVideo, ID: "abcdEFGHijk"}, false},
		{"yt bare id", "abcdEFGHijk", PlatformYouTube, Target{Domain: DomainLive, Platform: PlatformYouTube, Kind: KindLiveVideo, ID: "abcdEFGHijk"}, false},
		{"yt channel live", "https://www.youtube.com/@channel", "", Target{Domain: DomainLive, Platform: PlatformYouTube, Kind: KindChannel, ID: "channel"}, false},
		{"bili room", "https://live.bilibili.com/123", "", Target{Domain: DomainLive, Platform: PlatformBilibili, Kind: KindLiveRoom, ID: "123"}, false},
		{"bili bare room", "123", PlatformBilibili, Target{Domain: DomainLive, Platform: PlatformBilibili, Kind: KindLiveRoom, ID: "123"}, false},
		{"twitch url", "https://www.twitch.tv/somechannel", "", Target{Domain: DomainLive, Platform: PlatformTwitch, Kind: KindChannel, ID: "somechannel"}, false},
		{"twitch bare", "somechannel", PlatformTwitch, Target{Domain: DomainLive, Platform: PlatformTwitch, Kind: KindChannel, ID: "somechannel"}, false},
		{"x live", "https://x.com/elonmusk", "", Target{}, true},
		{"rss live", "https://example.com/feed.xml", "", Target{}, true},
		{"bili video live", "https://www.bilibili.com/video/BV1xx411c7mD", "", Target{}, true},
		{"twitch videos", "https://www.twitch.tv/videos/123", "", Target{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLive(tc.raw, tc.platform)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Domain != tc.want.Domain || got.Platform != tc.want.Platform || got.Kind != tc.want.Kind || got.ID != tc.want.ID {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseURLWithoutScheme(t *testing.T) {
	got, err := ParseFeed("x.com/elonmusk", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Platform != PlatformX || got.ID != "elonmusk" {
		t.Fatalf("got %+v", got)
	}
}
