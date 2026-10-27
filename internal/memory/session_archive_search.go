package memory

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

const archiveSearchBudget = 16 << 20
const archiveRecordLimit = 8 << 20
const archiveMessagePage = 64 << 10

type ArchiveMessageRef struct {
	SQLiteStep    string `json:"sqlite_step,omitempty" bson:"sqlite_step,omitempty"`
	RecordKey     string `json:"record_key,omitempty" bson:"record_key,omitempty"`
	MessageSHA256 string `json:"message_sha256,omitempty" bson:"message_sha256,omitempty"`

	ArchiveID string `json:"archive_id" bson:"archive_id"`
	SHA256    string `json:"sha256" bson:"sha256"`
	Chunk     int    `json:"chunk_index" bson:"chunk_index"`
	Offset    int    `json:"chunk_offset" bson:"chunk_offset"`
}
type ArchiveMessageView struct {
	Ref        ArchiveMessageRef `json:"ref"`
	Role       string            `json:"role"`
	Text       string            `json:"text"`
	TextOffset int               `json:"text_offset"`
	NextOffset *int              `json:"next_text_offset,omitempty"`
	At         *time.Time        `json:"at,omitempty"` // the record's own timestamp, when the log line carries one
	RawBytes   int               `json:"raw_bytes"`
	Authority  string            `json:"authority"`
}
type ArchiveSearchInput struct {
	ArchiveID string `json:"archive_id,omitempty"`
	Scope     Scope  `json:"scope"`
	Query     string `json:"q"`
	After     string `json:"after"`
	Limit     int    `json:"limit"`
}
type ArchiveSearchOutput struct {
	Items              []ArchiveMessageView `json:"items"`
	Next               string               `json:"next_cursor,omitempty"`
	ScannedBytes       int                  `json:"scanned_bytes"`
	Oversized          int                  `json:"oversized_records"`
	Invalid            int                  `json:"invalid_records"`
	Secrets            int                  `json:"secret_records"`
	SecretRefs         []ArchiveMessageRef  `json:"secret_refs,omitempty"` // where the withheld JSONL records sit (no text), so a window reader can skip only its own
	UnsupportedRecords int                  `json:"unsupported_records"`
	Unsupported        int                  `json:"unsupported_archives"`
	Authority          string               `json:"authority"`
}
type ArchiveMessageInput struct {
	Scope      Scope             `json:"scope"`
	Ref        ArchiveMessageRef `json:"ref"`
	TextOffset int               `json:"text_offset"`
}
type archiveSearchCursor struct {
	ArchiveID string            `json:"archive_id,omitempty"`
	Ref       ArchiveMessageRef `json:"ref"`
	After     string            `json:"after"`
	Skip      bool              `json:"skip"`
	QueryHash string            `json:"q_hash"`
}

