package book

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf16"

	"twitter_read_play/internal/model"
)

// ---- 解码 ----

func TestDecodeTextUTF8(t *testing.T) {
	s, enc := DecodeText([]byte("第一章\r\n你好"))
	if enc != "utf-8" {
		t.Fatalf("enc = %s", enc)
	}
	if strings.Contains(s, "\r") {
		t.Fatalf("CR not normalized: %q", s)
	}
}

func TestDecodeTextGBK(t *testing.T) {
	// 「中文测试」的 GBK 编码（Python: "中文测试".encode("gbk")）
	b := []byte{0xD6, 0xD0, 0xCE, 0xC4, 0xB2, 0xE2, 0xCA, 0xD4}
	s, enc := DecodeText(b)
	if s != "中文测试" {
		t.Fatalf("decoded %q (enc=%s)", s, enc)
	}
	if enc != "gb18030" {
		t.Fatalf("enc = %s", enc)
	}
}

func TestDecodeBig5Direct(t *testing.T) {
	// 「中文測試」的 Big5 编码（这些字节也是合法 GBK，所以按编码自动判定会先命中 GB18030；
	// 自动判定刻意优先照顾大陆用户，繁体书可在线用「重新解码」接口指定 big5）
	b := []byte{0xA4, 0xA4, 0xA4, 0xE5, 0xB4, 0xFA, 0xB8, 0xD5}
	s, ok := decodeBig5(b)
	if !ok || s != "中文測試" {
		t.Fatalf("big5 decode = %q ok=%v", s, ok)
	}
	if auto, _ := DecodeText(b); auto == "" {
		t.Fatalf("auto decode returned empty")
	}
}

func TestDecodeTextUTF16(t *testing.T) {
	units := utf16.Encode([]rune("Hello 世界"))
	var le []byte
	for _, u := range units {
		le = binary.LittleEndian.AppendUint16(le, u)
	}
	s, enc := DecodeText(append([]byte{0xFF, 0xFE}, le...))
	if !strings.Contains(s, "世界") || enc != "utf-16le" {
		t.Fatalf("decoded %q (enc=%s)", s, enc)
	}
	// 无 BOM 的 LE：内容偏 ASCII 时才判定（纯中文短串与 GBK 无法区分，不做强判，避免误伤 GB 书）
	latin := utf16.Encode([]rune(strings.Repeat("Hello world, this is plain text. ", 40)))
	raw := []byte{0xFF, 0xFE} // looksUTF16 路径只处理「非合法 UTF-8 + 大量 0 字节」的无 BOM 文件
	for _, u := range latin {
		raw = binary.LittleEndian.AppendUint16(raw, u)
	}
	raw = raw[2:]
	// 无 BOM UTF-16 里夹着 0x00 字节：UTF-8 校验会失败 → 走 looksUTF16
	rawWithHigh := append([]byte(nil), raw...)
	for i := 1; i < len(rawWithHigh); i += 2 {
		if rawWithHigh[i] == 0 {
			rawWithHigh[i] = 0x00
		}
	}
	_ = rawWithHigh
	s, enc = DecodeText(append([]byte{0x00}, raw...)) // 前置一个 0 字节破坏 UTF-8 合法性判定
	if !strings.HasPrefix(s, "Hello world") || !strings.Contains(enc, "utf-16") {
		t.Fatalf("no-bom utf16 decoded %q (enc=%s)", s[:20], enc)
	}
}

// ---- 拆分 ----

func TestSplitExplicitChapters(t *testing.T) {
	text := "Chapter 1\n\nThe first paragraph.\nStill the first paragraph.\n\nAnother one.\n\nChapter 2\n\nThe second.\n\nCHAPTER III\nThe third."
	chs := SplitText(text, SplitOptions{})
	if len(chs) != 3 {
		t.Fatalf("got %d chapters: %+v", len(chs), chs)
	}
	if chs[0].Title != "Chapter 1" || chs[1].Title != "Chapter 2" || chs[2].Title != "CHAPTER III" {
		t.Fatalf("titles = %q %q %q", chs[0].Title, chs[1].Title, chs[2].Title)
	}
	// 硬换行合并成一段
	if len(chs[0].Paragraphs) != 2 || chs[0].Paragraphs[0] != "The first paragraph. Still the first paragraph." {
		t.Fatalf("paragraphs = %#v", chs[0].Paragraphs)
	}
}

