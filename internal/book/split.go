package book

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
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

// head 一个章节标题行。
type head struct {
	line  int
	title string
}

// chapterPrefix 显式章节标记的前半段（章号部分）。整行判定见 isExplicitMarker：
// 标记后可以跟一小段标题文字（"第三回 风起"），但不能再像一整句正文。
//
// 注意：Go 的 RE2 里 `\s` 含换行，`\S` 不含换行 —— 用 `\s*\S$` 时行尾换行会把正文首字「吃掉」，
// 中文标题因此匹配失败；这里只用字面空格类，避免这个坑。
var chapterPrefix = regexp.MustCompile(`(?i)^(?:` +
	`(?:chapter|chap\.|part|book|volume|section|prologue|epilogue|preface|introduction|foreword|afterword)` +
	`(?:[.:·\-–—]? ?(?:[0-9]{1,6}|(?i:[ivxlcdm]{1,6})))?` + // Chapter 1 / CHAPTER IV / Part 2（Chapter / Prologue 也可不带号）
	`|第 ?[0-9０-９一二三四五六七八九十百千零〇两]{1,6} ?[章回节卷部篇集]` + // 第1章 / 第三回 / 卷二
	`|卷 ?[0-9一二三四五六七八九十百千零〇两]+` + // 卷二
	`|§ ?[0-9]+` + // §12
	`|(?:[0-9０-９]{1,3}|[一二三四五六七八九十百]{1,4})[.、]` + // 1. / 一、
	`|(?:[一二三四五六七八九十]|one|two|three|four|five|six|seven|eight|nine|ten)` + // 中文数字前缀（第三回）
	`)`)

// englishMarker 纯英文关键词 + 数字（"Book Three" / "Part 2"），中文章节词不在其中。
var englishMarker = regexp.MustCompile(`(?i)^(?:chapter|chap\.|part|book|volume|section|prologue|epilogue|preface|introduction|foreword|afterword)\b`)

// romanLine 罗马数字单独成行（I / IV / XLII）。
var (
	romanLine = regexp.MustCompile(`^[IVXLCDM]{1,7}$`)
	shortLine = regexp.MustCompile(`^\S(?:.{0,48})\S$`)
)

// detectHeads 找出所有章节标题行；ok=false 表示没找到足够可信的标题。
func detectHeads(lines []string, markdown bool) ([]head, bool) {
	var heads []head
	mdLevel := 1
	// 第一遍：显式标记
	for i, raw := range lines {
		s := strings.TrimSpace(raw)
		if s == "" || len(s) > 80 {
			continue
		}
		if isExplicitMarker(s) {
			heads = append(heads, head{line: i, title: s})
		}
	}
	ownHeads := len(heads) // 显式标记数（Markdown 标题可能另算）
	heads = dropTOCDuplicates(heads)
	if len(heads) == 0 {
		// 第二遍：Markdown 标题
		if markdown {
			mdLevel = markdownLevel(lines)
			for i, raw := range lines {
				s := strings.TrimSpace(raw)
				if !strings.HasPrefix(s, "#") {
					continue
				}
				// 多级标题：只取最先出现的级别。书籍文件通常两级都用（"#" 书名 + "##" 章），
				// 不能把书名也当一个空章；"### Genesis" 这种则不能把 1 级当章节，否则被拆成两套。
				lvl := len(s) - len(strings.TrimLeft(s, "#"))
				td := strings.TrimSpace(strings.TrimLeft(s, "#"))
				if td == "" || len(td) > 80 {
					continue
				}
				if lvl == mdLevel {
					heads = append(heads, head{line: i, title: td})
				}
			}
		}
	}
	// Markdown # 标题是作者显式写的，1 个以上就认（EPUB 转文本只给每章生成一个 # 标题行）
	if len(heads) >= 1 && markdown && hasMarkdownHead(lines) {
		return heads, true
	}
	if len(heads) >= 3 {
		return heads, true
	}
	// 显式标记很可信：只要有 1 个就按它切（短书/短示例稿只有一两章）
	if ownHeads >= 1 && len(heads) >= 1 {
		return heads, true
	}
	// 第三遍：罗马数字单独成行（≥3 处才认，避免把「I」「II」当章节）
	var romans []head
	for i, raw := range lines {
		s := strings.TrimSpace(raw)
		if romanLine.MatchString(s) && i+1 < len(lines) {
			romans = append(romans, head{line: i, title: s})
		}
	}
	if len(romans) >= 3 {
		return romans, true
	}
	// 第四遍：空行夹着的短行（中文小说常见「第一章 风起」之外的自定义分节）
	var shorts []head
	for i, raw := range lines {
		s := strings.TrimSpace(raw)
		if s == "" || len(s) > 40 {
			continue
		}
		beforeBlank := i == 0 || strings.TrimSpace(lines[i-1]) == ""
		afterBlank := i == len(lines)-1 || strings.TrimSpace(lines[i+1]) == ""
		if !beforeBlank || !afterBlank || !shortLine.MatchString(s) {
			continue
		}
		if endsWithSentencePunct(s) {
			continue
		}
		shorts = append(shorts, head{line: i, title: s})
	}
	if len(shorts) >= 3 {
		return shorts, true
	}
	return nil, false
}

