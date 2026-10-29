package watch

import (
	"testing"
	"time"
)

func TestEstimatePlaybackDuration(t *testing.T) {
	if got := estimatePlaybackDuration(""); got != 0 {
		t.Fatalf("空文本应为 0，got %s", got)
	}
	short := estimatePlaybackDuration("hi")
	long := estimatePlaybackDuration(string(make([]rune, 500)))
	if short <= 0 || long <= short {
		t.Fatalf("估时应随长度增长：short=%s long=%s", short, long)
	}
	huge := estimatePlaybackDuration(string(make([]rune, 1<<20)))
	if huge > playbackMaxWait-time.Second {
		t.Fatalf("估时应封顶在 %s 内，got %s", playbackMaxWait-time.Second, huge)
	}
}
