package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

var errArchiveZcodeMetadata = errors.New("unsupported Zcode metadata")
var errArchiveZcodeRoleMissing = errors.New("Zcode message role missing")

// Resolve only the requested message from the authenticated original. The scan
// retains one bounded record, not a map proportional to the whole session.
func (s *Service) archiveZcodeRole(ctx context.Context, h Record, messageID string) (string, int, error) {
	return s.archiveZcodeRoleHash(ctx, h, ContentHash(messageID))
}
func (s *Service) archiveZcodeRoleHash(ctx context.Context, h Record, messageHash string) (string, int, error) {
	r := newArchiveLineReader(s, ctx, h, ArchiveMessageRef{ArchiveID: h.ID, SHA256: h.SessionArchive.SHA256})
	scanned, matches := 0, 0
	role := ""
	for {
		if err := ctx.Err(); err != nil {
			return "", scanned, err
		}
		start := r.pos
		budget := archiveRecordLimit + 1
		raw, complete, oversize, eof, err := r.line(&budget, false)
		scanned += archiveRecordLimit + 1 - budget
		if err != nil {
			return "", scanned, err
		}
		var table, id, foundRole string
		if oversize || !complete && !eof {
			var rec *archiveLargeRecord
			rec, table, id, foundRole, err = s.archiveZcodeLargeRoleEntry(ctx, h, start)
			if rec != nil {
				scanned += rec.scanned
			}
			if err != nil {
				return "", scanned, err
			}
			r.pos = rec.next
			eof = rec.next.Chunk >= len(h.SessionArchive.Chunks)
		} else if table, id, foundRole, err = archiveZcodeRowRole(raw); err != nil {
			return "", scanned, err
		}
		if table != "" && table != "message" {
			break
		}
		if table == "message" && id == messageHash {
			matches++
			if matches > 1 {
				return "", scanned, errArchiveZcodeMetadata
			}
			role = foundRole
		}
		if eof {
			break
		}
	}
	if matches == 0 {
		return "", scanned, errArchiveZcodeRoleMissing
	}
	return role, scanned, nil
}
func (s *Service) archiveZcodeLargeRoleEntry(ctx context.Context, h Record, start ArchiveMessageRef) (*archiveLargeRecord, string, string, string, error) {
	rec, err := s.archiveLargeRecord(ctx, h, start)
	if err != nil {
		return nil, "", "", "", err
	}
	table, id, foundRole, err := archiveLargeZcodeRoleEntry(rec)
	if rec.reader.failure != nil {
		err = rec.reader.failure
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			err = errArchiveZcodeMetadata
		}
		return rec, "", "", "", err
	}
	return rec, table, id, foundRole, nil
}

// The id is returned hashed, as archiveLargeZcodeRoleEntry returns it.
func archiveZcodeRowRole(raw []byte) (string, string, string, error) {
	var row struct {
		Table   string `json:"table"`
		Channel string `json:"channel"`
		Type    string `json:"type"`
		Record  struct {
			Channel string `json:"channel"`
			Type    string `json:"type"`
			ID      string `json:"id"`
			Data    string `json:"data"`
		} `json:"record"`
	}
	if len(raw) > 0 && (!utf8.Valid(raw) || json.Unmarshal(raw, &row) != nil) {
		return "", "", "", errArchiveZcodeMetadata
	}
	if row.Table != "message" {
		return row.Table, "", "", nil
	}
	var data struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Channel string `json:"channel"`
	}
	if row.Record.ID == "" || json.Unmarshal([]byte(row.Record.Data), &data) != nil {
		return "", "", "", errArchiveZcodeMetadata
	}
	role := data.Role
	if hiddenArchiveEnvelope(data.Channel, data.Type) || hiddenArchiveEnvelope(row.Channel, row.Type) || hiddenArchiveEnvelope(row.Record.Channel, row.Record.Type) {
		role = "ignored"
	}
	return row.Table, ContentHash(row.Record.ID), role, nil
}

func (s *Service) archiveSafeMessageForHeader(ctx context.Context, h Record, raw []byte, cache map[string]string) (string, string, bool, int, error) {
	if h.SessionArchive.Format != "reconstructed_zcode" {
		text, role, valid := archiveSafeMessage(raw)
		return text, role, valid, 0, nil
	}
	var row struct {
		Table  string `json:"table"`
		Record struct {
			MessageID string `json:"message_id"`
		} `json:"record"`
	}
	if !utf8.Valid(raw) || json.Unmarshal(raw, &row) != nil {
		return "", "", false, 0, nil
	}
	if row.Table != "part" {
		return "", "", true, 0, nil
	}
	if row.Record.MessageID == "" {
		return "", "", false, 0, nil
	}
	role, found := cache[row.Record.MessageID]
	scanned := 0
	if !found {
		var err error
		role, scanned, err = s.archiveZcodeRole(ctx, h, row.Record.MessageID)
		if errors.Is(err, errArchiveZcodeRoleMissing) {
			return "", "", false, scanned, nil
		}
		if err != nil {
			return "", "", false, scanned, err
		}
		if len(cache) < 128 {
			cache[row.Record.MessageID] = role
		}
	}
	text, role, valid := archiveSafeMessageWithRoles(raw, map[string]string{row.Record.MessageID: role})
	return text, role, valid, scanned, nil
}