// dropTOCDuplicates 去掉「目录里的重复标题」：有的电子书开头有目录（"Chapter 1\nChapter 2\n…"），
// 目录里每一行也都像标题，直接切会得到一堆空章。
// 判据：标题 i 与 i+K 同名，且中间恰好是 i..i+K-1 这个序列本身的首尾相接 → i..i+K-1 是目录，丢掉它们。
func dropTOCDuplicates(heads []head) []head {
	drop := make([]bool, len(heads))
	for i := 0; i < len(heads); i++ {
		if drop[i] {
			continue
		}
		for j := i + 1; j < len(heads); j++ {
			if drop[j] || heads[j].title != heads[i].title {
				continue
			}
			if isTOCSeq(heads, i, j) {
				for k := i; k < j; k++ {
					drop[k] = true
				}
			}
			break
		}
	}
	out := make([]head, 0, len(heads))
	for i, h := range heads {
		if !drop[i] {
			out = append(out, h)
		}
	}
	return out
}

// hasMarkdownHead 文本里是否有 Markdown "#" 标题行。
func hasMarkdownHead(lines []string) bool {
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#") {
			return true
		}
	}
	return false
}

// isTOCSeq heads[i:j] 是否是 heads[j:] 开头若干项的前缀（逐项相同），且至少 2 项。
func isTOCSeq(heads []head, i, j int) bool {
	n := j - i
	if n < 2 {
		return false
	}
	for k := 0; k < n; k++ {
		if j+k >= len(heads) || heads[j+k].title != heads[i+k].title {
			return false
		}
	}
	return true
}

// isExplicitMarker 是否为显式章节标记行（可带一小段紧跟的标题）。
func isExplicitMarker(s string) bool {
	loc := chapterPrefix.FindStringIndex(s)
	if loc == nil {
		// "Book Three" / "Part One"：关键字后跟英文数字词
		if w, ok := englishWordNumeral(s); ok && w {
			return true
		}
		return false
	}
	rest := strings.TrimSpace(s[loc[1]:])
	// "Chapter 1 was the best part…"：数字后不能直接接普通单词（英文只有少量标题用词可跟）
	if englishMarker.MatchString(s) && rest != "" {
		first, _, _ := strings.Cut(rest, " ")
		if !isSpecialSectionWord(first) && !looksNumericToken(first) && !isPunctuationWord(first) {
			return false
		}
	}
	// 剩下的是标题：不能太长、不能是整句正文。
	// 中文常见「第一章 风起。」并在同一行直接接正文，所以中文标记允许整行通读；
	// 英文「Chapter 1 + 副标题」要求短、且中间不能有句读。
	if chineseMarker(s) {
		if len([]rune(rest)) > 40 {
			return false
		}
	} else if len([]rune(rest)) > 30 || midSentencePunct(rest) {
		return false
	}
	// 紧贴的汉字很多说明是句子（「第一章讲的是经济学的历史，弗里德曼在开头」），不是标题：
	// 中文标记后紧跟建议的标题要么空、要么由空格隔开，汉字串超过 8 个就当成正文
	if u := unbrokenHan(rest); u > 8 && !unicode.IsSpace(firstRuneOf(rest)) {
		return false
	}
	return true
}