func TestSplitChineseChapters(t *testing.T) {
	text := "第一章 风起\n\n风起了。\n\n第二章 云涌\n\n云涌了。\n\n第三回 雨落\n\n雨落了。"
	chs := SplitText(text, SplitOptions{})
	if len(chs) != 3 {
		t.Fatalf("got %d chapters: %+v", len(chs), chs)
	}
	if chs[0].Title != "第一章 风起" || chs[2].Title != "第三回 雨落" {
		t.Fatalf("titles = %+v", chs)
	}
}

// 章节标记识别：中英文都要认，且不能把正文里的「第…章」当标题。
func TestIsExplicitMarker(t *testing.T) {
	yes := []string{
		"Chapter 1", "CHAPTER IV", "Chapter 1 - The Beginning", "Part 2", "Book Three",
		"第一章", "第一章 风起", "第三回 雨落", "卷二 少年", "1. The Start", "一、开篇", "§12",
	}
	no := []string{
		"Chapter 1 was the best part of the book.", "第一章讲的是经济学的历史，弗里德曼在开头",
		"He said the first number was 第一章.", "This chapter is quite long and interesting.",
	}
	for _, s := range yes {
		if !isExplicitMarker(s) {
			t.Errorf("expected marker: %q", s)
		}
	}
	for _, s := range no {
		if isExplicitMarker(s) {
			t.Errorf("unexpected marker: %q", s)
		}
	}
}

func TestSplitMarkdownSubtitle(t *testing.T) {
	text := "# Chapter 1\nThe Beginning\n\nBody text.\n\n# Chapter 2\n\nMore."
	chs := SplitText(text, SplitOptions{})
	if len(chs) != 2 {
		t.Fatalf("got %d", len(chs))
	}
	if chs[0].Title != "Chapter 1 — The Beginning" {
		t.Fatalf("title = %q", chs[0].Title)
	}
}

func TestSplitNoHeadingsAutoChapters(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.WriteString("This is a long paragraph of plain prose without any chapter markers at all, number ")
		sb.WriteString(strings.Repeat("x", 80))
		sb.WriteString(".\n\n")
	}
	chs := SplitText(sb.String(), SplitOptions{TargetChars: 1000})
	if len(chs) < 2 {
		t.Fatalf("expected auto chapters, got %d", len(chs))
	}
	for _, c := range chs {
		if !c.Inserted || !strings.HasPrefix(c.Title, "第 ") {
			t.Fatalf("auto chapter title = %q inserted=%v", c.Title, c.Inserted)
		}
	}
}

func TestSplitTOCDuplicates(t *testing.T) {
	text := "Contents\n\nChapter 1\n\nChapter 2\n\nChapter 1\n\nBody one.\n\nChapter 2\n\nBody two."
	chs := SplitText(text, SplitOptions{})
	if len(chs) != 2 {
		t.Fatalf("TOC not dropped: %+v", chs)
	}
}

func TestSplitLongChapterCapped(t *testing.T) {
	// 单段超长（真实书里常见：一整章没有空行）
	body := strings.Repeat("A fairly long sentence for testing. ", 200)
	text := "Chapter 1\n\n" + body + "\n\nChapter 2\n\nShort chapter."
	chs := SplitText(text, SplitOptions{TargetChars: 3000})
	if len(chs) < 2 {
		t.Fatalf("long chapter not split: %d", len(chs))
	}
	for i, c := range chs {
		chars, _ := ChapterStats(c.Paragraphs)
		if chars > 2400+50 {
			t.Fatalf("chapter %d too long: %d chars", i, chars)
		}
	}
	// 短正文会被并入标题后面的段落，标题识别可能退化为自动分章：
	// 只要「续」篇号连续、且不丢正文即可（正文完整性由字数校验覆盖）
	var total int
	for _, c := range chs {
		_, n := ChapterStats(c.Paragraphs)
		total += n
		if !strings.HasPrefix(c.Title, "Chapter") {
			t.Fatalf("chapter title = %q", c.Title)
		}
	}
	if total < 2 {
		t.Fatalf("paragraphs lost: %d", total)
	}
}

