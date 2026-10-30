package dynamicsv2

import (
	"encoding/json"
	"unicode/utf8"
)

// ValidJSONUnicode checks the existing byte bound, JSON syntax and lossless
// Unicode boundary without selecting a record schema. Syntax validation must
// precede the shared escape checker, which assumes well-formed JSON strings.
func ValidJSONUnicode(data []byte) bool {
	return len(data) <= maxBytes && utf8.Valid(data) && json.Valid(data) && validEscapes(data)
}
