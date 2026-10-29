package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"twitter_read_play/internal/source"
	"twitter_read_play/internal/store"
)

type channelTestAdapter struct{ info source.Info }

func (a channelTestAdapter) Info() source.Info                  { return a.info }
func (a channelTestAdapter) Match(t source.Target) source.Match { return source.BaseMatch(a.info, t) }

func newChannelTestServer(t *testing.T, infos ...source.Info) *Server {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	cm, err := source.NewManager(st)
	if err != nil {
		t.Fatalf("source.NewManager: %v", err)
	}
	reg := source.NewRegistry()
	for _, info := range infos {
		if err := reg.Register(channelTestAdapter{info: info}); err != nil {
			t.Fatalf("Register(%s): %v", info.ID, err)
		}
	}
	return &Server{Store: st, Channels: cm, Adapters: reg, Mode: source.ModeDesktop}
}

func upsertChannelReq(t *testing.T, s *Server, body string, id string) (*httptest.ResponseRecorder, source.Channel) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(body))
	if id != "" {
		req.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	s.upsertChannel(rec, req)
	var ch source.Channel
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &ch); err != nil {
			t.Fatalf("解析响应: %v (%s)", err, rec.Body.String())
		}
	}
	return rec, ch
}

// 留空 domain 时应落到适配器可用域，而不是写成空值或固定 feed。
func TestUpsertChannelDomainFallback(t *testing.T) {
	s := newChannelTestServer(t,
		source.Info{ID: "live_only", Domains: []source.Domain{source.DomainLive}},
		source.Info{ID: "feed_only", Domains: []source.Domain{source.DomainFeed}},
	)

	rec, ch := upsertChannelReq(t, s, `{"name":"live","adapter_id":"live_only","enabled":true}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("创建 live 渠道失败: %d %s", rec.Code, rec.Body.String())
	}
	if ch.Domain != source.DomainLive {
		t.Fatalf("live_only 留空域应回落 live，实际 %q", ch.Domain)
	}

	rec, ch = upsertChannelReq(t, s, `{"name":"feed","adapter_id":"feed_only","enabled":true}`, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("创建 feed 渠道失败: %d %s", rec.Code, rec.Body.String())
	}
	if ch.Domain != source.DomainFeed {
		t.Fatalf("feed_only 留空域应为 feed，实际 %q", ch.Domain)
	}
}

// 更新时留空 domain 应沿用已有渠道的域，避免把 live 渠道静默改成 feed。
func TestUpsertChannelKeepsExistingDomain(t *testing.T) {
	s := newChannelTestServer(t, source.Info{ID: "both", Domains: []source.Domain{source.DomainLive, source.DomainFeed}})
	if _, err := s.Channels.Upsert(source.Channel{
		ID: "ch-existing", Name: "existing", Domain: source.DomainLive,
		AdapterID: "both", Enabled: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec, ch := upsertChannelReq(t, s, `{"name":"existing","adapter_id":"both","enabled":true}`, "ch-existing")
	if rec.Code != http.StatusOK {
		t.Fatalf("更新失败: %d %s", rec.Code, rec.Body.String())
	}
	if ch.Domain != source.DomainLive {
		t.Fatalf("更新留空域应沿用 live，实际 %q", ch.Domain)
	}
}

func TestUpsertChannelRejectsBadDomain(t *testing.T) {
	s := newChannelTestServer(t, source.Info{ID: "live_only", Domains: []source.Domain{source.DomainLive}})

	// s.err 目前不写状态码（全站既有行为），这里只校验拒绝原因。
	cases := []struct {
		body string
		want string
	}{
		{`{"name":"x","adapter_id":"live_only","domain":"feed"}`, "does not support the feed domain"},
		{`{"name":"x","adapter_id":"live_only","domain":"custom"}`, "Unknown domain"},
	}
	for _, tc := range cases {
		rec, _ := upsertChannelReq(t, s, tc.body, "")
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s 应被拒绝（%s），实际 %s", tc.body, tc.want, rec.Body.String())
		}
	}
}

// 错误提示走 i18n：默认英文，?lang=zh 返回中文。
func TestUpsertChannelErrorI18n(t *testing.T) {
	s := newChannelTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader(`{"adapter_id":"missing"}`))
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	rec := httptest.NewRecorder()
	s.upsertChannel(rec, req)
	if !strings.Contains(rec.Body.String(), "Unknown adapter") {
		t.Fatalf("英文提示缺失: %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/channels?lang=zh", strings.NewReader(`{"adapter_id":"missing"}`))
	rec = httptest.NewRecorder()
	s.upsertChannel(rec, req)
	if !strings.Contains(rec.Body.String(), "未知适配器") {
		t.Fatalf("中文提示缺失: %s", rec.Body.String())
	}
}
