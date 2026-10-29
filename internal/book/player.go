package book

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/model"

	clt01 "workshop_inner_sdk"
)

// ---- 设备朗读 ----
//
// 一段朗读与「推文播报」共用同一套设备语义（通道选择、角色声音校验、播完等待），但书是长文本：
//   - 分段：段落按句末标点切成 ≤ maxSegmentRunes 的朗读段（TTS 每段起播有开销，太碎反而慢）；
//   - 排队：同一台设备同一时刻只能念一段 —— 按 machineID 串行，入队立刻返回，后台逐段下发；
//   - 停止：执行体每段之间检查 run 是否被取消（前端「停止」= cancel）；
//   - 续读：每播完一段就写进度（章、段）与随机去重表，重启/断线后从断点继续。

const (
	maxSegmentRunes = 240 // 单段最长字符数（设备 TTS 一~两分钟一段，听感与等待都合适）
	playPollStep    = time.Second
	playStartGrace  = 8 * time.Second
	playMaxWait     = 5 * time.Minute // 单段等待上限（超长段落也可能到 240 字符 × 慢中文）
)

// Binding 朗读目标（设备 + 槽位 + 可选指定声音）。
type Binding struct {
	MachineID string
	Slot      int
	RoleID    string
	RoleName  string
	VoiceID   string
	GroupKey  string
}

// SpeakOpts 一次朗读的下发参数。
type SpeakOpts struct {
	Mode        string   // ""=auto（≥1.5.3 走 tts/utterances 直读，<1.5.3 走 tts_audition）；tts / chat（设备 LLM）
	Preface     string   // 段首提示语（如「Genesis, chapter 1」）
	Wrap        []string // 包装模板：{PARA} 占位（如「请用缓慢的语调朗读以下经文：\n{PARA}」）
	Language    string
	ForceVoice  bool // 忽略 Binding.VoiceID，用设备当前音色（未克隆时）
	RolePrompt  string
	MachineName string
}

// SpeakResult 一段的下发结果。
type SpeakResult struct {
	Channel string
	Chars   int
	Dur     time.Duration
}

// Player 设备朗读器：持有 client 工厂与「当前播放」状态。由 watch.Manager 提供所需能力。
type Player struct {
	Client func(machineID string) (*clt01.Client, error)
	// DeviceLocale 设备 AI 语言（chat 通道切语言用），可空。
	Logf func(format string, args ...any)

	mu   sync.Mutex
	runs map[string]*PlayRun // machineID → 当前播放
}

// PlayRun 一次连续的朗读（一章、一段或随机）；
// 以 machineID 为键，同一台设备同时只跑一个。
type PlayRun struct {
	ID        string
	RoleID    string // 发起朗读的角色（播放状态按角色隔离）
	RoleName  string
	MachineID string
	BookID    string
	BookTitle string
	Chapter   int
	Segment   int
	Text      string
	Total     int
	Mode      string // play|random
	Status    string // running | done | stopped | failed
	Error     string
	StartedAt time.Time
	UpdatedAt time.Time
	Pos       int // 已播到本组第几段（1-based，展示用）
	Segments  int // 本组总段数

	cancel context.CancelFunc
	done   chan struct{}
}

// MarshalJSON 播放状态（cancel/done 不序列化；字段统一小写下划线，与其他接口一致）。
func (r *PlayRun) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID        string    `json:"id"`
		RoleID    string    `json:"role_id,omitempty"`
		RoleName  string    `json:"role_name,omitempty"`
		MachineID string    `json:"machine_id"`
		BookID    string    `json:"book_id"`
		BookTitle string    `json:"book_title"`
		Chapter   int       `json:"chapter"`
		Segment   int       `json:"segment"`
		Text      string    `json:"text"`
		Total     int       `json:"total"`
		Mode      string    `json:"mode"`
		Status    string    `json:"status"`
		Error     string    `json:"error,omitempty"`
		Pos       int       `json:"pos"`
		Segments  int       `json:"segments"`
		Playing   bool      `json:"playing"`
		StartedAt time.Time `json:"started_at"`
		UpdatedAt time.Time `json:"updated_at"`
	}{
		ID: r.ID, RoleID: r.RoleID, RoleName: r.RoleName,
		MachineID: r.MachineID, BookID: r.BookID, BookTitle: r.BookTitle,
		Chapter: r.Chapter, Segment: r.Segment, Text: r.Text, Total: r.Total, Mode: r.Mode,
		Status: r.Status, Error: r.Error, Pos: r.Pos, Segments: r.Segments,
		Playing: r.Status == "running", StartedAt: r.StartedAt, UpdatedAt: r.UpdatedAt,
	})
}

