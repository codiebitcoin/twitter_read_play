// Package book 图书：上传（txt/md/epub…）→ 解码 → 章节拆分 → 章节/段落朗读。
//
// 设计取舍：
//   - 正文按章落文件（data/books/{id}/ch-0001.txt），库里只存索引：一本书的正文不进 JSON blob，
//     朗读时按章读文件，内存占用与书大小无关；
//   - 书归属角色（Book.RoleID），角色之间不共享；程序不再附带内置书；
//   - 本包只依赖标准库（EPUB = zip + xml + 正则），桌面/服务端两种模式都能编。
package book

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
)

const (
	// maxUploadBytes 单本上传上限（KJV 纯文本约 4MB、常见 EPUB 几 MB；200MB 是给图文并茂的大部头留的余量，
	// 但本模块只抽正文，图不落盘）。
	maxUploadBytes = 200 << 20
	maxChapters    = 20000
)

// ErrNotFound 书不存在。
var ErrNotFound = errors.New("book not found")

// Library 图书库（索引存 SQLite blobs「books」，正文存 data/books/）。
type Library struct {
	mu  sync.Mutex
	st  *store.Store
	dir string // data/books

	playMu sync.Mutex
	plays  map[string]model.BookPlayback // roleID|bookID → 进度
}

// NewLibrary 打开图书库；data/books 不存在时创建。
func NewLibrary(st *store.Store) (*Library, error) {
	dir := filepath.Join(st.Dir, "books")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Library{st: st, dir: dir, plays: map[string]model.BookPlayback{}}, nil
}

// Dir 原文与章节文件所在目录（测试/维护用）。
func (l *Library) Dir() string { return l.dir }

