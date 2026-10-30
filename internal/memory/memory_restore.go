package memory

import (
	"context"
	"net/http"
	"time"
)

type MemoryRestoreInput struct {
	Scope            Scope `json:"scope"`
	ExpectedRevision int64 `json:"expected_revision"`
}

// RestoreMemory brings back a memory a person deleted (only a human can move a memory to rejected).
// Only a human can do it, the same permission as the delete; it never revives an erased,
// quarantined, expired or superseded memory, or one nobody approved.
func (s *Service) RestoreMemory(ctx context.Context, token, id string, in MemoryRestoreInput) (Record, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 || containsSecret(structuredText(in.Scope)) {
		return Record{}, ErrInvalid
	}
	actor, err := s.authorize(ctx, token, "transition:memories", in.Scope, id, "")
	if err != nil {
		return Record{}, err
	}
	if actor.Role != roleHuman {
		return Record{}, ErrDenied
	}
	if s.Repo == nil {
		return Record{}, ErrUnavailable
	}
	ctx = bindRequestActor(ctx, actor)
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colMemories, in.Scope, id)
		if err != nil {
			return err
		}
		if r.ContentErased || r.PrivacyQuarantined {
			return ErrNotFound
		}
		if r.Revision != in.ExpectedRevision || r.ConflictID != "" {
			return ErrConflict
		}
		if r.Status != "rejected" || r.ApprovedBy == "" || r.ExpiresAt != nil && !r.ExpiresAt.After(time.Now()) {
			return ErrInvalid
		}
		if err = checkSources(ctx, tx, r); err != nil {
			return err
		}
		if err = checkRelations(ctx, tx, r); err != nil {
			return err
		}
		r.Status = statusActive
		r.Removal = nil
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		if _, err = writeTransition(ctx, tx, colMemories, r, in.ExpectedRevision, nil, nil, actor); err != nil {
			return err
		}
		if err = audit(ctx, tx, in.Scope, actor.PrincipalID, "transition:memories:restored", id); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	return out, nil
}

func memoryRestoreRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/memories/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
		var in MemoryRestoreInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.RestoreMemory(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, err)
	})
}
