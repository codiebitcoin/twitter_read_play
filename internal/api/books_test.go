package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"twitter_read_play/internal/book"
	"twitter_read_play/internal/model"
)

func newBookTestServer(t *testing.T) (*Server, *book.Library) {
	t.Helper()
	s := newQueueTestServer(t)
	lib, err := book.NewLibrary(s.Store)
	if err != nil {
		t.Fatalf("book.NewLibrary: %v", err)
	}
	s.Watches.SetBooks(lib)
	for _, r := range []*model.Role{{ID: "r1", Name: "Elon"}, {ID: "r2", Name: "Other"}} {
		if err := s.Roles.Upsert(r); err != nil {
			t.Fatalf("role.Upsert(%s): %v", r.ID, err)
		}
	}
	return s, lib
}

// state 只返回当前角色的书，不把别的角色的书透出去。
func TestBookStateScopedToRole(t *testing.T) {
	s, lib := newBookTestServer(t)
	b1, err := lib.UploadFor("r1", "one.txt", []byte("Chapter 1\n\nBody."), book.UploadOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.UploadFor("r2", "two.txt", []byte("Chapter 1\n\nBody."), book.UploadOpts{}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.bookState(rec, httptest.NewRequest(http.MethodGet, "/api/books/state?role_id=r1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Books      []model.Book   `json:"books"`
		Progress   map[string]any `json:"progress"`
		Runs       map[string]any `json:"runs"`
		Unassigned []model.Book   `json:"unassigned"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("非 JSON: %v (%s)", err, rec.Body.String())
	}
	if len(body.Books) != 1 || body.Books[0].ID != b1.ID {
		t.Fatalf("r1 books = %+v", body.Books)
	}
	if len(body.Unassigned) != 0 {
		t.Fatalf("unassigned = %+v", body.Unassigned)
	}
}

// apiErr 现有接口的错误统一走 s.err（HTTP 200 + {"error": ...}），这里按这个约定断言。
func apiErr(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("非 JSON: %v (%s)", err, rec.Body.String())
	}
	return body.Error
}

// 跨角色读写一律拒绝。
func TestBookOwnershipEnforced(t *testing.T) {
	s, lib := newBookTestServer(t)
	b1, err := lib.UploadFor("r1", "one.txt", []byte("Chapter 1\n\nBody."), book.UploadOpts{})
	if err != nil {
		t.Fatal(err)
	}

	get := httptest.NewRequest(http.MethodGet, "/api/roles/r2/books/"+b1.ID, nil)
	get.SetPathValue("id", "r2")
	get.SetPathValue("book_id", b1.ID)
	rec := httptest.NewRecorder()
	s.roleBookGet(rec, get)
	if msg := apiErr(t, rec); msg == "" {
		t.Fatalf("r2 get must be rejected: %s", rec.Body.String())
	}

	del := httptest.NewRequest(http.MethodDelete, "/api/roles/r2/books/"+b1.ID, nil)
	del.SetPathValue("id", "r2")
	del.SetPathValue("book_id", b1.ID)
	rec = httptest.NewRecorder()
	s.roleBookDelete(rec, del)
	if msg := apiErr(t, rec); msg == "" {
		t.Fatalf("r2 delete must be rejected: %s", rec.Body.String())
	}

	// owner 自己能读
	get = httptest.NewRequest(http.MethodGet, "/api/roles/r1/books/"+b1.ID, nil)
	get.SetPathValue("id", "r1")
	get.SetPathValue("book_id", b1.ID)
	rec = httptest.NewRecorder()
	s.roleBookGet(rec, get)
	if msg := apiErr(t, rec); msg != "" {
		t.Fatalf("r1 get = %q (%s)", msg, rec.Body.String())
	}
}

// 给不存在的角色上传/认领书要拒绝。
func TestBookRequiresExistingRole(t *testing.T) {
	s, _ := newBookTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/roles/nope/books/claim",
		http.NoBody)
	req.SetPathValue("id", "nope")
	rec := httptest.NewRecorder()
	s.roleBookClaim(rec, req)
	if msg := apiErr(t, rec); msg == "" {
		t.Fatalf("claim for missing role must be rejected: %s", rec.Body.String())
	}
}