// NewPlayer 创建朗读器。
func NewPlayer(clientFn func(machineID string) (*clt01.Client, error)) *Player {
	return &Player{Client: clientFn, runs: map[string]*PlayRun{}}
}

// RunView 前端轮询用的播放状态（内嵌 PlayRun，外加「是否仍在播」）。
type RunView struct {
	*PlayRun
	Playing bool `json:"playing"`
}

// Runs 当前/最近的播放状态（按机器）。返回副本，调用方改不到 Player 内部状态。
func (p *Player) Runs() map[string]RunView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]RunView, len(p.runs))
	for k, r := range p.runs {
		cp := *r
		out[k] = RunView{PlayRun: &cp, Playing: r.Status == "running"}
	}
	return out
}

// Cancel 停止某台机器上正在进行的朗读（返回是否真的停掉了）。
func (p *Player) Cancel(machineID string) bool {
	p.mu.Lock()
	r := p.runs[machineID]
	p.mu.Unlock()
	if r == nil || r.Status != "running" {
		return false
	}
	r.cancel()
	return true
}

// CancelFor 只停止 roleID 自己发起的朗读（播放状态按角色隔离，不能替别的角色停）。
func (p *Player) CancelFor(roleID, machineID string) bool {
	p.mu.Lock()
	r := p.runs[machineID]
	if r == nil || r.Status != "running" || r.RoleID != roleID {
		p.mu.Unlock()
		return false
	}
	p.mu.Unlock()
	r.cancel()
	return true
}

// ---- 朗读计划（纯函数，便于单测）----

// Plan 一段朗读的分段结果。
type Plan struct {
	Segments []string // 每段正文（已含提示语/包装）
	Raw      []string // 对应的原始正文（写进度/预览用）
}

// BuildPlan 把一章的若干段正文切成可下发的朗读段。
func BuildPlan(paras []string, opt SpeakOpts) Plan {
	var p Plan
	for _, para := range paras {
		// 每段先按句合并到 maxSegmentRunes，再按段首提示语 + 包装下发
		for _, piece := range SplitSentences(para, maxSegmentRunes) {
			if strings.TrimSpace(piece) == "" {
				continue
			}
			p.Raw = append(p.Raw, piece)
			p.Segments = append(p.Segments, renderSegment(piece, opt))
		}
	}
	return p
}

// renderSegment 段首提示语 + 包装模板。
// 包装模板里 {PARA} 是正文占位；没有占位符则模板在前、正文换行追加（与角色 SpeakTemplate 同语义）。
// renderSegment 段首提示语（章节标题）+ 包装模板。
// 包装模板里 {PARA} 是正文占位、{TITLE} 是章节提示语（已被提示语吸收时渲染为空串）；
// 没有占位符则模板在前、正文换行追加（与角色 SpeakTemplate 同语义）。
func renderSegment(raw string, opt SpeakOpts) string {
	preface := strings.TrimSpace(opt.Preface)
	out := raw
	// 模板会自己报章节（含 {TITLE}）时，正文里不再重复章名
	wrapHasTitle := false
	for _, w := range opt.Wrap {
		if strings.Contains(w, "{TITLE}") {
			wrapHasTitle = true
		}
	}
	if preface != "" && !wrapHasTitle {
		out = preface + " " + out
	}
	if len(opt.Wrap) > 0 {
		tpl := strings.Join(opt.Wrap, "\n")
		title := ""
		if wrapHasTitle {
			title = preface
		}
		tpl = strings.ReplaceAll(tpl, "{TITLE}", title)
		if strings.Contains(tpl, "{PARA}") {
			return strings.TrimSpace(strings.ReplaceAll(tpl, "{PARA}", out))
		}
		return tpl + "\n" + out
	}
	return out
}

