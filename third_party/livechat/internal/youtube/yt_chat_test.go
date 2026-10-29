package youtube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 构造一个最小化的"直播页 HTML"，覆盖两个正则的提取路径。
const fixtureHTML = `<!doctype html><script>ytcfg.set({"INNERTUBE_API_KEY":"AIzaTestKey","INNERTUBE_CONTEXT":{"client":{"hl":"en","gl":"US","clientName":"WEB","clientVersion":"2.20240101.00.00"}},"INNERTUBE_CONTEXT_CLIENT_NAME":"1","INNERTUBE_CLIENT_VERSION":"2.20240101.00.00"});</script>
<script>ytInitialData = {"contents":{"twoColumnWatchNextResults":{"conversationBar":{"liveChatRenderer":{"continuations":[{"reloadContinuationData":{"continuation":"CONT-TOKEN-123"}}]}}}}};</script>`

func TestParseInitialData_FromFixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(fixtureHTML))
	}))
	defer srv.Close()

	cont, cfg, err := ParseInitialData(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("ParseInitialData: %v", err)
	}
	if cont != "CONT-TOKEN-123" {
		t.Errorf("continuation = %q, want CONT-TOKEN-123", cont)
	}
	if cfg.INNERTUBE_API_KEY != "AIzaTestKey" {
		t.Errorf("api key = %q", cfg.INNERTUBE_API_KEY)
	}
	if cfg.INNERTUBE_CONTEXT.Client.ClientVersion != "2.20240101.00.00" {
		t.Errorf("client version = %q", cfg.INNERTUBE_CONTEXT.Client.ClientVersion)
	}
}

func TestParseInitialData_NotLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<script>ytcfg.set({"INNERTUBE_API_KEY":"k"});</script><script>ytInitialData = {"contents":{}};</script>`))
	}))
	defer srv.Close()
	_, _, err := ParseInitialData(context.Background(), srv.URL)
	if err == nil || err != ErrStreamNotLive {
		t.Fatalf("want ErrStreamNotLive, got %v", err)
	}
}

// [FIX 验证] 非 200 时必须返回错误（原库返回 nil error）
func TestParseInitialData_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "captcha", http.StatusForbidden)
	}))
	defer srv.Close()
	_, _, err := ParseInitialData(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("want error on HTTP 403, got nil")
	}
}

func TestFetchChatMessages_ParsesActionsAndContinuation(t *testing.T) {
	resp := map[string]any{
		"continuationContents": map[string]any{
			"liveChatContinuation": map[string]any{
				"actions": []any{
					map[string]any{
						"addChatItemAction": map[string]any{
							"item": map[string]any{
								"liveChatTextMessageRenderer": map[string]any{
									"message": map[string]any{
										"runs": []any{
											map[string]any{"text": "Hello "},
											map[string]any{"text": "world"},
										},
									},
									"authorName":    map[string]any{"simpleText": "Alice"},
									"timestampUsec": "1700000000000000",
								},
							},
						},
					},
				},
				"continuations": []any{
					map[string]any{"timedContinuationData": map[string]any{"continuation": "NEXT", "timeoutMs": 1}},
				},
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["continuation"] != "CONT-TOKEN-123" {
			t.Errorf("request continuation = %v", got["continuation"])
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	// 临时改写 API 地址指向本地测试服务
	oldURL := LIVE_CHAT_URL
	LIVE_CHAT_URL = srv.URL + "/live_chat/get_%s?key=%s"
	defer func() { LIVE_CHAT_URL = oldURL }()

	cfg := YtCfg{INNERTUBE_API_KEY: "AIzaTestKey"}
	msgs, next, err := FetchContinuationChat(context.Background(), "CONT-TOKEN-123", cfg)
	if err != nil {
		t.Fatalf("FetchContinuationChat: %v", err)
	}
	if next != "NEXT" {
		t.Errorf("next continuation = %q", next)
	}
	if len(msgs) != 1 || msgs[0].AuthorName != "Alice" || msgs[0].Message != "Hello world" {
		t.Fatalf("messages = %+v", msgs)
	}
	want := time.Unix(1700000000, 0)
	if !msgs[0].Timestamp.Equal(want) {
		t.Errorf("timestamp = %v, want %v", msgs[0].Timestamp, want)
	}
}

func TestFetchChatMessages_StreamOver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"continuationContents":{"liveChatContinuation":{"actions":[]}}}`))
	}))
	defer srv.Close()
	oldURL := LIVE_CHAT_URL
	LIVE_CHAT_URL = srv.URL + "/get_%s?key=%s"
	defer func() { LIVE_CHAT_URL = oldURL }()

	_, _, err := FetchContinuationChat(context.Background(), "c", YtCfg{INNERTUBE_API_KEY: "k"})
	if err == nil || !strings.Contains(err.Error(), "live stream over") {
		t.Fatalf("want ErrLiveStreamOver, got %v", err)
	}
}
