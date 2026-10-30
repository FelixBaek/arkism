package receiptchain

import "unicode/utf8"

func validJSONUnicode(raw []byte) bool {
	if !utf8.Valid(raw) {
		return false
	}
	quoted := false
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			quoted = !quoted
			continue
		}
		if !quoted || raw[i] != '\\' {
			continue
		}
		end, ok := unicodeEscapeEnd(raw, i)
		if !ok {
			return false
		}
		i = end
	}
	return true
}
func unicodeHex(raw []byte, start int) (uint16, bool) {
	if start+4 > len(raw) {
		return 0, false
	}
	var out uint16
	for _, c := range raw[start : start+4] {
		out *= 16
		switch {
		case c >= '0' && c <= '9':
			out += uint16(c - '0')
		case c >= 'a' && c <= 'f':
			out += uint16(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			out += uint16(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return out, true
}

func unicodeEscapeEnd(raw []byte, i int) (int, bool) {
	if i+1 >= len(raw) {
		return 0, false
	}
	if raw[i+1] != 'u' {
		return i + 1, true
	}
	value, ok := unicodeHex(raw, i+2)
	if !ok || value >= 0xdc00 && value <= 0xdfff {
		return 0, false
	}
	if value >= 0xd800 && value <= 0xdbff {
		if i+12 > len(raw) || raw[i+6] != '\\' || raw[i+7] != 'u' {
			return 0, false
		}
		low, ok := unicodeHex(raw, i+8)
		if !ok || low < 0xdc00 || low > 0xdfff {
			return 0, false
		}
		return i + 11, true
	}
	return i + 5, true
}