func encodeArchiveCursor(c archiveSearchCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeArchiveCursor(in ArchiveSearchInput) (archiveSearchCursor, error) {
	c := archiveSearchCursor{ArchiveID: in.ArchiveID, QueryHash: ContentHash(in.Query)}
	if in.After == "" {
		return c, nil
	}
	if len(in.After) > 2048 {
		return c, ErrInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(in.After)
	if err != nil {
		return c, ErrInvalid
	}
	c = archiveSearchCursor{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil || !validArchiveCursor(c, in) {
		return c, ErrInvalid
	}
	return c, nil
}
func validArchiveCursor(c archiveSearchCursor, in ArchiveSearchInput) bool {
	if c.QueryHash != ContentHash(in.Query) || c.ArchiveID != in.ArchiveID || c.After != "" && !sourceHashPattern.MatchString(c.After) {
		return false
	}
	if c.Skip && c.Ref.Chunk == 0 && c.Ref.Offset == 0 {
		return false
	}
	if c.Ref.MessageSHA256 != "" || c.Ref.RecordKey != "" {
		return false
	}
	return validArchiveCursorRef(c, in.ArchiveID)
}
func validArchiveCursorRef(c archiveSearchCursor, archiveID string) bool {
	if c.Ref.ArchiveID != "" && !validArchiveMessageRef(c.Ref) {
		return false
	}
	if c.Ref.ArchiveID == "" && (c.Skip || c.Ref.Offset != 0 || c.Ref.Chunk != 0 || c.Ref.SHA256 != "" || c.Ref.SQLiteStep != "") {
		return false
	}
	if archiveID != "" && ((c.Ref.ArchiveID != "" && c.Ref.ArchiveID != archiveID) || (c.After != "" && c.After != archiveID)) {
		return false
	}
	return true
}
func validArchiveMessageRef(ref ArchiveMessageRef) bool {
	if !validSQLiteStep(ref.SQLiteStep) {
		return false
	}
	if ref.SQLiteStep != "" && (ref.Chunk != 0 || ref.Offset != 0 || ref.RecordKey != "") {
		return false
	}
	if ref.RecordKey != "" && (!sourceHashPattern.MatchString(ref.RecordKey) || ref.MessageSHA256 == "") {
		return false
	}
	if ref.MessageSHA256 != "" && !sourceHashPattern.MatchString(ref.MessageSHA256) {
		return false
	}
	if !sourceHashPattern.MatchString(ref.ArchiveID) || !sourceHashPattern.MatchString(ref.SHA256) {
		return false
	}
	return ref.Chunk >= 0 && ref.Chunk < sessionArchiveMaxChunks &&
		ref.Offset >= 0 && ref.Offset <= SessionArchiveChunkSize
}

// Only a current, authorized header is passed to this in-memory reader.
// Positions address the ordered manifest, so duplicate and short chunks work.
type archiveLineReader struct {
	s      *Service
	ctx    context.Context
	header Record
	pos    ArchiveMessageRef
	buffer []byte
	loaded int
}

func newArchiveLineReader(s *Service, ctx context.Context, h Record, pos ArchiveMessageRef) *archiveLineReader {
	return &archiveLineReader{s: s, ctx: ctx, header: h, pos: pos, loaded: -1}
}
func (r *archiveLineReader) chunk(index int) ([]byte, error) {
	if index < 0 || index >= len(r.header.SessionArchive.Chunks) {
		return nil, io.EOF
	}
	id := r.header.SessionArchive.Chunks[index]
	chunk, err := r.s.Repo.Get(r.ctx, colSessionArchiveChunks, r.header.Scope, id)
	if err != nil {
		return nil, err
	}
	if !isSessionArchiveChunkFor(chunk, r.header.ID) || sessionArchiveChunkID(r.header.Scope, r.header.ID, chunk.ContentHash) != id {
		return nil, ErrConflict
	}
	return r.s.openSessionArchiveChunk(r.header.Scope, r.header.ID, chunk.ContentHash, chunk.SessionArchive.Cipher)
}
func (r *archiveLineReader) boundary() error {
	if r.pos.Chunk >= len(r.header.SessionArchive.Chunks) {
		return ErrInvalid
	}
	data, err := r.chunk(r.pos.Chunk)
	if err != nil {
		return err
	}
	r.buffer = data
	r.loaded = r.pos.Chunk
	if r.pos.Offset > len(data) {
		return ErrInvalid
	}
	if r.pos.Offset > 0 {
		if data[r.pos.Offset-1] != '\n' {
			return ErrInvalid
		}
		return nil
	}
	if r.pos.Chunk == 0 {
		return nil
	}
	previous, err := r.chunk(r.pos.Chunk - 1)
	if err != nil {
		return err
	}
	if len(previous) == 0 || previous[len(previous)-1] != '\n' {
		return ErrInvalid
	}
	return nil
}

// A page can stop inside an oversized record; skip=true never decodes its tail.
// Incomplete normal records resume at their start to avoid losing a message.
func (r *archiveLineReader) line(budget *int, skip bool) (raw []byte, complete, oversized, eof bool, err error) {
	oversized = skip
	for *budget > 0 {
		if err := r.ctx.Err(); err != nil {
			return nil, false, false, false, err
		}
		if r.pos.Chunk >= len(r.header.SessionArchive.Chunks) {
			return raw, len(raw) > 0 || oversized, oversized, true, nil
		}
		if r.loaded != r.pos.Chunk {
			r.buffer, err = r.chunk(r.pos.Chunk)
			if err != nil {
				return nil, false, false, false, err
			}
			r.loaded = r.pos.Chunk
		}
		if r.pos.Offset > len(r.buffer) {
			return nil, false, false, false, ErrInvalid
		}
		if r.pos.Offset == len(r.buffer) {
			r.pos.Chunk++
			r.pos.Offset = 0
			continue
		}
		part := r.buffer[r.pos.Offset:]
		if len(part) > *budget {
			part = part[:*budget]
		}
		at := bytes.IndexByte(part, '\n')
		if at >= 0 {
			part = part[:at+1]
		}
		r.pos.Offset += len(part)
		*budget -= len(part)
		if !oversized {
			if len(raw)+len(part) > archiveRecordLimit {
				raw = nil
				oversized = true
			} else {
				raw = append(raw, part...)
			}
		}
		if at >= 0 {
			return raw, true, oversized, false, nil
		}
	}
	if r.loaded == len(r.header.SessionArchive.Chunks)-1 && r.pos.Offset == len(r.buffer) {
		return raw, len(raw) > 0 || oversized, oversized, true, nil
	}
	return raw, false, oversized, false, nil
}

// Deliberate allowlist: never recursively walk arbitrary tool/system objects.
func archiveSafeMessage(raw []byte) (text, role string, valid bool) {
	var row map[string]json.RawMessage
	if !utf8.Valid(raw) || json.Unmarshal(raw, &row) != nil {
		return "", "", false
	}
	str := archiveRawString
	ignored := func(m map[string]json.RawMessage) bool {
		return hiddenArchiveEnvelope(str(m, "channel"), str(m, "type"))
	}
	if ignored(row) {
		return "", "", true
	}
	if table := str(row, "table"); table != "" {
		if table != "messages" && table != "message" {
			return "", "", true
		}
		wrapped := row["record"]
		row = nil
		if json.Unmarshal(wrapped, &row) != nil {
			return "", "", false
		}
		if ignored(row) {
			return "", "", true
		}
		if data := row["data"]; len(data) > 0 {
			var encoded string
			row = nil
			if json.Unmarshal(data, &encoded) != nil || json.Unmarshal([]byte(encoded), &row) != nil {
				return "", "", false
			}
		}
	}
	if ignored(row) {
		return "", "", true
	}
	for _, key := range []string{"message", "payload"} {
		if value := row[key]; len(value) > 0 && value[0] == '{' {
			row = nil
			if json.Unmarshal(value, &row) != nil {
				return "", "", false
			}
			break
		}
	}
	role = str(row, "role")
	if role != "user" && role != "assistant" {
		return "", "", true
	}
	if ignored(row) {
		return "", "", true
	}
	return archiveSafeContent(row["content"], role)
}

// archiveMessageTime is the top-level "timestamp" agents write on each log line; nil when absent or unreadable.
func archiveMessageTime(raw []byte) *time.Time {
	var row struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(raw, &row) != nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, row.Timestamp)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}
func archiveRawString(m map[string]json.RawMessage, key string) string {
	var v string
	_ = json.Unmarshal(m[key], &v)
	return v
}
func archiveSafeContent(content json.RawMessage, role string) (string, string, bool) {
	if len(content) == 0 {
		return "", "", true
	}
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text, role, true
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(content, &blocks) != nil {
		return "", "", false
	}
	var b strings.Builder
	prev := ""
	for _, block := range blocks {
		kind := archiveRawString(block, "type")
		if host.ArchiveTextBlock(kind) {
			b.WriteString(host.ArchiveBlockSeparator(prev, kind))
			b.WriteString(archiveRawString(block, "text"))
			prev = kind
		}
	}
	return b.String(), role, true
}

func archiveSupported(format string) bool {
	return format == "sqlite_snapshot" || format == "jsonl" || format == "reconstructed_agentsview" || format == "reconstructed_zcode"
}
func archiveTextView(ref ArchiveMessageRef, role, text string, offset, limit, rawBytes int) ArchiveMessageView {
	end := min(len(text), offset+limit)
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	out := ArchiveMessageView{Ref: ref, Role: role, Text: text[offset:end], TextOffset: offset, RawBytes: rawBytes, Authority: AuthorityDataOnly}
	if end < len(text) {
		out.NextOffset = &end
	}
	return out
}
func (s *Service) archiveSearchHeader(ctx context.Context, token string, scope Scope, ref ArchiveMessageRef, actor Decision) (Record, error) {
	who, err := s.authorize(ctx, token, "read:"+colSessionArchives, scope, ref.ArchiveID, "")
	if err != nil {
		return Record{}, err
	}
	if who.PrincipalID != actor.PrincipalID || who.Role != actor.Role {
		return Record{}, ErrDenied
	}
	h, err := s.Repo.Get(ctx, colSessionArchives, scope, ref.ArchiveID)
	if err != nil {
		return Record{}, err
	}
	if !isSessionArchiveHeader(h) || sessionArchiveStateOf(h, s.sessionArchiveNow()) != sessionArchiveStateActive || ref.SHA256 != "" && h.SessionArchive.SHA256 != ref.SHA256 {
		return Record{}, ErrConflict
	}
	return h, nil
}
func (s *Service) fenceArchiveSearch(ctx context.Context, token string, headers []Record, actor Decision) error {
	// Recheck policy after bounded decryption, outside the database transaction.
	for _, h := range headers {
		who, err := s.authorize(ctx, token, "read:"+colSessionArchives, h.Scope, h.ID, "")
		if err != nil {
			return err
		}
		if who.PrincipalID != actor.PrincipalID || who.Role != actor.Role {
			return ErrDenied
		}
	}
	return s.sessionArchiveTransaction(ctx, func(ctx context.Context, tx Tx) error {
		for _, h := range headers {
			current, err := tx.Get(ctx, colSessionArchives, h.Scope, h.ID)
			if err != nil {
				return err
			}
			if !isSessionArchiveHeader(current) || current.Revision != h.Revision || current.SessionArchive.SHA256 != h.SessionArchive.SHA256 || sessionArchiveStateOf(current, s.sessionArchiveNow()) != sessionArchiveStateActive {
				return ErrConflict
			}
			if err = tx.Replace(ctx, colSessionArchives, current, current.Revision); err != nil {
				return err
			}
			if err = audit(ctx, tx, h.Scope, actor.PrincipalID, "read:session_archive_messages", h.ID); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *Service) SearchSessionArchiveMessages(ctx context.Context, token string, in ArchiveSearchInput) (ArchiveSearchOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	empty := ArchiveSearchOutput{}
	if s.Repo == nil {
		return empty, ErrUnavailable
	}
	if in.Limit == 0 {
		in.Limit = 20
	}
	if !validScope(in.Scope) || (in.Query == "" && in.ArchiveID == "") || (in.ArchiveID != "" && !sourceHashPattern.MatchString(in.ArchiveID)) || len(in.Query) > 256 || !utf8.ValidString(in.Query) || in.Limit < 1 || in.Limit > 50 {
		return empty, ErrInvalid
	}
	cursor, err := decodeArchiveCursor(in)
	if err != nil {
		return empty, err
	}
	actor, err := s.authorize(ctx, token, "read:"+colSessionArchives, in.Scope, "", "")
	if err != nil {
		return empty, err
	}
	store, ok := s.Repo.(SessionArchiveStore)
	if !ok {
		return empty, ErrUnavailable
	}
	ctx = bindRequestActor(ctx, actor)
	out := ArchiveSearchOutput{Items: []ArchiveMessageView{}, Authority: AuthorityDataOnly}
	budget := archiveSearchBudget
	metadataScanned := 0
	var headers []Record
	finished := false
	for len(headers) < 20 && budget > 0 && len(out.Items) < in.Limit {
		ref := cursor.Ref
		if ref.ArchiveID == "" {
			if in.ArchiveID != "" {
				if cursor.After == in.ArchiveID {
					finished = true
					break
				}
				ref = ArchiveMessageRef{ArchiveID: in.ArchiveID}
			} else {
				rows, err := store.SessionArchiveRows(ctx, in.Scope, cursor.After, 1, "")
				if err != nil {
					return empty, err
				}
				if len(rows) == 0 {
					finished = true
					break
				}
				if rows[0].Scope != in.Scope || !isSessionArchiveHeader(rows[0]) {
					return empty, ErrUnavailable
				}
				ref = ArchiveMessageRef{ArchiveID: rows[0].ID, SHA256: rows[0].SessionArchive.SHA256}
				cursor.Ref = ref
			}
		}
		h, err := s.archiveSearchHeader(ctx, token, in.Scope, ref, actor)
		if err != nil {
			return empty, err
		}
		if ref.SHA256 == "" {
			ref.SHA256 = h.SessionArchive.SHA256
		}
		cursor.Ref = ref
		headers = append(headers, h)
		if !archiveSupported(h.SessionArchive.Format) {
			out.Unsupported++
			cursor.After = h.ID
			cursor.Ref = ArchiveMessageRef{}
			cursor.Skip = false
			continue
		}
		if h.SessionArchive.Format == "sqlite_snapshot" {
			err = s.searchArchiveSQLite(ctx, h, in, &cursor, &out, &budget)
			if errors.Is(err, errArchiveSQLiteUnsupported) {
				out.Unsupported++
				cursor.After, cursor.Ref, cursor.Skip = h.ID, ArchiveMessageRef{}, false
			} else if err != nil {
				return empty, sqliteArchiveSearchError(err)
			}
			if cursor.Ref.ArchiveID != "" {
				break
			}
			continue
		}
		if ref.SQLiteStep != "" {
			return empty, ErrInvalid
		}
		roles := map[string]string{}
		reader := newArchiveLineReader(s, ctx, h, ref)
		if !cursor.Skip {
			if err = reader.boundary(); err != nil {
				return empty, err
			}
		}
		for budget > 0 && len(out.Items) < in.Limit {
			start := reader.pos
			raw, complete, oversized, eof, err := reader.line(&budget, cursor.Skip)
			if err != nil {
				return empty, err
			}
			if oversized && !cursor.Skip {
				view, record, state, largeErr := s.archiveLargeMessage(ctx, h, start, in.Query, 0, false)
				if record == nil {
					return empty, largeErr
				}
				budget -= record.scanned
				reader.pos = record.next
				cursor.Ref, cursor.Skip = record.next, false
				if errors.Is(largeErr, errArchiveZcodeMetadata) {
					out.Unsupported++
					cursor.After, cursor.Ref, cursor.Skip = h.ID, ArchiveMessageRef{}, false
					break
				}
				if largeErr != nil {
					if errors.Is(largeErr, ErrInvalid) || errors.Is(largeErr, io.EOF) || errors.Is(largeErr, io.ErrUnexpectedEOF) {
						out.Invalid++
					} else {
						return empty, largeErr
					}
				} else if state == "secret" {
					out.Secrets++
					out.SecretRefs = append(out.SecretRefs, start)
				} else if state == "found" {
					out.Items = append(out.Items, view)
				}
				if record.next.Chunk >= len(h.SessionArchive.Chunks) {
					cursor.After, cursor.Ref = h.ID, ArchiveMessageRef{}
					break
				}
				continue
			}
			if !complete && !eof {
				if oversized {
					cursor.Ref = reader.pos
					cursor.Skip = true
				} else {
					cursor.Ref = start
					cursor.Skip = false
				}
				break
			}
			cursor.Skip = false
			cursor.Ref = reader.pos
			if oversized {
				out.Oversized++
			} else if len(raw) > 0 {
				text, role, valid, used, messageErr := s.archiveSafeMessageForHeader(ctx, h, raw, roles)
				metadataScanned += used
				if errors.Is(messageErr, errArchiveZcodeMetadata) {
					out.Unsupported++
					cursor.After, cursor.Ref, cursor.Skip = h.ID, ArchiveMessageRef{}, false
					break
				}
				if messageErr != nil {
					return empty, messageErr
				}
				if !valid {
					out.Invalid++
				} else if text != "" {
					if containsSecret(text) {
						out.Secrets++
						out.SecretRefs = append(out.SecretRefs, start)
					} else if at := strings.Index(text, in.Query); at >= 0 {
						begin := max(0, at-256)
						for begin > 0 && !utf8.RuneStart(text[begin]) {
							begin--
						}
						start.MessageSHA256 = archiveMessageHash(h.SessionArchive.Format, role, raw)
						start.RecordKey = archiveRecordKey(h.SessionArchive.Format, raw)
						view := archiveTextView(start, role, text, begin, 4096, len(raw))
						view.At = archiveMessageTime(raw)
						out.Items = append(out.Items, view)
					}
				}
			}
			if eof {
				cursor.After = h.ID
				cursor.Ref = ArchiveMessageRef{}
				break
			}
		}
		if cursor.Ref.ArchiveID != "" {
			break
		}
	}
	out.ScannedBytes = archiveSearchBudget - budget + metadataScanned
	if !finished {
		out.Next = encodeArchiveCursor(cursor)
	}
	if err := s.fenceArchiveSearch(ctx, token, headers, actor); err != nil {
		return empty, err
	}
	return out, nil
}
func (s *Service) ReadSessionArchiveMessage(ctx context.Context, token string, in ArchiveMessageInput) (ArchiveMessageView, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	empty := ArchiveMessageView{}
	if s.Repo == nil {
		return empty, ErrUnavailable
	}
	if !validScope(in.Scope) || !validArchiveMessageRef(in.Ref) || in.TextOffset < 0 {
		return empty, ErrInvalid
	}
	actor, err := s.authorize(ctx, token, "read:"+colSessionArchives, in.Scope, in.Ref.ArchiveID, "")
	if err != nil {
		return empty, err
	}
	ctx = bindRequestActor(ctx, actor)
	headerRef := in.Ref
	if in.Ref.MessageSHA256 != "" {
		headerRef.SHA256 = ""
	}
	h, err := s.archiveSearchHeader(ctx, token, in.Scope, headerRef, actor)
	if err != nil {
		return empty, err
	}
	if !archiveSupported(h.SessionArchive.Format) {
		return empty, ErrInvalid
	}
	if h.SessionArchive.Format == "sqlite_snapshot" {
		return s.readArchiveSQLite(ctx, token, h, in, actor)
	}
	if in.Ref.SQLiteStep != "" {
		return empty, ErrInvalid
	}
	return s.readArchiveLineMessage(ctx, token, h, in, actor)
}
func (s *Service) readArchiveLineMessage(ctx context.Context, token string, h Record, in ArchiveMessageInput, actor Decision) (ArchiveMessageView, error) {
	empty := ArchiveMessageView{}
	currentRef := in.Ref
	var raw []byte
	var err error
	if in.Ref.RecordKey != "" && h.SessionArchive.Format != "reconstructed_zcode" {
		return empty, ErrInvalid
	}
	if in.Ref.RecordKey != "" && in.Ref.SHA256 != h.SessionArchive.SHA256 {
		raw, currentRef, err = s.relocateZcodePart(ctx, h, in.Ref)
		if err != nil {
			return empty, err
		}
	}
	if raw == nil {
		reader := newArchiveLineReader(s, ctx, h, currentRef)
		if err = reader.boundary(); err != nil {
			return empty, err
		}
		budget := archiveSearchBudget
		var complete, oversized bool
		raw, complete, oversized, _, err = reader.line(&budget, false)
		if err != nil {
			return empty, err
		}
		if !complete || oversized {
			return s.readArchiveLargeMessage(ctx, token, h, currentRef, in.TextOffset, actor)
		}
	}
	return s.readArchiveRawMessage(ctx, token, h, in, currentRef, raw, actor)
}
func (s *Service) readArchiveLargeMessage(ctx context.Context, token string, h Record, ref ArchiveMessageRef, textOffset int, actor Decision) (ArchiveMessageView, error) {
	empty := ArchiveMessageView{}
	view, _, state, largeErr := s.archiveLargeMessage(ctx, h, ref, "", textOffset, true)
	if largeErr != nil {
		if errors.Is(largeErr, errArchiveZcodeMetadata) || errors.Is(largeErr, io.EOF) || errors.Is(largeErr, io.ErrUnexpectedEOF) {
			largeErr = ErrInvalid
		}
		return empty, largeErr
	}
	if state == "secret" {
		return empty, ErrDenied
	}
	if state == "unmatched" {
		return empty, ErrInvalid
	}
	if state != "found" {
		return empty, ErrNotFound
	}
	if err := s.fenceArchiveSearch(ctx, token, []Record{h}, actor); err != nil {
		return empty, err
	}
	return view, nil
}
func (s *Service) readArchiveRawMessage(ctx context.Context, token string, h Record, in ArchiveMessageInput, currentRef ArchiveMessageRef, raw []byte, actor Decision) (ArchiveMessageView, error) {
	empty := ArchiveMessageView{}
	key := archiveRecordKey(h.SessionArchive.Format, raw)
	if in.Ref.RecordKey != "" && in.Ref.RecordKey != key {
		return empty, ErrConflict
	}
	text, role, valid, _, messageErr := s.archiveSafeMessageForHeader(ctx, h, raw, map[string]string{})
	if errors.Is(messageErr, errArchiveZcodeMetadata) {
		return empty, ErrInvalid
	}
	if messageErr != nil {
		return empty, messageErr
	}
	if !valid || text == "" {
		return empty, ErrNotFound
	}
	if containsSecret(text) {
		return empty, ErrDenied
	}
	digest := archiveMessageHash(h.SessionArchive.Format, role, raw)
	if in.Ref.MessageSHA256 != "" && in.Ref.MessageSHA256 != digest {
		return empty, ErrConflict
	}
	if in.TextOffset >= len(text) || !utf8.RuneStart(text[in.TextOffset]) {
		return empty, ErrInvalid
	}
	if err := s.fenceArchiveSearch(ctx, token, []Record{h}, actor); err != nil {
		return empty, err
	}
	currentRef.RecordKey = key
	currentRef.SHA256 = h.SessionArchive.SHA256
	currentRef.MessageSHA256 = digest
	return archiveTextView(currentRef, role, text, in.TextOffset, archiveMessagePage, len(raw)), nil
}
func sessionArchiveSearchRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/session-archives/search", func(w http.ResponseWriter, r *http.Request) {
		var in ArchiveSearchInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.SearchSessionArchiveMessages(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/session-archives/message", func(w http.ResponseWriter, r *http.Request) {
		var in ArchiveMessageInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.ReadSessionArchiveMessage(r.Context(), bearer(r), in)
		respond(w, out, err)
	})
}

func archiveMessageHash(format, role string, raw []byte) string {
	return ContentHash("arkism-message-v1\n" + format + "\n" + role + "\n" + ContentHash(string(bytes.TrimSuffix(raw, []byte{'\n'}))))
}
