package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	clt01 "workshop_inner_sdk"
)

// ---- 设备播报状态（队列靠它实现「一条放完再放下一条」）----
// POST /chat/text 与 POST /tts_audition 都是「设备受理即返回」，**不等音频放完**
// （chat/text 只回一个 chat_id）。所以必须轮询设备状态才能知道真的播完了。
//
// tts 模式复用 voice.go 试听探测那套：auditionStatePath / auditionState / auditionVerdict；
// chat 模式查对话状态机 GET /v1/api/data/user/conversation/state（Idle/Recording/Thinking/Working/Talking/Error）。
//
// 等待策略（不再全程 500ms 盲轮询）：
//  1. 先按「文字长度 × 时间常数」估一个播放时长，安静睡到估算点（不发状态请求）；
//  2. 到点后 check 一次；没播完再降频轮询（playbackPollIdle）；
//  3. 每条播报打一行 [playback] 日志：字数 / 估算 / 实测 / 结束原因，
//     用它回归校正 playbackEstOffset / playbackEstPerRune 两个常数。

const (
	playbackPollInterval = 500 * time.Millisecond
	playbackPollIdle     = 1 * time.Second       // 估算时长之后（大概率已播完或接近播完）的降频轮询
	playbackStartGrace   = 5 * time.Second       // 迟迟没进入播放态（设备拒绝/无音频）就别干等
	playbackMaxWait      = 3 * time.Minute       // 单条播报等待上限
	playbackEstOffset    = 2 * time.Second       // 估算：起播延迟常数（LLM/TTS 合成 + 设备出声前固定开销）
	playbackEstPerRune   = 60 * time.Millisecond // 估算：每字符朗读时长（中文/英文混合经验值，靠日志校正）
)

// estimatePlaybackDuration 按文字长度估算播放总时长（下限 0，上限留 1s 给收尾 check）。
func estimatePlaybackDuration(text string) time.Duration {
	n := len([]rune(strings.TrimSpace(text)))
	if n <= 0 {
		return 0
	}
	d := playbackEstOffset + time.Duration(n)*playbackEstPerRune
	if max := playbackMaxWait - time.Second; d > max {
		d = max
	}
	return d
}

// conversationState 对话状态机快照（chat 模式）。
type conversationState struct {
	State       string `json:"state"`
	UpdatedAtMs int64  `json:"updated_at_ms"`
}

// playbackBaseline 播放前取一次状态时间戳：用于区分「本次播放」与「上一次残留的 ENDED」。
// 取不到（旧固件没有该接口）返回 0，调用方据此退化为「受理即认为播完」。
func playbackBaseline(ctx context.Context, c *clt01.Client, speakMode string) int64 {
	if speakMode == "chat" {
		var st conversationState
		if raw, err := c.ConversationState(ctx); err == nil {
			_ = json.Unmarshal(raw, &st)
		}
		return st.UpdatedAtMs
	}
	if speakMode == "tts-utterances" {
		// 独立文本播报每次提交都有独立 utterance_id，按任务状态机判定，无需共享基线
		return 0
	}
	var st auditionState
	if raw, err := c.TTSAuditionState(ctx); err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st.UpdatedAtMs
}

