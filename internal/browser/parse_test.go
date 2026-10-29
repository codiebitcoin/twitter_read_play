package browser

import (
	"encoding/json"
	"testing"
)

func TestParseX(t *testing.T) {
	raw := `{
	  "data": {"user": {"result": {"timeline": {"timeline": {"instructions": [
	    {"entries": [{"content": {
	      "entryType": "TimelineTimelineItem",
	      "itemContent": {
	        "itemType": "TimelineTweet",
	        "tweet_results": {"result": {
	          "legacy": {"id_str": "123", "full_text": "hello world", "favorite_count": 42, "created_at": "now",
	            "entities": {"media": [{"media_url_https": "https://img/1.jpg"}]}},
	          "core": {"user_results": {"result": {"core": {"screen_name": "alice"}}}}
	        }}
	      }
	    }}]}
	  ]}}}}}
	}`
	got := ParseX([]Captured{{JSON: json.RawMessage(raw)}})
	if len(got) != 1 {
		t.Fatalf("want 1 tweet, got %d", len(got))
	}
	tw := got[0]
	if tw.ID != "123" || tw.User != "alice" || tw.Text != "hello world" || tw.Likes != 42 {
		t.Fatalf("unexpected tweet: %+v", tw)
	}
	if len(tw.Images) != 1 || tw.Images[0] != "https://img/1.jpg" {
		t.Fatalf("unexpected images: %+v", tw.Images)
	}
}

func TestParseWeibo(t *testing.T) {
	raw := []byte(`{"data":{"cards":[{"card_type":9,"mblog":{
	  "id":5012345678901234,
	  "text":"hello <a href=\"https://t.cn/x\">link</a>&nbsp;&amp; more",
	  "user":{"screen_name":"weibo_user"},
	  "attitudes_count":3,"comments_count":2,"reposts_count":1,
	  "pic_infos":{"p1":{"large":{"url":"https://img/w.jpg"}}}
	}}]}}`)
	got := ParseWeibo(raw)
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	tw := got[0]
	if tw.ID != "5012345678901234" || tw.User != "weibo_user" {
		t.Fatalf("unexpected: %+v", tw)
	}
	if tw.Text != "hello link & more" {
		t.Fatalf("strip html: %q", tw.Text)
	}
	if tw.Likes != 3 || tw.CommentCount != 2 || tw.ShareCount != 1 {
		t.Fatalf("counts: %+v", tw)
	}
	if len(tw.Images) != 1 {
		t.Fatalf("images: %+v", tw.Images)
	}
}

func TestParseXHS(t *testing.T) {
	raw := []byte(`{"data":{"notes":[{
	  "note_id":"n1","display_title":"title","desc":"desc",
	  "user":{"nickname":"xhs_user"},
	  "interact_info":{"liked_count":"1.2万","comment_count":"12","collected_count":"3","share_count":"4"},
	  "cover":{"url_default":"https://img/cover.jpg"},
	  "time":1760000000000
	}]}}`)
	got := ParseXHS(raw)
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	tw := got[0]
	if tw.ID != "n1" || tw.User != "xhs_user" || tw.Text != "title\ndesc" {
		t.Fatalf("unexpected: %+v", tw)
	}
	if tw.Likes != 12000 || tw.CommentCount != 12 || tw.CollectCount != 3 || tw.ShareCount != 4 {
		t.Fatalf("counts: %+v", tw)
	}
	if len(tw.Images) != 1 {
		t.Fatalf("images: %+v", tw.Images)
	}
}

func TestParseYouTubeChat(t *testing.T) {
	raw := `{"continuationContents":{"liveChatContinuation":{"actions":[
	  {"addChatItemAction":{"item":{"liveChatTextMessageRenderer":{
	    "id":"m1","authorName":{"simpleText":"yt_user"},
	    "message":{"runs":[{"text":"hi "},{"text":"there"}]},
	    "timestampUsec":"1760000000000000"
	  }}}},
	  {"addChatItemAction":{"item":{"liveChatPaidMessageRenderer":{
	    "id":"m2","authorName":{"simpleText":"sc_user"},
	    "message":{"runs":[{"text":"great"}]},
	    "purchaseAmountText":{"simpleText":"$5.00"}
	  }}}}
	]}}}`
	got := ParseYouTubeChat([]Captured{{JSON: json.RawMessage(raw)}})
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].ID != "m1" || got[0].User != "yt_user" || got[0].Text != "hi there" || got[0].Kind != "text" {
		t.Fatalf("unexpected m1: %+v", got[0])
	}
	if got[1].ID != "m2" || got[1].Kind != "superchat" || got[1].Amount != "$5.00" {
		t.Fatalf("unexpected m2: %+v", got[1])
	}
}