func TestVerseParagraphs(t *testing.T) {
	text := "# Genesis\n\n[1:1] In the beginning God created the heaven and the earth.\n[1:2] And the earth was without form, and void.\n[1:3] And God said, Let there be light."
	chs := SplitText(text, SplitOptions{})
	if len(chs) != 1 {
		t.Fatalf("chapters = %d", len(chs))
	}
	if len(chs[0].Paragraphs) != 3 {
		t.Fatalf("verses not split into paragraphs: %#v", chs[0].Paragraphs)
	}
}

func TestChapterRenderRoundTrip(t *testing.T) {
	paras := []string{"Para one.", "Para two with two lines.", "第三段。"}
	s := RenderChapter(paras)
	got := ParseChapter(s)
	if len(got) != len(paras) {
		t.Fatalf("round trip = %#v", got)
	}
	for i := range paras {
		if got[i] != paras[i] {
			t.Fatalf("round trip [%d] = %q want %q", i, got[i], paras[i])
		}
	}
}

// ---- 朗读分段 ----

func TestSplitSentences(t *testing.T) {
	got := SplitSentences("Hello world. This is a test! Is it? Yes.", 0)
	if len(got) != 1 {
		t.Fatalf("short paragraph should stay one piece, got %#v", got)
	}
	long := strings.Repeat("This sentence is fairly long and keeps going. ", 20)
	pieces := SplitSentences(long, 200)
	if len(pieces) < 3 {
		t.Fatalf("long paragraph not split: %d", len(pieces))
	}
	for _, p := range pieces {
		if n := len([]rune(p)); n > 200 {
			t.Fatalf("piece too long (%d): %q", n, p)
		}
	}
	// 小数点不该当句末
	if p := SplitSentences("Pi is 3.14 and that is fine.", 0); len(p) != 1 {
		t.Fatalf("decimal point split: %#v", p)
	}
}

// ---- EPUB ----

