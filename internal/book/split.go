package book

import (
	"fmt"
	"regexp"
	"strings"
)

// ---- 章节拆分模块 ----
//
// 输入：一本书的纯文本（已解码）。
// 输出：有序章节，每章 = 标题 + 段落列表。
//
// 识别策略（按优先级，命中即切）：
//  1. 显式章节标记行：
//     - 大小写不敏感的 "Chapter 1" / "CHAPTER IV" / "Part 2" / "Book Three" / "第3章" / "卷三" / "第三章"；
//     - 书内独立成行的序号："1." / "一、" / "§12" / "XII."；
//     - 罗马数字单独成行（I / II / … / XL），至少出现 3 行才认定（避免把人名缩写当标题）；
//     标记行允许前后夹着一定字数的短说明（如「Chapter 1\nThe Beginning」），标题取标记行 + 紧随的短行。
//  2. Markdown "# 标题"，或空行夹着的短行（≤40 字符、不以句读结尾）——至少 3 处才认定，
//     否则中文小说里「某某说。」这类短段会被误判成标题。
//  3. 都没识别到：按章长机械切分（每章目标 3000 字符，不切断句子），标题生成「第 N 章」并标记 Inserted。
//
// 段落规则：连续非空行为一段（自动合并被 PDF/文本导出硬换行拆开的句子；
// 诗歌/清单的视觉换行也合并成一段，朗读时更自然），段落之间保留空行写回文件。
//
// 标题识别的匹配器与启发式在 heads.go，朗读小段的句子切分在 sentences.go。

// SplitOptions 拆分参数（都可不填，用内置默认）。
type SplitOptions struct {
	// TargetChars 未识别到章节时的自动分章目标长度（字符）。<=0 → 3000。
	TargetChars int
	// DetectHeaders 是否识别 Markdown "#" 标题（默认 true；txt 一般关掉以防误判）。
	DetectHeaders *bool
}

// ChapterText 拆分结果的一章。
type ChapterText struct {
	Title      string
	Paragraphs []string
	Inserted   bool
}

// SplitText 把书正文拆成章节。
func SplitText(text string, opt SplitOptions) []ChapterText {
	target := opt.TargetChars
	if target <= 0 {
		target = 3000
	}
	markdown := opt.DetectHeaders == nil || *opt.DetectHeaders

	lines := strings.Split(text, "\n")
	// 去掉首尾空行
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	heads, ok := detectHeads(lines, markdown)
	var chapters []ChapterText
	if ok {
		chapters = cutByHeads(lines, heads)
	}
	if len(chapters) == 0 {
		paras := collectParagraphs(lines)
		if len(paras) == 0 {
			return nil
		}
		chapters = autoChapters(paras, target)
	}
	// 章节内再套一层长度上限：超长章（一部成册的圣经、整卷小说）再机械切分，保证单次朗读不超过设备等待上限。
	var out []ChapterText
	for _, ch := range chapters {
		parts := splitLongChapter(ch, target)
		// 续篇标题跟在原章标题后：先把生成的章号按最终位置重排，续篇再引用它
		auto := isAutoTitle(ch.Title)
		base := ch.Title
		if auto {
			base = fmt.Sprintf("第 %d 章", len(out)+1)
		}
		for i, p := range parts {
			p.Title = base
			if i > 0 {
				p.Title = fmt.Sprintf("%s（续 %d）", base, i)
			}
			out = append(out, p)
		}
	}
	return out
}

