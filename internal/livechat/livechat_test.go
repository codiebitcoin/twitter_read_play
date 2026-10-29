package livechat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"twitter_read_play/internal/store"
)

// ---- ParseVideoID ----

func TestParseVideoID(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=jfKfPfyJRdk": "jfKfPfyJRdk",
		"https://youtu.be/jfKfPfyJRdk":                "jfKfPfyJRdk",
		"https://www.youtube.com/live/jfKfPfyJRdk":    "jfKfPfyJRdk",
		"jfKfPfyJRdk":                   "jfKfPfyJRdk",
		"https://www.youtube.com/@lofi": "",
		"not-an-id":                     "",
	}
	for in, want := range cases {
		if got := ParseVideoID(in); got != want {
			t.Errorf("ParseVideoID(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- YouTube item 解析 ----

func TestParseYouTubeItem(t *testing.T) {
	raw := json.RawMessage(`{
		"id": "m1",
		"snippet": {
			"type": "superChatEvent",
			"publishedAt": "2026-10-23T04:00:00Z",
			"displayMessage": "好耶！",
			"superChatDetails": {"amountDisplayValue": "5", "currency": "USD"}
		},
		"authorDetails": {"displayName": "Alice"}
	}`)
	m, ok := parseYouTubeItem(raw)
	if !ok {
		t.Fatal("解析失败")
	}
	if m.Kind != "superchat" || m.Amount != "5 USD" || m.User != "Alice" || m.Text != "好耶！" || m.ID != "m1" {
		t.Fatalf("字段不对: %+v", m)
	}
	if m.At.IsZero() || m.At.Year() != 2026 {
		t.Fatalf("时间解析失败: %v", m.At)
	}
}

// ---- Twitch event 解析 ----

func TestParseTwitchChatEvent(t *testing.T) {
	m, ok := parseTwitchChatEvent(json.RawMessage(`{
		"broadcaster_user_id": "B", "chatter_user_login": "alice", "chatter_user_name": "Alice",
		"message_id": "m1", "message": {"text": "hi"},
		"message_type": "text", "timestamp": "2026-10-23T04:00:00Z"
	}`))
	if !ok || m.Text != "hi" || m.User != "Alice" || m.Kind != "text" || m.ID != "m1" {
		t.Fatalf("普通弹幕解析错误: %+v", m)
	}

	m, ok = parseTwitchChatEvent(json.RawMessage(`{
		"chatter_user_name": "Bob", "message": {"text": "Cheer100"},
		"cheer": {"bits": 100}
	}`))
	if !ok || m.Kind != "cheer" || m.Amount != "100 bits" {
		t.Fatalf("cheer 解析错误: %+v", m)
	}
}

// ---- chat_downloader 行解析 ----

func TestParseChatDLLine(t *testing.T) {
	// 字符串 message
	m, ok := parseChatDLLine([]byte(`{"message":"hi","message_id":"1","type":"text_message","author":{"name":"A"},"timestamp":1761187200}`))
	if !ok || m.Text != "hi" || m.User != "A" || m.Kind != "text" {
		t.Fatalf("文本解析错误: %+v", m)
	}
	if m.At.Unix() != 1761187200 {
		t.Fatalf("秒级时间戳错误: %v", m.At)
	}
	// 碎片数组 message + 毫秒时间戳 + super_chat
	m, ok = parseChatDLLine([]byte(`{"message":[{"text":"超"},{"text":"话！"}],"message_id":"2","type":"super_chat","author":{"name":"B"},"timestamp":1761187200000,"money":{"text":"$5.00"}}`))
	if !ok || m.Text != "超话！" || m.Kind != "superchat" || m.Amount != "$5.00" {
		t.Fatalf("superchat 解析错误: %+v", m)
	}
	if m.At.Unix() != 1761187200 {
		t.Fatalf("毫秒时间戳错误: %v", m.At)
	}
	// 非 JSON / 空文本行被忽略
	if _, ok := parseChatDLLine([]byte(`not-json`)); ok {
		t.Fatal("非 JSON 行应被忽略")
	}
	if _, ok := parseChatDLLine([]byte(`{"message":"","type":"text_message"}`)); ok {
		t.Fatal("空文本应被忽略")
	}
}

// ---- Manager 生命周期（注入假 Streamer）----

type fakeStreamer struct {
	mu   sync.Mutex
	on   func(Message)
	err  chan<- error
	name string
}

func (f *fakeStreamer) Name() string { return f.name }
func (f *fakeStreamer) Stream(ctx context.Context, on func(Message)) error {
	f.mu.Lock()
	f.on = on
	f.mu.Unlock()
	<-ctx.Done() // 持续活着，直到被停止
	return ctx.Err()
}

// (f *fakeStreamer) emit 前先等 ready，避免 on 尚未注册。
func (f *fakeStreamer) waitReady(t *testing.T) {
	t.Helper()
	for i := 0; i < 200 && func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.on == nil }(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.on == nil {
		t.Fatal("fake streamer 未就绪")
	}
}

func (f *fakeStreamer) emit(m Message) {
	f.mu.Lock()
	on := f.on
	f.mu.Unlock()
	if on != nil {
		on(m)
	}
}

func newTestManager(t *testing.T) (*Manager, *fakeStreamer) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeStreamer{name: "fake"}
	m.streamerFor = func(cfg SourceConfig) (Streamer, error) { return fs, nil }
	t.Cleanup(m.Close)
	return m, fs
}

func TestManagerLifecycle(t *testing.T) {
	m, fs := newTestManager(t)

	c, err := m.SaveConfig(SourceConfig{Title: "测试", Kind: KindChatDownloader, Channel: "ch1", Enabled: true})
	if err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if c.ID == "" {
		t.Fatal("ID 未生成")
	}
	// 校验：未知 kind 拒绝
	if _, err := m.SaveConfig(SourceConfig{Kind: "nope", Channel: "x"}); err == nil {
		t.Fatal("未知 kind 应被拒绝")
	}

	// 等启动
	for i := 0; i < 50 && !m.Running(c.ID); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !m.Running(c.ID) {
		t.Fatal("Enabled 配置未被启动")
	}
	fs.waitReady(t)

	// 弹幕进缓冲、seq 单调
	fs.emit(Message{ID: "a", Text: "hello"})
	fs.emit(Message{ID: "b", Text: "world"})
	msgs, _ := m.Messages(c.ID, 0, 0, 0)
	if len(msgs) != 2 || msgs[0].Text != "hello" || msgs[1].Seq <= msgs[0].Seq {
		t.Fatalf("缓冲错误: %+v", msgs)
	}
	// 增量拉取
	if got, _ := m.Messages(c.ID, msgs[0].Seq, 0, 0); len(got) != 1 || got[0].Text != "world" {
		t.Fatalf("增量拉取错误: %+v", got)
	}
	// limit 截尾保留最新
	if got, _ := m.Messages(c.ID, 0, 0, 1); len(got) != 1 || got[0].Text != "world" {
		t.Fatalf("limit 截尾错误: %+v", got)
	}
	// 分页翻历史：before=world 的 seq 拿到 hello，且没有更早的了
	if got, older := m.Messages(c.ID, 0, msgs[1].Seq, 1); len(got) != 1 || got[0].Text != "hello" || older {
		t.Fatalf("分页翻历史错误: %+v older=%v", got, older)
	}

	// 停止
	m.Stop(c.ID)
	if m.Running(c.ID) {
		t.Fatal("停止后仍 running")
	}
	// 删除
	m.DeleteConfig(c.ID)
	if _, ok := m.GetConfig(c.ID); ok {
		t.Fatal("删除后配置仍在")
	}
	if rest, _ := m.Messages(c.ID, 0, 0, 0); len(rest) != 0 {
		t.Fatal("删除后缓冲应清空")
	}
}

func TestEmptyConfigPersistedOnFirstBoot(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	m1.Close()
	var raw []SourceConfig
	if err := st.Load("livechat.json", &raw); err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		t.Fatal("empty livechat blob should be [] not nil after default migration")
	}
	m2, err := NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if n := len(m2.Configs()); n != 0 {
		t.Fatalf("want 0 configs, got %d", n)
	}
}

func TestConfigPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	st, _ := store.New(dir)
	m1, _ := NewManager(st)
	m1.streamerFor = func(cfg SourceConfig) (Streamer, error) { return &fakeStreamer{name: cfg.Kind}, nil }
	c, err := m1.SaveConfig(SourceConfig{Kind: KindYouTubeAPI, Channel: "jfKfPfyJRdk", YouTubeAPIKey: "k", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m1.Close()

	// 重启：配置还在，且 Enabled 的自动启动
	m2, err := NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	defer m2.Close()
	if _, ok := m2.GetConfig(c.ID); !ok {
		t.Fatal("重启后配置丢失")
	}
	for i := 0; i < 50 && !m2.Running(c.ID); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if !m2.Running(c.ID) {
		t.Fatal("重启后 Enabled 配置未自动启动")
	}
}

// ---- YouTube 全链路（httptest 假 API）----

func TestYouTubeStreamFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/videos"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"liveStreamingDetails":{"activeLiveChatId":"CHAT1"}}]}`))
		case strings.HasPrefix(r.URL.Path, "/liveChat/messages"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"nextPageToken": "",
				"pollingIntervalMillis": 5000,
				"items": [{
					"id": "m1",
					"snippet": {"type": "textMessageEvent", "publishedAt": "2026-10-23T04:00:00Z",
						"displayMessage": "第一弹"},
					"authorDetails": {"displayName": "Alice"}
				}]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	y := &YouTubeAPI{Cfg: SourceConfig{Kind: KindYouTubeAPI, Channel: "https://youtu.be/jfKfPfyJRdk", YouTubeAPIKey: "k"}, APIBase: srv.URL, MinInterval: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Message, 4)
	done := make(chan error, 1)
	go func() { done <- y.Stream(ctx, func(m Message) { got <- m }) }()

	select {
	case m := <-got:
		if m.Text != "第一弹" || m.User != "Alice" || m.Channel != "jfKfPfyJRdk" {
			t.Fatalf("弹幕字段错误: %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未收到弹幕")
	}
	cancel()
	if err := <-done; err != nil && err != context.Canceled {
		t.Fatalf("Stream 退出错误: %v", err)
	}
}

// ---- Twitch EventSub 全链路（httptest 假 Helix + 假 WS）----

func TestTwitchEventSubFlow(t *testing.T) {
	notif := map[string]any{
		"metadata": map[string]any{"message_type": "notification"},
		"payload": map[string]any{
			"event": map[string]any{
				"broadcaster_user_id": "B", "chatter_user_login": "alice",
				"chatter_user_name": "Alice", "message_id": "m1",
				"message":      map[string]any{"text": "hi from twitch"},
				"message_type": "text", "timestamp": "2026-10-23T04:00:00Z",
			},
		},
	}
	var up = websocket.Upgrader{}
	var wsMu sync.Mutex
	var subscribed bool

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(map[string]any{
			"metadata": map[string]any{"message_type": "session_welcome"},
			"payload":  map[string]any{"session": map[string]any{"id": "SID"}},
		})
		// 等订阅请求送达后推弹幕
		for i := 0; i < 100; i++ {
			wsMu.Lock()
			ok := subscribed
			wsMu.Unlock()
			if ok {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		_ = c.WriteJSON(notif)
		time.Sleep(500 * time.Millisecond)
	})
	mux.HandleFunc("/helix/users", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("login") != "testchannel" {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"B","login":"testchannel"}]}`))
	})
	mux.HandleFunc("/validate", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"user_id":"UID"}`))
	})
	mux.HandleFunc("/helix/eventsub/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		wsMu.Lock()
		subscribed = true
		wsMu.Unlock()
		_, _ = w.Write([]byte(`{"data":[{"id":"S1"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tw := &TwitchEventSub{
		Cfg:         SourceConfig{Kind: KindTwitchEventSub, Channel: "@testchannel", TwitchClientID: "cid", TwitchToken: "tok"},
		APIBase:     srv.URL + "/helix",
		ValidateURL: srv.URL + "/validate",
		WSURL:       "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws",
		HTTP:        srv.Client(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan Message, 4)
	done := make(chan error, 1)
	go func() { done <- tw.Stream(ctx, func(m Message) { got <- m }) }()

	select {
	case m := <-got:
		if m.Text != "hi from twitch" || m.User != "Alice" || m.Channel != "testchannel" {
			t.Fatalf("弹幕字段错误: %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未收到弹幕")
	}
	cancel()
	if err := <-done; err != nil && err != context.Canceled {
		t.Fatalf("Stream 退出错误: %v", err)
	}
}

// ---- SourceConfig 校验 ----

func TestConfigValidate(t *testing.T) {
	if err := (SourceConfig{Kind: KindTwitchEventSub, Channel: "x"}).Validate(); err == nil {
		t.Fatal("twitch 缺 token 应报错")
	}
	if err := (SourceConfig{Kind: KindYouTubeAPI, Channel: "x"}).Validate(); err == nil {
		t.Fatal("youtube 缺 key/token 应报错")
	}
	if err := (SourceConfig{Kind: KindYouTubeAPI, Channel: "x", YouTubeAccessToken: "ya29.x"}).Validate(); err != nil {
		t.Fatalf("有 Google token 不应报错: %v", err)
	}
	if err := (SourceConfig{Kind: KindTwitchEventSub, Channel: "x", TwitchClientID: "c", TwitchToken: "t", Subscribe: []string{"nope"}}).Validate(); err == nil {
		t.Fatal("非法 subscribe 应报错")
	}
	if err := (SourceConfig{Kind: KindChatDownloader, Channel: "https://x.com"}).Validate(); err != nil {
		t.Fatalf("chat_downloader 免凭证不应报错: %v", err)
	}
	if err := (SourceConfig{Kind: KindYouTubeAPI, Channel: "x", YouTubeAPIKey: "k"}).Validate(); err != nil {
		t.Fatalf("完整配置不应报错: %v", err)
	}
	if err := (SourceConfig{Kind: KindBilibili, Channel: "not-a-room"}).Validate(); err == nil {
		t.Fatal("bilibili 非法房间号应报错")
	}
	if err := (SourceConfig{Kind: KindBilibili, Channel: "https://live.bilibili.com/6"}).Validate(); err != nil {
		t.Fatalf("bilibili 无 Cookie 也应能过: %v", err)
	}
	if err := (SourceConfig{Kind: KindBilibili, Channel: "https://live.bilibili.com/6", BilibiliCookie: "SESSDATA=only"}).Validate(); err == nil {
		t.Fatal("bilibili 残缺 Cookie 应报错")
	}
	if err := (SourceConfig{Kind: KindBilibili, Channel: "https://live.bilibili.com/6", BilibiliCookie: "SESSDATA=a; bili_jct=b"}).Validate(); err != nil {
		t.Fatalf("bilibili URL+Cookie 不应报错: %v", err)
	}
	if err := (SourceConfig{Kind: KindYouTubeNoAuth, Channel: "https://www.youtube.com/watch?v=xxxxxxxxxxx"}).Validate(); err != nil {
		t.Fatalf("youtube_noauth 免凭证不应报错: %v", err)
	}
}

func TestParseBiliRoomID(t *testing.T) {
	cases := map[string]int{
		"6":                                 6,
		"  12345  ":                         12345,
		"https://live.bilibili.com/6":       6,
		"https://live.bilibili.com/6?spm=1": 6,
		"https://www.bilibili.com/video/1":  0,
		"not-a-room":                        0,
		"":                                  0,
	}
	for in, want := range cases {
		if got := ParseBiliRoomID(in); got != want {
			t.Errorf("ParseBiliRoomID(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestOAuthStateAndDefaults(t *testing.T) {
	m, _ := newTestManager(t)
	st := m.NewOAuthState("google")
	if err := m.ConsumeOAuthState(st, "twitch"); err == nil {
		t.Fatal("provider 不匹配应失败")
	}
	st2 := m.NewOAuthState("google")
	if err := m.ConsumeOAuthState(st2, "google"); err != nil {
		t.Fatal(err)
	}
	if err := m.ConsumeOAuthState(st2, "google"); err == nil {
		t.Fatal("state 只能用一次")
	}

	if err := m.SetGoogleToken("g-tok", "g-ref", "a@b.com", time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := m.SetTwitchToken("t-tok", "uid", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SaveOAuthApp(OAuthSettings{TwitchClientID: "cid", TwitchClientSecret: "sec"}); err != nil {
		t.Fatal(err)
	}
	c := SourceConfig{Kind: KindTwitchEventSub, Channel: "streamer"}
	m.applyOAuthDefaults(&c)
	if c.TwitchToken != "t-tok" || c.TwitchClientID != "cid" || c.TwitchUserID != "uid" || len(c.Subscribe) == 0 {
		t.Fatalf("twitch 默认值未补齐: %+v", c)
	}
	if _, err := m.SaveServiceURLs("https://yt.example/v3", "https://chat.example", "https://twitch.example/helix", "https://bili.example"); err != nil {
		t.Fatal(err)
	}
	y := SourceConfig{Kind: KindYouTubeAPI, Channel: "jfKfPfyJRdk"}
	m.applyOAuthDefaults(&y)
	if y.YouTubeAccessToken != "g-tok" || y.BaseURL != "https://yt.example/v3" {
		t.Fatalf("youtube token / 服务地址未带上: %+v", y)
	}
	dl := SourceConfig{Kind: KindChatDownloader, Channel: "https://www.youtube.com/watch?v=x"}
	m.applyOAuthDefaults(&dl)
	if dl.BaseURL != "https://chat.example" {
		t.Fatalf("chat_downloader 应走自己的服务地址: %+v", dl)
	}
	twitchDL := SourceConfig{Kind: KindChatDownloader, Channel: "https://www.twitch.tv/caseoh_"}
	m.applyOAuthDefaults(&twitchDL)
	if twitchDL.BaseURL != "https://chat.example" {
		t.Fatalf("Twitch 的 chat_downloader 也应走 chat_downloader 服务地址: %+v", twitchDL)
	}
	if got := (&YouTubeAPI{Cfg: y}).apiBase(); got != "https://yt.example/v3" {
		t.Fatalf("apiBase = %s", got)
	}
	u, err := serviceChatURL("https://yt.example/v3", "https://youtu.be/abc")
	if err != nil || !strings.Contains(u, "url=") {
		t.Fatalf("serviceChatURL = %s %v", u, err)
	}
	if _, err := m.SaveOAuthApp(OAuthSettings{BilibiliCookie: "SESSDATA=a; bili_jct=b"}); err != nil {
		t.Fatal(err)
	}
	b := SourceConfig{Kind: KindBilibili, Channel: "6"}
	m.applyOAuthDefaults(&b)
	if b.BilibiliCookie != "SESSDATA=a; bili_jct=b" || b.BaseURL != "https://bili.example" {
		t.Fatalf("bilibili cookie / 服务地址未带上: %+v", b)
	}
	tw := SourceConfig{Kind: KindTwitchEventSub, Channel: "someone"}
	m.applyOAuthDefaults(&tw)
	if tw.BaseURL != "https://twitch.example/helix" {
		t.Fatalf("twitch 服务地址未带上: %+v", tw)
	}
	if !m.OAuth().Public().BilibiliCookieSet {
		t.Fatal("Public 应标记 B 站 Cookie 已配置")
	}
	if m.OAuth().Public().GoogleEmail != "a@b.com" || !m.OAuth().Public().TwitchConnected {
		t.Fatal("Public 视图不对")
	}
}

// 服务地址不能隐式兜底到 twitter-read：那边没有 chat 端点，GET 会拿到后台
// HTML 页（200 空流），弹幕源会无限重连且一条消息都收不到。
func TestChatAndBilibiliServiceURLStayEmptyWithoutConfig(t *testing.T) {
	m, _ := newTestManager(t)
	dl := SourceConfig{Kind: KindChatDownloader, Channel: "https://www.twitch.tv/x"}
	m.applyOAuthDefaults(&dl)
	b := SourceConfig{Kind: KindBilibili, Channel: "6"}
	m.applyOAuthDefaults(&b)
	if dl.BaseURL != "" || b.BaseURL != "" {
		t.Fatalf("未配置服务地址时应留空（走本机/直连）: chat=%q bili=%q", dl.BaseURL, b.BaseURL)
	}
	pub := m.OAuth().Public()
	if pub.ChatDownloaderServiceURL != "" || pub.BilibiliServiceURL != "" {
		t.Fatalf("设置页不应凭空显示服务地址: %+v", pub)
	}
}

func TestParseTwitchEventVariants(t *testing.T) {
	if c := twitchCondition("channel.cheer", "B", "U"); c["user_id"] != "" || c["broadcaster_user_id"] != "B" {
		t.Fatalf("cheer condition 只要 broadcaster: %v", c)
	}
	if c := twitchCondition("channel.chat.message", "B", "U"); c["user_id"] != "U" {
		t.Fatalf("chat condition 要 user_id: %v", c)
	}
	m, ok := parseTwitchEvent("channel.cheer", json.RawMessage(`{"user_name":"Bob","message":"hi","bits":50}`))
	if !ok || m.Kind != "cheer" || m.Amount != "50 bits" {
		t.Fatalf("cheer 解析错误: %+v", m)
	}
	m, ok = parseTwitchEvent("channel.subscribe", json.RawMessage(`{"user_name":"C","tier":"1000"}`))
	if !ok || m.Kind != "sub" {
		t.Fatalf("sub 解析错误: %+v", m)
	}
}

func TestPickRandomLive(t *testing.T) {
	if _, err := PickRandomLive(context.Background(), "nope", OAuthSettings{}, nil); err == nil {
		t.Fatal("未知 kind 应报错")
	}
	if _, err := PickRandomLive(context.Background(), KindYouTubeAPI, OAuthSettings{}, nil); err == nil {
		t.Fatal("YouTube 未登录应报错")
	}
	if _, err := PickRandomLive(context.Background(), KindTwitchEventSub, OAuthSettings{}, nil); err == nil {
		t.Fatal("Twitch 未登录应报错")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"id":{"videoId":"dQw4w9WgXcQ"},"snippet":{"title":"lofi","channelTitle":"Ch"}}]}`))
	})
	mux.HandleFunc("/streams", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"user_login":"xqc","user_name":"xQc","title":"hi"}]}`))
	})
	mux.HandleFunc("/recommend", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":[{"roomid":6,"title":"哔哩哔哩直播","uname":"哔哩哔哩直播"}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	oldYT, oldTW, oldBL := youtubeSearchURL, twitchStreamsURL, biliRecommendURL
	youtubeSearchURL, twitchStreamsURL, biliRecommendURL = srv.URL+"/search", srv.URL+"/streams", srv.URL+"/recommend"
	t.Cleanup(func() {
		youtubeSearchURL, twitchStreamsURL, biliRecommendURL = oldYT, oldTW, oldBL
	})

	yt, err := PickRandomLive(context.Background(), KindYouTubeAPI, OAuthSettings{GoogleAccessToken: "tok"}, srv.Client())
	if err != nil || yt.Channel != "dQw4w9WgXcQ" {
		t.Fatalf("youtube pick: %+v %v", yt, err)
	}
	tw, err := PickRandomLive(context.Background(), KindTwitchEventSub, OAuthSettings{TwitchClientID: "c", TwitchAccessToken: "t"}, srv.Client())
	if err != nil || tw.Channel != "xqc" {
		t.Fatalf("twitch pick: %+v %v", tw, err)
	}
	bl, err := PickRandomLive(context.Background(), KindBilibili, OAuthSettings{}, srv.Client())
	if err != nil || ParseBiliRoomID(bl.Channel) != 6 {
		t.Fatalf("bili pick: %+v %v", bl, err)
	}

	m, _ := newTestManager(t)
	saved, err := m.CreateRandom(context.Background(), KindBilibili)
	if err != nil || saved.Kind != KindBilibili || ParseBiliRoomID(saved.Channel) != 6 {
		t.Fatalf("CreateRandom: %+v %v", saved, err)
	}
	again, err := m.CreateRandom(context.Background(), KindBilibili)
	if err != nil || again.ID != saved.ID {
		t.Fatalf("CreateRandom 应覆盖同 kind 随机源: %+v vs %+v %v", again, saved, err)
	}
}

func TestStartRecordsBiliCookieError(t *testing.T) {
	m, _ := newTestManager(t)
	m.mu.Lock()
	m.cfgs = append(m.cfgs, SourceConfig{ID: "bili-x", Kind: KindBilibili, Channel: "6", Enabled: true, BilibiliCookie: "SESSDATA=only"})
	m.mu.Unlock()
	if err := m.Start("bili-x"); err == nil {
		t.Fatal("残缺 Cookie 启动应失败")
	}
	var last string
	for _, s := range m.Statuses() {
		if s.ID == "bili-x" {
			last = s.LastError
		}
	}
	if last == "" || !strings.Contains(last, "bili_jct") {
		t.Fatalf("应把 Cookie 错误写到状态，实际 %q", last)
	}
}
