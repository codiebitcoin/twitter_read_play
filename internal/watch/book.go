package watch

import (
	"fmt"
	"log"
	"strings"
	"time"

	"twitter_read_play/internal/book"
	"twitter_read_play/internal/model"
)

// ---- book 模式（Feed 第 5 个 tab）：上传图书 → 拆章节 → 点章/点段/随机朗读 ----
//
// 复用播报链路里「确实把声音放出来」的那一段（角色包下发、声音校验、通道选择、播完等待），
// 但书是长文本，不走推文队列：
//   - 推文队列按「一条一条」排队，书要按「一段一段」连续下发、随时能停；Player 按机器串行，
//     新请求会顶掉旧的（用户点「播放」就是要这一条）；
//   - 进度（断点/随机去重）按角色×书存 book 库，见 internal/book/progress.go。

// Books 图书库与朗读器（为 nil 表示未启用）。
type Books struct {
	Lib    *book.Library
	Player *book.Player
}

// SetBooks 注入图书库（启动时调用；nil 不启用 book 模式）。
func (m *Manager) SetBooks(lib *book.Library) {
	m.books = &Books{Lib: lib}
	if lib != nil {
		m.books.Player = book.NewPlayer(m.machines.Client)
		m.books.Player.Logf = func(format string, args ...any) { log.Printf(format, args...) }
	}
}

// BookLibrary 书的读接口（API 层用）。
func (m *Manager) BookLibrary() *book.Library {
	if m.books == nil {
		return nil
	}
	return m.books.Lib
}

// BookPlayer 朗读器（API 层用，nil = 未启用）。
// BookWrapFor 角色朗读模板渲染（调试/测试用）：给定书名与章节，返回下发时的包装模板。
func (m *Manager) BookWrapFor(roleID, bookID string, chapter int) []string {
	if m.books == nil {
		return nil
	}
	b, err := m.books.Lib.GetFor(roleID, bookID)
	if err != nil || b == nil {
		return nil
	}
	return m.bookWrap(roleID, b)
}

// BookPlayer 朗读器：当前播放状态与停止（前端轮询用）。
func (m *Manager) BookPlayer() *book.Player {
	if m.books == nil {
		return nil
	}
	return m.books.Player
}

// BookTarget 解析角色的朗读目标绑定（取第一个启用绑定；可用 machine_id/slot 覆盖）。
// 返回的 Binding 已带上设备上登记的角色声音（没克隆过就为空 → 设备当前音色）。
func (m *Manager) BookTarget(roleID, machineID string, slot int) (book.Binding, error) {
	r, err := m.roles.Get(roleID)
	if err != nil {
		return book.Binding{}, err
	}
	var b model.Binding
	found := false
	if machineID != "" && slot > 0 {
		if bb := bindingOf(r, machineID, slot); bb != nil {
			b, found = *bb, true
		} else {
			b, found = model.Binding{MachineID: machineID, Slot: slot, Enabled: true}, true
		}
	}
	if !found {
		en := m.enabledBindings(r)
		if len(en) == 0 {
			return book.Binding{}, ErrNeedBinding
		}
		b = en[0]
	}
	voiceID, groupKey := m.voiceIDFor(r, b)
	return book.Binding{
		MachineID: b.MachineID, Slot: b.Slot,
		RoleID: r.ID, RoleName: r.Name,
		VoiceID: voiceID, GroupKey: groupKey,
	}, nil
}

// BookSpeakOpts 前端可调的朗读参数。
type BookSpeakOpts struct {
	Mode    string // tts=按文字念（默认）/ chat=设备 LLM 管线
	Preface bool   // 段首念章节标题
}