// cutByHeads 按标题行切章；标题后紧跟的短行（≤60 字符、非空、不以句读结尾、且下一行是空行）
// 视作副标题并入标题 —— 电子书常见「Chapter 1\nThe Beginning」。
func cutByHeads(lines []string, heads []head) []ChapterText {
	var out []ChapterText
	for k, h := range heads {
		title := h.title
		bodyStart := h.line + 1
		// 副标题：紧跟一行短标题行，且它的下一行是空行或已经是下一个标题（目录式排版）。
		// 下一行本身是标题时（目录页）不吃这行，免得两章抢同一行。
		nextIsHeadLine := k+1 < len(heads) && heads[k+1].line == bodyStart
		if bodyStart < len(lines) && !nextIsHeadLine {
			sub := strings.TrimSpace(lines[bodyStart])
			// 下一行是空行，或紧随其后的就是下一个标题行（目录式排版）
			nextIsHead := bodyStart+1 >= len(lines) || strings.TrimSpace(lines[bodyStart+1]) == ""
			if k+1 < len(heads) && heads[k+1].line == bodyStart+1 {
				nextIsHead = true
			}
			if sub != "" && len([]rune(sub)) <= 45 && !singleLineSentence(sub) &&
				!strings.HasPrefix(sub, "#") && nextIsHead {
				title = title + " — " + sub
				bodyStart++
			}
		}
		end := len(lines)
		if k+1 < len(heads) {
			end = heads[k+1].line
		}
		paras := collectParagraphs(lines[bodyStart:end])
		if len(paras) == 0 && title == h.title {
			continue // 空章（目录、孤立的标题行）：不占章节编号
		}
		out = append(out, ChapterText{Title: title, Paragraphs: paras})
	}
	return out
}

// collectParagraphs 连续非空行合并为一段。
// 两个细节：
//   - 紧凑排版（章节标题行之后没有空行）时，「下一行像章节标题」要在这里断开，
//     否则标题行会被并进上一章最后一段；
//   - 圣经/诗歌类文本常把整章挤在一个没有空行的块里（"[1:1] … [1:2] …"），
//     合并后再按「段落引用」二次切段，否则整章只有 1 段，逐段播放没意义。
func collectParagraphs(lines []string) []string {
	var out []string
	var buf []string
	flush := func() {
		if len(buf) == 0 {
			return
		}
		out = append(out, splitInlineParagraphs(strings.Join(buf, " "))...)
		buf = nil
	}
	for _, raw := range lines {
		s := strings.TrimSpace(raw)
		if s == "" {
			flush()
			continue
		}
		if strings.HasPrefix(s, "#") { // markdown 残留标题（EPUB 转文本时按 <h1>/<h2> 生成）
			flush()
			continue
		}
		if i := markerIndex(s); i > 0 {
			// 行内出现章节标记：标记前的正文收尾，标记后的部分丢弃（下一个标题会重新开章）
			if head := strings.TrimSpace(s[:i]); head != "" {
				buf = append(buf, head)
			}
			flush()
			continue
		}
		buf = append(buf, s)
	}
	flush()
	return out
}

// markerIndex 返回 s 中出现「行内章节标记」的字节位置（0 = 不是标记行 / 没有内联标记）。
// 只在标记前是句末标点（一段结束）时才认，避免 "he read chapter 1 of the book" 被误切。
func markerIndex(s string) int {
	if isExplicitMarker(s) { // 整行就是章节标题（切章时已处理）
		return 0
	}
	for i := 1; i < len(s); i++ {
		if !strings.HasPrefix(s[i:], "第") {
			continue
		}
		tail := s[i:]
		// 行内标记：章号后必须紧跟空格/标题（「第三章 雨落」），且整段很短
		if chapterPrefix.FindStringIndex(tail) == nil || len([]rune(tail)) > 30 {
			continue
		}
		// 标记前必须是句末标点（一段结束）
		prev := strings.TrimSpace(s[:i])
		if prev == "" {
			return 0
		}
		r := []rune(prev)
		if strings.ContainsRune("。！？.!?\"'”’", r[len(r)-1]) {
			return i
		}
	}
	return 0
}

// paraRefRe 段内段落引用：[1:12] / (1:12) / {1:12}（圣经章节体例）。
var paraRefRe = regexp.MustCompile(`\[(\d{1,3}:\d{1,3})\]`)

