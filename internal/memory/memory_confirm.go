package memory

import (
	"context"
	"net/http"
	"time"
)

// A confirm is reflect's note that a stored lesson held again in a later
// session. It counts on the existing memory or waiting candidate: proof_count
// and last_confirmed_at move, the text and revision do not, so no approval,
// edit or digest basis changes. The count is a fact for people and ranking to
// read; it never approves a candidate or renews a memory (ADR-0022).
type MemoryConfirmInput struct {
	Scope Scope `json:"scope"`
}

func (s *Service) ConfirmMemory(ctx context.Context, token, col, id string, in MemoryConfirmInput) (Record, error) {
	if col != colMemories && col != "memory_candidates" || !validScope(in.Scope) || !validRunLabel(id, 256) || containsSecret(structuredText(in.Scope)) {
		return Record{}, ErrInvalid
	}
	actor, err := s.authorize(ctx, token, "write:"+col, in.Scope, id, "")
	if err != nil {
		return Record{}, err
	}
	if actor.Role != roleHuman && actor.Role != "agent" {
		return Record{}, ErrDenied
	}
	if s.Repo == nil {
		return Record{}, ErrUnavailable
	}
	ctx = bindRequestActor(ctx, actor)
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, col, in.Scope, id)
		if err != nil {
			return err
		}
		if r.ContentErased || r.PrivacyQuarantined {
			return ErrNotFound
		}
		if col == colMemories && !live(r) || col != colMemories && r.Status != "candidate" {
			return ErrConflict
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		r.ProofCount++
		r.LastConfirmedAt = &now
		// Same revision on purpose: a counter is not an edit (checkReplaceRevision allows it).
		if err = tx.Replace(ctx, col, r, r.Revision); err != nil {
			return err
		}
		if err = audit(ctx, tx, in.Scope, actor.PrincipalID, "confirm:"+col, id); err != nil {
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

func memoryConfirmRoutes(mux routeRegistrar, s *Service) {
	for _, col := range []string{colMemories, "memory_candidates"} {
		mux.HandleFunc("POST /v1/"+col+"/{id}/confirm", func(w http.ResponseWriter, r *http.Request) {
			var in MemoryConfirmInput
			if !decode(w, r, &in) {
				return
			}
			out, err := s.ConfirmMemory(r.Context(), bearer(r), col, r.PathValue("id"), in)
			respond(w, out, err)
		})
	}
}
