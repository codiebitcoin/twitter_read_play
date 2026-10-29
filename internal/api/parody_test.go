package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// withPathValues 给请求塞好 {id}/{tid} 路径参数（直接调 handler 时 ServeMux 不参与）。
func withPathValues(r *http.Request, kv ...string) *http.Request {
	for i := 0; i+1 < len(kv); i += 2 {
		r.SetPathValue(kv[i], kv[i+1])
	}
	return r
}

// fakeLLMServer 假 OpenAI 兼容端点：固定返回一条「带包裹」的回复，验证清洗链路。
func fakeLLMServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"Post: \"To Mars, obviously.\""}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// parody 全链路（走 HTTP handler）：配置 LLM → 生成 → 落库 → 独立列表能查到，
// 且真实推文列表（broadcast/random 用）里不会出现这条仿写。
func TestParodyGenerateAndList(t *testing.T) {
	s := newQueueTestServer(t)
	srv := fakeLLMServer(t)
	for k, v := range map[string]string{
		"llm_base_url": srv.URL, "llm_api_key": "test-key", "llm_model": "gpt-test",
	} {
		if err := s.Store.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	// 空库初始化会插入内置角色 musk
	roleID := "musk"

	rec := httptest.NewRecorder()
	s.roleParodyGenerate(rec, withPathValues(httptest.NewRequest(http.MethodPost, "/api/roles/musk/parody/generate", nil), "id", roleID))
	if rec.Code != http.StatusOK {
		t.Fatalf("generate status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var gen struct {
		OK    bool `json:"ok"`
		Tweet struct {
			ID     string `json:"id"`
			Text   string `json:"text"`
			Source string `json:"source"`
		} `json:"tweet"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &gen); err != nil {
		t.Fatalf("generate 非 JSON: %v (%s)", err, rec.Body.String())
	}
	if !gen.OK || gen.Tweet.ID == "" {
		t.Fatalf("generate 返回异常: %s", rec.Body.String())
	}
	if gen.Tweet.Text != "To Mars, obviously." {
		t.Errorf("生成文本未清洗: %q", gen.Tweet.Text)
	}
	if gen.Tweet.Source != "parody" {
		t.Errorf("source = %q, want parody", gen.Tweet.Source)
	}

	// 独立列表里能查到
	rec = httptest.NewRecorder()
	s.roleParodyList(rec, withPathValues(httptest.NewRequest(http.MethodGet, "/api/roles/musk/parody", nil), "id", roleID))
	var list struct {
		Items []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("list 非 JSON: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != gen.Tweet.ID {
		t.Fatalf("parody 列表 = %+v, want 1 条 id=%s", list.Items, gen.Tweet.ID)
	}
	if list.Items[0].Source != "parody" {
		t.Errorf("列表条目 source = %q, want parody", list.Items[0].Source)
	}

	// 真实推文列表（broadcast/random 用）不应包含仿写
	rec = httptest.NewRecorder()
	s.roleTweets(rec, withPathValues(httptest.NewRequest(http.MethodGet, "/api/roles/musk/tweets?limit=50", nil), "id", roleID))
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" && body != "null" {
		t.Errorf("真实推文列表应为空（仿写不该混进来），got %s", body)
	}

	// 删除
	rec = httptest.NewRecorder()
	req := withPathValues(httptest.NewRequest(http.MethodDelete, "/api/roles/musk/parody/"+gen.Tweet.ID, nil), "id", roleID)
	req = withPathValues(req, "tid", gen.Tweet.ID)
	s.roleParodyDelete(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	s.roleParodyList(rec, withPathValues(httptest.NewRequest(http.MethodGet, "/api/roles/musk/parody", nil), "id", roleID))
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list.Items) != 0 {
		t.Fatalf("删除后列表应为空，got %+v", list.Items)
	}
}

// captureLLMServer 假 OpenAI 兼容端点：把收到的请求体记进 got，便于断言真正下发的 messages。
func captureLLMServer(t *testing.T, got *[]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = b
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"To Mars, obviously."}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 回归：parody 生成只能下发那唯一的生成 prompt（作为 system），绝不能拿 Persona 的
// r.Prompt 顶替——内置 musk 的 Prompt 是「朗读播报」指令（"Start by saying you are reading
// your latest post..."），拿它当 system 会让模型输出开场白 + 结尾点评，而不是纯推文。
func TestParodySendsSinglePromptNotPersonaReadingPrompt(t *testing.T) {
	s := newQueueTestServer(t)
	var body []byte
	srv := captureLLMServer(t, &body)
	for k, v := range map[string]string{
		"llm_base_url": srv.URL, "llm_api_key": "test-key", "llm_model": "gpt-test",
	} {
		if err := s.Store.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	// 前置条件：内置 musk 的 Persona prompt 确实是朗读指令（否则这条回归没意义）
	role, err := s.Roles.Get("musk")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(role.Prompt), "read the given post aloud") {
		t.Fatalf("前置条件不成立：musk 的 Persona prompt 不是朗读指令: %q", role.Prompt)
	}

	rec := httptest.NewRecorder()
	s.roleParodyGenerate(rec, withPathValues(httptest.NewRequest(http.MethodPost, "/api/roles/musk/parody/generate", nil), "id", "musk"))
	if rec.Code != http.StatusOK {
		t.Fatalf("generate status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("请求体非 JSON: %v (%s)", err, string(body))
	}
	if len(req.Messages) < 2 || req.Messages[0].Role != "system" {
		t.Fatalf("messages 结构异常: %+v", req.Messages)
	}
	sys := req.Messages[0].Content
	low := strings.ToLower(sys)
	// 1) 绝不能用朗读指令
	if strings.Contains(low, "read the given post aloud") || strings.Contains(low, "reading your latest post") {
		t.Errorf("system 用了 Persona 朗读 prompt（会导致开场白+结尾点评）:\n%s", sys)
	}
	// 2) 变量必须渲染干净，并带上人设
	if strings.Contains(sys, "{{") {
		t.Errorf("system 里模板变量没替换干净:\n%s", sys)
	}
	for _, want := range []string{"Elon Musk", "@elonmusk"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system 缺人设信息 %q:\n%s", want, sys)
		}
	}
	// 3) 只出正文的约束必须在
	for _, want := range []string{"Output ONLY the tweet text", "no closing remark"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system 缺输出约束 %q:\n%s", want, sys)
		}
	}
}

// 没配 LLM 时生成要明确报错（前端据此提示去 Settings 配置）。
func TestParodyGenerateWithoutLLM(t *testing.T) {
	s := newQueueTestServer(t)
	rec := httptest.NewRecorder()
	s.roleParodyGenerate(rec, withPathValues(httptest.NewRequest(http.MethodPost, "/api/roles/musk/parody/generate", nil), "id", "musk"))
	// 注意：本项目的 s.err 一律返回 HTTP 200 + {"error":...}（前端 api() 靠 body.error 判错）
	if !strings.Contains(rec.Body.String(), "LLM API 未配置") {
		t.Fatalf("没配 LLM 应返回明确错误，got %s", rec.Body.String())
	}
}
