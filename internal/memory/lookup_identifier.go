package memory

import (
	"strings"
	"unicode"
)

// Restrict literal identifier lookups to a single ASCII alphanumeric token.
// Ordinary questions and explicit semantic/graph searches retain their lanes.
func lookupIdentifier(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return ""
	}
	for _, word := range words[1:] {
		switch strings.ToLower(word) {
		case "표식", "라벨", "로드맵", "label", "marker":
		default:
			return ""
		}
	}
	id := words[0]
	if len(id) < 8 || !asciiIdentifierLetter(id[0]) {
		return ""
	}
	hasDigit := false
	for i := 0; i < len(id); i++ {
		if id[i] >= '0' && id[i] <= '9' {
			hasDigit = true
			continue
		}
		if !asciiIdentifierLetter(id[i]) {
			return ""
		}
	}
	if !hasDigit {
		return ""
	}
	return id
}

func asciiIdentifierLetter(b byte) bool { return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' }

func containsLookupIdentifier(text, id string) bool {
	for _, word := range strings.FieldsFunc(text, func(r rune) bool { return r != '_' && !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if strings.EqualFold(word, id) {
			return true
		}
	}
	return false
}
