package twitter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAnyInt(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{float64(123), 123},
		{"1234", 1234},
		{"1.2万", 12000},
		{"3.4亿", 340000000},
		{"5k", 5000},
		{"5K", 5000},
		{"", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := anyInt(c.in); got != c.want {
			t.Fatalf("anyInt(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSocialReadProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/weibo/user/人民日报" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
		  "platform":"weibo","id":"1001","handle":"人民日报","name":"人民日报",
		  "avatar":"https://img/a.jpg","bio":"官方微博",
		  "followers_count":123,"following_count":4,"posts_count":56,
		  "verified":true,"web_url":"https://weibo.com/u/1001"
		}`))
	}))
	defer srv.Close()
	s := &SocialRead{Platform: "weibo", BaseURL: srv.URL, APIKey: "k", HTTP: srv.Client()}
	p, err := s.Profile(context.Background(), "人民日报")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "1001" || p.Handle != "人民日报" || p.Followers != 123 || p.Posts != 56 || !p.Verified {
		t.Fatalf("unexpected profile: %+v", p)
	}
	if p.CanonicalURL != "https://weibo.com/u/1001" {
		t.Fatalf("canonical: %s", p.CanonicalURL)
	}
}

func TestWeiboProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("value"); got != "123" {
			t.Fatalf("value = %q", got)
		}
		_, _ = w.Write([]byte(`{"ok":1,"data":{"userInfo":{
		  "id":123,"screen_name":"测试用户","profile_image_url":"https://img/b.jpg",
		  "description":"简介","followers_count":"1.2万","friends_count":"34",
		  "statuses_count":56,"verified":true,"verified_reason":"认证"
		}}}`))
	}))
	defer srv.Close()
	w := &Weibo{BaseURL: srv.URL}
	p, err := w.Profile(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "123" || p.Handle != "测试用户" || p.Followers != 12000 || p.Following != 34 || p.Posts != 56 {
		t.Fatalf("unexpected profile: %+v", p)
	}
	if !p.Verified || p.VerifiedNote != "认证" {
		t.Fatalf("verified: %+v", p)
	}
}

func TestRSSProfile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
		<rss version="2.0"><channel><title>Example Feed</title>
		  <item><title>a</title><link>https://e/a</link></item>
		  <item><title>b</title><link>https://e/b</link></item>
		</channel></rss>`))
	}))
	defer srv.Close()
	r := &RSSFeed{}
	p, err := r.Profile(context.Background(), srv.URL+"/feed.xml")
	if err != nil {
		t.Fatal(err)
	}
	if p.DisplayName != "Example Feed" || p.Posts != 2 || !p.Partial {
		t.Fatalf("unexpected profile: %+v", p)
	}
}
