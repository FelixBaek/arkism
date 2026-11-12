package capture

// <private>…</private> marks words the user keeps to themselves: they are never captured, queued,
// searched with or sent to a model. Each span becomes privateMark; an unclosed tag hides everything
// after it in that message. clean() runs this first, so every reader of typed text (prompt hooks,
// transcripts, chat logs, reflect) gets it.

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
)

const privateMark = "[비공개]"
const privateTagPrefix = "<private"

var privateTagRE = regexp.MustCompile(`(?is)<private(?:\s[^>]*)?>|</private\s*>`)

// redactPrivate tracks nesting: an inner closing tag cannot expose the outer span.
// An unfinished outer span hides the rest of the message.
func redactPrivate(text string) string {
	if !strings.Contains(strings.ToLower(text), "<private") {
		return text
	}
	var out strings.Builder
	cursor, scan, depth := 0, 0, 0
	for scan < len(text) {
		match := privateTagRE.FindStringIndex(text[scan:])
		if match == nil {
			break
		}
		start, end := scan+match[0], scan+match[1]
		if text[start+1] == '/' {
			if depth > 0 {
				depth--
				if depth == 0 {
					out.WriteString(privateMark)
					cursor = end
				}
			}
		} else {
			if depth == 0 {
				out.WriteString(text[cursor:start])
			}
			depth++
		}
		scan = end
	}
	if depth > 0 {
		out.WriteString(privateMark)
	} else {
		out.WriteString(text[cursor:])
	}
	return out.String()
}

// onlyPrivate reports a message with nothing left but private marks: nothing to remember.
func onlyPrivate(text string) bool {
	return strings.Contains(text, privateMark) && strings.TrimSpace(strings.ReplaceAll(text, privateMark, "")) == ""
}

// privateMarked reports whether a person typed a <private> mark in a transcript. Such a session is
// not archived: the archive keeps transcripts byte for byte (offsets and hashes), so the marked
// text cannot be cut out of it. Its memories are still captured, from redacted text.
// Inspection failures remain errors so callers can retry without declaring an opt-out.
func privateMarked(path string) (bool, error) {
	return privateMarkedLimit(path, archiveMaxBytes)
}

func privateMarkedLimit(path string, limit int64) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, valueErr("archive_source_not_regular")
	}
	if info.Size() > limit {
		return false, valueErr("too_large")
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return false, valueErr("archive_source_changed")
	}
	if opened.Size() > limit {
		return false, valueErr("too_large")
	}
	return scanPrivateLimit(f, limit)
}

// privateLineCap bounds one transcript line kept for parsing; a longer marked line counts as typed.
const privateLineCap = 64 << 20

// scanPrivateTyped reads a JSONL transcript and reports a <private> mark only where a person typed
// it. Agent replies and tool output that merely quote the tag (code, docs, logs) must not drop the
// whole session; a line of unknown shape still counts (fail closed).
func scanPrivateTyped(r io.Reader) (bool, error) {
	var marker privateMarker
	var line []byte
	over := false
	typedEnd := func() bool { return marker.marked() && (over || privateTypedLine(line)) }
	buf := make([]byte, 64<<10)
	for {
		n, err := r.Read(buf)
		if err != nil && err != io.EOF {
			return false, err
		}
		for data := buf[:n]; len(data) > 0; {
			part := data
			nl := bytes.IndexByte(data, '\n')
			if nl >= 0 {
				part = data[:nl+1]
			}
			data = data[len(part):]
			marker.write(part)
			if !over && len(line)+len(part) > privateLineCap {
				over, line = true, nil
			} else if !over {
				line = append(line, part...)
			}
			if marker.marked() && !over && !bytes.HasPrefix(bytes.TrimLeft(line, " \t\r"), []byte("{")) {
				return true, nil // not a JSON record: no need to read the rest of the line
			}
			if nl >= 0 {
				if typedEnd() {
					return true, nil
				}
				marker, line, over = privateMarker{}, line[:0], false
			}
		}
		if err == io.EOF {
			return typedEnd(), nil
		}
	}
}

// privateTypedLine reports a record that carries typed words: a human user turn outside tool
// results (Claude Code) or a user message (Codex). Unknown or unparsable records count.
func privateTypedLine(line []byte) bool {
	var d map[string]any
	if json.Unmarshal(line, &d) != nil {
		return true
	}
	payload, _ := d["payload"].(map[string]any)
	switch d["type"] {
	case "assistant", "api-request-blob", "queue-operation":
		// Request blobs and queued prompts repeat records judged on their own lines.
		return false
	case "user":
		if !humanTyped(d) {
			return false
		}
		msg, _ := d["message"].(map[string]any)
		content, isList := msg["content"].([]any)
		if !isList {
			return true
		}
		typed := []any{}
		for _, b := range content {
			if m, ok := b.(map[string]any); !ok || m["type"] != "tool_result" {
				typed = append(typed, b)
			}
		}
		return privatePayload(typed)
	case "response_item":
		return payload["type"] == "message" && payload["role"] == "user"
	case "event_msg":
		return payload["type"] == "user_message"
	}
	return true
}