// ChapterPreface 章节开场提示（「Genesis, chapter 1.」/「《创世记》第一章」），空标题不念。
func ChapterPreface(title, lang string) string {
	title = strings.Trim(strings.TrimSpace(title), "# ")
	if title == "" {
		return ""
	}
	if isChineseLang(lang) {
		return "《" + title + "》。"
	}
	if isJapaneseLang(lang) {
		return title + "。"
	}
	return title + "."
}

func isChineseLang(l string) bool {
	l = strings.ToLower(strings.TrimSpace(l))
	return strings.HasPrefix(l, "zh") || l == "chinese"
}

func isJapaneseLang(l string) bool {
	l = strings.ToLower(strings.TrimSpace(l))
	return strings.HasPrefix(l, "ja") || l == "japanese"
}

// PlayJob 一次朗读请求（由 watch.Manager 组装后交给 Player）。
type PlayJob struct {
	Binding Binding
	Opts    SpeakOpts
	// Plan 已渲染好的朗读段（每段一次设备下发）
	Plan Plan
	// Stage 进度回调：位置（1-based，1=开工、total+1=本组播完）、本组目标总数
	Stage func(pos, total int) error
	// StageTotal 本组目标个数（章播放 = 章内段落数；<=0 用朗读段数）
	StageTotal int
	// StopBetween 每段前调用：返回 true 则停止（前端取消 / 随机规则停止）
	StopBetween func() bool
	// Tag 日志前缀
	Tag string
}

// Start 启动一次朗读，立即返回 run 视图；执行在后台按机器串行。
// 如果该机器已在朗读，先取消旧的再开始新的（用户点「播放」= 要这一条）。
func (p *Player) Start(r *PlayRun, job PlayJob) RunView {
	p.mu.Lock()
	if old := p.runs[r.MachineID]; old != nil && old.Status == "running" {
		old.cancel()
		p.mu.Unlock()
		<-old.done
		p.mu.Lock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	r.Status, r.StartedAt, r.UpdatedAt = "running", time.Now(), time.Now()
	r.Segments = len(job.Plan.Segments)
	p.runs[r.MachineID] = r
	view := RunView{PlayRun: r, Playing: true}
	p.mu.Unlock()

	go func() {
		defer close(r.done)
		defer cancel()
		err := p.playLoop(ctx, r, job)
		p.mu.Lock()
		r.UpdatedAt = time.Now()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				r.Status = "stopped"
			} else {
				r.Status, r.Error = "failed", err.Error()
				log.Printf("[book] play failed machine=%s book=%s ch=%d: %v", r.MachineID, r.BookID, r.Chapter, err)
			}
		} else {
			r.Status = "done"
		}
		p.mu.Unlock()
	}()
	return view
}

// playLoop 逐段下发。
func (p *Player) playLoop(ctx context.Context, r *PlayRun, job PlayJob) error {
	c, err := p.Client(job.Binding.MachineID)
	if err != nil {
		return err
	}
	if job.Binding.Slot <= 0 {
		return fmt.Errorf("no target slot")
	}
	// total 是「本组要处理的目标个数」：章播放时 = 章内段落数（可少于朗读段数），单段播放时 = 1。
	// 进度按目标推进：一开工就把当前目标标成「已播」，整组播完再推进到下一个
	// —— 否则中途失败/停止时重播会从同一章第一段重新开始（听书用户最痛的点）。
	total := job.StageTotal
	if total <= 0 {
		total = len(job.Plan.Segments)
	}
	if job.Stage != nil {
		if err := job.Stage(1, total); err != nil {
			return err
		}
	}
	for i, seg := range job.Plan.Segments {
		if ctx.Err() != nil {
			return context.Canceled
		}
		if job.StopBetween != nil && job.StopBetween() {
			return nil
		}
		p.mu.Lock()
		r.Pos, r.Segment, r.Text, r.UpdatedAt = i+1, i, previewOf(job.Plan.Raw, i), time.Now()
		p.mu.Unlock()
		res, err := p.Speak(ctx, c, job.Binding, job.Opts, seg)
		if err != nil {
			if ctx.Err() != nil {
				return context.Canceled
			}
			return err
		}
		if p.Logf != nil && res.Chars > 0 {
			p.Logf("[book] spoke machine=%s slot=%d %s seg=%d/%d chars=%d took=%s channel=%s",
				job.Binding.MachineID, job.Binding.Slot, job.Tag, i+1, len(job.Plan.Segments), res.Chars, res.Dur.Round(time.Millisecond), res.Channel)
		}
	}
	if job.Stage != nil {
		_ = job.Stage(total+1, total) // 大于 total = 本组播完
	}
	return nil
}

