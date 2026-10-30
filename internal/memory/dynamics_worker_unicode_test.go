package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The pinned runtime config must not be accepted after a lossy Unicode decode (SYS-05 independent review).
func TestDynamicsWorkerConfigUnicode(t *testing.T) {
	raw, _ := json.Marshal(dynamicsFixtureConfig())
	old := `"profile":"isolated-fixture-v1"`
	for _, tc := range []struct {
		name, value, want string
	}{
		{"raw_invalid_utf8", "\"a\xff\"", ""},
		{"raw_utf8_surrogate", "\"a\xed\xa0\x80\"", ""},
		{"escaped_lone_high", `"a\ud800"`, ""},
		{"escaped_lone_low", `"a\udc00"`, ""},
		{"literal_replacement_character", "\"a�\"", "a�"},
		{"escaped_replacement_character", `"a\ufffd"`, "a�"},
		{"escaped_surrogate_pair", `"a\ud83d\ude00"`, "a😀"},
		{"literal_emoji", "\"a😀\"", "a😀"},
		{"literal_backslash_text", `"a\\ud800"`, `a\ud800`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := strings.Replace(string(raw), old, `"profile":`+tc.value, 1)
			r, err := NewDynamicsLoadRuntime([]byte(value))
			if tc.want == "" {
				if !errors.Is(err, ErrInvalid) || r != nil {
					t.Fatalf("lossy profile accepted: %v", err)
				}
				return
			}
			if err != nil || r.config.Profile != tc.want {
				t.Fatalf("lossless profile rejected or changed: %v", err)
			}
		})
	}
}
