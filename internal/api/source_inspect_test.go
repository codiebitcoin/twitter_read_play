package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"twitter_read_play/internal/source"
	"twitter_read_play/internal/store"
)

type inspectTestAdapter struct {
	info    source.Info
	profile source.AccountProfile
	err     error
	calls   int32
}

func (a *inspectTestAdapter) Info() source.Info                  { return a.info }
func (a *inspectTestAdapter) Match(t source.Target) source.Match { return source.BaseMatch(a.info, t) }
func (a *inspectTestAdapter) InspectAccount(ctx context.Context, ch source.Channel, t source.Target) (source.AccountProfile, error) {
	atomic.AddInt32(&a.calls, 1)
	return a.profile, a.err
}

func (a *inspectTestAdapter) callCount() int { return int(atomic.LoadInt32(&a.calls)) }

func newInspectServer(t *testing.T, adapters ...source.Adapter) *Server {
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
	for _, a := range adapters {
		if err := reg.Register(a); err != nil {
			t.Fatalf("Register(%s): %v", a.Info().ID, err)
		}
	}
	return &Server{Store: st, Channels: cm, Adapters: reg, Mode: source.ModeDesktop}
}

func callInspect(t *testing.T, s *Server, body string) (*httptest.ResponseRecorder, inspectResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/source/inspect", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.inspectSource(rec, req)
	var resp inspectResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("解析响应: %v (%s)", err, rec.Body.String())
		}
	}
	return rec, resp
}

func xInfo(id string, priority int) source.Info {
	return source.Info{
		ID: id, Name: id,
		Domains:   []source.Domain{source.DomainFeed},
		Platforms: []source.Platform{source.PlatformX},
		Kinds:     map[source.Platform][]source.TargetKind{source.PlatformX: {source.KindProfile}},
		Modes:     []source.Mode{source.ModeDesktop, source.ModeWeb},
		Priority:  priority, Visible: true,
	}
}

func TestInspectSourceOKAndCache(t *testing.T) {
	a := &inspectTestAdapter{
		info: xInfo("x_test", 10),
		profile: source.AccountProfile{
			Platform: source.PlatformX, Kind: source.KindProfile, ID: "1",
			Handle: "@elonmusk", DisplayName: "Elon Musk",
			AvatarURL: "https://img/e.jpg", Followers: 123,
		},
	}
	s := newInspectServer(t, a)
	if _, err := s.Channels.Upsert(source.Channel{
		ID: "ch-x", Name: "x", Domain: source.DomainFeed,
		AdapterID: "x_test", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	rec, resp := callInspect(t, s, `{"domain":"feed","raw":"https://x.com/elonmusk"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if resp.Account == nil || resp.Account.DisplayName != "Elon Musk" || resp.Account.ID != "1" {
		t.Fatalf("unexpected account: %+v", resp.Account)
	}
	if resp.ViewURL != "https://x.com/elonmusk" {
		t.Fatalf("view_url = %q", resp.ViewURL)
	}
	if resp.Resolved == nil || resp.Resolved.AdapterID != "x_test" {
		t.Fatalf("unexpected resolved: %+v", resp.Resolved)
	}
	if resp.Inspect.Cached || !resp.Inspect.OK {
		t.Fatalf("unexpected inspect: %+v", resp.Inspect)
	}

	_, resp2 := callInspect(t, s, `{"domain":"feed","raw":"https://x.com/elonmusk"}`)
	if !resp2.Inspect.Cached || resp2.Account == nil {
		t.Fatalf("second call should hit cache: %+v", resp2.Inspect)
	}
	if a.callCount() != 1 {
		t.Fatalf("adapter calls = %d, want 1", a.callCount())
	}

	_, resp3 := callInspect(t, s, `{"domain":"feed","raw":"https://x.com/elonmusk","refresh":true}`)
	if resp3.Inspect.Cached {
		t.Fatalf("refresh should bypass cache: %+v", resp3.Inspect)
	}
	if a.callCount() != 2 {
		t.Fatalf("adapter calls after refresh = %d, want 2", a.callCount())
	}
}

func TestInspectSourceFallsBack(t *testing.T) {
	bad := &inspectTestAdapter{
		info: xInfo("x_login", 10),
		err:  &source.InspectError{Kind: source.InspectNeedLogin, Message: "未登录 X"},
	}
	good := &inspectTestAdapter{
		info: xInfo("x_ok", 20),
		profile: source.AccountProfile{
			Platform: source.PlatformX, Kind: source.KindProfile, ID: "1",
			Handle: "@fallback", DisplayName: "fallback",
		},
	}
	s := newInspectServer(t, bad, good)
	for _, id := range []struct{ channel, adapter string }{{"ch-1", "x_login"}, {"ch-2", "x_ok"}} {
		if _, err := s.Channels.Upsert(source.Channel{
			ID: id.channel, Name: id.channel, Domain: source.DomainFeed,
			AdapterID: id.adapter, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	_, resp := callInspect(t, s, `{"domain":"feed","raw":"https://x.com/elonmusk"}`)
	if resp.Account == nil || resp.Account.DisplayName != "fallback" {
		t.Fatalf("want fallback account, got %+v", resp.Account)
	}
	if len(resp.Inspect.Tried) != 2 {
		t.Fatalf("want 2 attempts, got %+v", resp.Inspect.Tried)
	}
	if resp.Inspect.Tried[0].ErrorKind != string(source.InspectNeedLogin) {
		t.Fatalf("first attempt = %+v", resp.Inspect.Tried[0])
	}
}

func TestInspectSourceStaticOnly(t *testing.T) {
	// 只实现 Adapter，不实现 AccountInspector。
	s := newInspectServer(t, channelTestAdapter{info: xInfo("x_plain", 10)})
	if _, err := s.Channels.Upsert(source.Channel{
		ID: "ch-x", Name: "x", Domain: source.DomainFeed,
		AdapterID: "x_plain", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	rec, resp := callInspect(t, s, `{"domain":"feed","raw":"https://x.com/elonmusk"}`)
	if rec.Code != http.StatusOK || resp.Account != nil {
		t.Fatalf("want static-only, got %d %+v", rec.Code, resp)
	}
	if resp.Inspect.ErrorKind != string(source.InspectUnsupported) {
		t.Fatalf("unexpected inspect: %+v", resp.Inspect)
	}
	if resp.ViewURL != "https://x.com/elonmusk" {
		t.Fatalf("view_url = %q", resp.ViewURL)
	}
}

func TestInspectSourceUnsupportedTarget(t *testing.T) {
	s := newInspectServer(t)
	rec, _ := callInspect(t, s, `{"domain":"feed","raw":"not a link"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "supported") || !strings.Contains(body, "examples") {
		t.Fatalf("missing supported/examples: %s", body)
	}
}
