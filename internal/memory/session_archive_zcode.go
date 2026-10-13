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
		if oversize || !complete && !eof {
			rec, err := s.archiveLargeRecord(ctx, h, start)
			if err != nil {
				return "", scanned, err
			}
			table, id, foundRole, err := archiveLargeZcodeRoleEntry(rec)
			scanned += rec.scanned
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
			r.pos = rec.next
			if rec.next.Chunk >= len(h.SessionArchive.Chunks) {
				break
			}
			continue
		}
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
			return "", scanned, errArchiveZcodeMetadata
		}
		if row.Table != "" && row.Table != "message" {
			break
		}
		if row.Table == "message" {
			var data struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Channel string `json:"channel"`
			}
			if row.Record.ID == "" || json.Unmarshal([]byte(row.Record.Data), &data) != nil {
				return "", scanned, errArchiveZcodeMetadata
			}
			if ContentHash(row.Record.ID) == messageHash {
				matches++
				if matches > 1 {
					return "", scanned, errArchiveZcodeMetadata
				}
				role = data.Role
				if hiddenArchiveEnvelope(data.Channel, data.Type) || hiddenArchiveEnvelope(row.Channel, row.Type) || hiddenArchiveEnvelope(row.Record.Channel, row.Record.Type) {
					role = "ignored"
				}
			}
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
			rec, err := s.archiveLargeRecord(ctx, h, start)
			if err != nil {
				return nil, ref, err
			}
			key, err = archiveLargeZcodeKey(rec)
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
				return nil, ref, err
			}
			reader.pos = rec.next
			eof = rec.next.Chunk >= len(h.SessionArchive.Chunks)
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
