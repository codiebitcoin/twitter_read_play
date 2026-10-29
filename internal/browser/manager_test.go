package browser

import (
	"errors"
	"testing"
	"time"

	"twitter_read_play/internal/store"
)

// 登录态快照要跨进程保留：重启后先展示上次结果，而不是回到「未登录」。
func TestStatusPersistsAcrossRestart(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(nil, st)
	if got := m.Status(ProviderWeibo); got.Known {
		t.Fatalf("fresh manager should be unknown, got %+v", got)
	}

	at := time.Now().Truncate(time.Second)
	m.setStatus(ProviderWeibo, Status{
		Known: true, Connected: true, Account: "42", LastCheck: at,
	})

	m2 := NewManager(nil, st)
	got := m2.Status(ProviderWeibo)
	if !got.Known || !got.Connected || got.Account != "42" {
		t.Fatalf("status not restored: %+v", got)
	}
	if !got.LastCheck.Equal(at) {
		t.Fatalf("last_check not restored: %v", got.LastCheck)
	}
}

func TestApplyProbeResult(t *testing.T) {
	now := time.Now()
	knownIn := Status{Provider: ProviderX, Known: true, Connected: true, Account: "@me"}

	t.Run("inconclusive keeps last connected", func(t *testing.T) {
		got := applyProbeResult(knownIn, map[string]any{
			"checked": false, "connected": false, "error": "Load failed",
		}, nil, now)
		if !got.Known || !got.Connected || got.Account != "@me" {
			t.Fatalf("inconclusive probe should keep last status: %+v", got)
		}
		if got.Error != "Load failed" {
			t.Fatalf("error not updated: %+v", got)
		}
	})

	t.Run("definitive logged out wins", func(t *testing.T) {
		got := applyProbeResult(knownIn, map[string]any{
			"checked": true, "connected": false, "error": "HTTP 401",
		}, nil, now)
		if !got.Known || got.Connected {
			t.Fatalf("checked=false should mark logged out: %+v", got)
		}
	})

	t.Run("script error keeps last status", func(t *testing.T) {
		got := applyProbeResult(knownIn, nil, errors.New("page timeout"), now)
		if !got.Connected || got.Account != "@me" {
			t.Fatalf("probe error should keep last status: %+v", got)
		}
	})

	t.Run("optimistic hint only when unknown", func(t *testing.T) {
		got := applyProbeResult(Status{Provider: ProviderX}, map[string]any{
			"checked": false, "connected": true, "account": "@me",
		}, nil, now)
		if !got.Known || !got.Connected {
			t.Fatalf("first-seen optimistic hint should be accepted: %+v", got)
		}
	})
}

func TestLoginStateMachine(t *testing.T) {
	now := time.Now()
	prev := Status{Provider: ProviderInstagram, Known: true, Connected: true, Account: "@me"}

	expired := applyProbeResult(prev, map[string]any{"checked": true, "connected": false}, nil, now)
	if expired.LoginState() != StateExpired {
		t.Fatalf("logged-in -> logged-out should be expired, got %q", expired.LoginState())
	}

	// 再次探测仍未登录：过期标记保留（直到重新登录或主动退出）
	again := applyProbeResult(expired, map[string]any{"checked": true, "connected": false}, nil, now)
	if again.LoginState() != StateExpired {
		t.Fatalf("expired should stick, got %q", again.LoginState())
	}

	// 重新登录成功：回到 connected，过期标记清掉
	back := applyProbeResult(again, map[string]any{"checked": true, "connected": true, "account": "@me"}, nil, now)
	if back.LoginState() != StateConnected {
		t.Fatalf("re-login should clear expired, got %q", back.LoginState())
	}

	// 从未检测过：unknown，不显示状态
	if got := (Status{Provider: ProviderInstagram}).LoginState(); got != StateUnknown {
		t.Fatalf("fresh status should be unknown, got %q", got)
	}

	// 明确未登录且从未登录过：disconnected
	if got := (Status{Provider: ProviderInstagram, Known: true}).LoginState(); got != StateDisconnected {
		t.Fatalf("known logged-out should be disconnected, got %q", got)
	}
}
