package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// H3 回归：PUT /api/roles/{id} 写入 {"icon":"../app.db"} 后 GET /icon 曾能读走
// 存有全部密钥的 SQLite。修复后素材路径字段必须属于本角色名下，非法一律 400。
func TestUpsertRoleRejectsAssetTraversal(t *testing.T) {
	s := newQueueTestServer(t)

	// 建一个正常角色
	rec := httptest.NewRecorder()
	s.upsertRole(rec, httptest.NewRequest(http.MethodPost, "/api/roles", strings.NewReader(`{"name":"Traversal Target"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("新建角色失败: %d %s", rec.Code, rec.Body.String())
	}
	id := pathValueFromRoleResp(rec.Body.String())

	for _, tc := range []struct{ field, val string }{
		{"icon", "../app.db"},
		{"icon", "roles/r-other/evil.png"},
		{"local_vrm", "../../etc/passwd"},
		{"local_voice", "/etc/passwd"},
		{"local_vrms", `[{"file":"../../app.db"}]`},
	} {
		body := `{"name":"Traversal Target"`
		switch tc.field {
		case "local_vrms", "local_voices":
			body += `,"` + tc.field + `":` + tc.val
		default:
			body += `,"` + tc.field + `":"` + tc.val + `"`
		}
		body += `}`
		req := httptest.NewRequest(http.MethodPut, "/api/roles/"+id, strings.NewReader(body))
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		s.upsertRole(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s=%q: status = %d, want 400（body: %s）", tc.field, tc.val, rec.Code, rec.Body.String())
		}
	}

	// 合法路径（本角色名下）仍可保存 —— 与服务端上传接口产出的值同形
	req := httptest.NewRequest(http.MethodPut, "/api/roles/"+id,
		strings.NewReader(`{"name":"Traversal Target","icon":"roles/`+id+`/icon.png"}`))
	req.SetPathValue("id", id)
	rec = httptest.NewRecorder()
	s.upsertRole(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("own-role icon path should save: %d %s", rec.Code, rec.Body.String())
	}
}

func pathValueFromRoleResp(body string) string {
	// 响应是完整角色 JSON，只取 id 字段
	i := strings.Index(body, `"id":"`)
	if i < 0 {
		return ""
	}
	rest := body[i+len(`"id":"`):]
	return rest[:strings.Index(rest, `"`)]
}
