package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

// Zcode's typed envelopes reject non-string metadata. Keep that contract
// separate from JSONL's permissive optional-string fields.
func archiveZcodeString(v archiveJSONSpan) (string, error) {
	if v.kind != 0 && v.kind != 'n' && v.kind != '"' {
		return "", ErrInvalid
	}
	return archiveJSONSmall(v)
}
func archiveZcodeIDHash(v archiveJSONSpan, prefix string) (string, error) {
	if v.kind == 0 || v.kind == 'n' {
		return "", nil
	}
	if v.kind != '"' {
		return "", ErrInvalid
	}
	reader, err := archiveJSONString(v)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = io.WriteString(digest, prefix)
	n, err := io.Copy(digest, reader)
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", nil
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
func archiveZcodeHidden(fields map[string]archiveJSONSpan) (bool, error) {
	channel, err := archiveZcodeString(fields["channel"])
	if err != nil {
		return false, err
	}
	kind, err := archiveZcodeString(fields["type"])
	return hiddenArchiveEnvelope(channel, kind), err
}
func (r *archiveLargeRecord) fields() (map[string]archiveJSONSpan, error) {
	fields, err := archiveJSONFields(archiveJSONSpan{r.source, 0, -1, '{'})
	if r.reader.failure != nil {
		return nil, r.reader.failure
	}
	return fields, err
}
func archiveZcodeEnvelope(row map[string]archiveJSONSpan) (string, map[string]archiveJSONSpan, bool, error) {
	table, err := archiveZcodeString(row["table"])
	if err != nil {
		return "", nil, false, err
	}
	hidden, err := archiveZcodeHidden(row)
	if err != nil {
		return "", nil, false, err
	}
	if table != "message" && table != "part" {
		return table, nil, hidden, nil
	}
	if row["record"].kind != '{' {
		return "", nil, false, ErrInvalid
	}
	record, err := archiveJSONFields(row["record"])
	if err != nil {
		return "", nil, false, err
	}
	innerHidden, err := archiveZcodeHidden(record)
	return table, record, hidden || innerHidden, err
}
func archiveZcodeData(record map[string]archiveJSONSpan) (map[string]archiveJSONSpan, error) {
	data := record["data"]
	if data.kind != '"' {
		return nil, ErrInvalid
	}
	return archiveJSONFields(archiveJSONSpan{archiveJSONEmbedded(data), 0, -1, '{'})
}
func archiveLargeZcodeKey(record *archiveLargeRecord) (string, error) {
	row, err := record.fields()
	if err != nil {
		return "", err
	}
	table, fields, _, err := archiveZcodeEnvelope(row)
	if err != nil || table != "part" {
		return "", err
	}
	return archiveZcodeIDHash(fields["id"], "arkism-zcode-part-v1\n")
}
func (s *Service) archiveLargeZcodeText(ctx context.Context, h Record, rec *archiveLargeRecord) (string, string, func() (io.ReadCloser, error), error) {
	row, err := rec.fields()
	if err != nil {
		return "", "", nil, err
	}
	table, record, hidden, err := archiveZcodeEnvelope(row)
	if err != nil || table != "part" || hidden {
		return "", "", nil, err
	}
	key, err := archiveZcodeIDHash(record["id"], "arkism-zcode-part-v1\n")
	if err != nil {
		return "", "", nil, err
	}
	messageID, err := archiveZcodeIDHash(record["message_id"], "")
	if err != nil {
		return "", "", nil, err
	}
	if messageID == "" {
		return "", "", nil, ErrInvalid
	}
	part, shown, err := archiveZcodeTextPart(record)
	if err != nil || !shown {
		return "", "", nil, err
	}
	role, used, err := s.archiveZcodeRoleHash(ctx, h, messageID)
	rec.scanned += used
	if errors.Is(err, errArchiveZcodeRoleMissing) {
		err = ErrInvalid
	}
	if err != nil {
		return "", "", nil, err
	}
	if role != "user" && role != "assistant" || part["text"].kind != '"' {
		return "", "", nil, nil
	}
	return role, key, func() (io.ReadCloser, error) {
		r, err := archiveJSONString(part["text"])
		if err != nil {
			return nil, err
		}
		return io.NopCloser(r), nil
	}, nil
}

// shown is false for a hidden, synthetic or non-text part.
func archiveZcodeTextPart(record map[string]archiveJSONSpan) (map[string]archiveJSONSpan, bool, error) {
	part, err := archiveZcodeData(record)
	if err != nil {
		return nil, false, err
	}
	hidden, err := archiveZcodeHidden(part)
	if err != nil {
		return nil, false, err
	}
	kind, err := archiveZcodeString(part["type"])
	if err != nil {
		return nil, false, err
	}
	synthetic := part["synthetic"].kind
	if synthetic != 0 && synthetic != 'n' && synthetic != 't' && synthetic != 'f' {
		return nil, false, ErrInvalid
	}
	if hidden || kind != "text" || synthetic == 't' {
		return nil, false, nil
	}
	if part["text"].kind != 0 && part["text"].kind != 'n' && part["text"].kind != '"' {
		return nil, false, ErrInvalid
	}
	return part, true, nil
}

func archiveLargeZcodeRoleEntry(rec *archiveLargeRecord) (string, string, string, error) {
	row, err := rec.fields()
	if err != nil {
		return "", "", "", err
	}
	table, record, hidden, err := archiveZcodeEnvelope(row)
	if err != nil || table != "message" {
		return table, "", "", err
	}
	id, err := archiveZcodeIDHash(record["id"], "")
	if err != nil {
		return "", "", "", err
	}
	if id == "" {
		return "", "", "", ErrInvalid
	}
	data, err := archiveZcodeData(record)
	if err != nil {
		return "", "", "", err
	}
	innerHidden, err := archiveZcodeHidden(data)
	if err != nil {
		return "", "", "", err
	}
	role, err := archiveZcodeString(data["role"])
	if hidden || innerHidden {
		role = "ignored"
	}
	return table, id, role, err
}
