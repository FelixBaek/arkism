package memory

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestInspectArtifactText(t *testing.T) {
	for _, tc := range []struct {
		name, mime, text string
		want             error
		personal         bool
	}{
		{"plain", "text/plain", "한글 original\n\tcode\r\n", nil, false},
		{"json", "application/json", `{"items":[{"count":1e400},true,null],"label":"normal"}`, nil, false},
		{"scalar", "application/json", `"ordinary text"`, nil, false},
		{"empty containers", "application/json", `{"a":{},"b":[],"c":false}`, nil, false},
		{"pii", "text/plain", "contact@example.test", nil, true},
		{"escaped pii", "application/json", `{"contact":"person\u0040example.test"}`, nil, true},
		{"empty", "text/plain", "", ErrInvalid, false},
		{"utf8", "text/plain", "bad\xff", ErrInvalid, false},
		{"nul", "text/plain", "a\x00b", ErrInvalid, false},
		{"del", "text/plain", "a\x7fb", ErrInvalid, false},
		{"escaped nul", "application/json", `{"message":"a\u0000b"}`, ErrInvalid, false},
		{"escaped control key", "application/json", `{"pass\u0000word":"synthetic"}`, ErrInvalid, false},
		{"binary MIME", "image/png", "ordinary", ErrInvalid, false},
		{"pdf MIME", "application/pdf", "%PDF-1.7", ErrInvalid, false},
		{"disguised pdf", "text/plain", "%PDF-1.7\n", ErrInvalid, false},
		{"disguised html", "text/plain", "<!DOCTYPE html><html></html>", ErrInvalid, false},
		{"markup text", "text/plain", `<a href="https://example.test">link</a>`, ErrInvalid, false},
		{"markup comment", "text/plain", "<!-- TODO -->", ErrInvalid, false},
		{"xml text", "text/plain", `<?xml version="1.0"?>`, ErrInvalid, false},
		{"signature text", "text/plain", "GIF89a is a file format name", ErrInvalid, false},
		{"later signature", "text/plain", strings.Repeat("A", 600) + "GIF89a", nil, false},
		{"json BOM", "application/json", "\ufeff{}", ErrInvalid, false},
		{"plain BOM", "text/plain", "\ufeff{}", ErrInvalid, false},
		{"audio MIME", "audio/wav", "RIFF", ErrInvalid, false},
		{"mime parameters", "text/plain; charset=utf-8", "text", ErrInvalid, false},
		{"bad json", "application/json", `{"a":`, ErrInvalid, false},
		{"trailing json", "application/json", `{} {}`, ErrInvalid, false},
		{"plain secret", "text/plain", "password=synthetic", ErrSecretDetected, false},
		{"escaped key", "application/json", `{"pass\u0077ord":"synthetic"}`, ErrSecretDetected, false},
		{"escaped value", "application/json", `{"message":"\u0073k-abcdefghijklmnop"}`, ErrSecretDetected, false},
		{"duplicate key", "application/json", `{"message":"\u0073k-abcdefghijklmnop","message":"safe"}`, ErrSecretDetected, false},
		{"plain json", "text/plain", `{"pass\u0077ord":"synthetic"}`, ErrSecretDetected, false},
		{"array secret", "application/json", `["safe",{"a":["pass\u0077ord=synthetic"]}]`, ErrSecretDetected, false},
		{"numeric password", "application/json", `{"pass\u0077ord":12345}`, ErrSecretDetected, false},
		{"container password", "application/json", `{"pass\u0077ord":{"value":"synthetic"}}`, ErrSecretDetected, false},
		{"empty password", "application/json", `{"password":{}}`, ErrSecretDetected, false},
		{"empty token", "application/json", `{"token":[]}`, ErrSecretDetected, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := []byte(tc.text)
			before := bytes.Clone(content)
			report, err := inspectArtifactText(content, tc.mime)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if !bytes.Equal(before, content) {
				t.Fatal("inspection changed original bytes")
			}
			if err != nil {
				if report != (Inspection{}) {
					t.Fatal("rejected input returned inspection result")
				}
			} else if report.RulesVersion != inspectionVersion || report.Redacted || report.PersonalData != tc.personal {
				t.Fatalf("unexpected report: %+v", report)
			}
		})
	}
}
