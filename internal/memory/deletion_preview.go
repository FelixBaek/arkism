package memory

import (
	"context"
	"net/url"
)

const deletionPreviewLimit = 1000

type DeletionPreview struct {
	Target                Ref            `json:"target"`
	TargetStatus          string         `json:"target_status"`
	TargetHasStoredVector bool           `json:"target_has_stored_vector"`
	DirectReferences      map[string]int `json:"direct_references"`
	Coverage              string         `json:"coverage"`
	CascadePlanAvailable  bool           `json:"cascade_plan_available"`
	Authority             string         `json:"authority"`
}

// Strict parsing is essential here: ignoring an invalid dry-run flag would delete data.
func parseDeleteQuery(raw string) (bool, error) {
	q, e := url.ParseQuery(raw)
	if e != nil || raw != "" && len(q) != 1 {
		return false, ErrInvalid
	}
	for key, values := range q {
		if key != "dry_run" || len(values) != 1 || values[0] != "true" && values[0] != "false" {
			return false, ErrInvalid
		}
	}
	return q.Get("dry_run") == "true", nil
}

// DeletionReferences returns IDs/revisions only, including expired/historical rows.

func (s *Service) PreviewDeletion(ctx context.Context, token, col, id string, in ChangeInput) (DeletionPreview, error) {
	if col != "sources" && col != "memories" || !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 {
		return DeletionPreview{}, ErrInvalid
	}
	who, e := s.authorize(ctx, token, "delete:"+col, in.Scope, id, "")
	if e != nil {
		return DeletionPreview{}, e
	}
	if who.Role != "human" {
		return DeletionPreview{}, ErrDenied
	}
	reader, e := s.authorize(ctx, token, "read:"+col, in.Scope, id, "")
	if e != nil {
		return DeletionPreview{}, e
	}
	if reader.PrincipalID != who.PrincipalID || reader.Role != who.Role {
		return DeletionPreview{}, ErrDenied
	}
	if s.Repo == nil {
		return DeletionPreview{}, ErrUnavailable
	}
	var out DeletionPreview
	// shortcut: at most 1000 per-row policy checks inside one snapshot; move
	// authorization outside with revision fences if measured latency requires it.
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = DeletionPreview{}
		target, e := tx.Get(ctx, col, in.Scope, id)
		if e != nil {
			return e
		}
		if target.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		out = DeletionPreview{Target: Ref{Collection: col, ID: id, Revision: target.Revision}, TargetStatus: target.Status, TargetHasStoredVector: target.Embedding != nil && len(target.Embedding.Vector) > 0, DirectReferences: map[string]int{}, Coverage: "direct_references", Authority: AuthorityDataOnly}
		total := 0
		for _, collection := range Collections {
			if collection == "audit_events" || collection == colBlobs {
				continue
			}
			rows, e := tx.DeletionReferences(ctx, collection, in.Scope, out.Target, deletionPreviewLimit-total+1)
			if e != nil {
				return e
			}
			total += len(rows)
			if total > deletionPreviewLimit {
				return ErrUnavailable
			}
			for _, ref := range rows {
				if ref.Collection != collection || !validRunLabel(ref.ID, 256) || ref.Revision < 1 {
					return ErrUnavailable
				}
				d, e := s.authorize(ctx, token, "read:"+collection, in.Scope, ref.ID, "")
				if e != nil {
					return e
				}
				if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
					return ErrDenied
				}
			}
			out.DirectReferences[collection] = len(rows)
		}
		return audit(ctx, tx, in.Scope, who.PrincipalID, "preview:delete:"+col, id)
	})
	if e != nil {
		return DeletionPreview{}, e
	}
	return out, nil
}
