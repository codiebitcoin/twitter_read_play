package book

import (
	"strings"
	"unicode"
)

// splitSentences 按句末标点把一段切成 ≤limit 字符的若干块。
func splitSentences(p string, limit int) []string {
	var out []string
	var sb strings.Builder
	for _, r := range p {
		sb.WriteRune(r)
		if strings.ContainsRune("。！？.!?；;", r) && len([]rune(sb.String())) >= limit/2 {
			// 切完以空格起步会让后续片段多出前导空格
			out = append(out, strings.TrimSpace(sb.String()))
			sb.Reset()
		}
	}
	if s := strings.TrimSpace(sb.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// SplitSentences 把一段正文切成「朗读小段」：默认按句末标点切，英文/中文混排都适用。
// 句子连起来不能太长（一口气读不完），也不宜太碎（TTS 每段都有起播开销），
// 所以把相邻短句合并到 ~maxLen 字符后再切。
func SplitSentences(p string, maxLen int) []string {
	if maxLen <= 0 {
		maxLen = 240
	}
	runes := []rune(strings.TrimSpace(p))
	if len(runes) == 0 {
		return nil
	}
	// 句末标点后切（保留标点在句尾；右引号/右括号归入前一句）
	var sent []string
	var cur []rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		cur = append(cur, r)
		if !strings.ContainsRune("。！？!?；;", r) {
			continue
		}
		// 英文 "." 后面不是空格/结尾的不当句末（小数点、缩写）
		if r == '.' {
			if i+1 < len(runes) && !unicode.IsSpace(runes[i+1]) {
				continue
			}
		}
		for i+1 < len(runes) && strings.ContainsRune("”’\"')）】」』", runes[i+1]) {
			i++
			cur = append(cur, runes[i])
		}
		sent = append(sent, string(cur))
		cur = nil
	}
	if rest := strings.TrimSpace(string(cur)); rest != "" {
		sent = append(sent, rest)
	}
	// 合并短句
	var out []string
	var sb strings.Builder
	size := 0
	flush := func() {
		if s := strings.TrimSpace(sb.String()); s != "" {
			out = append(out, s)
		}
		sb.Reset()
		size = 0
	}
	for _, s := range sent {
		n := len([]rune(s))
		if n > maxLen {
			// 单句超长：按空格/逗号硬切
			flush()
			out = append(out, hardWrap(s, maxLen)...)
			continue
		}
		if size+n > maxLen && size > 0 {
			flush()
		}
		if sb.Len() > 0 {
			sb.WriteString(" ")
		}
		sb.WriteString(s)
		size += n
	}
	flush()
	return out
}

// hardWrap 超长单句按空格（无空格按逗号）切成 ≤maxLen 的块。
func hardWrap(s string, maxLen int) []string {
	runes := []rune(s)
	var out []string
	for len(runes) > maxLen {
		cut := maxLen
		for i := maxLen; i > maxLen/2; i-- {
			if unicode.IsSpace(runes[i]) || strings.ContainsRune("，,、：:", runes[i]) {
				cut = i + 1
				break
			}
		}
		out = append(out, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	if s := strings.TrimSpace(string(runes)); s != "" {
		out = append(out, s)
	}
	return out
}
