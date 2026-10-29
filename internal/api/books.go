package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"twitter_read_play/internal/book"
	"twitter_read_play/internal/watch"
)

// ---- Feed「book」模式：上传图书 → 拆章节 → 点章/点段/随机朗读 ----
//
// 书归属角色（Book.RoleID），角色之间不共享；程序不附带内置书。
// 所有图书操作都挂在角色下，归属校验在 handler 里完成（越权一律 404）。
//
// API：
//
//	GET    /api/roles/{id}/books                          该角色的书库（不含正文）
//	POST   /api/roles/{id}/books                          上传（multipart file 字段；txt/md/epub）
//	POST   /api/roles/{id}/books/claim {book_id}          认领一本未归属的旧书
//	GET    /api/roles/{id}/books/{book_id}                单本详情（章节索引）
//	DELETE /api/roles/{id}/books/{book_id}                删除书（含文件与进度）
//	POST   /api/roles/{id}/books/{book_id}/redecode       按指定编码重新解码 + 重新拆分
//	GET    /api/roles/{id}/books/{book_id}/chapters/{idx} 章节正文（段落数组 + 可点播信息）
//	GET    /api/books/state?role_id=                      该角色书库 + 进度 + 本角色播放状态 + 未归属旧书
//	POST   /api/roles/{id}/book/play {book_id, chapter, paragraph?, mode?, preface?}  朗读章/段
//	POST   /api/roles/{id}/book/random {book_id, mode?, preface?}                     随机朗读
//	POST   /api/roles/{id}/book/stop {machine_id?}                                    停止
//	POST   /api/roles/{id}/book/progress {book_id, chapter, paragraph}                设置断点/标记播过

func (s *Server) bookLib() *book.Library { return s.Watches.BookLibrary() }

// needBooks 图书模式未启用时统一 503。
func (s *Server) needBooks(w http.ResponseWriter) *book.Library {
	lib := s.bookLib()
	if lib == nil {
		s.err(w, http.StatusServiceUnavailable, errStr("book mode is not enabled"))
	}
	return lib
}

// needRole 角色不存在时 404（防止给不存在的角色上传/认领书）。
func (s *Server) needRole(w http.ResponseWriter, id string) bool {
	if _, err := s.Roles.Get(id); err != nil {
		s.err(w, http.StatusNotFound, err)
		return false
	}
	return true
}

func (s *Server) roleBookList(w http.ResponseWriter, r *http.Request) {
	lib := s.needBooks(w)
	if lib == nil {
		return
	}
	roleID := r.PathValue("id")
	if !s.needRole(w, roleID) {
		return
	}
	s.json(w, lib.ListFor(roleID))
}

func (s *Server) roleBookUpload(w http.ResponseWriter, r *http.Request) {
	lib := s.needBooks(w)
	if lib == nil {
		return
	}
	roleID := r.PathValue("id")
	if !s.needRole(w, roleID) {
		return
	}
	// 200MB 上限（epub 里可能带图，正文只抽文字）；与 book.maxUploadBytes 对齐
	r.Body = http.MaxBytesReader(w, r.Body, 220<<20)
	file, header, err := r.FormFile("file")
	if err != nil {
		s.err(w, 400, fmt.Errorf("missing file field: %w", err))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		s.err(w, 400, fmt.Errorf("read upload: %w", err))
		return
	}
	b, err := lib.UploadFor(roleID, header.Filename, data, book.UploadOpts{Title: r.FormValue("title")})
	if err != nil {
		s.err(w, 400, err)
		return
	}
	s.json(w, b)
}