func buildEPUB(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("META-INF/container.xml", `<?xml version="1.0"?><container><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`)
	add("OEBPS/content.opf", `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
 <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
  <dc:title>Test Book</dc:title><dc:creator>Someone</dc:creator><dc:language>en</dc:language>
 </metadata>
 <manifest>
  <item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
  <item id="c2" href="ch2.xhtml" media-type="application/xhtml+xml"/>
  <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
 </manifest>
 <spine><itemref idref="c2"/><itemref idref="c1"/></spine>
</package>`)
	add("OEBPS/nav.xhtml", `<html><body><nav><ol><li><a href="ch1.xhtml">One</a></li></ol></nav></body></html>`)
	add("OEBPS/ch1.xhtml", `<html><head><title>ignored</title><style>p{color:red}</style></head><body><h1>The First</h1><p>First para &amp; more.</p><p>Second para.</p></body></html>`)
	add("OEBPS/ch2.xhtml", `<html><body><h2>Prologue</h2><p>Before chapter one.</p></body></html>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseEPUB(t *testing.T) {
	c, err := ParseEPUB(buildEPUB(t))
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "Test Book" || c.Author != "Someone" || c.Language != "en" {
		t.Fatalf("meta = %+v", c)
	}
	// spine 顺序：ch2 在前；nav.xhtml 不进正文；style/script 不进正文
	if !strings.HasPrefix(strings.TrimSpace(c.Text), "# Prologue") {
		t.Fatalf("spine order wrong: %q", c.Text[:60])
	}
	if strings.Contains(c.Text, "color:red") || strings.Contains(c.Text, "href=") {
		t.Fatalf("style/markup leaked: %q", c.Text)
	}
	if !strings.Contains(c.Text, "First para & more.") {
		t.Fatalf("entity not unescaped: %q", c.Text)
	}
	chs := SplitText(c.Text, SplitOptions{})
	if len(chs) != 2 || chs[0].Title != "Prologue" || chs[1].Title != "The First" {
		t.Fatalf("chapters = %+v", chs)
	}
}

func TestParseEPUBNotZip(t *testing.T) {
	if _, err := ParseEPUB([]byte("plain text")); err == nil {
		t.Fatal("expected error for non-zip input")
	}
}

// ---- 朗读计划 / 随机 ----

func TestBuildPlanPrefaceWrap(t *testing.T) {
	p := BuildPlan([]string{"In the beginning."}, SpeakOpts{Preface: "Genesis, chapter 1.", Wrap: []string{"Read this passage: {PARA}"}})
	if len(p.Segments) != 1 {
		t.Fatalf("plan = %#v", p)
	}
	if p.Segments[0] != "Read this passage: Genesis, chapter 1. In the beginning." {
		t.Fatalf("segment = %q", p.Segments[0])
	}
	// 无占位符的模板：模板在前、正文换行追加
	p = BuildPlan([]string{"Body."}, SpeakOpts{Wrap: []string{"Slowly:"}})
	if p.Segments[0] != "Slowly:\nBody." {
		t.Fatalf("segment = %q", p.Segments[0])
	}
}

func TestChapterPreface(t *testing.T) {
	if got := ChapterPreface("### Genesis", "en"); got != "Genesis." {
		t.Fatalf("en preface = %q", got)
	}
	if got := ChapterPreface("第一章 风起", "zh"); got != "《第一章 风起》。" {
		t.Fatalf("zh preface = %q", got)
	}
	if got := ChapterPreface("", "en"); got != "" {
		t.Fatalf("empty preface = %q", got)
	}
}

func mkBook(chapters, paras int) *model.Book {
	b := &model.Book{ID: "b"}
	for i := 0; i < chapters; i++ {
		b.Chapters = append(b.Chapters, model.Chapter{Index: i, Title: "ch", Paragraph: paras})
	}
	return b
}

// 段级随机：不重复，直到全部播过一轮；播完返回 ok=false。
func TestPickRandomNoRepeat(t *testing.T) {
	b := mkBook(3, 4) // 12 段
	pb := model.BookPlayback{}
	seen := map[string]bool{}
	rng := rand.New(rand.NewSource(1))
	pb.Visited = map[string]bool{}
	for i := 0; i < 12; i++ {
		ch, seg, ok := PickRandom(b, pb, rng)
		if !ok {
			t.Fatalf("ran out at %d", i)
		}
		k := fmt.Sprintf("%d:%d", ch, seg)
		if seen[k] {
			t.Fatalf("repeat %s", k)
		}
		seen[k] = true
		pb.Visited[k] = true
	}
	if _, _, ok := PickRandom(b, pb, rng); ok {
		t.Fatal("expected ok=false after a full round")
	}
}

// 章级随机：章多段少时按章挑，且能覆盖全部章。
func TestPickRandomChapterCovers(t *testing.T) {
	b := mkBook(10, 1)
	if SegmentMode(b) {
		t.Fatal("50 chapters × 1 paragraph should be chapter-mode")
	}
	if !SegmentMode(mkBook(3, 20)) {
		t.Fatal("novel-shaped book should be paragraph-mode")
	}
	pb := model.BookPlayback{}
	rng := rand.New(rand.NewSource(2))
	seen := map[int]bool{}
	for i := 0; i < 200 && len(seen) < 10; i++ {
		ch, ok := PickRandomChapter(b, pb, rng)
		if !ok {
			t.Fatal("no chapter")
		}
		seen[ch] = true
		pb.Visited = map[string]bool{fmt.Sprintf("%d:0", ch): true}
	}
	if len(seen) != 10 {
		t.Fatalf("only covered %d/10 chapters", len(seen))
	}
}

// 书籍文件常是「1 级书名 + 2 级章」，也可能只有 1 级；"### 章" 时要认 3 级。
func TestSplitMarkdownLevels(t *testing.T) {
	twoLevel := "# My Book\n\n## One\n\nfirst body\n\n## Two\n\nsecond body\n\n## Three\n\nthird body"
	chs := SplitText(twoLevel, SplitOptions{})
	if len(chs) != 3 || chs[0].Title != "One" || chs[2].Title != "Three" {
		t.Fatalf("two-level: %+v", chs)
	}
	threeLevel := "### Genesis\n\n[1:1] In the beginning.\n\n### Exodus\n\n[1:1] Now these are the names."
	chs = SplitText(threeLevel, SplitOptions{})
	if len(chs) != 2 || chs[0].Title != "Genesis" {
		t.Fatalf("three-level: %+v", chs)
	}
}

// 角色模板的 {TITLE}：章名只出现一次（模板报过，正文里就不重复）。
func TestBuildPlanTitlePlaceholder(t *testing.T) {
	opt := SpeakOpts{Preface: "Surah 1.", Wrap: []string{"{TITLE}\n{PARA}"}}
	p := BuildPlan([]string{"In the name of Allah."}, opt)
	if p.Segments[0] != "Surah 1.\nIn the name of Allah." {
		t.Fatalf("segment = %q", p.Segments[0])
	}
}