// PlayChapter 朗读一章；seg >= 0 时只播这一段（整段正文，不拆句）。
// block=true：同步等播放结束（测试/小段用）；否则立即返回 run 状态。
// skip=true：不念章节标题（段内点播时）。
func (m *Manager) PlayChapter(roleID, bookID string, chapter, seg int, opts BookSpeakOpts) (*book.PlayRun, error) {
	if m.books == nil {
		return nil, fmt.Errorf("book mode is not enabled")
	}
	lib := m.books.Lib
	b, err := lib.GetFor(roleID, bookID)
	if err != nil {
		return nil, err
	}
	if chapter < 0 || chapter >= len(b.Chapters) {
		return nil, fmt.Errorf("chapter %d out of range (0..%d)", chapter, len(b.Chapters)-1)
	}
	ch, paras, err := lib.Chapter(bookID, chapter)
	if err != nil {
		return nil, err
	}
	var raw []string
	preface := ""
	if seg >= 0 {
		if seg >= len(paras) {
			return nil, fmt.Errorf("paragraph %d out of range (0..%d)", seg, len(paras)-1)
		}
		raw = []string{paras[seg]}
		if opts.Preface {
			preface = book.ChapterPreface(ch.Title, b.Language)
		}
	} else {
		raw = paras
		if opts.Preface {
			preface = book.ChapterPreface(ch.Title, b.Language)
		}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("chapter is empty")
	}
	sopt := book.SpeakOpts{Mode: opts.Mode, Language: b.Language, Wrap: m.bookWrap(roleID, b), Preface: preface}
	plan := book.BuildPlan(raw, sopt)
	if len(plan.Segments) == 0 {
		return nil, fmt.Errorf("chapter has no readable text")
	}
	return m.startBookRun(roleID, b, chapter, seg, plan, sopt, "play")
}

// startBookRun 组装并启动一次朗读（chapter 朗读 / 单段 / 随机共用）。
func (m *Manager) startBookRun(roleID string, b *model.Book, chapter, seg int, plan book.Plan, sopt book.SpeakOpts, kind string) (*book.PlayRun, error) {
	lib := m.books.Lib
	r, err := m.roles.Get(roleID)
	if err != nil {
		return nil, err
	}
	target, err := m.BookTarget(roleID, "", 0)
	if err != nil {
		return nil, err
	}
	// 播前确保设备上的角色包/声音就是本角色（与推文播报同一套理由：设备只有一个激活槽位）
	m.syncMu.Lock()
	perr := m.applyRolePackage(r, model.Binding{MachineID: target.MachineID, Slot: target.Slot, Enabled: true}, true)
	m.syncMu.Unlock()
	if perr != nil {
		m.syncLog(r, target.MachineID, false, "book: apply role package: "+perr.Error(), "")
		// 不阻断：下面的下发会把真实错误带回来（例如设备离线）
	}
	nParas := 1
	// 角色模板里有 {TITLE}（需要报章节）时，整章播放每段都要带上章名；
	// 否则只在用户勾选了「先念章节标题」时给章节开头的第一段带提示语。
	chapterTitle := book.ChapterPreface(b.Chapters[chapter].Title, b.Language)
	// 单段播放沿用前端「先念章节标题」勾选；整章播放只在模板带 {TITLE}（要报章节）或有标题提示语时
	// 给每段带上章名 —— 否则一整章重复报章名很打断。
	preface := chapterTitle
	if seg < 0 && strings.TrimSpace(sopt.Preface) == "" && !wrapHasTitle(sopt.Wrap) {
		preface = ""
	}
	if seg >= 0 && strings.TrimSpace(sopt.Preface) == "" {
		preface = ""
	}
	total := nParas
	if seg < 0 && total < len(plan.Segments) {
		total = len(plan.Segments) // 进度展示按实际下发段数（章内段数少于朗读段数时也一样）
	}
	run := &book.PlayRun{
		ID: fmt.Sprintf("book-%d", time.Now().UnixMilli()), RoleID: r.ID, RoleName: r.Name,
		MachineID: target.MachineID,
		BookID:    b.ID, BookTitle: b.Title, Chapter: chapter, Segment: seg, Mode: kind, Total: total,
	}
	lang := sopt.Language
	if lang == "" {
		lang = b.Language
	}
	job := book.PlayJob{
		Binding: target,
		Opts: book.SpeakOpts{
			Mode: sopt.Mode, Language: lang, Wrap: sopt.Wrap,
			Preface: preface,
		},
		Plan:       plan,
		Tag:        fmt.Sprintf("book=%q ch=%d", b.Title, chapter),
		StageTotal: total,
		Stage: func(pos, t int) error {
			// 位置语义见 book.PlayJob.Stage：开工 = 当前目标已播，t+1 = 整组播完
			if pos <= t {
				if seg < 0 {
					// 段序号按「朗读段」推进（章内段落数可能少于朗读段数）
					lib.SetPosition(roleID, b.ID, chapter, minInt(pos, t))
				}
				return nil
			}
			if seg < 0 {
				lib.SetPosition(roleID, b.ID, chapter+1, 0)
			} else {
				lib.SetPosition(roleID, b.ID, chapter, seg+1)
			}
			return nil
		},
		StopBetween: func() bool { return false },
	}
	view := m.books.Player.Start(run, job)
	m.logBook(r, target.MachineID, b, chapter, seg, plan, kind)
	return view.PlayRun, nil
}

