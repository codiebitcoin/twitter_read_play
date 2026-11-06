// Package guides 提供「怎么申请凭证」的分来源教程。
//
// 正文是独立 HTML（html/<kind>.<lang>.html），改文案不用动 JSX。
// SQLite 是索引：provider_guides 把 kind 映射到文件基名，API 读这张表——
// 加一篇教程 = 丢 html + 登记一行，不用改代码路径。
// 英语是默认：没 ?lang=、也没有我们支持的 Accept-Language 时，走 .en.html。
package guides

import (
	"embed"
	"html"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"twitter_read_play/internal/store"
)

//go:embed html/*.html
var dir embed.FS

// Seed 把打包的教程登记进 SQLite。每次启动都 upsert：改了文件内容会更新，表里基名不变。
func Seed(st *store.Store) error {
	for kind, file := range Files {
		if err := st.UpsertGuide(kind, file); err != nil {
			return err
		}
	}
	return nil
}

// lang 把请求语言标签（"zh-CN"、"en-US"、"ja"）收成包内语言。
// 未知标签原样往下传，交给文件查找的回退链。
func lang(tag string) string {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexByte(tag, ','); i >= 0 {
		tag = tag[:i]
	}
	if i := strings.IndexAny(tag, "-_"); i > 0 {
		tag = tag[:i]
	}
	return tag
}

// acceptLangs 按 q 值从高到低返回 Accept-Language 标签。
func acceptLangs(header string) []string {
	type entry struct {
		tag string
		q   float64
	}
	var entries []entry
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		tag, q := part, 1.0
		if i := strings.IndexByte(part, ';'); i >= 0 {
			tag = strings.TrimSpace(part[:i])
			if v := strings.TrimSpace(part[i+1:]); strings.HasPrefix(v, "q=") {
				if f, err := strconv.ParseFloat(v[2:], 64); err == nil {
					q = f
				}
			}
		}
		if tag == "*" || tag == "" {
			continue
		}
		entries = append(entries, entry{tag, q})
	}
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].q > entries[j-1].q; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
	tags := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		tag := lang(e.tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	return tags
}

// readVariant 读 html/<base>.<lang>.html，回退链：请求语言 → 打包语言（en 优先）→ 任意变体。
func readVariant(base string, langs []string) (string, bool) {
	seen := map[string]bool{}
	for _, l := range langs {
		l = lang(l)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		if raw, err := dir.ReadFile("html/" + base + "." + l + ".html"); err == nil {
			return string(raw), true
		}
	}
	langs = append(append([]string{}, langs...), Langs...)
	for _, l := range langs {
		l = lang(l)
		if !seen[l] {
			seen[l] = true
			if raw, err := dir.ReadFile("html/" + base + "." + l + ".html"); err == nil {
				return string(raw), true
			}
		}
	}
	matches, _ := fs.Glob(dir, "html/"+base+".*.html")
	for _, m := range matches {
		if raw, err := dir.ReadFile(m); err == nil {
			return string(raw), true
		}
	}
	return "", false
}

// Serve 处理 GET /api/providers/{kind}/guide。
// 语言：?lang=（前端转发 UI 语言）→ Accept-Language → 默认 en。
// 文件里的 {{BASE}} 用 ?base= 替换，回调地址跟当前部署一致（桌面 loopback vs 公网）。
func Serve(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kind := strings.TrimSpace(r.PathValue("kind"))
		file, ok, err := st.GuideFile(kind)
		if err != nil {
			http.Error(w, "guide lookup failed", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "no guide for "+kind, http.StatusNotFound)
			return
		}
		var prefs []string
		if q := strings.TrimSpace(r.URL.Query().Get("lang")); q != "" {
			prefs = append(prefs, lang(q))
		} else if al := r.Header.Get("Accept-Language"); al != "" {
			prefs = acceptLangs(al)
		}
		prefs = append(prefs, Langs...)
		raw, ok := readVariant(file, prefs)
		if !ok {
			http.Error(w, "guide file missing", http.StatusInternalServerError)
			return
		}
		// base 是外部可控查询参数，嵌进 HTML 正文前必须转义（M2 反射型 XSS），
		// 并去掉控制字符（换行/零宽字符可干扰页面结构）
		base := sanitizeBase(r.URL.Query().Get("base"))
		body := strings.ReplaceAll(raw, "{{BASE}}", base)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(body))
	}
}

// sanitizeBase 清洗 ?base= 回显值：去首尾空白与尾部斜杠、剔除控制字符、HTML 转义。
func sanitizeBase(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	return html.EscapeString(b.String())
}
