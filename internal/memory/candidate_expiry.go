package memory

import (
	"context"
	"time"
)

type ExpireCandidatesInput struct {
	Scope  Scope     `json:"scope"`
	Before time.Time `json:"before"`
	Limit  int       `json:"limit"`
}
type ExpireCandidatesOutput struct {
	Expired []Ref `json:"expired"`
}

func expiresBefore(r Record, before time.Time) bool {
	return r.Status == "candidate" && r.ConflictID == "" && !r.CreatedAt.IsZero() && !r.UpdatedAt.IsZero() && !r.CreatedAt.After(before) && !r.UpdatedAt.After(before)
}

func (s *Service) ExpireCandidates(ctx context.Context, token string, in ExpireCandidatesInput) (ExpireCandidatesOutput, error) {
	if in.Limit == 0 {
		in.Limit = 50
	}
	in.Before = in.Before.UTC().Truncate(time.Millisecond)
	if !validScope(in.Scope) || in.Before.IsZero() || !in.Before.Before(time.Now()) || in.Limit < 1 || in.Limit > 100 {
		return ExpireCandidatesOutput{}, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "expire:memory_candidates", in.Scope, "", "")
	if e != nil {
		return ExpireCandidatesOutput{}, e
	}
	allowedRole := func(role string) bool { return role == "human" || role == "service" || role == "pipeline" }
	if !allowedRole(actor.Role) {
		return ExpireCandidatesOutput{}, ErrDenied
	}
	if s.Repo == nil {
		return ExpireCandidatesOutput{}, ErrUnavailable
	}
	rows, e := s.Repo.ExpiringCandidates(ctx, in.Scope, in.Before, in.Limit)
	if e != nil {
		return ExpireCandidatesOutput{}, e
	}
	if len(rows) > in.Limit {
		return ExpireCandidatesOutput{}, ErrUnavailable
	}
	for _, r := range rows {
		if r.Scope != in.Scope || !expiresBefore(r, in.Before) {
			return ExpireCandidatesOutput{}, ErrUnavailable
		}
		d, e := s.authorize(ctx, token, "expire:memory_candidates", in.Scope, r.ID, "")
		if e != nil {
			return ExpireCandidatesOutput{}, e
		}
		if d.PrincipalID != actor.PrincipalID || d.Role != actor.Role {
			return ExpireCandidatesOutput{}, ErrDenied
		}
	}
	out := ExpireCandidatesOutput{Expired: []Ref{}}
	if len(rows) == 0 {
		return out, nil
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Expired = []Ref{}
		for _, row := range rows {
			r, e := tx.Get(ctx, "memory_candidates", in.Scope, row.ID)
			if e != nil {
				return e
			}
			if r.Revision != row.Revision || !expiresBefore(r, in.Before) {
				return ErrConflict
			}
			r.Status = "expired"
			r.Revision++
			r.UpdatedAt = time.Now().UTC()
			if e = tx.Replace(ctx, "memory_candidates", r, row.Revision); e != nil {
				return e
			}
			if e = audit(ctx, tx, in.Scope, actor.PrincipalID, "expire:memory_candidates", r.ID); e != nil {
				return e
			}
			out.Expired = append(out.Expired, Ref{Collection: "memory_candidates", ID: r.ID, Revision: r.Revision})
		}
		return nil
	})
	if e != nil {
		return ExpireCandidatesOutput{}, e
	}
	return out, nil
}
