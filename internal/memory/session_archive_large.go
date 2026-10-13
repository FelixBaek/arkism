package memory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"regexp"
	"regexp/syntax"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type archiveLargeRecord struct {
	source     *archiveJSONSource
	reader     *archiveSQLiteReader
	start, end int64
	rawBytes   int
	sha        string
	next       ArchiveMessageRef
	scanned    int
}
type archiveCountedReader struct{ record *archiveLargeRecord }

func (r archiveCountedReader) ReadAt(b []byte, at int64) (int, error) {
	n, err := r.record.reader.ReadAt(b, at)
	r.record.scanned += n
	return n, err
}
func (s *Service) archiveLargeRecord(ctx context.Context, h Record, ref ArchiveMessageRef) (*archiveLargeRecord, error) {
	budget := math.MaxInt
	original, err := newArchiveSQLiteReader(ctx, s, h, &budget)
	if err != nil {
		return nil, err
	}
	if ref.Chunk < 0 || ref.Chunk >= len(original.ends) {
		return nil, ErrInvalid
	}
	start := int64(ref.Offset)
	if ref.Chunk > 0 {
		start += original.ends[ref.Chunk-1]
	}
	if start > original.ends[ref.Chunk] || start >= original.ends[len(original.ends)-1] {
		return nil, ErrInvalid
	}
	rec := &archiveLargeRecord{reader: original, start: start}
	input := bufio.NewReaderSize(io.NewSectionReader(archiveCountedReader{rec}, start, original.ends[len(original.ends)-1]-start), 32<<10)
	digest := sha256.New()
	length := 0
	hasLF := false
	for {
		part, readErr := input.ReadSlice('\n')
		length += len(part)
		if len(part) > 0 && part[len(part)-1] == '\n' {
			hasLF = true
			_, _ = digest.Write(part[:len(part)-1])
			break
		}
		_, _ = digest.Write(part)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil && !errors.Is(readErr, bufio.ErrBufferFull) {
			return nil, readErr
		}
	}
	rec.rawBytes = length
	rec.end = start + int64(length)
	rec.sha = hex.EncodeToString(digest.Sum(nil))
	bodyEnd := rec.end
	if hasLF {
		bodyEnd--
	}
	rec.source = &archiveJSONSource{open: func(from, to int64) (io.Reader, error) {
		if to < 0 {
			to = bodyEnd - start
		}
		if from < 0 || to < from || to > bodyEnd-start {
			return nil, ErrInvalid
		}
		return io.NewSectionReader(archiveCountedReader{rec}, start+from, to-from), nil
	}}
	next := ref
	next.Chunk = sort.Search(len(original.ends), func(i int) bool { return original.ends[i] > rec.end })
	next.Offset = 0
	if next.Chunk < len(original.ends) {
		base := int64(0)
		if next.Chunk > 0 {
			base = original.ends[next.Chunk-1]
		}
		next.Offset = int(rec.end - base)
	}
	rec.next = next
	return rec, nil
}

var archiveStreamSecrets = func() *regexp.Regexp {
	parts := make([]string, len(secretRules))
	for i, r := range secretRules {
		parts[i] = "(?:" + r.String() + ")"
	}
	return regexp.MustCompile(strings.Join(parts, "|"))
}()

// Extract only mandatory leading literals. An unfamiliar expression falls
// back to the full matcher; this filter must never decide that a candidate is safe.
func archiveSecretPrefix(expr *syntax.Regexp) *syntax.Regexp {
	switch expr.Op {
	case syntax.OpLiteral:
		return expr
	case syntax.OpCapture:
		return archiveSecretPrefix(expr.Sub[0])
	case syntax.OpConcat:
		for _, sub := range expr.Sub {
			switch sub.Op {
			case syntax.OpEmptyMatch, syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText, syntax.OpWordBoundary, syntax.OpNoWordBoundary:
				continue
			}
			return archiveSecretPrefix(sub)
		}
	case syntax.OpAlternate:
		out := &syntax.Regexp{Op: syntax.OpAlternate}
		for _, sub := range expr.Sub {
			out.Sub = append(out.Sub, archiveSecretPrefix(sub))
		}
		return out
	case syntax.OpPlus:
		return archiveSecretPrefix(expr.Sub[0])
	case syntax.OpRepeat:
		if expr.Min > 0 {
			return archiveSecretPrefix(expr.Sub[0])
		}
	}
	return &syntax.Regexp{Op: syntax.OpEmptyMatch}
}

type archiveSecretStart struct {
	text   string
	folded bool
}
type archiveSecretStartFilter []archiveSecretStart

