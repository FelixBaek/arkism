package memory

import (
	"context"
	"errors"
	"time"
)

// An evaluation obligation is committed with activation. It is an event-driven
// job: the embedding/erasure worker must not claim it or exhaust retries while
// waiting for an outcome. Recording an independent, rated evaluation settles
// it in that evaluation's transaction. Pending is neither success nor failure.
const memoryEvaluationKind = "memory_evaluation"

type MemoryEvaluationTracking struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

func memoryEvaluationID(scope Scope, target Ref) string {
	return "me_" + ContentHash(structuredText(struct {
		Scope  Scope
		Target Ref
	}{scope, target}))
}

func enqueueMemoryEvaluation(ctx context.Context, tx Tx, m Record) error {
	target := Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}
	now := time.Now().UTC()
	return tx.Insert(ctx, "jobs", Record{ID: memoryEvaluationID(m.Scope, target), Scope: m.Scope,
		Kind: memoryEvaluationKind, Status: JobStatusPending, Revision: 1,
		Target: &target, CreatedAt: now, UpdatedAt: now})
}

// Completion records that evidence was received, never that a memory helped.
// Failed independent checks also settle the obligation; self-rating and missing
// observations do not. Historical untracked memories remain compatible.
func recordMemoryEvaluation(ctx context.Context, tx Tx, ev Record) error {
	if ev.Target == nil || ev.Target.Collection != colMemories || ev.Evaluation == nil || ev.Evaluation.EvalType == "model_self" || ev.Evaluation.Passed == nil {
		return nil
	}
	job, err := tx.Get(ctx, "jobs", ev.Scope, memoryEvaluationID(ev.Scope, *ev.Target))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if job.Kind != memoryEvaluationKind || job.Target == nil || *job.Target != *ev.Target {
		return ErrConflict
	}
	if job.Status == JobStatusDone {
		return nil
	}
	if job.Status != JobStatusPending {
		return ErrConflict
	}
	target, err := tx.Get(ctx, colMemories, ev.Scope, ev.Target.ID)
	if err != nil {
		return err
	}
	if target.Revision != ev.Target.Revision || !live(target) || target.ContentErased || target.PrivacyQuarantined {
		return ErrConflict
	}
	if err = checkSources(ctx, tx, target); err != nil {
		return err
	}
	if err = checkEvaluationEvidence(ctx, tx, ev); err != nil {
		return err
	}
	rev := job.Revision
	job.Revision++
	job.Status = JobStatusDone
	job.UpdatedAt = time.Now().UTC()
	job.Relations = []Ref{{Collection: "evaluations", ID: ev.ID, Revision: ev.Revision}}
	return tx.Replace(ctx, "jobs", job, rev)
}

func memoryEvaluationTracking(ctx context.Context, tx Tx, scope Scope, target Ref) (*MemoryEvaluationTracking, error) {
	if target.Collection != colMemories {
		return nil, nil
	}
	job, err := tx.Get(ctx, "jobs", scope, memoryEvaluationID(scope, target))
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if job.Kind != memoryEvaluationKind || job.Target == nil || *job.Target != target {
		return nil, ErrConflict
	}
	return &MemoryEvaluationTracking{JobID: job.ID, Status: job.Status}, nil
}