// previewOf 取第 i 段前 60 字做队列/状态展示。
func previewOf(raw []string, i int) string {
	if i < len(raw) {
		return model.CleanText(raw[i], 60)
	}
	return ""
}

// Speak 下发一段并等它播完。mode：tts（设备直读文字，不调 LLM）/ chat（设备 LLM+TTS 管线）。
func (p *Player) Speak(ctx context.Context, c *clt01.Client, b Binding, opt SpeakOpts, text string) (SpeakResult, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return SpeakResult{}, errors.New("empty text")
	}
	// 设备 TTS 单次上限 16000 字符（tts/utterances）；防御性截断
	if r := []rune(text); len(r) > 16000 {
		text = string(r[:16000])
	}
	mode := strings.ToLower(strings.TrimSpace(opt.Mode))
	if mode == "" || mode == "auto" {
		mode = "tts"
	}
	if mode == "chat" && c.PredictPlayChatChannel() != clt01.ChannelChatTurns {
		mode = "tts" // <1.5.3 没有 chat 通道
	}
	cc := &clt01.CallContext{MachineID: b.MachineID, Slot: b.Slot, CharacterName: b.RoleName}
	if mode == "chat" {
		// chat 通道：一次性下发指令（包装已在 renderSegment 里带上），等会话回 Idle。
		// 「没等多久就结束」说明设备没真正起播（受理后立即闲置）：报错，别让队列/进度假装播完了。
		base, _ := conversationBase(ctx, c)
		opts := &clt01.PlayChatOptions{Language: opt.Language, VoiceID: b.VoiceID, GroupKey: b.GroupKey}
		start := time.Now()
		res, err := c.PlayChat(ctx, cc, text, opts)
		if err != nil {
			return SpeakResult{}, err
		}
		out := waitConversation(ctx, c, base)
		if ctx.Err() != nil {
			return SpeakResult{}, ctx.Err()
		}
		if !out.played && !out.failed {
			return SpeakResult{}, fmt.Errorf("device accepted the chat turn but never started speaking (state=%s) — try Read mode = TTS", out.state)
		}
		return SpeakResult{Channel: res.Channel, Chars: len([]rune(text)), Dur: time.Since(start)}, nil
	}
	// tts 通道：≥1.5.3 优先 tts/utterances（不调 LLM，适合逐字朗读长文）；
	// 失败（如设备该端点不可用）再退回 tts_audition。
	if c.TTSUtteranceAvailable() {
		req, err := c.TTSUtteranceCtx(ctx, b.Slot, text, "")
		if err == nil {
			start := time.Now()
			waitUtterance(ctx, c, req.UtteranceID)
			if ctx.Err() != nil {
				return SpeakResult{}, ctx.Err()
			}
			return SpeakResult{Channel: clt01.ChannelTTSUtterance, Chars: len([]rune(text)), Dur: time.Since(start)}, nil
		}
		p.Logf("book utterances failed (slot=%d), fallback to tts_audition: %v", b.Slot, err)
	}
	voiceID, groupKey := b.VoiceID, b.GroupKey
	base := auditionBase(ctx, c)
	start := time.Now()
	if err := c.TTSAuditionCtx(ctx, voiceID, text, groupKey); err != nil {
		return SpeakResult{}, err
	}
	waitAudition(ctx, c, base)
	if ctx.Err() != nil {
		return SpeakResult{}, ctx.Err()
	}
	return SpeakResult{Channel: "tts", Chars: len([]rune(text)), Dur: time.Since(start)}, nil
}

