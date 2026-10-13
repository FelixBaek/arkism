package memory

import (
	"context"
	"time"
)

// MemoryExpiryInput sets or clears (ExpiresAt == nil) the optional end date of
// an active memory. Once that time passes every read path that uses live()
// or the Postgres expires_at filter treats the memory as expired: it is no
// longer recalled, searched or listed as active.
type MemoryExpiryInput struct {
	Scope            Scope      `json:"scope"`
	ExpectedRevision int64      `json:"expected_revision"`
	ExpiresAt        *time.Time `json:"expires_at"`
}

func (s *Service) SetMemoryExpiry(ctx context.Context, token, id string, in MemoryExpiryInput) (Record, error) {
	if !validScope(in.Scope) || containsSecret(structuredText(in.Scope)) || in.ExpectedRevision < 1 || !validRunLabel(id, 256) || containsSecret(id) {
		return Record{}, ErrInvalid
	}
	now := time.Now().UTC()
	if in.ExpiresAt != nil {
		at := in.ExpiresAt.UTC().Truncate(time.Millisecond)
		if !at.After(now) || at.Year() > 9999 {
			return Record{}, ErrInvalid
		}
		in.ExpiresAt = &at
	}
	actor, e := s.authorize(ctx, token, "transition:memories", in.Scope, id, "")
	if e != nil {
		return Record{}, e
	}
	if actor.Role != "human" {
		return Record{}, ErrDenied
	}
	if s.Repo == nil {
		return Record{}, ErrUnavailable
	}
	var out Record
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out = Record{}
		r, e := tx.Get(ctx, "memories", in.Scope, id)
		if e != nil {
			return e
		}
		if r.PrivacyQuarantined || r.ContentErased {
			return ErrNotFound
		}
		if r.ConflictID != "" || r.Revision != in.ExpectedRevision || !live(r) {
			return ErrConflict
		}
		r.ExpiresAt = in.ExpiresAt
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		if e = tx.Replace(ctx, "memories", r, in.ExpectedRevision); e != nil {
			return e
		}
		action := "set_expiry:memories"
		if in.ExpiresAt == nil {
			action = "clear_expiry:memories"
		}
		if e = audit(ctx, tx, in.Scope, actor.PrincipalID, action, id); e != nil {
			return e
		}
		out = r
		return nil
	})
	if e != nil {
		return Record{}, e
	}
	return out, nil
}