// numeralWords 英文数字词（"Book Three" 这类整卷标题）。
var numeralWords = map[string]bool{
	"one": true, "two": true, "three": true, "four": true, "five": true, "six": true,
	"seven": true, "eight": true, "nine": true, "ten": true, "eleven": true, "twelve": true,
}

// englishWordNumeral "Book One…" / "Part Three" 判定（关键字 + 英文数字词，或长块名单独成行）。
func englishWordNumeral(s string) (bool, bool) {
	fields := strings.Fields(strings.ToLower(s))
	if len(fields) < 2 {
		// "Prologue" / "Epilogue" / "Preface" 单独成行
		if len(fields) == 1 {
			switch strings.Trim(fields[0], ".:") {
			case "prologue", "epilogue", "preface", "foreword", "afterword", "introduction":
				return true, true
			}
		}
		return false, false
	}
	if !englishMarker.MatchString(s) {
		return false, false
	}
	return numeralWords[strings.Trim(fields[1], ".:-–—")], true
}

// firstRuneOf s 的第一个字符（空串返回 0）。
func firstRuneOf(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// unbrokenHan rest 开头连续汉字的个数。
func unbrokenHan(rest string) int {
	n := 0
	for _, r := range rest {
		if unicode.Is(unicode.Han, r) {
			n++
			continue
		}
		break
	}
	return n
}

// isPunctuationWord 纯标点词（"Chapter 1 - The Beginning" 里的 "-"）。
func isPunctuationWord(w string) bool {
	w = strings.TrimSpace(w)
	if w == "" {
		return false
	}
	for _, r := range w {
		if !strings.ContainsRune("-–—:.,·", r) {
			return false
		}
	}
	return true
}

// chineseMarker 标记是中文体例（第X章 / 卷X / 一、）。
func chineseMarker(s string) bool {
	if strings.HasPrefix(s, "第") || strings.HasPrefix(s, "卷") {
		return true
	}
	r := []rune(s)
	return len(r) > 1 && unicode.Is(unicode.Han, r[0]) && strings.ContainsRune("、.", r[1])
}

// midSentencePunct rest 中间有句末/分句标点 → 是一整句正文而不是标题。
func midSentencePunct(rest string) bool {
	body := strings.TrimSuffix(strings.TrimSpace(rest), "。")
	body = strings.TrimSuffix(body, ".")
	for _, r := range body {
		if strings.ContainsRune("。！？!?，,；;：:", r) {
			return true
		}
	}
	return false
}

// isSpecialSectionWord 长块名可以没有编号（"Prologue" / "Epilogue"）；带编号时也允许紧跟这些词之外的词。
func isSpecialSectionWord(w string) bool {
	switch strings.ToLower(strings.Trim(w, ".:-–—")) {
	case "the", "of", "a", "an", "and", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten",
		"prologue", "epilogue", "part", "chapter", "book", "volume", "section", "i", "ii", "iii", "iv", "v", "vi",
		"vii", "viii", "ix", "x", "-", "–", "—", ":", ".":
		return true
	}
	return false
}

// looksNumericToken 1 / 12 / IV / XII / 一 / 三
func looksNumericToken(s string) bool {
	if s == "" || len(s) > 6 {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) && !strings.ContainsRune("IVXLCDMivxlcdm一二三四五六七八九十百千零〇两", r) {
			return false
		}
	}
	return true
}

// singleLineSentence 看起来是一整句正文（以句末标点结尾，或以逗号/分号结尾的短句），
// 而不是标题/副标题。副标题极少带句末标点。
func singleLineSentence(s string) bool {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return false
	}
	switch r[len(r)-1] {
	case '。', '！', '？', '.', '!', '?', '，', ',', '；', ';', '：', ':':
		return true
	}
	return false
}

