package book

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"twitter_read_play/internal/model"
	"twitter_read_play/internal/store"
)

func newTestLibrary(t *testing.T) *Library {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	lib, err := NewLibrary(st)
	if err != nil {
		t.Fatal(err)
	}
	return lib
}

// 上传一本 txt：落盘章节文件、索引可读、正文可回读。
func TestLibraryUploadTXT(t *testing.T) {
	lib := newTestLibrary(t)
	text := "Chapter 1\n\nHello there. This is the first chapter.\n\nChapter 2\n\nSecond chapter body."
	b, err := lib.UploadFor("role1", "mybook.txt", []byte(text), UploadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Title != "mybook" || b.Format != "txt" || len(b.Chapters) != 2 {
		t.Fatalf("book = %+v", b)
	}
	for _, f := range []string{b.File, b.Chapters[0].File, b.Chapters[1].File} {
		if _, err := os.Stat(filepath.Join(lib.Dir(), "..", f)); err != nil {
			t.Fatalf("file %s missing: %v", f, err)
		}
	}
	ch, paras, err := lib.Chapter(b.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Title != "Chapter 2" || len(paras) != 1 || paras[0] != "Second chapter body." {
		t.Fatalf("chapter = %+v %#v", ch, paras)
	}
	// 列表里能拿到（不含正文）；别的角色看不到
	if list := lib.ListFor("role1"); len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("list = %+v", list)
	}
	if list := lib.ListFor("role2"); len(list) != 0 {
		t.Fatalf("role2 must not see role1 books: %+v", list)
	}
	if _, err := lib.GetFor("role2", b.ID); err != ErrNotFound {
		t.Fatalf("GetFor role2 = %v, want ErrNotFound", err)
	}
}

// GBK 上传自动解码成正确中文。
func TestLibraryUploadGBK(t *testing.T) {
	lib := newTestLibrary(t)
	gbk := []byte{0xB5, 0xDA, 0xD2, 0xBB, 0xD5, 0xC2} // 「第一章」GBK 编码
	data := append(gbk, []byte("\n\nhello\n")...)
	b, err := lib.UploadFor("role1", "cn.txt", data, UploadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if b.TXTEncoding != "gb18030" {
		t.Fatalf("encoding = %s", b.TXTEncoding)
	}
	if len(b.Chapters) == 0 || !strings.Contains(b.Chapters[0].Title, "第一章") {
		t.Fatalf("chapters = %+v", b.Chapters)
	}
}

// 重新解码：指定 utf-8 后正文按 UTF-8 读（GBK 字节按 UTF-8 解会乱码，但接口要能跑）。
func TestLibraryReDecode(t *testing.T) {
	lib := newTestLibrary(t)
	b, err := lib.UploadFor("role1", "x.txt", []byte("第一章\n\n正文。\n\n第二章\n\n正文二。"), UploadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := lib.ReDecodeFor("role1", b.ID, "utf-8")
	if err != nil {
		t.Fatal(err)
	}
	if out.TXTEncoding != "utf-8" || len(out.Chapters) != 2 {
		t.Fatalf("redecoded = %+v", out)
	}
	if _, err := lib.ReDecodeFor("role2", b.ID, "utf-8"); err != ErrNotFound {
		t.Fatalf("role2 redecode = %v, want ErrNotFound", err)
	}
}

// 删除书：索引与文件目录都清掉。
func TestLibraryDelete(t *testing.T) {
	lib := newTestLibrary(t)
	b, err := lib.UploadFor("role1", "x.txt", []byte("Chapter 1\n\nBody."), UploadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if err := lib.DeleteFor("role2", b.ID); err != ErrNotFound {
		t.Fatalf("role2 delete = %v, want ErrNotFound", err)
	}
	if err := lib.DeleteFor("role1", b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Get(b.ID); err == nil {
		t.Fatal("book still present after delete")
	}
	if _, err := os.Stat(filepath.Join(lib.Dir(), b.ID)); !os.IsNotExist(err) {
		t.Fatalf("dir not removed: %v", err)
	}
}

// makeUnowned 造一本「旧数据」：上传后清掉归属（模拟升级前的全局书库）。
func makeUnowned(t *testing.T, lib *Library, title, text string) *model.Book {
	t.Helper()
	b, err := lib.UploadFor("tmp-role", title+".txt", []byte(text), UploadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	lib.mu.Lock()
	var all []model.Book
	if err := lib.st.Load("books", &all); err != nil {
		lib.mu.Unlock()
		t.Fatal(err)
	}
	for i := range all {
		if all[i].ID == b.ID {
			all[i].RoleID = ""
			b = &all[i]
		}
	}
	if err := lib.saveLocked(all); err != nil {
		lib.mu.Unlock()
		t.Fatal(err)
	}
	lib.mu.Unlock()
	return b
}

// 旧书迁移：有首选角色时全部归它；只写归属，不删书。
func TestMigrateOwnersPreferred(t *testing.T) {
	lib := newTestLibrary(t)
	old := makeUnowned(t, lib, "old", "Chapter 1\n\nBody.")
	if n := lib.MigrateOwners([]string{"r1", "elon"}, "elon"); n != 1 {
		t.Fatalf("migrated = %d, want 1", n)
	}
	if got := lib.ListFor("elon"); len(got) != 1 || got[0].ID != old.ID {
		t.Fatalf("elon books = %+v", got)
	}
	if _, err := os.Stat(filepath.Join(lib.Dir(), old.ID)); err != nil {
		t.Fatalf("migration must not delete files: %v", err)
	}
	if n := lib.MigrateOwners([]string{"r1", "elon"}, "elon"); n != 0 {
		t.Fatalf("second migration = %d, want 0 (idempotent)", n)
	}
}

// 旧书迁移：没有首选角色时按朗读进度归属；多个角色都无进度则保持未归属。
func TestMigrateOwnersByProgress(t *testing.T) {
	lib := newTestLibrary(t)
	played := makeUnowned(t, lib, "played", "Chapter 1\n\nBody.")
	orphan := makeUnowned(t, lib, "orphan", "Chapter 1\n\nBody.")
	lib.SetPosition("r2", played.ID, 4, 1)

	if n := lib.MigrateOwners([]string{"r1", "r2"}, ""); n != 1 {
		t.Fatalf("migrated = %d, want 1", n)
	}
	if got := lib.ListFor("r2"); len(got) != 1 || got[0].ID != played.ID {
		t.Fatalf("r2 books = %+v", got)
	}
	un := lib.Unassigned()
	if len(un) != 1 || un[0].ID != orphan.ID {
		t.Fatalf("unassigned = %+v", un)
	}
	if _, err := lib.AssignOwner(orphan.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.AssignOwner(played.ID, "r1"); err == nil {
		t.Fatal("must not steal an already-owned book")
	}
}

// 删除角色：只删该角色名下的书，其他角色的书不动。
func TestDeleteByRole(t *testing.T) {
	lib := newTestLibrary(t)
	a1, err := lib.UploadFor("r1", "a1.txt", []byte("Chapter 1\n\nBody."), UploadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := lib.UploadFor("r1", "a2.txt", []byte("Chapter 1\n\nBody."), UploadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	b1, err := lib.UploadFor("r2", "b1.txt", []byte("Chapter 1\n\nBody."), UploadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	lib.SetPosition("r1", a1.ID, 1, 0)

	if n := lib.DeleteByRole("r1"); n != 2 {
		t.Fatalf("deleted = %d, want 2", n)
	}
	if got := lib.ListFor("r1"); len(got) != 0 {
		t.Fatalf("r1 books after delete = %+v", got)
	}
	if got := lib.ListFor("r2"); len(got) != 1 || got[0].ID != b1.ID {
		t.Fatalf("r2 books = %+v", got)
	}
	for _, id := range []string{a1.ID, a2.ID} {
		if _, err := os.Stat(filepath.Join(lib.Dir(), id)); !os.IsNotExist(err) {
			t.Fatalf("dir %s not removed: %v", id, err)
		}
	}
	if pb := lib.Progress("r1", a1.ID); pb.Chapter != 0 || pb.Segment != 0 {
		t.Fatalf("progress not cleared: %+v", pb)
	}
}

// 进度：断点推进、随机去重、角色删除清理。
func TestProgressAndVisited(t *testing.T) {
	lib := newTestLibrary(t)
	lib.SetPosition("role1", "bk", 3, 2)
	pb := lib.MarkPlayed("role1", "bk", 3, 2, 5)
	if pb.Chapter != 3 || pb.Segment != 3 || !pb.Visited["3:2"] {
		t.Fatalf("after mark = %+v", pb)
	}
	// 最后一段：推进到下一章开头
	pb = lib.MarkPlayed("role1", "bk", 3, 4, 5)
	if pb.Chapter != 4 || pb.Segment != 0 {
		t.Fatalf("advance = %+v", pb)
	}
	lib.ForgetRole("role1")
	if pb := lib.Progress("role1", "bk"); pb.Chapter != 0 {
		t.Fatalf("forget role left progress: %+v", pb)
	}
}
