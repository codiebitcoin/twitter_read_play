package xapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadAuthJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "profiles", "cicd-agent", "auth.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"cookies":[{"name":"auth_token","value":"tok-1","domain":".x.com"},{"name":"ct0","value":"csrf-1","domain":".x.com"}]}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	tok, ct0 := loadAuthFile(dir)
	if tok != "tok-1" || ct0 != "csrf-1" {
		t.Fatalf("got %q %q", tok, ct0)
	}
}

func TestExecUserAndTweets(t *testing.T) {
	userBody := `{
	  "data":{"user":{"result":{
	    "rest_id":"44196397",
	    "core":{"screen_name":"elonmusk","name":"Elon Musk"},
	    "legacy":{"screen_name":"elonmusk","name":"Elon Musk","followers_count":1,"verified":true}
	  }}}}`
	tweetBody := `{
	  "data":{"user":{"result":{"timeline":{"timeline":{"instructions":[
	    {"entries":[
	      {"content":{"entryType":"TimelineTimelineItem","itemContent":{
	        "itemType":"TimelineTweet",
	        "tweet_results":{"result":{
	          "legacy":{"id_str":"111","full_text":"hi","favorite_count":3},
	          "core":{"user_results":{"result":{"rest_id":"44196397","legacy":{"screen_name":"elonmusk"}}}}
	        }}
	      }}},
	      {"content":{"cursorType":"Bottom","value":"CURSOR_X","entryType":"TimelineTimelineCursor"}}
	    ]}
	  ]}}}}}}`
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" || r.Header.Get("x-csrf-token") != "csrf" {
			t.Errorf("missing auth headers: %v", r.Header)
		}
		if r.URL.Query().Get("variables") != "" {
			sawAuth = true
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/i/api/graphql/qid-user/UserByScreenName" {
			_, _ = w.Write([]byte(userBody))
			return
		}
		if r.URL.Path == "/i/api/graphql/qid-tweets/UserTweets" {
			_, _ = w.Write([]byte(tweetBody))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := &Client{
		AuthToken: "tok",
		CT0:       "csrf",
		Origin:    srv.URL,
		HTTP:      srv.Client(),
		loaded:    true,
		qids:      map[string]string{"UserByScreenName": "qid-user", "UserTweets": "qid-tweets"},
		qidAt:     time.Now(),
		features:  defaultFeatures(),
	}
	got, err := c.Exec(context.Background(), "user", "--screen", "elonmusk")
	if err != nil {
		t.Fatal(err)
	}
	user, _ := got["user"].(map[string]any)
	if str(user["id_str"]) != "44196397" || str(user["screen_name"]) != "elonmusk" {
		t.Fatalf("user = %#v", user)
	}
	tw, err := c.Exec(context.Background(), "tweets", "--screen", "elonmusk", "--count", "20")
	if err != nil {
		t.Fatal(err)
	}
	if !sawAuth {
		t.Fatal("没有打到 graphql")
	}
	arr, _ := tw["tweets"].([]any)
	if len(arr) != 1 || tw["next_cursor"] != "CURSOR_X" {
		raw, _ := json.Marshal(tw)
		t.Fatalf("tweets = %s", raw)
	}
	first := arr[0].(map[string]any)
	if str(first["full_text"]) != "hi" || str(first["id_str"]) != "111" {
		t.Fatalf("tweet = %#v", first)
	}
	u, _ := first["user"].(map[string]any)
	if str(u["screen_name"]) != "elonmusk" {
		t.Fatalf("tweet user = %#v", u)
	}
}

func TestMissingFeatureRetry(t *testing.T) {
	if got := missingFeatures([]byte(`{"errors":[{"message":"The following features cannot be null: foo, bar"}]}`)); len(got) != 2 || got[0] != "foo" {
		t.Fatalf("%v", got)
	}
}
