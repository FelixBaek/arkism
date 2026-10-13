package capture

// <private>…</private> marks words the user keeps to themselves: they are never captured, queued,
// searched with or sent to a model. Each span becomes privateMark; an unclosed tag hides everything
// after it in that message. clean() runs this first, so every reader of typed text (prompt hooks,
// transcripts, chat logs, reflect) gets it.

import (
	"bytes"
	"os"
	"regexp"
	"strings"
)

const privateMark = "[비공개]"

var (
	privateSpanRE = regexp.MustCompile(`(?is)<private(?:\s[^>]*)?>.*?</private\s*>`)
	privateOpenRE = regexp.MustCompile(`(?is)<private(?:\s[^>]*)?>.*`)
)

// redactPrivate replaces each <private> span (any letter case) with privateMark.
func redactPrivate(text string) string {
	if !strings.Contains(strings.ToLower(text), "<private") {
		return text
	}
	text = privateSpanRE.ReplaceAllLiteralString(text, privateMark)
	return privateOpenRE.ReplaceAllLiteralString(text, privateMark)
}

// onlyPrivate reports a message with nothing left but private marks: nothing to remember.
func onlyPrivate(text string) bool {
	return strings.Contains(text, privateMark) && strings.TrimSpace(strings.ReplaceAll(text, privateMark, "")) == ""
}

// privateMarked reports whether a transcript file has a <private> mark anywhere. Such a session is
// not archived: the archive keeps transcripts byte for byte (offsets and hashes), so the marked
// text cannot be cut out of it. Its memories are still captured, from redacted text.
func privateMarked(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	const tag = "<private"
	buf := make([]byte, 1<<20)
	carry := []byte{}
	for {
		n, err := f.Read(buf)
		chunk := append(carry, bytes.ToLower(buf[:n])...)
		if bytes.Contains(chunk, []byte(tag)) {
			return true
		}
		if len(chunk) >= len(tag) {
			carry = append([]byte{}, chunk[len(chunk)-len(tag)+1:]...)
		}
		if err != nil {
			return false
		}
	}
}