// ---- 等待播放完成 ----

type auditionStateJSON struct {
	State       string `json:"state"`
	UpdatedAtMs int64  `json:"updated_at_ms"`
}

func auditionBase(ctx context.Context, c *clt01.Client) int64 {
	var st auditionStateJSON
	if raw, err := c.TTSAuditionState(ctx); err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st.UpdatedAtMs
}

// waitAudition 轮询 tts_audition 状态机直到 ENDED/FAILED（或超时）。
func waitAudition(ctx context.Context, c *clt01.Client, base int64) {
	start := time.Now()
	seen := false
	for time.Since(start) < playMaxWait {
		select {
		case <-ctx.Done():
			return
		case <-time.After(playPollStep):
		}
		raw, err := c.TTSAuditionState(ctx)
		if err != nil {
			continue
		}
		var st auditionStateJSON
		if json.Unmarshal(raw, &st) != nil {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(st.State)) {
		case "STARTING", "WAITING", "PLAYING":
			seen = true
		case "ENDED", "SUCCESS", "DONE":
			if st.UpdatedAtMs != base || seen {
				return
			}
		case "FAILED", "ERROR", "FAILED_PLAYBACK":
			return
		}
		if !seen && time.Since(start) > playStartGrace && st.UpdatedAtMs == base {
			// 设备完全没动：可能命令丢失，按结束处理（下一段会再试）
			return
		}
	}
}

func waitUtterance(ctx context.Context, c *clt01.Client, id string) {
	if id == "" {
		return
	}
	start := time.Now()
	for time.Since(start) < playMaxWait {
		select {
		case <-ctx.Done():
			return
		case <-time.After(playPollStep):
		}
		raw, err := c.TTSUtteranceState(ctx, id)
		if err != nil {
			continue
		}
		var st clt01.TTSUtteranceStatus
		if json.Unmarshal(raw, &st) != nil {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(st.State)) {
		case "completed", "failed", "cancelled":
			return
		}
	}
}

func conversationBase(ctx context.Context, c *clt01.Client) (int64, string) {
	var st struct {
		State       string `json:"state"`
		UpdatedAtMs int64  `json:"updated_at_ms"`
	}
	if raw, err := c.ConversationState(ctx); err == nil {
		_ = json.Unmarshal(raw, &st)
	}
	return st.UpdatedAtMs, st.State
}

// conversationOutcome 会话等待结论：played=真的起播过；failed=设备报错。
type conversationOutcome struct {
	played bool
	failed bool
	state  string
}

// waitConversation chat 通道：进过 Thinking/Talking 后回 Idle 即播完。
func waitConversation(ctx context.Context, c *clt01.Client, base int64) conversationOutcome {
	start := time.Now()
	out := conversationOutcome{}
	for time.Since(start) < playMaxWait {
		select {
		case <-ctx.Done():
			return out
		case <-time.After(playPollStep):
		}
		raw, err := c.ConversationState(ctx)
		if err != nil {
			continue
		}
		var st struct {
			State       string `json:"state"`
			UpdatedAtMs int64  `json:"updated_at_ms"`
		}
		if json.Unmarshal(raw, &st) != nil {
			continue
		}
		state := strings.ToUpper(strings.TrimSpace(st.State))
		out.state = state
		switch state {
		case "THINKING", "WORKING", "TALKING":
			out.played = true
		case "ERROR", "FAILED":
			out.failed = true
			return out
		}
		if out.played && state != "THINKING" && state != "WORKING" && state != "TALKING" {
			return out
		}
		if st.UpdatedAtMs == base && time.Since(start) > playStartGrace*3 {
			return out // 设备没动静：放弃等待，避免卡死整段朗读
		}
	}
	return out
}