// Record only bytes and errors actually observed before the marker's early exit.
// A growing source can supply at most one byte beyond the inspection budget.
type privateInspectionReader struct {
	io.LimitedReader
	readErr error
}

func (r *privateInspectionReader) Read(p []byte) (int, error) {
	n, err := r.LimitedReader.Read(p)
	if err != nil && err != io.EOF {
		r.readErr = err
	}
	return n, err
}

func scanPrivateLimit(source io.Reader, limit int64) (bool, error) {
	if limit < 0 || limit > archiveMaxBytes {
		return false, valueErr("too_large")
	}
	r := privateInspectionReader{LimitedReader: io.LimitedReader{R: source, N: limit + 1}}
	marked, err := scanPrivateTyped(&r)
	if r.N == 0 {
		return false, valueErr("too_large")
	}
	if r.readErr != nil {
		return false, r.readErr
	}
	return marked, err
}

// privateMarker recognizes ASCII tags and JSON Unicode-escaped tags, including
// escaped backslashes and UTF-16 byte storage. Its state is bounded across reads;
// recursive escaping never needs repeated decoding or an unbounded carry buffer.
// Archive inspection is conservative: literal examples of a tag also opt out.
type privateMarker struct {
	matched int
	escape  int
	digits  int
	value   uint16
	hex     [4]byte
	found   bool
}

func (m *privateMarker) emit(b byte) {
	if b >= 'A' && b <= 'Z' {
		b += 'a' - 'A'
	}
	const tag = privateTagPrefix
	if m.matched == len(tag) {
		if b == '>' || b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v' {
			m.found = true
			m.matched = 0
			return
		}
		m.matched = 0
	}
	if b == tag[m.matched] {
		m.matched++
	} else if b == '<' {
		m.matched = 1
	} else {
		m.matched = 0
	}
}

func privateHex(b byte) (uint16, bool) {
	switch {
	case b >= '0' && b <= '9':
		return uint16(b - '0'), true
	case b >= 'a' && b <= 'f':
		return uint16(b - 'a' + 10), true
	case b >= 'A' && b <= 'F':
		return uint16(b - 'A' + 10), true
	}
	return 0, false
}

func (m *privateMarker) feed(b byte) {
	if m.found || b == 0 {
		return
	}
	switch m.escape {
	case 0:
		if b == '\\' {
			m.escape = 1
		} else {
			m.emit(b)
		}
	case 1:
		switch b {
		case '\\': // An encoded backslash can begin another layer of JSON escaping.
		case 't', 'n', 'r', 'f':
			m.escape = 0
			m.emit(' ') // Each JSON whitespace escape delimits the tag name.
		case 'u', 'U':
			m.escape = 2
			m.digits = 0
			m.value = 0
		default:
			m.escape = 0
			m.emit('\\')
			m.emit(b)
		}
	case 2:
		digit, ok := privateHex(b)
		if !ok {
			m.emit('\\')
			m.emit('u')
			for _, x := range m.hex[:m.digits] {
				m.emit(x)
			}
			m.escape = 0
			m.feed(b)
			return
		}
		m.hex[m.digits] = b
		m.digits++
		m.value = m.value*16 + digit
		if m.digits == 4 {
			m.escape = 0
			if m.value == '\\' {
				m.escape = 1
			} else if m.value < 128 {
				m.emit(byte(m.value))
			} else {
				m.emit(0xff)
			}
		}
	}
}

// At the end of a value, an unfinished opening prefix is conservative opt-out.
func (m *privateMarker) marked() bool { return m.found || m.matched == len(privateTagPrefix) }

func (m *privateMarker) write(data []byte) {
	for _, b := range data {
		m.feed(b)
		if m.found {
			return
		}
	}
}

func scanPrivate(r io.Reader) (bool, error) {
	var marker privateMarker
	buf := make([]byte, 64<<10)
	for {
		n, err := r.Read(buf)
		marker.write(buf[:n])
		if marker.found {
			return true, nil
		}
		if err == io.EOF {
			return marker.marked(), nil
		}
		if err != nil {
			return false, err
		}
	}
}
