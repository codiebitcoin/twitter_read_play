package watch

import (
	"testing"
	"time"

	"twitter_read_play/internal/livechat"
	"twitter_read_play/internal/machine"
	"twitter_read_play/internal/model"
	"twitter_read_play/internal/role"
	"twitter_read_play/internal/store"
)

func TestLivePassIndependentFromFeedFilters(t *testing.T) {
	msg := livechat.Message{User: "alice", Text: "hello mars", Kind: "text"}
	// Feed 排除词不应挡住 Stream
	r := &model.Role{FilterWords: "hello", RequireWords: "zzz"}
	if !livePass(r, msg) {
		t.Fatal("Feed FilterWords/RequireWords must not apply to Stream")
	}
	r.LiveFilterWords = "hello"
	if livePass(r, msg) {
		t.Fatal("LiveFilterWords should exclude")
	}
	r.LiveFilterWords = ""
	r.LiveRequireWords = "jupiter"
	if livePass(r, msg) {
		t.Fatal("LiveRequireWords miss should exclude")
	}
	r.LiveRequireWords = "mars"
	if !livePass(r, msg) {
		t.Fatal("LiveRequireWords hit should pass")
	}
}

func TestLivePassOnlySC(t *testing.T) {
	r := &model.Role{LiveOnlySC: true}
	if livePass(r, livechat.Message{Text: "hi", Kind: "text"}) {
		t.Fatal("plain chat must be skipped when LiveOnlySC")
	}
	if !livePass(r, livechat.Message{Text: "thanks", Kind: "superchat", Amount: "$5"}) {
		t.Fatal("superchat should pass")
	}
	if !livePass(r, livechat.Message{Text: "bits", Kind: "cheer", Amount: "100 bits"}) {
		t.Fatal("cheer should pass")
	}
	if !livePass(r, livechat.Message{Text: "", Kind: "gift"}) {
		t.Fatal("gift kind should pass even without amount")
	}
}

// 黑名单用户：用户名精确匹配（忽略大小写），命中即不播；SC 也挡。
func TestLivePassBlockUsers(t *testing.T) {
	r := &model.Role{LiveBlockUsers: "spam_王, Troll"}
	if livePass(r, livechat.Message{User: "spam_王", Text: "你好"}) {
		t.Fatal("blacklisted user must be dropped")
	}
	if livePass(r, livechat.Message{User: "TROLL", Text: "hello", Kind: "superchat", Amount: "$5"}) {
		t.Fatal("blacklist is case-insensitive and applies to SC too")
	}
	if !livePass(r, livechat.Message{User: "alice", Text: "spam_王 说的不对"}) {
		t.Fatal("mentioned name in text is not a blacklist hit")
	}
}

// 过滤SC：与「仅SC」相对——打赏类一律不播，普通弹幕照常。
func TestLivePassNoSC(t *testing.T) {
	r := &model.Role{LiveNoSC: true}
	if livePass(r, livechat.Message{User: "a", Text: "感谢老板", Kind: "superchat", Amount: "¥30"}) {
		t.Fatal("superchat must be dropped when LiveNoSC")
	}
	if livePass(r, livechat.Message{User: "b", Text: "", Kind: "gift", Amount: "¥10"}) {
		t.Fatal("gift must be dropped when LiveNoSC")
	}
	if !livePass(r, livechat.Message{User: "c", Text: "normal chat"}) {
		t.Fatal("plain chat should pass when LiveNoSC")
	}
}

// 最低用户等级：带等级且低于阈值 → 滤掉；不带等级（0，如 Twitch/YouTube）→ 放行。
func TestLivePassMinLevel(t *testing.T) {
	r := &model.Role{LiveMinLevel: 10}
	if livePass(r, livechat.Message{User: "newbie", Text: "hi", UserLevel: 3}) {
		t.Fatal("below min level must be dropped")
	}
	if !livePass(r, livechat.Message{User: "veteran", Text: "hi", UserLevel: 20}) {
		t.Fatal("at/above min level should pass")
	}
	if !livePass(r, livechat.Message{User: "noLevelPlatform", Text: "hi"}) {
		t.Fatal("platforms without levels (0) must pass the level gate")
	}
}

func TestNewLiveTweet(t *testing.T) {
	tw := model.NewLiveTweet("bob", "nice", "$5", "youtube_api", 9)
	if tw.Source != model.LiveSource {
		t.Fatalf("source = %q", tw.Source)
	}
	if tw.ID != "live:youtube_api:9" {
		t.Fatalf("id = %q", tw.ID)
	}
	if tw.Text != "bob $5 nice" {
		t.Fatalf("text = %q", tw.Text)
	}
}

type fakeLive struct {
	seq  int64
	msgs []livechat.Message
}

func (f *fakeLive) Messages(_ string, after, before int64, _ int) ([]livechat.Message, bool) {
	var out []livechat.Message
	for _, m := range f.msgs {
		if before > 0 && m.Seq >= before {
			continue
		}
		if m.Seq > after {
			out = append(out, m)
		}
	}
	return out, false
}

func (f *fakeLive) LatestSeq(string) int64 { return f.seq }

func TestConsumeLiveSkipsBacklogThenSpeaks(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rm, err := role.NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	mm, err := machine.NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := mm.Add("desk", "127.0.0.1:8081", "")
	if err != nil {
		t.Fatal(err)
	}
	r := &model.Role{
		ID: "r-live", Name: "L", LiveEnabled: true, LiveSourceID: "s1", LiveInterval: 1,
		Bindings: []model.Binding{{MachineID: mc.ID, Slot: 1, Enabled: true}},
	}
	if err := rm.Upsert(r); err != nil {
		t.Fatal(err)
	}
	m := &Manager{st: st, roles: rm, machines: mm, live: &fakeLive{
		seq: 3,
		msgs: []livechat.Message{
			{Seq: 1, User: "a", Text: "old", Kind: "text", Source: "youtube_api"},
			{Seq: 3, User: "b", Text: "new", Kind: "text", Source: "youtube_api"},
		},
	}}
	m.queue().exec = func(*QueueItem) error { return nil }

	got, _ := m.roles.Get("r-live")
	m.consumeLive(got)
	got, _ = m.roles.Get("r-live")
	if got.LiveLastSeq != 3 || got.LiveLastAt.IsZero() {
		t.Fatalf("first consume should skip backlog, cursor=%d at=%v", got.LiveLastSeq, got.LiveLastAt)
	}
	if n := len(m.queue().Snapshot()); n != 0 {
		t.Fatalf("backlog should not enqueue, got %d", n)
	}

	m.live = &fakeLive{seq: 4, msgs: []livechat.Message{
		{Seq: 4, User: "c", Text: "fresh", Kind: "text", Source: "youtube_api"},
	}}
	got, _ = m.roles.Get("r-live")
	got.LiveLastAt = time.Now().Add(-2 * time.Second)
	if err := rm.Upsert(got); err != nil {
		t.Fatal(err)
	}
	got, _ = m.roles.Get("r-live")
	m.consumeLive(got)
	deadline := time.Now().Add(2 * time.Second)
	var snap []QueueItem
	for time.Now().Before(deadline) {
		snap = m.queue().Snapshot()
		if len(snap) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(snap) != 1 {
		t.Fatalf("expected 1 live enqueue, got %d", len(snap))
	}
	if snap[0].Source != "live" || snap[0].TweetID != "live:youtube_api:4" {
		t.Fatalf("queued %+v", snap[0])
	}
}
