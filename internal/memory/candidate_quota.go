package memory

import (
	"context"
	"errors"
	"time"
)

var candidateQuotaScope = Scope{WorkspaceID: "101-internal", ProjectID: "candidate-quota"}

func reserveCandidate(ctx context.Context, tx Tx, scope Scope, accepted time.Time, limit int) error {
	if local, ok := tx.(interface {
		ReserveCandidateQuota(context.Context, Scope, time.Time, int) error
	}); ok {
		return local.ReserveCandidateQuota(ctx, scope, accepted, limit)
	}
	if limit < 1 || limit > 100000 {
		return ErrUnavailable
	}
	id := "candidate-quota-" + accepted.UTC().Format("2006-01-02")
	r, e := tx.Get(ctx, "jobs", candidateQuotaScope, id)
	if errors.Is(e, ErrNotFound) {
		return tx.Insert(ctx, "jobs", Record{ID: id, Scope: candidateQuotaScope, Kind: "candidate_quota", Status: "done", Revision: 1, QuotaUsed: 1, CreatedAt: accepted, UpdatedAt: accepted})
	}
	if e != nil {
		return e
	}
	if r.Kind != "candidate_quota" || r.Status != "done" || r.QuotaUsed < 1 || r.Revision < 1 {
		return ErrUnavailable
	}
	if r.QuotaUsed >= limit {
		return ErrCandidateQuota
	}
	previous := r.Revision
	r.QuotaUsed++
	r.Revision++
	r.UpdatedAt = accepted
	return tx.Replace(ctx, "jobs", r, previous)
}
