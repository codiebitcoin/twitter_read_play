package watch

import "testing"

// TestRecordedVoiceKeep 已记录克隆的处置决策：探测开启时「能播就沿用」，
// 尤其是「设备没登记但实测能播」必须沿用（否则会白克隆一次）。
func TestRecordedVoiceKeep(t *testing.T) {
	cases := []struct {
		name          string
		probe, exists bool
		playable      bool
		want          bool
	}{
		{"探测+能播+设备已登记 → 沿用", true, true, true, true},
		{"探测+能播+设备没登记 → 仍沿用（关键：不白克隆）", true, false, true, true},
		{"探测+播不出来 → 重克隆", true, true, false, false},
		{"探测+播不出来+没登记 → 重克隆", true, false, false, false},
		{"不探测+设备已登记 → 沿用（原有快速路径）", false, true, true, true},
		{"不探测+设备没登记 → 重克隆（原有行为）", false, false, true, false},
	}
	for _, c := range cases {
		if got := recordedVoiceKeep(c.probe, c.exists, c.playable); got != c.want {
			t.Errorf("%s: recordedVoiceKeep(probe=%v, exists=%v, playable=%v) = %v, 期望 %v",
				c.name, c.probe, c.exists, c.playable, got, c.want)
		}
	}
}

// TestAuditionVerdict 覆盖试听状态机判定：只有「本次」试听真正播完才算可播，
// FAILED 才允许触发重克隆，未结束/旧状态一律继续等待（避免误判 → 误重克隆）。
func TestAuditionVerdict(t *testing.T) {
	const prev = int64(1000)
	cases := []struct {
		name    string
		state   string
		updated int64
		done    bool
		ok      bool
	}{
		{"waiting 继续等", "WAITING", 2000, false, false},
		{"playing 继续等", "PLAYING", 2000, false, false},
		{"ended 且时间前进=本次播完", "ENDED", 2000, true, true},
		{"ended 但时间未变=上一次的结果，不能当本次成功", "ENDED", prev, false, false},
		{"failed=明确不可播", "FAILED", 2000, true, false},
		{"error 也算不可播", "ERROR", 2000, true, false},
		{"大小写与空格容错", " ended ", 2000, true, true},
		{"未知状态不误判", "SOMETHING_NEW", 2000, false, false},
		{"空状态不误判", "", 2000, false, false},
	}
	for _, c := range cases {
		done, ok := auditionVerdict(prev, auditionState{State: c.state, UpdatedAtMs: c.updated})
		if done != c.done || ok != c.ok {
			t.Errorf("%s: auditionVerdict(%q, ts=%d) = (done=%v, ok=%v), 期望 (done=%v, ok=%v)",
				c.name, c.state, c.updated, done, ok, c.done, c.ok)
		}
	}
}

// TestAuditionVerdictOldEndedThenFailed 关键回归：上一轮 ENDED 的残留状态不能掩盖本轮 FAILED。
func TestAuditionVerdictOldEndedThenFailed(t *testing.T) {
	const prev = int64(5000)
	// 轮询刚开始读到的是上一轮遗留的 ENDED（updated_at_ms 未变）→ 必须继续等
	if done, _ := auditionVerdict(prev, auditionState{State: "ENDED", UpdatedAtMs: prev}); done {
		t.Fatal("残留 ENDED 被误判为本次成功")
	}
	// 随后设备给出本轮结果 FAILED → 判为不可播，允许重克隆
	done, ok := auditionVerdict(prev, auditionState{State: "FAILED", UpdatedAtMs: prev + 900})
	if !done || ok {
		t.Fatalf("本轮 FAILED 未被识别：done=%v ok=%v", done, ok)
	}
}
