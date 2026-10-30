package dynamicsv2

import "testing"

// Same cases as the independent SYS-05 daemon encoding review: lossy inputs fail, lossless ones pass.
func TestValidJSONUnicode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value []byte
		valid bool
	}{
		{"raw_invalid_utf8", []byte{'"', 0xff, '"'}, false},
		{"raw_utf8_surrogate", []byte{'"', 0xed, 0xa0, 0x80, '"'}, false},
		{"escaped_lone_high", []byte(`"\ud800"`), false},
		{"escaped_lone_low", []byte(`"\udc00"`), false},
		{"escaped_high_then_text", []byte(`"\ud800x"`), false},
		{"escaped_high_then_high", []byte(`"\ud800\ud800"`), false},
		{"truncated_escape", []byte(`"\u12"`), false},
		{"not_json", []byte(`{"a":`), false},
		{"literal_replacement_character", []byte("\"�\""), true},
		{"escaped_replacement_character", []byte(`"\ufffd"`), true},
		{"escaped_surrogate_pair", []byte(`"\ud83d\ude00"`), true},
		{"literal_emoji", []byte("\"😀\""), true},
		{"literal_backslash_text", []byte(`"\\ud800"`), true},
		{"nested_object_key", []byte(`{"\ud83d\ude00":["\\u", 1]}`), true},
		{"nested_lone_key", []byte(`{"\udc00":1}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidJSONUnicode(tc.value); got != tc.valid {
				t.Fatalf("ValidJSONUnicode(%q) = %v, want %v", tc.value, got, tc.valid)
			}
		})
	}
	if ValidJSONUnicode(make([]byte, maxBytes+1)) {
		t.Fatal("over the byte bound")
	}
}
