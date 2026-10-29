package browser

import (
	"context"
	"strings"
	"testing"
)

// Bluesky 没有服务端登出接口：会话在 localStorage，退出时必须主动清掉，
// 否则销毁窗口后下一次探测又会把旧会话读回来。
func TestBlueskyLogoutClearsLocalSession(t *testing.T) {
	d := &loginCloseDriver{available: true}
	m := NewManager(d, nil)
	if _, err := m.ensureWork(context.Background(), ProviderBluesky); err != nil {
		t.Fatal(err)
	}
	work := d.work
	if work == nil {
		t.Fatal("work window not opened")
	}
	if err := m.Logout(context.Background(), ProviderBluesky); err != nil {
		t.Fatal(err)
	}
	if !work.destroyed {
		t.Fatal("work window should be destroyed after logout")
	}
	cleared := false
	for _, code := range work.evals {
		if strings.Contains(code, "BSKY_STORAGE") && strings.Contains(code, "removeItem") {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("logout did not clear BSKY_STORAGE (evals=%v)", work.evals)
	}
	if st := m.Status(ProviderBluesky); !st.Known || st.Connected {
		t.Fatalf("status after logout should be known/disconnected: %+v", st)
	}
}

func TestBlueskyProviderConfig(t *testing.T) {
	cfg, ok := configFor(ProviderBluesky)
	if !ok {
		t.Fatal("bluesky provider not registered")
	}
	if cfg.LoginURL == "" || cfg.HomeURL == "" || cfg.ProbeJS == "" || cfg.LogoutJS == "" {
		t.Fatalf("incomplete bluesky config: %+v", cfg)
	}
	if !cfg.LoginOptional {
		t.Fatal("bluesky public data needs no login; LoginOptional should be true")
	}
	if !ValidProvider("bluesky") {
		t.Fatal("ValidProvider(bluesky) should be true")
	}
	found := false
	for _, p := range AllProviders() {
		if p == ProviderBluesky {
			found = true
		}
	}
	if !found {
		t.Fatal("bluesky missing from AllProviders (frontend login bar builds from it)")
	}
	if got := NewManager(nil, nil).Status(ProviderBluesky); !got.LoginOptional {
		t.Fatalf("status should expose login_optional for the UI: %+v", got)
	}
}
