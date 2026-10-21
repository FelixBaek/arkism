package memory

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// A span refers to the original stream, including when that stream is a JSON
// string containing another JSON document. It never retains the whole value.
type archiveJSONSource struct {
	open func(int64, int64) (io.Reader, error)
}
type archiveJSONSpan struct {
	source     *archiveJSONSource
	start, end int64
	kind       byte
}
type archiveJSONParser struct {
	r      *bufio.Reader
	offset int64
}

func (v archiveJSONSpan) parser() (*archiveJSONParser, error) {
	r, err := v.source.open(v.start, v.end)
	if err != nil {
		return nil, err
	}
	return &archiveJSONParser{bufio.NewReaderSize(r, 32<<10), v.start}, nil
}
func (p *archiveJSONParser) peek() (byte, error) {
	b, err := p.r.Peek(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}
func (p *archiveJSONParser) byte() (byte, error) {
	b, err := p.r.ReadByte()
	if err == nil {
		p.offset++
	}
	return b, err
}
func (p *archiveJSONParser) expect(want byte) error {
	b, err := p.byte()
	if err != nil {
		return err
	}
	if b != want {
		return ErrInvalid
	}
	return nil
}
func (p *archiveJSONParser) space() error {
	for {
		b, err := p.peek()
		if err != nil {
			return err
		}
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			return nil
		}
		_, _ = p.byte()
	}
}
func hexRune(b []byte) (rune, bool) {
	var n rune
	for _, c := range b {
		n *= 16
		switch {
		case c >= '0' && c <= '9':
			n += rune(c - '0')
		case c >= 'a' && c <= 'f':
			n += rune(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			n += rune(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return n, true
}

// nextStringRune starts after the opening quote and consumes the closing quote.
// Like encoding/json, lone escaped surrogates decode to RuneError. Raw invalid
// UTF-8 is rejected, matching the archive's existing utf8.Valid check.
func (p *archiveJSONParser) nextStringRune() (rune, bool, error) {
	c, err := p.byte()
	if err != nil {
		return 0, false, err
	}
	if c == '"' {
		return 0, true, nil
	}
	if c < 0x20 {
		return 0, false, ErrInvalid
	}
	if c == '\\' {
		c, err = p.byte()
		if err != nil {
			return 0, false, err
		}
		switch c {
		case '"', '\\', '/':
			return rune(c), false, nil
		case 'b':
			return '\b', false, nil
		case 'f':
			return '\f', false, nil
		case 'n':
			return '\n', false, nil
		case 'r':
			return '\r', false, nil
		case 't':
			return '\t', false, nil
		case 'u':
			var raw [4]byte
			for i := range raw {
				raw[i], err = p.byte()
				if err != nil {
					return 0, false, err
				}
			}
			value, ok := hexRune(raw[:])
			if !ok {
				return 0, false, ErrInvalid
			}
			if value >= 0xd800 && value <= 0xdbff {
				next, _ := p.r.Peek(6)
				if len(next) == 6 && next[0] == '\\' && next[1] == 'u' {
					low, ok := hexRune(next[2:])
					if ok && low >= 0xdc00 && low <= 0xdfff {
						_, _ = p.r.Discard(6)
						p.offset += 6
						return utf16.DecodeRune(value, low), false, nil
					}
				}
				return utf8.RuneError, false, nil
			}
			if value >= 0xdc00 && value <= 0xdfff {
				return utf8.RuneError, false, nil
			}
			return value, false, nil
		default:
			return 0, false, ErrInvalid
		}
	}
	if c < utf8.RuneSelf {
		return rune(c), false, nil
	}
	if err = p.r.UnreadByte(); err != nil {
		return 0, false, err
	}
	p.offset--
	value, n, err := p.r.ReadRune()
	p.offset += int64(n)
	if err != nil {
		return 0, false, err
	}
	if value == utf8.RuneError && n == 1 {
		return 0, false, ErrInvalid
	}
	return value, false, nil
}
func (p *archiveJSONParser) string(limit int) (string, error) {
	if err := p.expect('"'); err != nil {
		return "", err
	}
	var out strings.Builder
	large := false
	for {
		if limit == 0 || large {
			if _, err := p.r.Peek(1); err != nil {
				return "", err
			}
			available, _ := p.r.Peek(p.r.Buffered())
			n := 0
			for n < len(available) && available[n] != '"' && available[n] != '\\' && available[n] >= 0x20 {
				n++
			}
			if n > 0 {
				valid := n
				for valid > 0 && !utf8.Valid(available[:valid]) && n-valid < 3 {
					valid--
				}
				if !utf8.Valid(available[:valid]) {
					return "", ErrInvalid
				}
				if valid > 0 {
					_, _ = p.r.Discard(valid)
					p.offset += int64(valid)
					large = true
					continue
				}
			}
		}
		r, done, err := p.nextStringRune()
		if err != nil {
			return "", err
		}
		if done {
			break
		}
		if out.Len()+utf8.RuneLen(r) > limit {
			large = true
		}
		if !large {
			out.WriteRune(r)
		}
	}
	if large {
		return "\x00oversized", nil
	}
	return out.String(), nil
}
func (p *archiveJSONParser) number() error {
	consumeDigits := func() int {
		n := 0
		for {
			c, e := p.peek()
			if e != nil || c < '0' || c > '9' {
				return n
			}
			_, _ = p.byte()
			n++
		}
	}
	c, err := p.peek()
	if err != nil {
		return err
	}
	if c == '-' {
		_, _ = p.byte()
		c, err = p.peek()
		if err != nil {
			return err
		}
	}
	if c == '0' {
		_, _ = p.byte()
	} else if c >= '1' && c <= '9' {
		consumeDigits()
	} else {
		return ErrInvalid
	}
	c, err = p.peek()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	if c == '.' {
		_, _ = p.byte()
		if consumeDigits() == 0 {
			return ErrInvalid
		}
		c, err = p.peek()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	if c == 'e' || c == 'E' {
		_, _ = p.byte()
		c, err = p.peek()
		if err != nil {
			return err
		}
		if c == '+' || c == '-' {
			_, _ = p.byte()
		}
		if consumeDigits() == 0 {
			return ErrInvalid
		}
	}
	return nil
}
func (p *archiveJSONParser) value(depth int) error {
	if depth > 10000 {
		return ErrInvalid
	}
	if err := p.space(); err != nil {
		return err
	}
	c, err := p.peek()
	if err != nil {
		return err
	}
	switch c {
	case '"':
		_, err = p.string(0)
		return err
	case '{', '[':
		_, _ = p.byte()
		end := byte(']')
		if c == '{' {
			end = '}'
		}
		if err = p.space(); err != nil {
			return err
		}
		next, _ := p.peek()
		if next == end {
			return p.expect(end)
		}
		for {
			if c == '{' {
				if _, err = p.string(0); err != nil {
					return err
				}
				if err = p.space(); err != nil {
					return err
				}
				if err = p.expect(':'); err != nil {
					return err
				}
			}
			if err = p.value(depth + 1); err != nil {
				return err
			}
			if err = p.space(); err != nil {
				return err
			}
			next, _ = p.peek()
			if next == end {
				return p.expect(end)
			}
			if err = p.expect(','); err != nil {
				return err
			}
			if err = p.space(); err != nil {
				return err
			}
		}
	case 't', 'f', 'n':
		word := map[byte]string{'t': "true", 'f': "false", 'n': "null"}[c]
		for i := range word {
			if err = p.expect(word[i]); err != nil {
				return err
			}
		}
		return nil
	default:
		return p.number()
	}
}

var archiveJSONKeys = map[string]bool{"role": true, "channel": true, "type": true, "table": true, "record": true, "data": true, "message": true, "payload": true, "content": true, "text": true, "id": true, "message_id": true, "synthetic": true}

func archiveJSONFields(v archiveJSONSpan) (map[string]archiveJSONSpan, error) {
	p, err := v.parser()
	if err != nil {
		return nil, err
	}
	if err = p.space(); err != nil {
		return nil, err
	}
	if err = p.expect('{'); err != nil {
		return nil, err
	}
	fields := map[string]archiveJSONSpan{}
	if err = p.space(); err != nil {
		return nil, err
	}
	c, _ := p.peek()
	if c != '}' {
		for {
			name, err := p.string(64)
			if err != nil {
				return nil, err
			}
			if err = p.space(); err != nil {
				return nil, err
			}
			if err = p.expect(':'); err != nil {
				return nil, err
			}
			if err = p.space(); err != nil {
				return nil, err
			}
			start := p.offset
			kind, _ := p.peek()
			if err = p.value(1); err != nil {
				return nil, err
			}
			if archiveJSONKeys[name] {
				fields[name] = archiveJSONSpan{v.source, start, p.offset, kind}
			}
			if err = p.space(); err != nil {
				return nil, err
			}
			c, _ = p.peek()
			if c == '}' {
				break
			}
			if err = p.expect(','); err != nil {
				return nil, err
			}
			if err = p.space(); err != nil {
				return nil, err
			}
		}
	}
	if err = p.expect('}'); err != nil {
		return nil, err
	}
	if err = p.space(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = ErrInvalid
		}
		return nil, err
	}
	return fields, nil
}
func archiveJSONSmall(v archiveJSONSpan) (string, error) {
	if v.kind != '"' {
		return "", nil
	}
	p, err := v.parser()
	if err != nil {
		return "", err
	}
	return p.string(256)
}

type archiveJSONStringReader struct {
	p       *archiveJSONParser
	pending []byte
	storage [utf8.UTFMax]byte
	done    bool
}

func archiveJSONString(v archiveJSONSpan) (io.Reader, error) {
	p, err := v.parser()
	if err != nil {
		return nil, err
	}
	if err = p.expect('"'); err != nil {
		return nil, err
	}
	return &archiveJSONStringReader{p: p}, nil
}
func (r *archiveJSONStringReader) Read(b []byte) (int, error) {
	n := 0
	for n < len(b) {
		if len(r.pending) > 0 {
			used := copy(b[n:], r.pending)
			r.pending = r.pending[used:]
			n += used
			continue
		}
		if r.done {
			if n > 0 {
				return n, nil
			}
			return 0, io.EOF
		}
		if _, err := r.p.r.Peek(1); err == nil {
			available, _ := r.p.r.Peek(r.p.r.Buffered())
			count := 0
			for count < len(available) && count < len(b)-n && available[count] >= 0x20 && available[count] < utf8.RuneSelf && available[count] != '"' && available[count] != '\\' {
				count++
			}
			if count > 0 {
				copy(b[n:], available[:count])
				_, _ = r.p.r.Discard(count)
				r.p.offset += int64(count)
				n += count
				continue
			}
		}
		value, done, err := r.p.nextStringRune()
		if err != nil {
			return n, err
		}
		if done {
			r.done = true
			continue
		}
		r.pending = utf8.AppendRune(r.storage[:0], value)
	}
	return n, nil
}
func archiveJSONEmbedded(v archiveJSONSpan) *archiveJSONSource {
	return &archiveJSONSource{open: func(start, end int64) (io.Reader, error) {
		r, err := archiveJSONString(v)
		if err != nil {
			return nil, err
		}
		if _, err = io.CopyN(io.Discard, r, start); err != nil {
			return nil, err
		}
		if end >= 0 {
			r = io.LimitReader(r, end-start)
		}
		return r, nil
	}}
}

func archiveJSONHidden(fields map[string]archiveJSONSpan) (bool, error) {
	channel, err := archiveJSONSmall(fields["channel"])
	if err != nil {
		return false, err
	}
	kind, err := archiveJSONSmall(fields["type"])
	if err != nil {
		return false, err
	}
	return hiddenArchiveEnvelope(channel, kind), nil
}
func archiveJSONSafeText(root archiveJSONSpan) (string, func() (io.ReadCloser, error), error) {
	row, err := archiveJSONFields(root)
	if err != nil {
		return "", nil, err
	}
	hidden, err := archiveJSONHidden(row)
	if err != nil || hidden {
		return "", nil, err
	}
	table, err := archiveJSONSmall(row["table"])
	if err != nil {
		return "", nil, err
	}
	if table != "" {
		if table != "messages" && table != "message" {
			return "", nil, nil
		}
		if row["record"].kind != '{' {
			return "", nil, ErrInvalid
		}
		row, err = archiveJSONFields(row["record"])
		if err != nil {
			return "", nil, err
		}
		hidden, err = archiveJSONHidden(row)
		if err != nil || hidden {
			return "", nil, err
		}
		if data, exists := row["data"]; exists {
			if data.kind != '"' {
				return "", nil, ErrInvalid
			}
			src := archiveJSONEmbedded(data)
			row, err = archiveJSONFields(archiveJSONSpan{src, 0, -1, '{'})
			if err != nil {
				return "", nil, err
			}
		}
	}
	hidden, err = archiveJSONHidden(row)
	if err != nil || hidden {
		return "", nil, err
	}
	for _, key := range []string{"message", "payload"} {
		if row[key].kind == '{' {
			row, err = archiveJSONFields(row[key])
			if err != nil {
				return "", nil, err
			}
			break
		}
	}
	role, err := archiveJSONSmall(row["role"])
	if err != nil {
		return "", nil, err
	}
	if role != "user" && role != "assistant" {
		return "", nil, nil
	}
	hidden, err = archiveJSONHidden(row)
	if err != nil || hidden {
		return "", nil, err
	}
	content, exists := row["content"]
	if !exists || content.kind == 'n' {
		return "", nil, nil
	}
	if content.kind == '"' {
		return role, func() (io.ReadCloser, error) {
			r, e := archiveJSONString(content)
			if e != nil {
				return nil, e
			}
			return io.NopCloser(r), nil
		}, nil
	}
	if content.kind != '[' {
		return "", nil, ErrInvalid
	}
	return role, func() (io.ReadCloser, error) {
		p, e := content.parser()
		if e != nil {
			return nil, e
		}
		if e = p.expect('['); e != nil {
			return nil, e
		}
		reader, writer := io.Pipe()
		done := make(chan struct{})
		go func() {
			defer close(done)
			err := archiveJSONArrayText(p, content.source, writer)
			_ = writer.CloseWithError(err)
		}()
		return &archiveJSONTextPipe{reader, done}, nil
	}, nil
}
func archiveJSONArrayText(p *archiveJSONParser, src *archiveJSONSource, w io.Writer) error {
	if err := p.space(); err != nil {
		return err
	}
	kind, _ := p.peek()
	if kind == ']' {
		return p.expect(']')
	}
	prev := ""
	for {
		start := p.offset
		kind, _ := p.peek()
		if err := p.value(1); err != nil {
			return err
		}
		if kind != '{' && kind != 'n' {
			return ErrInvalid
		}
		if kind == '{' {
			block, err := archiveJSONFields(archiveJSONSpan{src, start, p.offset, kind})
			if err != nil {
				return err
			}
			typ, err := archiveJSONSmall(block["type"])
			if err != nil {
				return err
			}
			if typ == "text" || typ == "input_text" || typ == "output_text" {
				if sep := archiveBlockSeparator(prev, typ); sep != "" {
					if _, err = io.WriteString(w, sep); err != nil {
						return err
					}
				}
				prev = typ
				if block["text"].kind == '"' {
					text, err := archiveJSONString(block["text"])
					if err != nil {
						return err
					}
					if _, err = io.Copy(w, text); err != nil {
						return err
					}
				}
			}
		}
		if err := p.space(); err != nil {
			return err
		}
		kind, _ = p.peek()
		if kind == ']' {
			return p.expect(']')
		}
		if err := p.expect(','); err != nil {
			return err
		}
		if err := p.space(); err != nil {
			return err
		}
	}
}

// Closing an early-rejected secret stream also joins its producer, so no request
// outlives its source reader or races the read-cost accounting.
type archiveJSONTextPipe struct {
	*io.PipeReader
	done <-chan struct{}
}

func (r *archiveJSONTextPipe) Close() error { err := r.PipeReader.Close(); <-r.done; return err }