// logBook 记一条播报日志（kind 留空 = 播报类；前端日志页可按角色过滤）。
func (m *Manager) logBook(r *model.Role, machineID string, b *model.Book, chapter, seg int, plan book.Plan, kind string) {
	loc := fmt.Sprintf("%s · 章 %d", b.Title, chapter+1)
	if seg >= 0 {
		loc = fmt.Sprintf("%s · 章 %d · 段 %d", b.Title, chapter+1, seg+1)
	}
	var sb strings.Builder
	for _, raw := range plan.Raw {
		if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(raw)
	}
	m.appendLog(&model.BroadcastLog{
		Time: time.Now(), Platform: "book", Twitter: loc, TweetID: fmt.Sprintf("%s#%d:%d", b.ID, chapter, seg),
		Text: model.CleanText(sb.String(), 400), RoleID: r.ID, MachineID: machineID, Source: kind,
		OK: true, Detail: fmt.Sprintf("book=%s chapter=%d segment=%d", b.Title, chapter, seg),
	})
}

// wrapHasTitle 包装模板是否包含 {TITLE} 占位（对应「每段都报章节」）。
func wrapHasTitle(wrap []string) bool {
	return strings.Contains(strings.Join(wrap, "\n"), "{TITLE}")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// PlayRandom 随机朗读：
//   - 段级（章少段多，如小说）：随机挑「章 × 未播过的段」，同一轮不重复，播过一轮清表重来；
//   - 章级（章多段少，如圣经/诗集）：加权随机（没随机播过的章 3:1）挑一章，念完该章；
//   - 起始处只播一段（点「随机播放」= 随机来一段，不想听就再点随机/停止）。
func (m *Manager) PlayRandom(roleID, bookID string, opts BookSpeakOpts) (*book.PlayRun, error) {
	if m.books == nil {
		return nil, fmt.Errorf("book mode is not enabled")
	}
	lib := m.books.Lib
	b, err := lib.GetFor(roleID, bookID)
	if err != nil {
		return nil, err
	}
	if len(b.Chapters) == 0 {
		return nil, fmt.Errorf("book has no chapters")
	}
	pb := lib.Progress(roleID, bookID)
	sopt := book.SpeakOpts{Mode: opts.Mode, Language: b.Language, Wrap: m.bookWrap(roleID, b)}

	if book.SegmentMode(b) {
		ch, seg, ok := book.PickRandom(b, pb, book.RandSource())
		if !ok {
			// 整本一轮播完：清空去重表后从头再来
			lib.ResetVisited(roleID, bookID)
			pb = lib.Progress(roleID, bookID)
			ch, seg, ok = book.PickRandom(b, pb, book.RandSource())
			if !ok {
				return nil, fmt.Errorf("no readable paragraph in this book")
			}
		}
		chp, paras, err := lib.Chapter(bookID, ch)
		if err != nil {
			return nil, err
		}
		if seg >= len(paras) {
			return nil, fmt.Errorf("paragraph %d not found in chapter %d", seg, ch)
		}
		if opts.Preface {
			sopt.Preface = book.ChapterPreface(chp.Title, b.Language)
		}
		plan := book.BuildPlan([]string{paras[seg]}, sopt)
		run, err := m.startBookRun(roleID, b, ch, seg, plan, sopt, "random")
		if err != nil {
			return nil, err
		}
		lib.MarkPlayed(roleID, bookID, ch, seg, len(paras))
		return run, nil
	}

	// 章级随机（章多段少）：加权挑一章，念该章第一段（起点只播一段，不想听再点随机/停止）
	ch, ok := book.PickRandomChapter(b, pb, book.RandSource())
	if !ok {
		return nil, fmt.Errorf("no readable chapter in this book")
	}
	chp, paras, err := lib.Chapter(bookID, ch)
	if err != nil {
		return nil, err
	}
	if len(paras) == 0 {
		return nil, fmt.Errorf("chapter is empty")
	}
	if opts.Preface {
		sopt.Preface = book.ChapterPreface(chp.Title, b.Language)
	}
	plan := book.BuildPlan([]string{paras[0]}, sopt)
	run, err := m.startBookRun(roleID, b, ch, 0, plan, sopt, "random")
	if err != nil {
		return nil, err
	}
	// 章级随机只记「章 0 段」标记（影响下次加权），不推进整本断点
	lib.MarkPlayed(roleID, bookID, ch, 0, 0)
	return run, nil
}

// bookWrap 角色级朗读包装（取 Persona 的 chat 播报模板：{PARA} 或 {{TWEET}} 占位）。
// 只取「模板文本 + 角色语言」，不做正则替换（书正文不适合套推文的正则规则）；
// 旧版单一 speak_template 迁移后的去处是 chat 字段，书包装跟着它，别拿 tts 直读模板。
// 模板里没有书名占位时，在正文前报一下「书目 · 第 N 章」，听书时才分得清读的是哪一章。
func (m *Manager) bookWrap(roleID string, b *model.Book) []string {
	r, err := m.roles.Get(roleID)
	if err != nil || b == nil {
		return nil
	}
	tpl := r.SpeakTemplateFor("chat")
	if tpl == "" {
		return nil
	}
	// 推文模板里的 {{TWEET}} 换成书的正文占位；{{NAME}}/{{USERNAME}} 换成书名/章节
	tpl = strings.ReplaceAll(tpl, "{{TWEET}}", "{PARA}")
	tpl = strings.ReplaceAll(tpl, "{{NAME}}", b.Title)
	tpl = strings.ReplaceAll(tpl, "{{USERNAME}}", b.Title)
	if !strings.Contains(tpl, "{PARA}") {
		tpl += "\n{PARA}"
	}
	if !strings.Contains(tpl, "{TITLE}") {
		tpl = strings.ReplaceAll(tpl, "{PARA}", "{TITLE}\n{PARA}")
	}
	return []string{tpl}
}

// StopBook 停止某台机器上的书朗读（machineID 空 = 角色绑定那台）。
func (m *Manager) StopBook(roleID, machineID string) bool {
	if m.books == nil || m.books.Player == nil {
		return false
	}
	if machineID == "" {
		if t, err := m.BookTarget(roleID, "", 0); err == nil {
			machineID = t.MachineID
		}
	}
	if machineID == "" {
		return false
	}
	return m.books.Player.CancelFor(roleID, machineID)
}

// BookRuns 所有机器上的朗读状态（前端轮询）。
func (m *Manager) BookRuns() map[string]bool {
	out := map[string]bool{}
	if m.books == nil || m.books.Player == nil {
		return out
	}
	for k, v := range m.books.Player.Runs() {
		out[k] = v.Playing
	}
	return out
}