// List 全部书（维护/迁移用；前端请用 ListFor）。
func (l *Library) List() []model.Book {
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []model.Book
	_ = l.st.Load("books", &all)
	sort.SliceStable(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if all == nil {
		return []model.Book{}
	}
	return all
}

// ListFor 某角色自己的书（角色之间不共享）。新建在前；roleID 为空时返回空列表。
func (l *Library) ListFor(roleID string) []model.Book {
	out := []model.Book{}
	if roleID == "" {
		return out
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []model.Book
	_ = l.st.Load("books", &all)
	for _, b := range all {
		if b.RoleID == roleID {
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Unassigned 还没有归属的旧书（升级迁移期展示，可手动认领）。
func (l *Library) Unassigned() []model.Book {
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []model.Book
	_ = l.st.Load("books", &all)
	out := make([]model.Book, 0, len(all))
	for _, b := range all {
		if b.RoleID == "" {
			out = append(out, b)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Get 按 id 取书（不看归属；内部/维护用，API 请用 GetFor）。
func (l *Library) Get(id string) (*model.Book, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.getLocked(id)
}

// GetFor 按 id 取书并校验归属；不属于该角色时返回 ErrNotFound（不泄露书是否存在）。
func (l *Library) GetFor(roleID, id string) (*model.Book, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, err := l.getLocked(id)
	if err != nil {
		return nil, err
	}
	if b.RoleID != roleID {
		return nil, ErrNotFound
	}
	return b, nil
}

func (l *Library) getLocked(id string) (*model.Book, error) {
	var all []model.Book
	if err := l.st.Load("books", &all); err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			b := all[i]
			return &b, nil
		}
	}
	return nil, ErrNotFound
}

// save 加锁保存索引。
func (l *Library) saveLocked(all []model.Book) error { return l.st.Save("books", all) }

// ---- 上传 / 拆分 ----

// UploadOpts 上传参数。
type UploadOpts struct {
	Title string // 空 = 文件名 / EPUB 元数据
}

// UploadFor 保存并拆分一本属于 roleID 的书。返回导入后的索引（不含正文）。
func (l *Library) UploadFor(roleID, filename string, data []byte, opt UploadOpts) (*model.Book, error) {
	if strings.TrimSpace(roleID) == "" {
		return nil, errors.New("role_id required")
	}
	if len(data) == 0 {
		return nil, errors.New("empty file")
	}
	if len(data) > maxUploadBytes {
		return nil, fmt.Errorf("file too large (%d MB, limit %d MB)", len(data)>>20, maxUploadBytes>>20)
	}
	format := detectFormat(filename, data)
	var (
		text, enc string
		metaTitle string
		metaLang  string
		markdown  = true
	)
	switch format {
	case "epub":
		c, err := ParseEPUB(data)
		if err != nil {
			return nil, err
		}
		text, metaTitle, metaLang = c.Text, c.Title, c.Language
	case "txt", "text":
		text, enc = DecodeText(data)
		markdown = false
	case "md":
		text, enc = DecodeText(data)
	default:
		return nil, fmt.Errorf("unsupported format %q (supported: txt, md, epub)", format)
	}
	title := strings.TrimSpace(opt.Title)
	if title == "" {
		title = strings.TrimSpace(metaTitle)
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	}
	if title == "" {
		title = "Untitled"
	}
	lang := guessLang(text + " " + metaLang)
	md := markdown
	return l.importBook(model.Book{
		RoleID: roleID, Title: title, Language: lang, Format: format, TXTEncoding: enc, Source: "upload",
	}, data, text, SplitOptions{DetectHeaders: &md})
}

// importBook 落盘原文 + 章节并写索引。调用方已解码出 text。
func (l *Library) importBook(b model.Book, original []byte, text string, opt SplitOptions) (*model.Book, error) {
	chapters := SplitText(text, opt)
	if len(chapters) == 0 {
		return nil, errors.New("no readable text found")
	}
	if len(chapters) > maxChapters {
		return nil, fmt.Errorf("too many chapters (%d > %d)", len(chapters), maxChapters)
	}
	b.ID = newBookID()
	b.CreatedAt = time.Now()
	dir := filepath.Join(l.dir, b.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// 原件留档（便于以后改进拆分后重新拆分）
	if len(original) > 0 {
		ext := "." + b.Format
		if b.Format == "text" {
			ext = ".txt"
		}
		b.File = "books/" + b.ID + "/original" + ext
		if err := os.WriteFile(filepath.Join(dir, "original"+ext), original, 0o644); err != nil {
			return nil, err
		}
		b.Size = int64(len(original))
	}
	b.Chapters = make([]model.Chapter, 0, len(chapters))
	for i, ch := range chapters {
		name := fmt.Sprintf("ch-%04d.txt", i+1)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(RenderChapter(ch.Paragraphs)), 0o644); err != nil {
			return nil, err
		}
		chars, paras := ChapterStats(ch.Paragraphs)
		b.Chapters = append(b.Chapters, model.Chapter{
			Index: i, Title: ch.Title, File: "books/" + b.ID + "/" + name,
			Chars: chars, Paragraph: paras, Inserted: ch.Inserted, AddedAt: time.Now(),
		})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []model.Book
	if err := l.st.Load("books", &all); err != nil {
		return nil, err
	}
	all = append(all, b)
	if err := l.saveLocked(all); err != nil {
		return nil, err
	}
	log.Printf("[book] imported %q (id=%s role=%s format=%s enc=%s chapters=%d)", b.Title, b.ID, b.RoleID, b.Format, b.TXTEncoding, len(b.Chapters))
	out := b
	return &out, nil
}

// ReDecodeFor 按指定编码重新解码并重新拆分（老书编码识别错了可以救回来）。
// encoding: auto | utf-8 | gb18030 | big5 | utf-16le | utf-16be。返回拆分出的章节数。
func (l *Library) ReDecodeFor(roleID, id, encoding string) (*model.Book, error) {
	b, err := l.GetFor(roleID, id)
	if err != nil {
		return nil, err
	}
	if b.Format == "epub" {
		return nil, errors.New("epub books are parsed from XHTML, encoding is fixed by the package")
	}
	if b.File == "" {
		return nil, errors.New("original file not stored for this book")
	}
	data, err := os.ReadFile(filepath.Join(l.st.Dir, b.File))
	if err != nil {
		return nil, fmt.Errorf("read original: %w", err)
	}
	text, enc := forceDecode(data, encoding)
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("decoded text is empty")
	}
	md := b.Format == "md"
	chapters := SplitText(text, SplitOptions{DetectHeaders: &md})
	if len(chapters) == 0 {
		return nil, errors.New("no readable text after re-decode")
	}
	dir := filepath.Join(l.dir, b.ID)
	chaps := make([]model.Chapter, 0, len(chapters))
	for i, ch := range chapters {
		name := fmt.Sprintf("ch-%04d.txt", i+1)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(RenderChapter(ch.Paragraphs)), 0o644); err != nil {
			return nil, err
		}
		chars, paras := ChapterStats(ch.Paragraphs)
		chaps = append(chaps, model.Chapter{
			Index: i, Title: ch.Title, File: "books/" + b.ID + "/" + name,
			Chars: chars, Paragraph: paras, Inserted: ch.Inserted, AddedAt: time.Now(),
		})
	}
	l.mu.Lock()
	var all []model.Book
	if err := l.st.Load("books", &all); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			all[i].Chapters = chaps
			all[i].TXTEncoding = enc
			all[i].Language = guessLang(text + " " + all[i].Language)
		}
	}
	err = l.saveLocked(all)
	l.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return l.Get(id)
}

// DeleteFor 删除角色自己的一本书（索引 + 目录 + 进度）。不属于该角色时返回 ErrNotFound。
func (l *Library) DeleteFor(roleID, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []model.Book
	if err := l.st.Load("books", &all); err != nil {
		return err
	}
	kept := all[:0]
	found := false
	for _, b := range all {
		if b.ID == id && b.RoleID == roleID {
			found = true
			continue
		}
		kept = append(kept, b)
	}
	if !found {
		return ErrNotFound
	}
	if err := l.saveLocked(kept); err != nil {
		return err
	}
	l.removeFilesLocked(id)
	return nil
}

// DeleteByRole 角色删除时级联清掉它名下的全部书（索引 + 文件 + 进度）。返回删除本数。
func (l *Library) DeleteByRole(roleID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []model.Book
	if err := l.st.Load("books", &all); err != nil {
		log.Printf("[book] delete role %s: load books: %v", roleID, err)
		return 0
	}
	kept := make([]model.Book, 0, len(all))
	var removed []string
	for _, b := range all {
		if b.RoleID == roleID {
			removed = append(removed, b.ID)
			continue
		}
		kept = append(kept, b)
	}
	if len(removed) == 0 {
		return 0
	}
	if err := l.saveLocked(kept); err != nil {
		log.Printf("[book] delete role %s: save index: %v", roleID, err)
		return 0
	}
	for _, id := range removed {
		l.removeFilesLocked(id)
	}
	log.Printf("[book] role %s deleted: removed %d book(s)", roleID, len(removed))
	return len(removed)
}

// AssignOwner 把一本未归属的旧书归给指定角色（不能抢已归属的书）。
func (l *Library) AssignOwner(id, roleID string) (*model.Book, error) {
	if strings.TrimSpace(roleID) == "" {
		return nil, errors.New("role_id required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []model.Book
	if err := l.st.Load("books", &all); err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID != id {
			continue
		}
		if all[i].RoleID != "" {
			return nil, fmt.Errorf("book already belongs to role %q", all[i].RoleID)
		}
		all[i].RoleID = roleID
		if err := l.saveLocked(all); err != nil {
			return nil, err
		}
		out := all[i]
		log.Printf("[book] assigned %q (id=%s) → role %s", out.Title, out.ID, roleID)
		return &out, nil
	}
	return nil, ErrNotFound
}

// MigrateOwners 给旧数据（Book.RoleID 为空）补归属。只写归属，不删除任何书；可重入。
//
// 规则：
//  1. preferredRoleID 存在时，全部归它（本次升级要求：旧书统一迁到 Elon Musk 角色）；
//  2. 否则按朗读进度找 owner（多个取最近播放的）；
//  3. 完全没有进度且只有一个角色时归它；多角色则保持未归属，前端提供手动认领。
func (l *Library) MigrateOwners(roleIDs []string, preferredRoleID string) int {
	valid := make(map[string]bool, len(roleIDs))
	for _, id := range roleIDs {
		valid[id] = true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var all []model.Book
	if err := l.st.Load("books", &all); err != nil {
		log.Printf("[book] migrate owners: load books: %v", err)
		return 0
	}
	changed := 0
	for i := range all {
		b := &all[i]
		if b.RoleID != "" {
			continue
		}
		owner := ""
		if preferredRoleID != "" && valid[preferredRoleID] {
			owner = preferredRoleID
		} else {
			owner = l.bestLegacyOwnerLocked(b.ID, roleIDs)
		}
		if owner == "" {
			continue
		}
		b.RoleID = owner
		changed++
		log.Printf("[book] migrate owner: %q (id=%s) → role %s", b.Title, b.ID, owner)
	}
	if changed > 0 {
		if err := l.saveLocked(all); err != nil {
			log.Printf("[book] migrate owners: save index: %v", err)
			return 0
		}
	}
	return changed
}

// bestLegacyOwnerLocked 按进度挑旧书的 owner（调用方持 l.mu）。
func (l *Library) bestLegacyOwnerLocked(bookID string, roleIDs []string) string {
	l.playMu.Lock()
	defer l.playMu.Unlock()
	var best string
	var bestAt time.Time
	for _, rid := range roleIDs {
		pb, ok := l.plays[keyOf(rid, bookID)]
		if !ok {
			continue
		}
		at := pb.LastPlayed
		if at.IsZero() {
			at = pb.UpdatedAt
		}
		if at.IsZero() && pb.Chapter == 0 && pb.Segment == 0 {
			continue
		}
		if best == "" || at.After(bestAt) {
			best, bestAt = rid, at
		}
	}
	if best != "" {
		return best
	}
	if len(roleIDs) == 1 {
		return roleIDs[0]
	}
	return ""
}

// removeFilesLocked 删除某本书的正文目录与所有角色的朗读进度（调用方持 l.mu）。
func (l *Library) removeFilesLocked(id string) {
	if err := os.RemoveAll(filepath.Join(l.dir, id)); err != nil {
		log.Printf("[book] remove dir %s: %v", id, err)
	}
	l.playMu.Lock()
	for k := range l.plays {
		if strings.HasSuffix(k, "|"+id) {
			delete(l.plays, k)
		}
	}
	l.playMu.Unlock()
}

// ---- 章节正文 ----

// Chapter 读一章的段落（优先用落盘文件；索引损坏时返回错误）。
func (l *Library) Chapter(id string, idx int) (model.Chapter, []string, error) {
	b, err := l.Get(id)
	if err != nil {
		return model.Chapter{}, nil, err
	}
	if idx < 0 || idx >= len(b.Chapters) {
		return model.Chapter{}, nil, fmt.Errorf("chapter %d out of range (0..%d)", idx, len(b.Chapters)-1)
	}
	ch := b.Chapters[idx]
	raw, err := os.ReadFile(filepath.Join(l.st.Dir, ch.File))
	if err != nil {
		return ch, nil, fmt.Errorf("read chapter: %w", err)
	}
	return ch, ParseChapter(string(raw)), nil
}

// ChapterText 章的完整正文（段落用空行连接，可直接朗读）。
func (l *Library) ChapterText(id string, idx int) (model.Chapter, string, error) {
	ch, paras, err := l.Chapter(id, idx)
	if err != nil {
		return ch, "", err
	}
	return ch, strings.Join(paras, "\n\n"), nil
}

// ---- 原始整数 ----

func newBookID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("bk%d", time.Now().UnixNano())
	}
	return "bk" + hex.EncodeToString(buf)
}

// detectFormat 按扩展名 / 魔数判断格式。
func detectFormat(filename string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".epub":
		return "epub"
	case ".md", ".markdown":
		return "md"
	case ".txt", ".text":
		return "txt"
	case ".html", ".htm", ".xhtml":
		return "html"
	}
	if len(data) > 4 && data[0] == 'P' && data[1] == 'K' {
		return "epub" // zip 容器：按 epub 试
	}
	if strings.HasPrefix(strings.TrimSpace(string(data[:min(len(data), 512)])), "<!DOCTYPE html") {
		return "html"
	}
	return "txt" // 无扩展名 / mobi 等：按纯文本尽力而为
}

// guessLang 极简语言判断（中文/日文/英文），只用于朗读章节标题时的提示。
func guessLang(s string) string {
	var han, kana, latin int
	for _, r := range s {
		switch {
		case r >= 0x3040 && r <= 0x30FF:
			kana++
		case r >= 0x4E00 && r <= 0x9FFF:
			han++
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			latin++
		}
	}
	switch {
	case kana > han/4 && kana > 20:
		return "ja"
	case han > latin && han > 100:
		return "zh"
	case latin > 100:
		return "en"
	}
	return ""
}

// forceDecode 按指定编码解码；encoding=auto 走自动识别。
func forceDecode(b []byte, encoding string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "auto":
		return DecodeText(b)
	case "utf-8", "utf8":
		s := string(b)
		s = strings.TrimPrefix(s, "\uFEFF")
		return normalizeNewlines(s), "utf-8"
	case "gb18030", "gbk", "gb2312":
		s, _ := decodeGB18030(b)
		return s, "gb18030"
	case "big5":
		s, _ := decodeBig5(b)
		return s, "big5"
	case "utf-16le":
		return normalizeNewlines(decodeUTF16(b, false)), "utf-16le"
	case "utf-16be":
		return normalizeNewlines(decodeUTF16(b, true)), "utf-16be"
	}
	return DecodeText(b)
}