// ---- 随机挑选 ----

// PickRandom 从 (章节 × 未播过的段) 里随机挑一段：
//   - 段按段数加权（长章机会更多，但不至于让短章永远轮不到）；
//   - 章级随机播过的章（Visited["ch:0"]）权重降到 1/3；
//   - 同一轮里不重复：被占的候选（段本身或所在章章级键）直接剔除后重抽。
//
// 返回 ok=false 表示整本都播过一轮了，调用方应清空去重表后重来。
func PickRandom(b *model.Book, pb model.BookPlayback, rng *rand.Rand) (ch int, seg int, ok bool) {
	if b == nil || len(b.Chapters) == 0 || rng == nil {
		return 0, 0, false
	}
	type cand struct{ ch, n, w int }
	// 抽中但已占的候选：权重置 0（本轮内不再抽中），累计最多重抽 32 次
	dead := map[int]bool{}
	for attempt := 0; attempt < 32; attempt++ {
		var cands []cand
		total := 0
		for _, c := range b.Chapters {
			if c.Paragraph <= 0 || dead[c.Index] {
				continue
			}
			w := c.Paragraph * 3
			if pb.Visited[visitKey(c.Index, 0)] {
				w = c.Paragraph
			}
			cands = append(cands, cand{c.Index, c.Paragraph, w})
			total += w
		}
		if total == 0 {
			return 0, 0, false
		}
		r := rng.Intn(total)
		var pick cand
		for _, c := range cands {
			if r < c.w {
				pick = c
				break
			}
			r -= c.w
		}
		// 章内挑一个没播过的段
		found := false
		start := rng.Intn(pick.n)
		for k := 0; k < pick.n; k++ {
			s := (start + k) % pick.n
			if pb.Visited[visitKey(pick.ch, s)] {
				continue
			}
			seg, found = s, true
			break
		}
		if !found {
			dead[pick.ch] = true // 这一章都播过了
			continue
		}
		return pick.ch, seg, true
	}
	return 0, 0, false
}

func visitKey(ch, seg int) string { return fmt.Sprintf("%d:%d", ch, seg) }

// RandSource 随机源（每次新建带时间种子）。
func RandSource() *rand.Rand { return rand.New(rand.NewSource(time.Now().UnixNano())) }

// SegmentMode 书是否按「段」随机：章少段多（小说）→ 按段；章多段少（圣经/诗集）→ 按章。
func SegmentMode(b *model.Book) bool {
	if b == nil || len(b.Chapters) == 0 {
		return true
	}
	avg := 0
	for _, c := range b.Chapters {
		avg += c.Paragraph
	}
	avg /= len(b.Chapters)
	return avg >= 3
}

// PickRandomChapter 加权随机挑一章：没随机播过的章权重 3，播过的 1。
func PickRandomChapter(b *model.Book, pb model.BookPlayback, rng *rand.Rand) (int, bool) {
	if b == nil || len(b.Chapters) == 0 || rng == nil {
		return 0, false
	}
	type cand struct{ ch, w int }
	var cands []cand
	total := 0
	for _, c := range b.Chapters {
		w := 1
		if !pb.Visited[visitKey(c.Index, 0)] {
			w = 3
		}
		cands = append(cands, cand{c.Index, w})
		total += w
	}
	r := rng.Intn(total)
	for _, c := range cands {
		if r < c.w {
			return c.ch, true
		}
		r -= c.w
	}
	return 0, false
}

// shuffleChapters 章节随机顺序（Fisher-Yates），种子由调用方给（可测）。
func shuffleChapters(n int, rng *rand.Rand) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// estMinutes 粗略估算一段字符要念多久（英文 ~170 词/分钟 ≈ 900 字符/分钟；中文 ~250 字/分钟）。
func estMinutes(chars int, lang string) float64 {
	if chars <= 0 {
		return 0
	}
	perMin := 900.0
	if isChineseLang(lang) {
		perMin = 250
	}
	return math.Round(float64(chars)/perMin*10) / 10
}
