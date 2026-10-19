package memory

import (
	"context"
	"net/http"
	"time"
)

type MemoryReviewDueInput struct {
	Scope            Scope      `json:"scope"`
	ExpectedRevision int64      `json:"expected_revision"`
	ReviewDueAt      *time.Time `json:"review_due_at"`
}

// A review reminder never expires, approves or renews a memory. Only a human
// actor can schedule it; workers may report overdue reminders without acting.
func (s *Service) SetMemoryReviewDue(ctx context.Context, token, id string, in MemoryReviewDueInput) (Record, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 256) || in.ExpectedRevision < 1 || containsSecret(structuredText(in.Scope)) {
		return Record{}, ErrInvalid
	}
	if in.ReviewDueAt != nil {
		at := in.ReviewDueAt.UTC().Truncate(time.Millisecond)
		if !at.After(time.Now()) || !storableTime(at) {
			return Record{}, ErrInvalid
		}
		in.ReviewDueAt = &at
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
		if r.Revision != in.ExpectedRevision || !live(r) || r.ConflictID != "" {
			return ErrConflict
		}
		if err = checkSources(ctx, tx, r); err != nil {
			return err
		}
		r.ReviewDueAt = in.ReviewDueAt
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		if err = tx.Replace(ctx, colMemories, r, in.ExpectedRevision); err != nil {
			return err
		}
		if err = audit(ctx, tx, in.Scope, actor.PrincipalID, "set_review_due:memories", id); err != nil {
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

func memoryReviewDueRoutes(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("POST /v1/memories/{id}/review-due", func(w http.ResponseWriter, r *http.Request) {
		var in MemoryReviewDueInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.SetMemoryReviewDue(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, err)
	})
}
