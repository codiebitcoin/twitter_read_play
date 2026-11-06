package book

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"path"
	"regexp"
	"strings"
)

// ---- EPUB（EPUB2 / EPUB3、DRM-free）→ 纯文本 ----
//
// 拆书流程：META-INF/container.xml 找 OPF → OPF 的 spine 决定阅读顺序（不按文件名排序：
// 很多电子书正文文件按 1.html、10.html、2.html 命名，按名字排就是乱的）→ 逐份 XHTML 抽正文。
// 目录（toc.ncx / nav.xhtml）只用来给 spine 文件补标题，没有就沿用 html 里的 h1/h2 标题行。

// EPUBContent EPUB 解析结果。
type EPUBContent struct {
	Title    string
	Author   string
	Language string
	Text     string // 拼好的纯文本（章节标题行独立成行，正文段落间空行）
	XHTML    bool   // true → 拆分时额外认 Markdown 式标题行
}

type containerXML struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

type opfPackage struct {
	Metadata struct {
		Title       string   `xml:"http://purl.org/dc/elements/1.1/ title"`
		Creator     []string `xml:"http://purl.org/dc/elements/1.1/ creator"`
		Language    string   `xml:"http://purl.org/dc/elements/1.1/ language"`
		TitleAlt    string   `xml:"title"`
		CreatorAlt  []string `xml:"creator"`
		LanguageAlt string   `xml:"language"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID         string `xml:"id,attr"`
			Href       string `xml:"href,attr"`
			MediaType  string `xml:"media-type,attr"`
			Properties string `xml:"properties,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		ItemRefs []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

type ncxDoc struct {
	NavPoints []struct {
		Label  string `xml:"navLabel>text"`
		Source string `xml:"content>src,attr"`
	} `xml:"navMap>navPoint"`
}

// epub limits（M6）：上传入口已限 200MB 压缩包，但 zip 解压后可以任意膨胀
// （spine 重复引用同一个成员即可让内存无界增长）——读取总量与条目数都设上限。
const (
	epubMaxMemberBytes = 64 << 20   // 单个成员解压上限
	epubMaxTotalBytes  = 512 << 20  // 整本累计解压上限
	epubMaxMembers     = 20000      // zip 成员数上限（防极端目录表）
)

// ParseEPUB 解析 EPUB 字节流。
func ParseEPUB(data []byte) (*EPUBContent, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a valid epub (zip): %w", err)
	}
	if len(zr.File) > epubMaxMembers {
		return nil, fmt.Errorf("epub has too many entries (%d > %d)", len(zr.File), epubMaxMembers)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	var totalRead int64
	read := func(name string) ([]byte, error) {
		f, ok := files[name]
		if !ok {
			return nil, fmt.Errorf("missing %s", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		// 先试探额度，再按剩余额度读取（累计超限直接报错，不静默截断正文）
		if totalRead >= epubMaxTotalBytes {
			return nil, fmt.Errorf("epub decompressed size exceeds %d MB", epubMaxTotalBytes>>20)
		}
		b, err := io.ReadAll(io.LimitReader(rc, min64(epubMaxMemberBytes, epubMaxTotalBytes-totalRead)))
		totalRead += int64(len(b))
		return b, err
	}
	var cont containerXML
	cb, err := read("META-INF/container.xml")
	if err != nil {
		return nil, err
	}
	if err := xml.Unmarshal(cb, &cont); err != nil || len(cont.Rootfiles) == 0 {
		return nil, fmt.Errorf("epub container.xml invalid")
	}
	opfPath := cont.Rootfiles[0].FullPath
	opfPath = strings.TrimPrefix(opfPath, "./")
	ob, err := read(opfPath)
	if err != nil {
		return nil, err
	}
	var pkg opfPackage
	if err := xml.Unmarshal(ob, &pkg); err != nil {
		return nil, fmt.Errorf("epub opf invalid: %w", err)
	}
	out := &EPUBContent{
		Title:    firstNonEmpty(pkg.Metadata.Title, pkg.Metadata.TitleAlt),
		Language: firstNonEmpty(pkg.Metadata.Language, pkg.Metadata.LanguageAlt),
	}
	if len(pkg.Metadata.Creator) > 0 {
		out.Author = pkg.Metadata.Creator[0]
	} else if len(pkg.Metadata.CreatorAlt) > 0 {
		out.Author = pkg.Metadata.CreatorAlt[0]
	}
	baseDir := path.Dir(opfPath)
	mediaByID := map[string]string{}
	propsByID := map[string]string{}
	for _, it := range pkg.Manifest.Items {
		mediaByID[it.ID] = it.Href
		propsByID[it.ID] = it.Properties
	}
	// 目录（可选）：文件路径 → 目录标题
	titles := map[string]string{}
	for id, href := range mediaByID {
		href = strings.ToLower(href)
		if strings.HasSuffix(href, ".ncx") {
			if b, err := read(path.Join(baseDir, mediaByID[id])); err == nil {
				var ncx ncxDoc
				if xml.Unmarshal(b, &ncx) == nil {
					for _, np := range ncx.NavPoints {
						src := strings.Split(np.Source, "#")[0]
						if src != "" && np.Label != "" {
							titles[path.Join(baseDir, path.Clean(src))] = strings.TrimSpace(np.Label)
						}
					}
				}
			}
		}
	}

	var sb strings.Builder
	seenSpine := map[string]bool{} // M6：同一成员重复引用只读一次（zip 炸弹的主要放大器）
	for _, ref := range pkg.Spine.ItemRefs {
		href, ok := mediaByID[ref.IDRef]
		if !ok {
			continue
		}
		props := propsByID[ref.IDRef]
		if strings.Contains(props, "nav") { // nav.xhtml 是目录页，跳过正文
			continue
		}
		full := path.Join(baseDir, href)
		if i := strings.IndexByte(full, '#'); i >= 0 {
			full = full[:i]
		}
		if seenSpine[full] {
			continue
		}
		seenSpine[full] = true
		b, err := read(full)
		if err != nil {
			continue
		}
		text := htmlToText(b)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if t := titles[full]; t != "" {
			sb.WriteString("# " + t + "\n\n")
		}
		sb.WriteString(text)
		sb.WriteString("\n\n")
	}
	out.Text = sb.String()
	if strings.TrimSpace(out.Text) == "" {
		return nil, fmt.Errorf("epub has no readable text (DRM-protected or image-only)")
	}
	return out, nil
}

// htmlToText 极简 XHTML → 文本：
// 每个块级元素一行；h1–h3 → "# 标题"（拆分时识别），其余行内标签只保留文字。
// 不追求完美排版，追求「朗读时断句自然、不丢正文」。
var (
	tagBlockRe = regexp.MustCompile(`(?i)</?(p|div|br|li|tr|h[1-6]|section|article|blockquote|hr|title)(\s[^>]*)?/?>`)
	tagHeadRe  = regexp.MustCompile(`(?i)<h([1-3])(?:\s[^>]*)?>`)
	tagHeadEnd = regexp.MustCompile(`(?i)</h[1-3]>`)
	tagAnyRe   = regexp.MustCompile(`(?s)<[^>]+>`)
	entityRe   = regexp.MustCompile(`&[a-zA-Z]+;|&#\d+;|&#x[0-9a-fA-F]+;`)
)

func htmlToText(b []byte) string {
	s := string(b)
	// 去掉 head / script / style
	s = regexp.MustCompile(`(?is)<head[^>]*>.*?</head>`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`).ReplaceAllString(s, "")
	// 标题加井号
	s = tagHeadRe.ReplaceAllString(s, "\n\n@@H@@")
	s = tagHeadEnd.ReplaceAllString(s, "@@E@@\n\n")
	s = tagBlockRe.ReplaceAllString(s, "\n\n")
	s = tagAnyRe.ReplaceAllString(s, "")
	s = entityRe.ReplaceAllStringFunc(s, func(e string) string { return html.UnescapeString(e) })
	// 标题标记 → "# 标题"
	for {
		i := strings.Index(s, "@@H@@")
		if i < 0 {
			break
		}
		j := strings.Index(s[i:], "@@E@@")
		if j < 0 {
			s = strings.Replace(s, "@@H@@", "", 1)
			continue
		}
		title := strings.Join(strings.Fields(s[i+len("@@H@@"):i+j]), " ")
		s = s[:i] + "\n\n# " + title + "\n\n" + s[i+j+len("@@E@@"):]
	}
	// 压空行
	var out []string
	blank := 0
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, " \t\u00A0")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// min64 取两者较小值。
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