// endsWithSentencePunct 以句读结尾（这类短行是正文，不是标题）。
func endsWithSentencePunct(s string) bool {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return false
	}
	return strings.ContainsRune("。，；：、！？.,;:!?", r[len(r)-1])
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

// splitSentences 按句末标点把一段切成 ≤limit 字符的若干块。
func splitSentences(p string, limit int) []string {
	var out []string
	var sb strings.Builder
	for _, r := range p {
		sb.WriteRune(r)
		if strings.ContainsRune("。！？.!?；;", r) && len([]rune(sb.String())) >= limit/2 {
			// 切完以空格起步会让后续片段多出前导空格
			out = append(out, strings.TrimSpace(sb.String()))
			sb.Reset()
		}
	}
	if s := strings.TrimSpace(sb.String()); s != "" {
		out = append(out, s)
	}
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

// SplitSentences 把一段正文切成「朗读小段」：默认按句末标点切，英文/中文混排都适用。
// 句子连起来不能太长（一口气读不完），也不宜太碎（TTS 每段都有起播开销），
// 所以把相邻短句合并到 ~maxLen 字符后再切。
func SplitSentences(p string, maxLen int) []string {
	if maxLen <= 0 {
		maxLen = 240
	}
	runes := []rune(strings.TrimSpace(p))
	if len(runes) == 0 {
		return nil
	}
	// 句末标点后切（保留标点在句尾；右引号/右括号归入前一句）
	var sent []string
	var cur []rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		cur = append(cur, r)
		if !strings.ContainsRune("。！？!?；;", r) {
			continue
		}
		// 英文 "." 后面不是空格/结尾的不当句末（小数点、缩写）
		if r == '.' {
			if i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
				continue
			}
		}
		for i+1 < len(runes) && strings.ContainsRune("”’\"')）】」』", runes[i+1]) {
			i++
			cur = append(cur, runes[i])
		}
		sent = append(sent, string(cur))
		cur = nil
	}
	if rest := strings.TrimSpace(string(cur)); rest != "" {
		sent = append(sent, rest)
	}
	// 合并短句
	var out []string
	var sb strings.Builder
	size := 0
	flush := func() {
		if s := strings.TrimSpace(sb.String()); s != "" {
			out = append(out, s)
		}
		sb.Reset()
		size = 0
	}
	for _, s := range sent {
		n := len([]rune(s))
		if n > maxLen {
			// 单句超长：按空格/逗号硬切
			flush()
			out = append(out, hardWrap(s, maxLen)...)
			continue
		}
		if size+n > maxLen && size > 0 {
			flush()
		}
		if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(s)
		size += n
	}
	flush()
	return out
}

// hardWrap 超长单句按空格（无空格按逗号）切成 ≤maxLen 的块。
func hardWrap(s string, maxLen int) []string {
	runes := []rune(s)
	var out []string
	for len(runes) > maxLen {
		cut := maxLen
		for i := maxLen; i > maxLen/2; i-- {
			if unicode.IsSpace(runes[i]) || strings.ContainsRune("，,、：:", runes[i]) {
				cut = i + 1
				break
			}
		}
		out = append(out, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	if s := strings.TrimSpace(string(runes)); s != "" {
		out = append(out, s)
	}
	return out
}

// markdownLevel 用哪一级 Markdown 标题当「章」。
// 判据：有且只有 1 个 1 级标题 → 它是书名，章节用浅 1 级那一套；否则用最先出现的级别。
func markdownLevel(lines []string) int {
	first, top := 0, 0
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "#") {
			continue
		}
		n := len(t) - len(strings.TrimLeft(t, "#"))
		if n < 1 || n > 6 {
			continue
		}
		if first == 0 {
			first = n
		}
		if n == 1 {
			top++
		}
	}
	if top == 1 && first == 1 {
		return 2
	}
	if first == 0 {
		return 1
	}
	return first
}
