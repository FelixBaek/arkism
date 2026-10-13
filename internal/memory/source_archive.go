package memory

import (
	"context"
	"sort"
)

// References preserve provenance only, not raw text or execution authority.
// Reading them later always goes through the live archive message API.
func (s *Service) validateArchiveMessageRefs(ctx context.Context, token string, scope Scope, refs []ArchiveMessageRef, session, role string) ([]Record, []ArchiveMessageRef, error) {
	if len(refs) == 0 {
		return nil, nil, nil
	}
	if len(refs) > 8 {
		return nil, nil, ErrInvalid
	}
	normalized := make([]ArchiveMessageRef, 0, len(refs))
	seen := map[ArchiveMessageRef]bool{}
	seenKeys := map[string]bool{}
	headers := map[string]Record{}
	for _, ref := range refs {
		if !validArchiveMessageRef(ref) {
			return nil, nil, ErrInvalid
		}
		view, err := s.ReadSessionArchiveMessage(ctx, token, ArchiveMessageInput{Scope: scope, Ref: ref})
		if err != nil {
			return nil, nil, err
		}
		position := view.Ref
		position.SHA256, position.MessageSHA256, position.RecordKey = "", "", ""
		key := view.Ref.ArchiveID + ":" + view.Ref.RecordKey
		if seen[position] || view.Ref.RecordKey != "" && seenKeys[key] {
			return nil, nil, ErrInvalid
		}
		seen[position] = true
		if view.Ref.RecordKey != "" {
			seenKeys[key] = true
		}
		if role != "" && view.Role != role {
			return nil, nil, ErrInvalid
		}
		h, err := s.Repo.Get(ctx, colSessionArchives, scope, ref.ArchiveID)
		if err != nil {
			return nil, nil, err
		}
		if !isSessionArchiveHeader(h) || h.Scope != scope || h.SessionArchive.SHA256 != view.Ref.SHA256 || sessionArchiveStateOf(h, s.sessionArchiveNow()) != sessionArchiveStateActive {
			return nil, nil, ErrConflict
		}
		if session != "" && h.SessionArchive.SessionID != session {
			return nil, nil, ErrInvalid
		}
		if earlier, exists := headers[h.ID]; exists && earlier.SessionArchive.SHA256 != h.SessionArchive.SHA256 {
			return nil, nil, ErrConflict
		}
		normalized = append(normalized, view.Ref)
		headers[h.ID] = h
	}
	out := make([]Record, 0, len(headers))
	for _, h := range headers {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, normalized, nil
}
func (s *Service) fenceSourceArchives(ctx context.Context, tx Tx, headers []Record) error {
	for _, h := range headers {
		current, err := tx.Get(ctx, colSessionArchives, h.Scope, h.ID)
		if err != nil {
			return err
		}
		if !isSessionArchiveHeader(current) || current.Revision != h.Revision || current.SessionArchive.SHA256 != h.SessionArchive.SHA256 || sessionArchiveStateOf(current, s.sessionArchiveNow()) != sessionArchiveStateActive {
			return ErrConflict
		}
		if err := tx.Replace(ctx, colSessionArchives, current, current.Revision); err != nil {
			return err
		}
	}
	return nil
}

// Dedupe preserves the saved immutable provenance. A stable digest can prove
// equality after append; a legacy link still requires the exact archive SHA.
// Proposed references have already been verified against the current archive.
func sameSourceArchiveMessages(saved, proposed []ArchiveMessageRef) bool {
	if len(saved) != len(proposed) {
		return false
	}
	for i, a := range saved {
		b := proposed[i]
		if a.ArchiveID != b.ArchiveID || a.SQLiteStep != b.SQLiteStep {
			return false
		}
		stableKey := a.RecordKey != "" && a.RecordKey == b.RecordKey && a.MessageSHA256 != "" && a.MessageSHA256 == b.MessageSHA256
		if !stableKey && (a.Chunk != b.Chunk || a.Offset != b.Offset) {
			return false
		}
		if a.MessageSHA256 != "" && b.MessageSHA256 != "" {
			if a.MessageSHA256 != b.MessageSHA256 {
				return false
			}
		} else if a.SHA256 != b.SHA256 {
			return false
		}
	}
	return true
}
