package book

import (
	"bytes"
	"strings"
	"unicode"
	"unicode/utf8"
)

// 文本解码：上传的 txt 编码五花八门（UTF-8 BOM / GBK / GB18030 / UTF-16 / Big5 …），
// 直接按 UTF-8 读中文书会整本乱码。这里不引第三方编码库：
// BOM → 合法 UTF-8 → GB18030（内置双字节码表 + 四字节规则）→ Latin-1 兜底，绝不丢字节。

// DecodeText 把原始字节解成 UTF-8 文本，返回源编码名（写进 Book.TXTEncoding 便于排查）。
func DecodeText(b []byte) (string, string) {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return normalizeNewlines(string(b[3:])), "utf-8-bom"
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return normalizeNewlines(decodeUTF16(b[2:], false)), "utf-16le"
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return normalizeNewlines(decodeUTF16(b[2:], true)), "utf-16be"
	}
	// 无 BOM 的 UTF-16 先判：纯 ASCII 的 UTF-16 是合法 UTF-8（夹着 NUL），
	// 不该被当成 UTF-8 读出一串 "H\x00e\x00…"
	if be, ok := looksUTF16(b); ok {
		return normalizeNewlines(decodeUTF16(b, be)), "utf-16(no-bom)"
	}
	if utf8.Valid(b) {
		return normalizeNewlines(string(b)), "utf-8"
	}
	if s, gbOK := decodeGB18030(b); gbOK {
		return normalizeNewlines(s), "gb18030"
	}
	// Big5 常见于繁体电子书：用同样的双字节区间表，只换映射表（见 big5_data.go）
	if s, ok := decodeBig5(b); ok {
		return normalizeNewlines(s), "big5"
	}
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return normalizeNewlines(sb.String()), "latin-1"
}

// normalizeNewlines 统一换行、去掉零宽字符（部分电子书导出会带 U+FEFF）。
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.ReplaceAll(s, "\uFEFF", "")
}

// looksUTF16 无 BOM UTF-16 判定：前 4KB 里超过四分之一是 0 字节，且大多集中在奇数位（LE）
// 或偶数位（BE）。返回 (bigEndian, true)。
func looksUTF16(b []byte) (bool, bool) {
	n := len(b)
	if n < 4 {
		return false, false
	}
	if n > 4096 {
		n = 4096
	}
	zerosOdd, zerosEven := 0, 0
	for i := 0; i < n; i++ {
		if b[i] != 0 {
			continue
		}
		if i%2 == 1 {
			zerosOdd++
		} else {
			zerosEven++
		}
	}
	q := n / 4
	if zerosOdd > q && zerosOdd > zerosEven {
		return false, true
	}
	if zerosEven > q {
		return true, true
	}
	return false, false
}

// decodeUTF16 解 UTF-16；代理对合成一个 rune，坏字节替换为 U+FFFD。
func decodeUTF16(b []byte, bigEndian bool) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			units = append(units, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			units = append(units, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	var sb strings.Builder
	for i := 0; i < len(units); i++ {
		u := units[i]
		switch {
		case u >= 0xD800 && u <= 0xDBFF && i+1 < len(units) && units[i+1] >= 0xDC00 && units[i+1] <= 0xDFFF:
			sb.WriteRune(0x10000 + (rune(u-0xD800) << 10) + rune(units[i+1]-0xDC00))
			i++
		case u >= 0xD800 && u <= 0xDFFF:
			sb.WriteRune(unicode.ReplacementChar)
		default:
			sb.WriteRune(rune(u))
		}
	}
	return sb.String()
}

// decodeGB18030 解 GB18030（含 GBK/GB2312 全部字符）。返回 ok=false 表示字节流不像 GB 系编码。
func decodeGB18030(b []byte) (string, bool) {
	var sb strings.Builder
	sb.Grow(len(b))
	bad := 0
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c < 0x80:
			sb.WriteByte(c)
			i++
		case c == 0x80: // 单字节 €
			sb.WriteRune('€')
			i++
		case c == 0xFF || i+1 >= len(b):
			bad++
			sb.WriteRune(unicode.ReplacementChar)
			i++
		default:
			d := b[i+1]
			if d >= 0x30 && d <= 0x39 && i+3 < len(b) && b[i+2] >= 0x81 && b[i+2] <= 0xFE && b[i+3] >= 0x30 && b[i+3] <= 0x39 {
				code := (int(c)-0x81)*12600 + (int(d)-0x30)*1260 + (int(b[i+2])-0x81)*10 + int(b[i+3]) - 0x30
				sb.WriteRune(gb18030FourByte(code))
				i += 4
				continue
			}
			if d == 0x7F || d == 0xFF {
				bad++
				sb.WriteRune(unicode.ReplacementChar)
				i += 2
				continue
			}
			b2 := int(d)
			if b2 > 0x7F {
				b2-- // 0x7F 一列不在 GBK 码表里，后续列整体左移一格
			}
			idx := (int(c)-0x81)*190 + b2 - 0x40
			if idx >= len(gbkPoints) || gbkPoints[idx] == 0 {
				bad++
				sb.WriteRune(unicode.ReplacementChar)
			} else {
				sb.WriteRune(rune(gbkPoints[idx]))
			}
			i += 2
		}
	}
	// 大量非法字节：这多半不是 GB 系编码（比如 Shift_JIS/Big5 的旧文件），交给调用方兜底
	return sb.String(), bad*20 < len(b)
}

// gb18030FourByte 四字节码位 → Unicode：
// 0..39419 与 BMP 码位一致；189000 以后是 CJK 扩展区；中间保留段交回调用方置 U+FFFD。
func gb18030FourByte(code int) rune {
	switch {
	case code >= 0 && code <= 39419:
		return rune(code)
	case code >= 189000 && code <= 1237575:
		return rune(code - 189000 + 0x10000)
	default:
		return unicode.ReplacementChar
	}
}

// decodeBig5 解 Big5（含 HKSCS 扩展）。规则：首字节 0x81–0xFE、次字节 0x40–0x7E / 0xA1–0xFE。
// 非法字节过多返回 ok=false（调用方再做 Latin-1 兜底）。
func decodeBig5(b []byte) (string, bool) {
	var sb strings.Builder
	sb.Grow(len(b))
	bad := 0
	for i := 0; i < len(b); {
		c := b[i]
		if c < 0x80 {
			sb.WriteByte(c)
			i++
			continue
		}
		if c < 0x81 || i+1 >= len(b) {
			bad++
			sb.WriteRune(unicode.ReplacementChar)
			i++
			continue
		}
		d := b[i+1]
		if d < 0x40 || d == 0x7F || d > 0xFE {
			bad++
			sb.WriteRune(unicode.ReplacementChar)
			i++
			continue
		}
		idx := (int(c)-0xA1)*191 + (int(d) - 0x40)
		if idx < 0 || idx >= len(big5Points) || big5Points[idx] == 0 {
			bad++
			sb.WriteRune(unicode.ReplacementChar)
		} else {
			sb.WriteRune(rune(big5Points[idx]))
		}
		i += 2
	}
	return sb.String(), bad*5 < len(b)
}
