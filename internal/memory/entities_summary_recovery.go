package memory

import (
	"context"
	"errors"
	"slices"
	"time"
)

var errSummaryScheduleBusy = errors.New("summary scheduler busy")

// A receipt and the existing summary scheduler change commit together. It is
// deliberately separate from the running recovery lease/revision.
func recoverSummarySchedule(ctx context.Context, tx Tx, store namedEntityStore, r Record, set autoDeriveSettings, actor string) error {
	receiptID := "approved-summary-" + ContentHash(structuredText(Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}))
	receipt, e := tx.Get(ctx, colJobs, r.Scope, receiptID)
	if e == nil {
		if receipt.Kind != "approved_summary_receipt" || receipt.Target == nil || *receipt.Target != (Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(e, ErrNotFound) {
		return e
	}
	ids, e := approvedPeopleIn(ctx, store, r)
	if e != nil {
		return e
	}
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UTC()
	job, e := tx.Get(ctx, colJobs, r.Scope, autoDeriveJobID(r.Scope))
	if errors.Is(e, ErrNotFound) || e == nil && job.Status != JobStatusPending && job.Status != JobStatusRunning {
		if e = countApproval(ctx, tx, r.Scope, set, now); e != nil {
			return e
		}
		job, e = tx.Get(ctx, colJobs, r.Scope, autoDeriveJobID(r.Scope))
	}
	if e != nil {
		return e
	}
	if job.Kind != autoDeriveJobKind || job.AutoDerive == nil {
		return ErrConflict
	}
	// A running summary may already have read this person before approval.
	// Even an already queued ID needs a subsequent pass for the new evidence.
	if job.Status == JobStatusRunning {
		return errSummaryScheduleBusy
	}
	state := *job.AutoDerive
	if state.Entities, e = queueSummaryEntities(state.Entities, ids); e != nil {
		return e
	}
	if len(state.Entities) != len(job.AutoDerive.Entities) {
		prev := job.Revision
		job.Revision++
		job.AutoDerive = &state
		job.UpdatedAt = now
		if e = tx.Replace(ctx, colJobs, job, prev); e != nil {
			return e
		}
	}
	return tx.Insert(ctx, colJobs, Record{ID: receiptID, Scope: r.Scope, Kind: "approved_summary_receipt", Status: JobStatusDone, Revision: 1, Target: &Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}, CreatedBy: actor, CreatedAt: now, UpdatedAt: now})
}

// queueSummaryEntities returns a copy of queued with each of ids added once, or
// ErrConflict when the queue would pass autoDeriveEntitiesMax.
func queueSummaryEntities(queued, ids []string) ([]string, error) {
	out := slices.Clone(queued)
	for _, id := range ids {
		if !slices.Contains(out, id) {
			if len(out) >= autoDeriveEntitiesMax {
				return nil, ErrConflict
			}
			out = append(out, id)
		}
	}
	return out, nil
}

// deferClaimedJob puts a claimed job back to pending after delay and refunds
// its attempt: used when the cause is outside the job (a busy scheduler, an
// unreachable dependency), not a failed attempt. The exact claimed lease must
// still be ours before refunding that claim.
func (s *Service) deferClaimedJob(ctx context.Context, job Record, delay time.Duration) error {
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cur, e := tx.Get(ctx, colJobs, job.Scope, job.ID)
		if e != nil {
			return e
		}
		if cur.Status != JobStatusRunning || cur.Revision != job.Revision || cur.LeaseToken == "" || cur.LeaseToken != job.LeaseToken {
			return ErrConflict
		}
		now := time.Now().UTC()
		next := now.Add(delay)
		cur.Status, cur.RunAt, cur.LeaseToken, cur.LeaseUntil, cur.UpdatedAt = JobStatusPending, &next, "", time.Time{}, now
		cur.Attempts = max(0, cur.Attempts-1)
		cur.Revision++
		return tx.Replace(ctx, colJobs, cur, job.Revision)
	})
}