// watchDeviceStop 播报期间监听 ctx：ctx 取消（用户停止）时尽力让设备立刻停声。
// chat 通道取消会话；tts-utterances 取消朗读任务；<1.5.3 的 tts_audition 没有取消接口，
// 只能由宿主停止等待（剩余音频会在设备上自然放完）。返回的函数用于停止监听。
func watchDeviceStop(ctx context.Context, c *clt01.Client, mode, chatID, utteranceID string) func() {
	done := make(chan struct{})
	var once sync.Once
	go func() {
		select {
		case <-done:
			return
		case <-ctx.Done():
		}
		switch mode {
		case "chat":
			if chatID != "" {
				if err := c.CancelChat(chatID); err != nil {
					log.Printf("[playback] cancel chat %s: %v", chatID, err)
				}
			}
		case "tts-utterances":
			if utteranceID != "" {
				if err := c.TTSUtteranceCancel(context.Background(), utteranceID); err != nil {
					log.Printf("[playback] cancel tts utterance %s: %v", utteranceID, err)
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

// playbackOutcome 播报等待结论（队列/前端据此区分「真播完」与「设备报错」）。
type playbackOutcome struct {
	OK      bool   // 明确播完：tts ENDED 且时间戳前进 / chat 进过 Talking 又离开
	Failed  bool   // 设备明确报错：FAILED / ERROR / FAILED_PLAYBACK
	Stopped bool   // 用户手动停止（ctx 取消）：不算失败
	Reason  string // ended | stopped | device-failed | device-error | start-grace | poll-error | timeout
	State   string // 最后观察到的设备状态
}

// errPlaybackStopped 用户手动停止播报（队列据此标记为正常结束，不打失败日志）。
var errPlaybackStopped = errors.New("playback stopped")

// sleepCtx 可中断的 sleep：ctx 取消时立刻返回 false。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// errMsg 给队列/前端展示的失败描述。
func (o playbackOutcome) errMsg() string {
	switch o.Reason {
	case "device-error":
		return fmt.Sprintf("设备报播放错误，未出声（last_state=%s）", o.State)
	case "device-failed":
		return fmt.Sprintf("设备播报失败，未出声（last_state=%s）", o.State)
	default:
		return fmt.Sprintf("播报未确认（reason=%s, last_state=%s）", o.Reason, o.State)
	}
}

// playbackTag 播放日志前缀：机器/槽位/角色 + 通道 + 字数。
// 一次播报会扇出到多台机器、两条通道，没有这个前缀根本分不清是哪台在失败。
func playbackTag(cc *clt01.CallContext, speakMode string, chars int) string {
	machine := cc.MachineID
	if machine == "" {
		machine = cc.MachineName
	}
	if machine == "" {
		machine = "-"
	}
	char := cc.CharacterName
	if char == "" {
		char = "-"
	}
	return fmt.Sprintf("[playback] machine=%s slot=%d char=%s mode=%s chars=%d",
		machine, cc.Slot, char, speakMode, chars)
}

// waitPlaybackDone 等这次播报真正放完：
//
//	tts : STARTING/WAITING/PLAYING → ENDED（updated_at_ms 前进）/ FAILED
//	tts-utterances : queued/synthesizing/playing/cancelling → completed / failed / cancelled
//	chat: 进过 Thinking/Talking 后再回 Idle
//
// 先睡估算时长（见文件头注释）再轮询；但首查不晚于 playbackStartGrace，
// 否则 chat 通道会「醒来时已经播完」，只剩 Idle —— 既看不出播过，也分不出没播。
// 状态接口不可用 → 立刻返回（退化为「受理即认为播完」），绝不把队列卡死。
func waitPlaybackDone(ctx context.Context, c *clt01.Client, cc *clt01.CallContext, speakMode string, baselineMs int64, text string, utteranceID string) playbackOutcome {
	start := time.Now()
	chars := len([]rune(strings.TrimSpace(text)))
	est := estimatePlaybackDuration(text)
	tag := playbackTag(cc, speakMode, chars)
	out := playbackOutcome{Reason: "ended"}

	// 1) 安静睡到估算点，但最多先睡 playbackStartGrace：留出观察「起播」的窗口
	wait := est
	if wait > playbackStartGrace {
		wait = playbackStartGrace
	}
	if !sleepCtx(ctx, wait-time.Since(start)) {
		out.Reason, out.Stopped = "stopped", true
		return out
	}

	// 2) 降频轮询 check
	active := false
	verdict := false
	last := ""
	interval := playbackPollIdle
	for time.Since(start) < playbackMaxWait {
		if !sleepCtx(ctx, interval) {
			out.Reason, out.Stopped = "stopped", true
			break
		}
		done := false
		if speakMode == "chat" {
			raw, err := c.ConversationState(ctx)
			if err != nil {
				out.Reason = "poll-error"
				break
			}
			var st conversationState
			if json.Unmarshal(raw, &st) != nil {
				out.Reason = "poll-error"
				break
			}
			st.State = strings.ToUpper(strings.TrimSpace(st.State))
			if st.State != last {
				log.Printf("%s state t=%s state=%s", tag, time.Since(start).Round(time.Millisecond), st.State)
				last = st.State
			}
			switch st.State {
			case "THINKING", "WORKING", "TALKING":
				active = true // 会话已开始（LLM 思考也算，别误判成没起播）
			case "ERROR", "FAILED", "FAILED_PLAYBACK":
				out.Reason, done = "device-error", true
			}
			if active && !done && st.State != "TALKING" && st.State != "THINKING" && st.State != "WORKING" {
				done = true // 起播且已回到 Idle/终态 → 播完
			}
		} else if speakMode == "tts-utterances" && utteranceID != "" {
			// 独立文本播报（≥1.5.3）：按任务状态机轮询，终态即结论
			raw, err := c.TTSUtteranceState(ctx, utteranceID)
			if err != nil {
				out.Reason = "poll-error"
				break
			}
			var st clt01.TTSUtteranceStatus
			if json.Unmarshal(raw, &st) != nil {
				out.Reason = "poll-error"
				break
			}
			s := strings.ToLower(strings.TrimSpace(st.State))
			if s != last {
				log.Printf("%s state t=%s state=%s", tag, time.Since(start).Round(time.Millisecond), s)
				last = s
			}
			switch s {
			case "completed":
				out.State = s
				out.OK = true
				verdict = true
			case "failed", "cancelled":
				out.Reason, out.State, done = "device-failed", s, true
			case "cancelling", "queued", "synthesizing", "playing":
				active = true
			}
			if verdict || done {
				break
			}
		} else {
			raw, err := c.TTSAuditionState(ctx)
			if err != nil {
				out.Reason = "poll-error"
				break
			}
			var st auditionState
			if json.Unmarshal(raw, &st) != nil {
				out.Reason = "poll-error"
				break
			}
			s := strings.ToUpper(strings.TrimSpace(st.State))
			if s != last {
				log.Printf("%s state t=%s state=%s", tag, time.Since(start).Round(time.Millisecond), s)
				last = s
			}
			if d, ok := auditionVerdict(baselineMs, st); d {
				done = true
				if !ok {
					out.Reason = "device-failed" // FAILED/ERROR：设备合成/播放失败，不是「播完了」
				}
			} else {
				switch s {
				case "STARTING", "WAITING", "PLAYING":
					active = true
				}
			}
		}
		if done {
			verdict = true
			break
		}
		if !active && time.Since(start) > playbackStartGrace {
			out.Reason = "start-grace" // 没进入播放态（设备拒绝/无音频），别干等到超时
			break
		}
	}
	if !verdict && out.Reason == "ended" && time.Since(start) >= playbackMaxWait {
		out.Reason = "timeout"
	}
	out.State = last
	out.Failed = out.Reason == "device-failed" || out.Reason == "device-error"
	out.OK = verdict && out.Reason == "ended"

	actual := time.Since(start)
	if !out.OK {
		log.Printf("%s wait aborted est=%s actual=%s reason=%s last_state=%s",
			tag, est.Round(time.Millisecond), actual.Round(time.Millisecond), out.Reason, last)
	}
	log.Printf("%s wait est=%s actual=%s last_state=%s reason=%s ok=%v",
		tag, est.Round(time.Millisecond), actual.Round(time.Millisecond), last, out.Reason, out.OK)
	return out
}
