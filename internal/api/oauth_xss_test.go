package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestOAuthCallbackXSS H1 回归：?error= 是免登录可达的外部输入，历史上经 %q 拼进
// 内联 <script>，`</script>` 原样穿过可闭合标签注入脚本。修复后 scriptJSON 转义
// `<`/`>`/`&`，响应里不允许出现未转义的闭合标签。
func TestOAuthCallbackXSS(t *testing.T) {
	s := newQueueTestServer(t)
	h := s.Routes()

	payloads := []string{
		"</script><script>alert(1)</script>",
		"'\"); alert(2); //",
		"<img src=x onerror=alert(3)>",
		"</ScRiPt><script>alert(4)</script>",
	}
	for _, p := range payloads {
		q := url.Values{"error": []string{p}}.Encode()
		req := httptest.NewRequest(http.MethodGet, "/callback/google?"+q, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("payload %q: status = %d, want 200", p, rec.Code)
		}
		body := rec.Body.String()
		start := strings.Index(body, "<script>")
		end := strings.LastIndex(body, "</script>")
		if start < 0 || end <= start {
			t.Fatalf("payload %q: 响应缺少内联 script 块", p)
		}
		// script 块内不允许出现未转义的 </script> 或任何裸 <（转义形态是 \u003c）
		script := body[start+len("<script>") : end]
		if strings.Contains(script, "<") {
			t.Fatalf("payload %q: script 块内出现未转义 <（可闭合标签注入）: %s", p, script)
		}
		// <p> 里的 HTML 转义也一并确认
		if strings.Contains(body, "<img src=x") {
			t.Fatalf("payload %q: <p> 区未转义 HTML", p)
		}
	}
}