func archiveFold(s string) string {
	return strings.Map(func(r rune) rune {
		if r < utf8.RuneSelf {
			if r >= 'A' && r <= 'Z' {
				return r + ('a' - 'A')
			}
			return r
		}
		best := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < best {
				best = next
			}
		}
		if best >= 'A' && best <= 'Z' {
			best += 'a' - 'A'
		}
		return best
	}, s)
}
func (f archiveSecretStartFilter) Match(b []byte) bool { return f.MatchString(string(b)) }
func (f archiveSecretStartFilter) MatchString(s string) bool {
	folded := ""
	ready := false
	for _, prefix := range f {
		value := s
		if prefix.folded {
			if !ready {
				folded = archiveFold(s)
				ready = true
			}
			value = folded
		}
		if strings.Contains(value, prefix.text) {
			return true
		}
	}
	return false
}

var archiveSecretStarts, archiveSecretOverlap = func() (archiveSecretStartFilter, int) {
	var prefixes archiveSecretStartFilter
	width := 0
	var add func(*syntax.Regexp)
	add = func(e *syntax.Regexp) {
		switch e.Op {
		case syntax.OpAlternate:
			for _, sub := range e.Sub {
				add(sub)
			}
		case syntax.OpLiteral:
			prefix := archiveSecretStart{text: string(e.Rune), folded: e.Flags&syntax.FoldCase != 0}
			if prefix.folded {
				prefix.text = archiveFold(prefix.text)
			}
			prefixes = append(prefixes, prefix)
			width = max(width, len(e.Rune)*utf8.UTFMax)
		default:
			prefixes = append(prefixes, archiveSecretStart{}) // Always run full rules.
		}
	}
	for _, rule := range secretRules {
		parsed, err := syntax.Parse(rule.String(), syntax.Perl)
		if err != nil {
			panic(err)
		}
		add(archiveSecretPrefix(parsed))
	}
	return prefixes, width
}()

// Observe blocks before the regexp reader consumes runes. Query matching and
// window copying run once per block, not once for every byte of a huge message.
type archiveTextObserver struct {
	source                 io.Reader
	input                  *bufio.Reader
	query, tail, output    []byte
	total, start, limit    int
	found, invalid, direct bool
	failure                error
	prefixTail             []byte
	candidate              bool
	candidateOffset        int
}

func newArchiveTextObserver(r io.Reader, query string, offset, limit int, direct bool) *archiveTextObserver {
	o := &archiveTextObserver{source: r, query: []byte(query), start: offset, limit: limit, direct: direct, output: make([]byte, 0, limit)}
	o.input = bufio.NewReaderSize(o, 32<<10)
	if !direct && query == "" {
		o.found = true
		o.start = 0
	}
	return o
}
func (o *archiveTextObserver) ReadRune() (rune, int, error) { return o.input.ReadRune() }
func (o *archiveTextObserver) Read(dst []byte) (int, error) {
	n, err := o.source.Read(dst)
	if err != nil && !errors.Is(err, io.EOF) {
		o.failure = err
	}
	data := dst[:n]
	if !o.candidate {
		edge := append(append([]byte(nil), o.prefixTail...), data...)
		o.candidate = archiveSecretStarts.Match(edge)
		if o.candidate {
			o.candidateOffset = max(0, o.total-len(o.prefixTail)-utf8.UTFMax)
		}
		o.prefixTail = append(o.prefixTail[:0], edge[max(0, len(edge)-archiveSecretOverlap):]...)
	}
	base := o.total
	if !o.found && o.direct && o.start >= base && o.start < base+n {
		o.found = true
		o.invalid = !utf8.RuneStart(data[o.start-base])
	}
	if !o.found && !o.direct {
		// Include enough of the old block to recognize a match crossing the edge.
		edge := append(append([]byte(nil), o.tail...), data[:min(len(data), len(o.query))]...)
		match := bytes.Index(edge, o.query)
		if match >= 0 {
			match = base - len(o.tail) + match
		} else if at := bytes.Index(data, o.query); at >= 0 {
			match = base + at
		}
		if match >= 0 {
			o.found = true
			o.start = max(0, match-256)
			for o.start > 0 {
				var c byte
				if o.start < base {
					c = o.tail[o.start-(base-len(o.tail))]
				} else {
					c = data[o.start-base]
				}
				if utf8.RuneStart(c) {
					break
				}
				o.start--
			}
		}
	}
	if o.found && len(o.output) < o.limit {
		if len(o.output) == 0 && o.start < base {
			prior := o.tail[o.start-(base-len(o.tail)):]
			o.output = append(o.output, prior[:min(len(prior), o.limit)]...)
		}
		at := max(0, o.start-base)
		if at < len(data) {
			o.output = append(o.output, data[at:at+min(len(data)-at, o.limit-len(o.output))]...)
		}
	}
	o.total += n
	if !o.found {
		width := 256 + len(o.query) + 4
		if len(data) >= width {
			o.tail = append(o.tail[:0], data[len(data)-width:]...)
		} else {
			o.tail = append(o.tail, data...)
			if len(o.tail) > width {
				o.tail = append(o.tail[:0], o.tail[len(o.tail)-width:]...)
			}
		}
	}
	return n, err
}

