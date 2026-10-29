package watch

import (
	"context"
	"errors"
	"testing"

	"twitter_read_play/internal/model"
)

// stubProvider 固定返回一条推文（Text 用于区分是哪个 provider 被调用）。
type stubProvider struct {
	name  string
	text  string
	calls int
	err   error
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) FetchLatest(ctx context.Context, screenName string, count int) ([]model.Tweet, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return []model.Tweet{{ID: "1", User: screenName, Text: s.text, Source: "real"}}, nil
}

// 回归：sharedFetch 必须用「按角色解析出来的 provider」，不能拿 Manager 上那个兜底
// socialdata provider。早期版本忽略了解析结果，导致微博/小红书角色被当成 twitter 拉取
// （线上表现为「解析用户 … 失败 / socialdata.tools: HTTP 502」）。
func TestSharedFetchUsesGivenProvider(t *testing.T) {
	fallback := &stubProvider{name: "socialdata", text: "from-fallback"}
	perRole := &stubProvider{name: "socialread:weibo", text: "from-role"}

	m := &Manager{provider: fallback}

	tweets, err := m.sharedFetch(context.Background(), perRole, "socialread:weibo|u-1|weibo|人民日报", 0, "人民日报", 20)
	if err != nil {
		t.Fatal(err)
	}
	if perRole.calls != 1 {
		t.Fatalf("应按角色的 provider 拉取，实际调用次数 %d", perRole.calls)
	}
	if fallback.calls != 0 {
		t.Fatalf("不该走兜底 provider，实际被调用 %d 次", fallback.calls)
	}
	if len(tweets) != 1 || tweets[0].Text != "from-role" {
		t.Fatalf("结果应来自按角色的 provider，实际 %+v", tweets)
	}
}

// p 为 nil 时才退回兜底（老调用点兼容）。
func TestSharedFetchFallsBackWhenProviderNil(t *testing.T) {
	fallback := &stubProvider{name: "socialdata", text: "from-fallback"}
	m := &Manager{provider: fallback}
	tweets, err := m.sharedFetch(context.Background(), nil, "k", 0, "elonmusk", 20)
	if err != nil {
		t.Fatal(err)
	}
	if fallback.calls != 1 || len(tweets) != 1 || tweets[0].Text != "from-fallback" {
		t.Fatalf("nil provider 应退回兜底，实际 calls=%d tweets=%+v", fallback.calls, tweets)
	}
}

// 失败路径不该写进缓存（下一次调用要重新打上游）。
func TestSharedFetchErrorNotCached(t *testing.T) {
	p := &stubProvider{name: "socialread:weibo", err: errors.New("boom")}
	m := &Manager{provider: &stubProvider{name: "socialdata"}}
	for i := 0; i < 2; i++ {
		if _, err := m.sharedFetch(context.Background(), p, "k2", 0, "x", 20); err == nil {
			t.Fatal("应返回错误")
		}
	}
	if p.calls != 2 {
		t.Fatalf("失败不该被缓存，应调用 2 次，实际 %d", p.calls)
	}
}