// roleBookClaim POST /api/roles/{id}/books/claim {book_id}：把一本未归属的旧书归给该角色。
func (s *Server) roleBookClaim(w http.ResponseWriter, r *http.Request) {
	lib := s.needBooks(w)
	if lib == nil {
		return
	}
	roleID := r.PathValue("id")
	if !s.needRole(w, roleID) {
		return
	}
	var in struct {
		BookID string `json:"book_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.BookID == "" {
		s.err(w, 400, fmt.Errorf("book_id required"))
		return
	}
	b, err := lib.AssignOwner(in.BookID, roleID)
	if err != nil {
		s.err(w, 400, err)
		return
	}
	s.json(w, b)
}

func (s *Server) roleBookGet(w http.ResponseWriter, r *http.Request) {
	lib := s.needBooks(w)
	if lib == nil {
		return
	}
	b, err := lib.GetFor(r.PathValue("id"), r.PathValue("book_id"))
	if err != nil {
		s.err(w, 404, err)
		return
	}
	s.json(w, b)
}

func (s *Server) roleBookDelete(w http.ResponseWriter, r *http.Request) {
	lib := s.needBooks(w)
	if lib == nil {
		return
	}
	if err := lib.DeleteFor(r.PathValue("id"), r.PathValue("book_id")); err != nil {
		s.err(w, 404, err)
		return
	}
	s.json(w, map[string]bool{"ok": true})
}

func (s *Server) roleBookRedecode(w http.ResponseWriter, r *http.Request) {
	lib := s.needBooks(w)
	if lib == nil {
		return
	}
	var in struct {
		Encoding string `json:"encoding"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	b, err := lib.ReDecodeFor(r.PathValue("id"), r.PathValue("book_id"), in.Encoding)
	if err != nil {
		s.err(w, 400, err)
		return
	}
	s.json(w, b)
}

// roleBookChapter GET .../books/{book_id}/chapters/{idx}[?paragraph=N]：章节正文。
// 返回段落数组（每段带「点播」用的索引与前 120 字预览），paragraph 指定时附单段全文。
func (s *Server) roleBookChapter(w http.ResponseWriter, r *http.Request) {
	lib := s.needBooks(w)
	if lib == nil {
		return
	}
	roleID, bookID := r.PathValue("id"), r.PathValue("book_id")
	if _, err := lib.GetFor(roleID, bookID); err != nil {
		s.err(w, 404, err)
		return
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil {
		s.err(w, 400, fmt.Errorf("bad chapter index"))
		return
	}
	ch, paras, err := lib.Chapter(bookID, idx)
	if err != nil {
		s.err(w, 404, err)
		return
	}
	out := map[string]any{
		"chapter":    ch,
		"paragraphs": paras,
		"total":      len(paras),
	}
	if v := r.URL.Query().Get("paragraph"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n < len(paras) {
			out["paragraph"] = map[string]any{"index": n, "text": paras[n]}
		}
	}
	s.json(w, out)
}

// bookState GET /api/books/state?role_id=：该角色的书 + 各书进度 + 本角色播放状态 + 未归属旧书。
func (s *Server) bookState(w http.ResponseWriter, r *http.Request) {
	lib := s.needBooks(w)
	if lib == nil {
		return
	}
	roleID := r.URL.Query().Get("role_id")
	books := lib.ListFor(roleID)
	progress := map[string]any{}
	for _, b := range books {
		pb := lib.Progress(roleID, b.ID)
		progress[b.ID] = map[string]any{
			"chapter": pb.Chapter, "paragraph": pb.Segment,
			"visited": len(pb.Visited), "updated_at": pb.UpdatedAt, "last_played": pb.LastPlayed,
		}
	}
	runs := map[string]any{}
	if pl := s.Watches.BookPlayer(); pl != nil {
		for k, v := range pl.Runs() {
			// 播放状态按机器串行存：只回本角色发起的，别让 A 页面展示/停止 B 的朗读。
			if v.RoleID == roleID {
				runs[k] = v
			}
		}
	}
	s.json(w, map[string]any{
		"books": books, "progress": progress, "runs": runs,
		"unassigned": lib.Unassigned(),
	})
}

// bookPlayOpts 前端朗读参数。
type bookPlayOpts struct {
	BookID    string `json:"book_id"`
	Chapter   int    `json:"chapter"`
	Paragraph *int   `json:"paragraph"` // 指定段：只播这一段（不点 = 整章）
	Mode      string `json:"mode"`      // tts（默认，按文字念）/ chat（设备 LLM）
	Preface   bool   `json:"preface"`   // 先念章节标题
	MachineID string `json:"machine_id"`
	Slot      int    `json:"slot"`
}

// needOwnedBook 校验书属于该角色，否则 404。
func (s *Server) needOwnedBook(w http.ResponseWriter, roleID, bookID string) bool {
	lib := s.needBooks(w)
	if lib == nil {
		return false
	}
	if _, err := lib.GetFor(roleID, bookID); err != nil {
		s.err(w, 404, err)
		return false
	}
	return true
}

func (s *Server) roleBookPlay(w http.ResponseWriter, r *http.Request) {
	roleID := r.PathValue("id")
	var in bookPlayOpts
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.BookID == "" {
		s.err(w, 400, fmt.Errorf("book_id required"))
		return
	}
	if !s.needOwnedBook(w, roleID, in.BookID) {
		return
	}
	seg := -1
	if in.Paragraph != nil {
		seg = *in.Paragraph
	}
	run, err := s.Watches.PlayChapter(roleID, in.BookID, in.Chapter, seg,
		watchOpts(in.Mode, in.Preface))
	if err != nil {
		s.err(w, 400, err)
		return
	}
	s.json(w, map[string]any{"ok": true, "run": run})
}

func (s *Server) roleBookRandom(w http.ResponseWriter, r *http.Request) {
	roleID := r.PathValue("id")
	var in bookPlayOpts
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.BookID == "" {
		s.err(w, 400, fmt.Errorf("book_id required"))
		return
	}
	if !s.needOwnedBook(w, roleID, in.BookID) {
		return
	}
	run, err := s.Watches.PlayRandom(roleID, in.BookID, watchOpts(in.Mode, in.Preface))
	if err != nil {
		s.err(w, 400, err)
		return
	}
	s.json(w, map[string]any{"ok": true, "run": run})
}

func (s *Server) roleBookStop(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MachineID string `json:"machine_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if !s.Watches.StopBook(r.PathValue("id"), in.MachineID) {
		s.json(w, map[string]any{"ok": false, "error": "nothing playing"})
		return
	}
	s.json(w, map[string]any{"ok": true})
}

func (s *Server) roleBookProgress(w http.ResponseWriter, r *http.Request) {
	lib := s.needBooks(w)
	if lib == nil {
		return
	}
	var in struct {
		BookID    string `json:"book_id"`
		Chapter   int    `json:"chapter"`
		Paragraph int    `json:"paragraph"`
		Reset     bool   `json:"reset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.BookID == "" {
		s.err(w, 400, fmt.Errorf("book_id required"))
		return
	}
	roleID := r.PathValue("id")
	if _, err := lib.GetFor(roleID, in.BookID); err != nil {
		s.err(w, 404, err)
		return
	}
	if in.Reset {
		lib.ResetProgress(roleID, in.BookID)
	} else {
		lib.SetPosition(roleID, in.BookID, in.Chapter, in.Paragraph)
	}
	pb := lib.Progress(roleID, in.BookID)
	s.json(w, map[string]any{"ok": true, "chapter": pb.Chapter, "paragraph": pb.Segment})
}

// watchOpts 前端参数 → watch.BookSpeakOpts。
func watchOpts(mode string, preface bool) watch.BookSpeakOpts {
	return watch.BookSpeakOpts{Mode: mode, Preface: preface}
}