func archiveSafeMessageWithRoles(raw []byte, roles map[string]string) (string, string, bool) {
	if roles == nil {
		return archiveSafeMessage(raw)
	}
	var row struct {
		Table   string `json:"table"`
		Channel string `json:"channel"`
		Type    string `json:"type"`
		Record  struct {
			Channel   string `json:"channel"`
			Type      string `json:"type"`
			MessageID string `json:"message_id"`
			Data      string `json:"data"`
		} `json:"record"`
	}
	if !utf8.Valid(raw) || json.Unmarshal(raw, &row) != nil {
		return "", "", false
	}
	if row.Table != "part" || hiddenArchiveEnvelope(row.Channel, row.Type) || hiddenArchiveEnvelope(row.Record.Channel, row.Record.Type) {
		return "", "", true
	}
	var part struct {
		Channel   string `json:"channel"`
		Type      string `json:"type"`
		Text      string `json:"text"`
		Synthetic bool   `json:"synthetic"`
	}
	if json.Unmarshal([]byte(row.Record.Data), &part) != nil {
		return "", "", false
	}
	role, exists := roles[row.Record.MessageID]
	if !exists {
		return "", "", false
	}
	if hiddenArchiveEnvelope(part.Channel, part.Type) || part.Type != "text" || part.Synthetic || role != "user" && role != "assistant" {
		return "", "", true
	}
	return part.Text, role, true
}

func hiddenArchiveEnvelope(channel, kind string) bool {
	return channel != "" && channel != "final" || kind == "session_meta"
}

// Only part IDs are relocation identities; text similarity is never consulted.
func archiveRecordKey(format string, raw []byte) string {
	if format != "reconstructed_zcode" {
		return ""
	}
	var row struct {
		Table  string `json:"table"`
		Record struct {
			ID string `json:"id"`
		} `json:"record"`
	}
	if json.Unmarshal(raw, &row) != nil || row.Table != "part" || row.Record.ID == "" {
		return ""
	}
	return ContentHash("arkism-zcode-part-v1\n" + row.Record.ID)
}

func (s *Service) relocateZcodePart(ctx context.Context, h Record, ref ArchiveMessageRef) ([]byte, ArchiveMessageRef, error) {
	reader := newArchiveLineReader(s, ctx, h, ArchiveMessageRef{ArchiveID: h.ID, SHA256: h.SessionArchive.SHA256})
	var found []byte
	matched := false
	var position ArchiveMessageRef
	for {
		if err := ctx.Err(); err != nil {
			return nil, ref, err
		}
		start := reader.pos
		budget := archiveRecordLimit + 1
		raw, complete, oversized, eof, err := reader.line(&budget, false)
		if err != nil {
			return nil, ref, err
		}
		key := ""
		if oversized || !complete && !eof {
			var next ArchiveMessageRef
			key, next, err = s.relocateZcodeLargeKey(ctx, h, start)
			if err != nil {
				return nil, ref, err
			}
			reader.pos = next
			eof = next.Chunk >= len(h.SessionArchive.Chunks)
			raw = nil
		} else {
			if len(raw) > 0 && (!utf8.Valid(raw) || !json.Valid(raw)) {
				return nil, ref, ErrConflict
			}
			key = archiveRecordKey(h.SessionArchive.Format, raw)
		}
		if key == ref.RecordKey {
			if matched {
				return nil, ref, ErrConflict
			}
			matched = true
			found = raw
			position = start
		}
		if eof {
			break
		}
	}
	if !matched {
		return nil, ref, ErrConflict
	}
	position.MessageSHA256 = ref.MessageSHA256
	position.RecordKey = ref.RecordKey
	return found, position, nil
}
func (s *Service) relocateZcodeLargeKey(ctx context.Context, h Record, start ArchiveMessageRef) (string, ArchiveMessageRef, error) {
	rec, err := s.archiveLargeRecord(ctx, h, start)
	if err != nil {
		return "", start, err
	}
	key, err := archiveLargeZcodeKey(rec)
	if rec.reader.failure != nil {
		err = rec.reader.failure
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			err = ErrConflict
		}
		return "", start, err
	}
	return key, rec.next, nil
}