func TestParseInstagramProfile(t *testing.T) {
	// web_profile_info：data.user.edge_owner_to_timeline_media
	raw := `{"data":{"user":{"id":"42","username":"ig_user","full_name":"IG User",
	  "edge_followed_by":{"count":1234},"edge_follow":{"count":12},
	  "edge_owner_to_timeline_media":{"count":2,"edges":[
	    {"node":{"id":"a1","shortcode":"AAA","taken_at_timestamp":1700000000,
	      "edge_liked_by":{"count":9},"edge_media_to_comment":{"count":2},
	      "display_url":"https://img/a1.jpg",
	      "edge_media_to_caption":{"edges":[{"node":{"text":"first post"}}]},
	      "owner":{"username":"ig_user"}}},
	    {"node":{"id":"a2","shortcode":"BBB","taken_at_timestamp":1700000100,
	      "edge_liked_by":{"count":1},
	      "edge_media_to_caption":{"edges":[{"node":{"text":"second post"}}]},
	      "owner":{"username":"ig_user"}}}
	  ]}}}}`
	got := ParseInstagram([]byte(raw))
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].ID != "a1" || got[0].User != "ig_user" || got[0].Text != "first post" || got[0].Likes != 9 {
		t.Fatalf("unexpected post: %+v", got[0])
	}
	if got[0].Link != "https://www.instagram.com/p/AAA/" {
		t.Fatalf("link: %q", got[0].Link)
	}
	if len(got[0].Images) != 1 || got[0].Images[0] != "https://img/a1.jpg" {
		t.Fatalf("images: %+v", got[0].Images)
	}
}

func TestParseInstagramREST(t *testing.T) {
	// /api/v1/feed/user/…：items[] + caption.text + image_versions2
	raw := `{"items":[{"id":"b1","code":"CCC","taken_at":1700000000,
	  "caption":{"text":"rest post"},"like_count":7,"comment_count":1,
	  "user":{"username":"rest_user"},
	  "image_versions2":{"candidates":[{"url":"https://img/c1.jpg"},{"url":"https://img/c2.jpg"}]}}]}`
	got := ParseInstagram([]byte(raw))
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	if got[0].ID != "b1" || got[0].Text != "rest post" || got[0].Likes != 7 || got[0].CommentCount != 1 {
		t.Fatalf("unexpected: %+v", got[0])
	}
	if len(got[0].Images) != 1 || got[0].Images[0] != "https://img/c1.jpg" {
		t.Fatalf("images: %+v", got[0].Images)
	}
}

func TestParseInstagramUserJSON(t *testing.T) {
	raw := []byte(`{"data":{"user":{"id":"42","username":"ig_user","full_name":"IG User",
	  "biography":"hello","profile_pic_url_hd":"https://img/av.jpg",
	  "edge_followed_by":{"count":1234},"edge_follow":{"count":12},
	  "edge_owner_to_timeline_media":{"count":56},"is_verified":true}}}`)
	p, ok := ParseInstagramUserJSON(raw)
	if !ok {
		t.Fatal("want profile")
	}
	if p.Platform != "instagram" || p.ID != "42" || p.DisplayName != "IG User" || p.Followers != 1234 || p.Posts != 56 || !p.Verified {
		t.Fatalf("unexpected: %+v", p)
	}
}

func TestParseTruthStatuses(t *testing.T) {
	raw := `[
	  {"id":"111","created_at":"2025-01-01T00:00:00Z","content":"<p>hello &amp; world</p>",
	   "url":"https://truthsocial.com/@alice/111",
	   "favourites_count":7,"replies_count":2,"reblogs_count":3,
	   "account":{"username":"alice"},
	   "media_attachments":[{"url":"https://img/t1.jpg"}]},
	  {"id":"222","created_at":"2025-01-02T00:00:00Z","content":"",
	   "account":{"username":"bob"},
	   "reblog":{"id":"333","created_at":"2025-01-02T00:00:00Z","content":"<p>reposted</p>",
	     "url":"https://truthsocial.com/@carol/333","account":{"username":"carol"}}}
	]`
	got := ParseTruth([]byte(raw))
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].ID != "111" || got[0].User != "alice" || got[0].Text != "hello & world" || got[0].Likes != 7 {
		t.Fatalf("unexpected: %+v", got[0])
	}
	if len(got[0].Images) != 1 || got[0].Images[0] != "https://img/t1.jpg" {
		t.Fatalf("images: %+v", got[0].Images)
	}
	// 转推取原帖内容与原作者
	if got[1].ID != "333" || got[1].User != "carol" || got[1].Text != "reposted" {
		t.Fatalf("reblog: %+v", got[1])
	}
}

func TestParseTruthUserJSON(t *testing.T) {
	raw := []byte(`{"id":"42","username":"alice","display_name":"Alice","note":"<p>bio</p>",
	  "avatar":"https://img/a.jpg","followers_count":100,"following_count":5,"statuses_count":9,
	  "verified":true,"verified_reason":"public figure"}`)
	p, ok := ParseTruthUserJSON(raw)
	if !ok {
		t.Fatal("want profile")
	}
	if p.Platform != "truth" || p.ID != "42" || p.DisplayName != "Alice" || p.Bio != "bio" || p.Followers != 100 || !p.Verified {
		t.Fatalf("unexpected: %+v", p)
	}
}
