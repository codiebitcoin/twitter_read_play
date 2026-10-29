package twitter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBlueskyFetchLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xrpc/app.bsky.feed.getAuthorFeed":
			if got := r.URL.Query().Get("actor"); got != "alice.bsky.social" {
				t.Errorf("actor = %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"feed":[
			  {"post":{"uri":"at://did:plc:abc/app.bsky.feed.post/3k1",
			    "author":{"handle":"alice.bsky.social","displayName":"Alice"},
			    "record":{"text":"hello sky","createdAt":"2025-01-01T00:00:00Z"},
			    "likeCount":5,"replyCount":1,"repostCount":2,
			    "embed":{"$type":"app.bsky.embed.images#view","images":[{"fullsize":"https://img/1.jpg"}]}}},
			  {"post":{"uri":"at://did:plc:abc/app.bsky.feed.post/3k2",
			    "author":{"handle":"alice.bsky.social"},
			    "record":{"text":"","createdAt":"2025-01-02T00:00:00Z"},
			    "embed":{"$type":"app.bsky.embed.external#view","external":{"thumb":"https://img/card.jpg"}}}}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	b := &Bluesky{BaseURL: srv.URL}
	got, err := b.FetchLatest(context.Background(), "@alice.bsky.social", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].ID != "at://did:plc:abc/app.bsky.feed.post/3k1" || got[0].User != "alice.bsky.social" || got[0].Text != "hello sky" {
		t.Fatalf("unexpected: %+v", got[0])
	}
	if got[0].Likes != 5 || got[0].CommentCount != 1 || got[0].ShareCount != 2 {
		t.Fatalf("counts: %+v", got[0])
	}
	if got[0].Link != "https://bsky.app/profile/alice.bsky.social/post/3k1" {
		t.Fatalf("link: %q", got[0].Link)
	}
	if len(got[0].Images) != 1 || got[0].Images[0] != "https://img/1.jpg" {
		t.Fatalf("images: %+v", got[0].Images)
	}
	// 纯外链卡片：文本为空但卡片图要保留
	if len(got[1].Images) != 1 || got[1].Images[0] != "https://img/card.jpg" {
		t.Fatalf("external card: %+v", got[1])
	}
}

func TestBlueskyProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/xrpc/app.bsky.actor.getProfile" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"did":"did:plc:abc","handle":"alice.bsky.social","displayName":"Alice",
		  "description":"bio","avatar":"https://img/a.jpg","followersCount":100,"followsCount":5,"postsCount":9}`))
	}))
	defer srv.Close()

	b := &Bluesky{BaseURL: srv.URL}
	p, err := b.Profile(context.Background(), "alice.bsky.social")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "did:plc:abc" || p.Handle != "alice.bsky.social" || p.DisplayName != "Alice" || p.Followers != 100 {
		t.Fatalf("unexpected: %+v", p)
	}
	if p.CanonicalURL != "https://bsky.app/profile/alice.bsky.social" {
		t.Fatalf("canonical: %q", p.CanonicalURL)
	}
}