// splitInlineParagraphs 一行里出现 2 个及以上段落引用时，在每个引用前切开。
func splitInlineParagraphs(p string) []string {
	p = strings.Join(strings.Fields(p), " ")
	locs := paraRefRe.FindAllStringIndex(p, -1)
	if len(locs) < 2 {
		return []string{p}
	}
	var out []string
	prev := 0
	for _, loc := range locs[1:] {
		if seg := strings.TrimSpace(p[prev:loc[0]]); seg != "" {
			out = append(out, seg)
		}
		prev = loc[0]
	}
	if seg := strings.TrimSpace(p[prev:]); seg != "" {
		out = append(out, seg)
	}
	return out
}

// autoChapters 没识别到章节：按目标长度机械切分（不切断句子）。
func autoChapters(paras []string, target int) []ChapterText {
	var out []ChapterText
	var cur []string
	size := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		out = append(out, ChapterText{Title: fmt.Sprintf("第 %d 章", len(out)+1), Paragraphs: cur, Inserted: true})
		cur, size = nil, 0
	}
	for _, p := range paras {
		n := len([]rune(p))
		// 单段超长：先按句子切成多个「假段落」
		if n > target {
			flush()
			for _, piece := range splitSentences(p, target) {
				cur = append(cur, piece)
				size += len([]rune(piece))
			}
			flush()
			continue
		}
		if size+n > target && len(cur) > 0 {
			flush()
		}
		cur = append(cur, p)
		size += n
	}
	flush()
	return out
}

// autoTitleRe 自动分章/续篇的生成标题（重编号时认它）。
var autoTitleRe = regexp.MustCompile(`^第 \d+ 章(?:（续 \d+）)?$`)

// isAutoTitle 是否是拆分模块自动生成的标题。
func isAutoTitle(s string) bool { return autoTitleRe.MatchString(s) }

// splitLongChapter 超过 limit 的章节按段落再切（必要时按句再切），
// 保证单次下发不超设备等待上限（playbackMaxWait = 3 分钟），也保证「逐段播放」有感。
// limit = target*0.8，且不少于 1500 字符。
func splitLongChapter(ch ChapterText, target int) []ChapterText {
	limit := target * 8 / 10
	if limit < 1500 {
		limit = 1500
	}
	total := 0
	for _, p := range ch.Paragraphs {
		total += len([]rune(p))
	}
	if total <= limit {
		return []ChapterText{ch}
	}
	var out []ChapterText
	var cur []string
	size := 0
	flush := func() {
		if len(cur) == 0 {
			return
		}
		out = append(out, ChapterText{Title: ch.Title, Paragraphs: cur, Inserted: ch.Inserted || len(out) > 0})
		cur, size = nil, 0
	}
	for _, p := range ch.Paragraphs {
		n := len([]rune(p))
		if n > limit {
			flush()
			for _, piece := range splitSentences(p, limit) {
				pn := len([]rune(piece))
				if size+pn > limit && len(cur) > 0 {
					flush()
				}
				cur = append(cur, piece)
				size += pn
			}
			continue
		}
		if size+n > limit && len(cur) > 0 {
			flush()
		}
		cur = append(cur, p)
		size += n
	}
	flush()
	return out
}

// RenderChapter 章节段落 → 落盘文本（段落间空行，末行换行）。
func RenderChapter(paras []string) string {
	var sb strings.Builder
	for i, p := range paras {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(p)
	}
	if sb.Len() > 0 {
		sb.WriteString("\n")
	}
	return sb.String()
}

// ParseChapter 落盘文本 → 段落（与 RenderChapter 互逆）。
func ParseChapter(s string) []string {
	blocks := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n\n")
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		b = strings.Join(strings.Fields(b), " ")
		if b != "" {
			out = append(out, b)
		}
	}
	return out
}

// ChapterStats 章节字符数（不含分隔空行）与段落数。
func ChapterStats(paras []string) (chars, n int) {
	for _, p := range paras {
		chars += len([]rune(p))
		n++
	}
	return chars, n
}
