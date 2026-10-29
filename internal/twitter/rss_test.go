package twitter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRSSResolveURL(t *testing.T) {
	r := &RSSFeed{BaseURL: "https://rsshub.app"}
	cases := []struct {
		feed, want string
	}{
		{"https://hnrss.org/frontpage", "https://hnrss.org/frontpage"},
		{"twitter/user/elonmusk", "https://rsshub.app/twitter/user/elonmusk"},
		{"", "https://rsshub.app"},
	}
	for _, c := range cases {
		got, err := r.resolveURL(c.feed)
		if err != nil {
			t.Fatalf("resolveURL(%q): %v", c.feed, err)
		}
		if got != c.want {
			t.Errorf("resolveURL(%q) = %q, want %q", c.feed, got, c.want)
		}
	}
	empty := &RSSFeed{}
	if _, err := empty.resolveURL("not-a-url"); err == nil {
		t.Fatal("path without base should fail")
	}
	if _, err := empty.resolveURL(""); err == nil {
		t.Fatal("empty feed without base should fail")
	}
}

func TestRSSFetchLatestRSSAndAtom(t *testing.T) {
	rssBody := `<?xml version="1.0"?>
<rss version="2.0"><channel>
<title>Demo Feed</title>
<item><title>Hello</title><link>https://ex/a</link><guid>g1</guid><description>first post</description><pubDate>Tue, 01 Jan 2026 00:00:00 GMT</pubDate></item>
<item><title>Second</title><link>https://ex/b</link><description>two</description></item>
</channel></rss>`
	atomBody := `<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
<title>Atom Feed</title>
<entry><id>e1</id><title>Atom one</title><link href="https://ex/e1"/><updated>2026-01-02T00:00:00Z</updated><content>atom body</content></entry>
</feed>`

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/xml")
		if strings.Contains(r.URL.Path, "atom") {
			_, _ = w.Write([]byte(atomBody))
			return
		}
		_, _ = w.Write([]byte(rssBody))
	}))
	defer srv.Close()

	p := &RSSFeed{}
	tweets, err := p.FetchLatest(context.Background(), srv.URL+"/rss", 10)
	if err != nil {
		t.Fatalf("rss: %v", err)
	}
	if len(tweets) != 2 {
		t.Fatalf("rss items: %d", len(tweets))
	}
	if tweets[0].ID != "g1" || tweets[0].User != "Demo Feed" || tweets[0].Link != "https://ex/a" {
		t.Errorf("rss[0] = %+v", tweets[0])
	}
	if tweets[1].ID != "https://ex/b" {
		t.Errorf("rss[1].ID = %q", tweets[1].ID)
	}

	atom, err := p.FetchLatest(context.Background(), srv.URL+"/atom", 10)
	if err != nil {
		t.Fatalf("atom: %v", err)
	}
	if len(atom) != 1 || atom[0].ID != "e1" || !strings.Contains(atom[0].Text, "atom body") {
		t.Errorf("atom = %+v", atom)
	}
	if hits != 2 {
		t.Fatalf("hits %d", hits)
	}
}

func TestRSSFetchHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := (&RSSFeed{}).FetchLatest(context.Background(), srv.URL, 1); err == nil {
		t.Fatal("404 should fail")
	}
}
