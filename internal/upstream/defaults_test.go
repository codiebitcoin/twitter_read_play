package upstream

import (
	"testing"

	"twitter_read_play/internal/store"
)

func TestEnsureDefaultsSeedsRSS(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	list := m.List()
	if len(list) != 1 {
		t.Fatalf("want 1 default upstream, got %d", len(list))
	}
	if list[0].API != "rss" || list[0].Platform != "rss" || !list[0].Enabled {
		t.Fatalf("default upstream: %+v", list[0])
	}

	// 第二次启动不重复插入
	m2, err := NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(m2.List()); n != 1 {
		t.Fatalf("second boot duplicated defaults: %d", n)
	}
}

func TestEnsureDefaultsKeepsExistingRSS(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	u := m.List()[0]
	u.Name = "my rss"
	u.BaseURL = "https://rsshub.app"
	if err := m.Upsert(u); err != nil {
		t.Fatal(err)
	}
	m2, err := NewManager(st)
	if err != nil {
		t.Fatal(err)
	}
	got := m2.List()
	if len(got) != 1 || got[0].Name != "my rss" || got[0].BaseURL != "https://rsshub.app" {
		t.Fatalf("custom rss overwritten: %+v", got)
	}
}
