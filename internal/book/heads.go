package book

import (
	"regexp"
	"strings"
	"unicode"
)

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
