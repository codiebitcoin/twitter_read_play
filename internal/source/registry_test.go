package source

import (
	"context"
	"errors"
	"testing"
)

type fakeAdapter struct{ info Info }

func (f fakeAdapter) Info() Info           { return f.info }
func (f fakeAdapter) Match(t Target) Match { return BaseMatch(f.info, t) }

// fakeAdapter 同时满足 Feed/Live Worker 的形状，避免旧用例被意图门挡住。
func (f fakeAdapter) OpenFeed(ch Channel, t Target) (FeedWorker, error) { return nil, nil }
func (f fakeAdapter) TestFeed(ctx context.Context, ch Channel, t Target) (Probe, error) {
	return Probe{}, nil
}
func (f fakeAdapter) OpenLive(ch Channel, t Target) (LiveWorker, error) { return nil, nil }
func (f fakeAdapter) TestLive(ctx context.Context, ch Channel, t Target) (Probe, error) {
	return Probe{}, nil
}

type fakeInspector struct {
	fakeAdapter
	profile AccountProfile
	err     error
}

func (f fakeInspector) InspectAccount(ctx context.Context, ch Channel, t Target) (AccountProfile, error) {
	return f.profile, f.err
}

var (
	socialInfo = Info{
		ID: "socialdata", Name: "socialdata",
		Domains:   []Domain{DomainFeed},
		Platforms: []Platform{PlatformX},
		Kinds:     map[Platform][]TargetKind{PlatformX: {KindProfile}},
		Needs:     map[Platform][]CredentialKey{PlatformX: {CredAPIKey}},
		Modes:     []Mode{ModeDesktop, ModeWeb},
		Priority:  20, Visible: true,
	}
	ytBrowserInfo = Info{
		ID: "youtube_browser", Name: "YouTube 内置登录",
		Domains:   []Domain{DomainLive},
		Platforms: []Platform{PlatformYouTube},
		Kinds:     map[Platform][]TargetKind{PlatformYouTube: {KindLiveVideo, KindChannel}},
		Needs:     map[Platform][]CredentialKey{PlatformYouTube: {CredDesktopSession}},
		Modes:     []Mode{ModeDesktop},
		Priority:  15, Visible: true,
	}
	ytAPIInfo = Info{
		ID: "youtube_live_api", Name: "YouTube Live API",
		Domains:   []Domain{DomainLive},
		Platforms: []Platform{PlatformYouTube},
		Kinds:     map[Platform][]TargetKind{PlatformYouTube: {KindLiveVideo}},
		AnyOf:     map[Platform][][]CredentialKey{PlatformYouTube: {{CredAPIKey}, {CredOAuth}}},
		Modes:     []Mode{ModeDesktop, ModeWeb},
		Priority:  10, Visible: true,
	}
	remoteInfo = Info{
		ID: "chrome_driverless", Name: "chrome-driverless",
		Domains:   []Domain{DomainFeed, DomainLive},
		Platforms: []Platform{PlatformX, PlatformYouTube},
		Kinds: map[Platform][]TargetKind{
			PlatformX:       {KindProfile},
			PlatformYouTube: {KindLiveVideo},
		},
		Needs: map[Platform][]CredentialKey{
			PlatformX:       {CredBaseURL},
			PlatformYouTube: {CredBaseURL},
		},
		Modes:    []Mode{ModeWeb},
		Priority: 5, Visible: true,
	}
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry()
	for _, a := range []Adapter{
		fakeAdapter{socialInfo},
		fakeAdapter{ytBrowserInfo},
		fakeAdapter{ytAPIInfo},
		fakeAdapter{remoteInfo},
	} {
		if err := r.Register(a); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestBaseMatch(t *testing.T) {
	xProfile := Target{Domain: DomainFeed, Platform: PlatformX, Kind: KindProfile}
	if m := BaseMatch(socialInfo, xProfile); !m.OK {
		t.Fatalf("want ok, got %+v", m)
	}
	if m := BaseMatch(socialInfo, Target{Domain: DomainLive, Platform: PlatformX, Kind: KindProfile}); m.OK {
		t.Fatal("domain mismatch should fail")
	}
	if m := BaseMatch(socialInfo, Target{Domain: DomainFeed, Platform: PlatformWeibo, Kind: KindProfile}); m.OK {
		t.Fatal("platform mismatch should fail")
	}
	if m := BaseMatch(socialInfo, Target{Domain: DomainFeed, Platform: PlatformX, Kind: KindVideo}); m.OK {
		t.Fatal("kind mismatch should fail")
	}
}

func TestCanChannelServeCredentials(t *testing.T) {
	ch := Channel{ID: "ch", AdapterID: "socialdata", Enabled: true, Credentials: Credentials{APIKey: "k"}}
	a := fakeAdapter{socialInfo}
	target := Target{Domain: DomainFeed, Platform: PlatformX, Kind: KindProfile}
	if m := CanChannelServe(ch, a, target, ModeWeb); !m.OK {
		t.Fatalf("want ok, got %+v", m)
	}
	ch.Enabled = false
	if m := CanChannelServe(ch, a, target, ModeWeb); m.OK || m.Reason == "" {
		t.Fatalf("disabled channel should fail: %+v", m)
	}
	ch.Enabled = true
	ch.Credentials.APIKey = ""
	m := CanChannelServe(ch, a, target, ModeWeb)
	if m.OK || len(m.Missing) != 1 || m.Missing[0] != CredAPIKey {
		t.Fatalf("want missing api_key, got %+v", m)
	}
}

func TestCanChannelServeAnyOf(t *testing.T) {
	target := Target{Domain: DomainLive, Platform: PlatformYouTube, Kind: KindLiveVideo}
	a := fakeAdapter{ytAPIInfo}
	for _, c := range []Credentials{{APIKey: "k"}, {OAuth: "tok"}} {
		ch := Channel{ID: "ch", AdapterID: "youtube_live_api", Enabled: true, Credentials: c}
		if m := CanChannelServe(ch, a, target, ModeWeb); !m.OK {
			t.Fatalf("want ok for %+v, got %+v", c, m)
		}
	}
	ch := Channel{ID: "ch", AdapterID: "youtube_live_api", Enabled: true}
	m := CanChannelServe(ch, a, target, ModeWeb)
	if m.OK || len(m.Missing) == 0 {
		t.Fatalf("want missing credential, got %+v", m)
	}
}

func TestResolveAutoPriority(t *testing.T) {
	r := newTestRegistry(t)
	lowPriority := Info{
		ID: "slow", Name: "slow",
		Domains:   []Domain{DomainFeed},
		Platforms: []Platform{PlatformX},
		Kinds:     map[Platform][]TargetKind{PlatformX: {KindProfile}},
		Needs:     map[Platform][]CredentialKey{PlatformX: {CredAPIKey}},
		Modes:     []Mode{ModeDesktop, ModeWeb},
		Priority:  50,
	}
	if err := r.Register(fakeAdapter{lowPriority}); err != nil {
		t.Fatal(err)
	}
	target := Target{Domain: DomainFeed, Platform: PlatformX, Kind: KindProfile, ID: "elonmusk"}
	channels := []Channel{
		{ID: "ch-slow", AdapterID: "slow", Enabled: true, Credentials: Credentials{APIKey: "k"}},
		{ID: "ch-social", AdapterID: "socialdata", Enabled: true, Credentials: Credentials{APIKey: "k"}},
	}
	got, err := r.Resolve(ResolveRequest{Domain: DomainFeed, Target: target, Mode: ModeWeb}, channels)
	if err != nil {
		t.Fatal(err)
	}
	if got.Channel.ID != "ch-social" {
		t.Fatalf("want ch-social, got %s", got.Channel.ID)
	}
}

func TestResolveNoMatchDiagnostics(t *testing.T) {
	r := newTestRegistry(t)
	target := Target{Domain: DomainLive, Platform: PlatformYouTube, Kind: KindLiveVideo, ID: "abc"}
	channels := []Channel{
		{ID: "ch-social", AdapterID: "socialdata", Enabled: true, Credentials: Credentials{APIKey: "k"}},
	}
	_, err := r.Resolve(ResolveRequest{Domain: DomainLive, Target: target, Mode: ModeWeb}, channels)
	if err == nil {
		t.Fatal("want no-match error")
	}
	var nm *NoMatchError
	if !errors.As(err, &nm) {
		t.Fatalf("want NoMatchError, got %T %v", err, err)
	}
	if len(nm.Rejected) != 1 || nm.Rejected[0].Reason == "" {
		t.Fatalf("want rejection reason, got %+v", nm.Rejected)
	}
}

func TestResolveModeMismatch(t *testing.T) {
	r := newTestRegistry(t)
	target := Target{Domain: DomainLive, Platform: PlatformYouTube, Kind: KindLiveVideo, ID: "abc"}
	ch := Channel{ID: "ch-yt", AdapterID: "youtube_browser", Enabled: true, Credentials: Credentials{SessionProvider: "youtube"}}
	if _, err := r.Resolve(ResolveRequest{Domain: DomainLive, Target: target, Mode: ModeDesktop}, []Channel{ch}); err != nil {
		t.Fatalf("desktop should resolve: %v", err)
	}
	if _, err := r.Resolve(ResolveRequest{Domain: DomainLive, Target: target, Mode: ModeWeb}, []Channel{ch}); err == nil {
		t.Fatal("web should reject desktop-only adapter")
	}
}

func TestResolveExplicit(t *testing.T) {
	r := newTestRegistry(t)
	target := Target{Domain: DomainFeed, Platform: PlatformX, Kind: KindProfile, ID: "elonmusk"}
	channels := []Channel{
		{ID: "ch-good", AdapterID: "socialdata", Enabled: true, Credentials: Credentials{APIKey: "k"}},
		{ID: "ch-bad", AdapterID: "socialdata", Enabled: true},
	}
	got, err := r.Resolve(ResolveRequest{Domain: DomainFeed, Target: target, Mode: ModeWeb, ChannelID: "ch-good"}, channels)
	if err != nil || got.Channel.ID != "ch-good" {
		t.Fatalf("explicit good: %+v %v", got, err)
	}
	if _, err := r.Resolve(ResolveRequest{Domain: DomainFeed, Target: target, Mode: ModeWeb, ChannelID: "ch-bad"}, channels); err == nil {
		t.Fatal("explicit bad should fail")
	}
	if _, err := r.Resolve(ResolveRequest{Domain: DomainFeed, Target: target, Mode: ModeWeb, ChannelID: "missing"}, channels); err == nil {
		t.Fatal("missing channel should fail")
	}
}

func TestResolveRemoteWeb(t *testing.T) {
	r := newTestRegistry(t)
	target := Target{Domain: DomainLive, Platform: PlatformYouTube, Kind: KindLiveVideo, ID: "abc"}
	ch := Channel{ID: "ch-remote", AdapterID: "chrome_driverless", Enabled: true, Credentials: Credentials{BaseURL: "http://chrome-driverless"}}
	got, err := r.Resolve(ResolveRequest{Domain: DomainLive, Target: target, Mode: ModeWeb}, []Channel{ch})
	if err != nil || got.Channel.ID != "ch-remote" {
		t.Fatalf("web remote: %+v %v", got, err)
	}
	if _, err := r.Resolve(ResolveRequest{Domain: DomainLive, Target: target, Mode: ModeDesktop}, []Channel{ch}); err == nil {
		t.Fatal("remote should be web-only")
	}
}

func TestValidatePlatform(t *testing.T) {
	if err := ValidatePlatform("twitter", Target{Platform: PlatformX}); err != nil {
		t.Fatalf("twitter/x should match: %v", err)
	}
	if err := ValidatePlatform("", Target{Platform: PlatformX}); err != nil {
		t.Fatalf("empty role platform should pass: %v", err)
	}
	if err := ValidatePlatform("weibo", Target{Platform: PlatformX}); err == nil {
		t.Fatal("weibo/x should conflict")
	}
	var pc *PlatformConflictError
	if err := ValidatePlatform("weibo", Target{Platform: PlatformX}); !errors.As(err, &pc) {
		t.Fatalf("want PlatformConflictError, got %T", err)
	}
}

func TestCandidatesInspectOnlyInspectors(t *testing.T) {
	r := newTestRegistry(t)
	inspectInfo := Info{
		ID: "inspector", Name: "inspector",
		Domains:   []Domain{DomainFeed},
		Platforms: []Platform{PlatformX},
		Kinds:     map[Platform][]TargetKind{PlatformX: {KindProfile}},
		Needs:     map[Platform][]CredentialKey{PlatformX: {CredAPIKey}},
		Modes:     []Mode{ModeDesktop, ModeWeb},
		Priority:  10,
	}
	if err := r.Register(fakeInspector{fakeAdapter: fakeAdapter{info: inspectInfo}}); err != nil {
		t.Fatal(err)
	}
	target := Target{Domain: DomainFeed, Platform: PlatformX, Kind: KindProfile, ID: "elonmusk"}
	channels := []Channel{
		{ID: "ch-social", AdapterID: "socialdata", Enabled: true, Credentials: Credentials{APIKey: "k"}},
		{ID: "ch-inspect", AdapterID: "inspector", Enabled: true, Credentials: Credentials{APIKey: "k"}},
	}
	cands, rejected, err := r.Candidates(ResolveRequest{
		Domain: DomainFeed, Target: target, Mode: ModeWeb, Intent: IntentInspect,
	}, channels)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Channel.ID != "ch-inspect" {
		t.Fatalf("want only inspector candidate, got %+v", cands)
	}
	if len(rejected) != 1 || rejected[0].ChannelID != "ch-social" {
		t.Fatalf("want socialdata rejected, got %+v", rejected)
	}
}

func TestCandidatesExplicitChannelIsNotFallback(t *testing.T) {
	r := newTestRegistry(t)
	target := Target{Domain: DomainFeed, Platform: PlatformX, Kind: KindProfile, ID: "elonmusk"}
	channels := []Channel{
		{ID: "ch-social", AdapterID: "socialdata", Enabled: true, Credentials: Credentials{APIKey: "k"}},
	}
	cands, rejected, err := r.Candidates(ResolveRequest{
		Domain: DomainFeed, Target: target, Mode: ModeWeb, Intent: IntentInspect, ChannelID: "ch-social",
	}, channels)
	if err == nil || len(cands) != 0 {
		t.Fatalf("explicit non-inspector should fail without candidates: %+v %v", cands, err)
	}
	if len(rejected) != 1 || rejected[0].Reason != "适配器不支持账号详情识别" {
		t.Fatalf("unexpected rejection: %+v", rejected)
	}
}

func TestCandidatesMax(t *testing.T) {
	r := newTestRegistry(t)
	target := Target{Domain: DomainFeed, Platform: PlatformX, Kind: KindProfile, ID: "elonmusk"}
	channels := []Channel{
		{ID: "ch-1", AdapterID: "socialdata", Enabled: true, Credentials: Credentials{APIKey: "k"}},
		{ID: "ch-2", AdapterID: "chrome_driverless", Enabled: true, Credentials: Credentials{BaseURL: "http://x"}},
	}
	// chrome_driverless 是 web-only，两者都通过；只取一个。
	cands, _, err := r.Candidates(ResolveRequest{
		Domain: DomainFeed, Target: target, Mode: ModeWeb, MaxCandidates: 1,
	}, channels)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("want 1 candidate, got %d", len(cands))
	}
}
