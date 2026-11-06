package guides

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"twitter_read_play/internal/store"
)

var htmlLangRe = regexp.MustCompile(`<html lang="([a-z-]+)"`)

func htmlLang(raw string) string {
	if m := htmlLangRe.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	return ""
}

func TestReadVariantFallback(t *testing.T) {
	for _, kind := range []string{"google", "twitch", "chat_downloader", "bilibili", "youtube_noauth"} {
		base, ok := Files[kind]
		if !ok {
			t.Fatalf("kind %q not registered", kind)
		}
		for _, l := range []string{"en", "zh", "ja", "zh-CN", "fr"} {
			raw, ok := readVariant(base, []string{l})
			if !ok {
				t.Errorf("%s: no variant for lang %q", kind, l)
				continue
			}
			if strings.TrimSpace(raw) == "" {
				t.Errorf("%s: empty body for lang %q", kind, l)
			}
			want := l
			switch l {
			case "ja", "fr":
				want = "en"
			case "zh-CN":
				want = "zh"
			}
			if got := htmlLang(raw); got != want {
				t.Errorf("%s: lang %q served <html lang=%q>, want %q", kind, l, got, want)
			}
		}
	}
	if _, ok := readVariant("nosuchkind", []string{"en", "zh"}); ok {
		t.Error("unknown kind must not resolve")
	}
}

func TestLangNormalize(t *testing.T) {
	cases := map[string]string{
		"zh-CN":   "zh",
		"en-US":   "en",
		"en":      "en",
		"JA":      "ja",
		"zh-Hant": "zh",
		"":        "",
	}
	for in, want := range cases {
		if got := lang(in); got != want {
			t.Errorf("lang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAcceptLangs(t *testing.T) {
	got := acceptLangs("fr-CA,fr;q=0.9,en-US;q=0.8,zh-CN;q=0.7,*;q=0.5")
	want := []string{"fr", "en", "zh"}
	if len(got) != len(want) {
		t.Fatalf("acceptLangs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("acceptLangs = %v, want %v", got, want)
		}
	}
	if len(acceptLangs("")) != 0 {
		t.Error("empty header must yield no preferences")
	}
}

func serveWithTempStore(t *testing.T) http.HandlerFunc {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	if err := Seed(st); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	return Serve(st)
}

func TestServeDefaultsToEnglish(t *testing.T) {
	h := serveWithTempStore(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/providers/google/guide?base=http://127.0.0.1:8088", nil)
	req.SetPathValue("kind", "google")
	h(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if htmlLang(body) != "en" {
		t.Errorf("no ?lang= and no Accept-Language must serve the en variant, got %q", htmlLang(body))
	}
	if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
	}
	if strings.Contains(body, "{{BASE}}") {
		t.Error("{{BASE}} placeholder not replaced")
	}
	if !strings.Contains(body, "http://127.0.0.1:8088/api/livechat/oauth/google/callback") {
		t.Error("base not substituted into callback URLs")
	}
}

func TestServeLangSelection(t *testing.T) {
	h := serveWithTempStore(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/providers/google/guide?lang=zh", nil)
	req.SetPathValue("kind", "google")
	h(rec, req)
	if htmlLang(rec.Body.String()) != "zh" {
		t.Error("?lang=zh must serve the zh variant")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/providers/google/guide", nil)
	req.SetPathValue("kind", "google")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	h(rec, req)
	if htmlLang(rec.Body.String()) != "zh" {
		t.Error("Accept-Language zh must serve the zh variant")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/providers/google/guide?lang=en", nil)
	req.SetPathValue("kind", "google")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	h(rec, req)
	if htmlLang(rec.Body.String()) != "en" {
		t.Error("?lang=en must beat an Accept-Language zh header")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/providers/google/guide?lang=ja", nil)
	req.SetPathValue("kind", "google")
	h(rec, req)
	if rec.Code != 200 || htmlLang(rec.Body.String()) != "en" {
		t.Error("unsupported ?lang= must fall back to the en variant")
	}
}

// TestServeBaseEscaped M2 回归：?base= 原样替换进 HTML 正文构成反射型 XSS，
// 修复后必须 HTML 转义且剔除控制字符。
func TestServeBaseEscaped(t *testing.T) {
	h := serveWithTempStore(t)
	for _, payload := range []string{
		`"><script>alert(1)</script>`,
		`<img src=x onerror=alert(2)>`,
		"http://ok.example\ra<script>alert(3)</script>",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/providers/google/guide?base="+url.QueryEscape(payload), nil)
		req.SetPathValue("kind", "google")
		h(rec, req)
		if rec.Code != 200 {
			t.Fatalf("payload %q: status = %d", payload, rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, "<script>alert") || strings.Contains(body, "<img src=x") {
			t.Errorf("payload %q: 未转义进入正文", payload)
		}
	}
	// 正常 base 不受影响（转义后的 &amp; 等不在正常 URL 里）
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/providers/google/guide?base=http://127.0.0.1:8088", nil)
	req.SetPathValue("kind", "google")
	h(rec, req)
	if !strings.Contains(rec.Body.String(), "http://127.0.0.1:8088") {
		t.Error("normal base should pass through unchanged")
	}
}

func TestSeedAliases(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := Seed(st); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"google", "youtube_api", "twitch", "twitch_eventsub", "chat_downloader", "youtube_noauth", "bilibili", "bilibili_blivedm"} {
		if _, ok, err := st.GuideFile(kind); err != nil || !ok {
			t.Errorf("kind %q should be registered after Seed", kind)
		}
	}
}