func (s *Service) archiveLargeMessage(ctx context.Context, h Record, ref ArchiveMessageRef, query string, offset int, direct bool) (ArchiveMessageView, *archiveLargeRecord, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rec, err := s.archiveLargeRecord(ctx, h, ref)
	if err != nil {
		return ArchiveMessageView{}, nil, "", err
	}
	var role, key string
	var open func() (io.ReadCloser, error)
	if h.SessionArchive.Format == "reconstructed_zcode" {
		role, key, open, err = s.archiveLargeZcodeText(ctx, h, rec)
	} else {
		role, open, err = archiveJSONSafeText(archiveJSONSpan{rec.source, 0, -1, '{'})
	}
	if err != nil {
		if rec.reader.failure != nil {
			err = rec.reader.failure
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return ArchiveMessageView{}, rec, "", err
	}
	if open == nil {
		return ArchiveMessageView{}, rec, "ignored", nil
	}
	observe, state, err := archiveInspectText(ctx, cancel, open, query, offset, direct)
	if err != nil || state != "found" {
		return ArchiveMessageView{}, rec, state, err
	}
	digest := ContentHash("arkism-message-v1\n" + h.SessionArchive.Format + "\n" + role + "\n" + rec.sha)
	if ref.MessageSHA256 != "" && ref.MessageSHA256 != digest {
		return ArchiveMessageView{}, rec, "", ErrConflict
	}
	if ref.RecordKey != "" && ref.RecordKey != key {
		return ArchiveMessageView{}, rec, "", ErrConflict
	}
	ref.RecordKey = key
	ref.SHA256 = h.SessionArchive.SHA256
	ref.MessageSHA256 = digest
	view := ArchiveMessageView{Ref: ref, Role: role, Text: string(observe.output), TextOffset: observe.start, RawBytes: rec.rawBytes, Authority: AuthorityDataOnly}
	end := observe.start + len(observe.output)
	if end < observe.total {
		view.NextOffset = &end
	}
	return view, rec, "found", nil
}

// MatchReader consumes the entire stream when there is no secret. Keep parity
// tests against containsSecret: both use the same rule expressions.

// Takes ownership of the supplied child-context cancellation. Cancelling before
// closing joins JSON array producers even when a secret ends the scan early.
func archiveInspectText(ctx context.Context, cancel context.CancelFunc, open func() (io.ReadCloser, error), query string, offset int, direct bool) (*archiveTextObserver, string, error) {
	defer cancel()
	text, err := open()
	if err != nil {
		return nil, "", err
	}
	defer func() {
		cancel()
		if text != nil {
			_ = text.Close()
		}
	}()
	limit := 4096
	if direct {
		limit = archiveMessagePage
	}
	observe := newArchiveTextObserver(text, query, offset, limit, direct)
	// Exhaust the body once even if no secret prefix exists, keeping the
	// query window and validating decoding errors. No plaintext is retained.
	_, err = io.Copy(io.Discard, observe)
	if err != nil {
		return nil, "", err
	}
	if err = ctx.Err(); err != nil {
		return nil, "", err
	}
	secret := false
	if observe.candidate {
		if err = text.Close(); err != nil {
			return nil, "", err
		}
		text, err = open()
		if err != nil {
			return nil, "", err
		}
		// The first pass proved no rule can start before this position.
		// Preserve preceding bytes for word boundaries, and keep its query window.
		if _, err = io.CopyN(io.Discard, text, int64(observe.candidateOffset)); err != nil {
			return nil, "", err
		}
		check := newArchiveTextObserver(text, "", 0, 0, true)
		secret = archiveStreamSecrets.MatchReader(check)
		if check.failure != nil {
			return nil, "", check.failure
		}
	}
	if observe.failure != nil {
		return nil, "", observe.failure
	}
	if err = ctx.Err(); err != nil {
		return nil, "", err
	}
	if secret {
		return nil, "secret", nil
	}
	if observe.invalid {
		return nil, "", ErrInvalid
	}
	if observe.total == 0 {
		return nil, "empty", nil
	}
	if !observe.found {
		return nil, "unmatched", nil
	}
	// A bounded byte window may end inside one UTF-8 rune; trim only that suffix.
	for len(observe.output) > 0 && !utf8.Valid(observe.output) {
		observe.output = observe.output[:len(observe.output)-1]
	}

	return observe, "found", nil
}
