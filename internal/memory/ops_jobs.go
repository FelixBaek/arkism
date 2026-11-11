package memory

import (
	"context"
	"encoding/json"
	"time"
)

// Only background work kinds are operable. Erasure, privacy, quota, receipt,
// approval and counter rows in jobs keep their own state machines untouched.
// A file extraction runs under its uploader's token, so an operator may cancel
// or delete it but not requeue it; the uploader resumes it with its run route.
// An agent sees and cancels only the jobs it started itself.
var operableJobKinds = map[string]bool{"embedding": true, wikiJobKind: true, fileExtractJobKind: true}

// OperationView is the metadata form of one jobs row; payloads stay inside.
type OperationView struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	Revision      int64      `json:"revision"`
	Attempts      int        `json:"attempts"`
	ManualRetries int        `json:"manual_retries"`
	Target        *Ref       `json:"target,omitempty"`
	RunAt         *time.Time `json:"run_at,omitempty"`
	LeaseUntil    *time.Time `json:"lease_until,omitempty"`
	Error         string     `json:"error,omitempty"`
	CreatedBy     string     `json:"created_by,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Operable      bool       `json:"operable"`
	// LastRun and Outcome are the last pass of a consolidation job (auto_derive, derive_schedule): ok,
	// model_budget_exceeded, denied or model_not_configured.
	LastRun *time.Time `json:"last_run,omitempty"`
	Outcome string     `json:"outcome,omitempty"`
	// Route and Result belong to a deferred call (ops_deferred.go). Result is the answer the
	// route would have returned and is only on the single read, never in a list.
	Route  string          `json:"route,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

type OperationPage struct {
	Items      []OperationView `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

// OperationChangeInput carries the scope (and optionally the revision the
// caller saw) for cancel, retry and delete.
type OperationChangeInput struct {
	Scope            Scope `json:"scope"`
	ExpectedRevision int64 `json:"expected_revision,omitempty"`
}

func viewOperation(r Record) OperationView {
	v := OperationView{ID: r.ID, Kind: opsText(r.Kind), Status: r.Status, Revision: r.Revision, Attempts: r.Attempts, ManualRetries: r.ManualRetries,
		Target: opsRef(r.Target), RunAt: r.RunAt, CreatedBy: opsText(r.CreatedBy), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Operable: operableJobKinds[r.Kind]}
	if !r.LeaseUntil.IsZero() {
		at := r.LeaseUntil
		v.LeaseUntil = &at
	}
	if job := wikiJobOf(r); job != nil {
		v.Error = opsText(job.Error)
	}
	if a := r.AutoDerive; a != nil {
		v.LastRun, v.Outcome = a.LastRun, a.Outcome
	}
	if d := r.DeriveSchedule; d != nil {
		v.LastRun, v.Outcome = d.LastRun, d.Outcome
	}
	if c := r.Deferred; c != nil {
		v.Route, v.Error, v.Result = c.Route, c.Error, c.Result
		if r.Status == JobStatusRunning && !r.LeaseUntil.After(time.Now()) { // the process that ran it is gone
			v.Status, v.Error = JobStatusFailed, deferredInterrupted
		}
	}
	return v
}

func (s *Service) ListOperations(ctx context.Context, token string, in OpsListInput) (OperationPage, error) {
	out := OperationPage{Items: []OperationView{}}
	if in.Action != "" || in.Transport != "" {
		return out, ErrInvalid
	}
	d, err := s.opsAuthorize(ctx, token, "read:jobs", in.Scope, "", roleHuman, roleService, roleAgent)
	if err != nil {
		return out, err
	}
	if d.Role == roleAgent {
		in.Actor = d.PrincipalID
	}
	rows, next, err := s.opsPage(ctx, in, colJobs)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		v := viewOperation(r)
		v.Result = nil // a list names the work; the answer is on the single read
		out.Items = append(out.Items, v)
	}
	out.NextCursor = next
	return out, nil
}

func (s *Service) GetOperation(ctx context.Context, token string, scope Scope, id string) (OperationView, error) {
	d, err := s.opsAuthorize(ctx, token, "read:jobs", scope, id, roleHuman, roleService, roleAgent)
	if err != nil {
		return OperationView{}, err
	}
	if s.Repo == nil {
		return OperationView{}, ErrUnavailable
	}
	r, err := s.Repo.Get(ctx, colJobs, scope, id)
	if err != nil {
		return OperationView{}, err
	}
	if !ownsJob(d, r) {
		return OperationView{}, ErrNotFound
	}
	return viewOperation(r), nil
}

// CancelOperation stops pending or running work. A running job loses its
// lease: the revision moves on, so the holder's finish is refused and the
// claim queries (which only take pending/retry/expired-running) skip it.
func (s *Service) CancelOperation(ctx context.Context, token, id string, in OperationChangeInput) (OperationView, error) {
	var reserved map[string]string
	out, err := s.changeOperation(ctx, token, id, in, "transition:jobs", "cancel:jobs", true, func(r *Record) (bool, error) {
		if r.Status != JobStatusPending && r.Status != JobStatusRetry && r.Status != JobStatusRunning {
			return false, ErrConflict
		}
		r.Status, r.LeaseToken, r.LeaseUntil, r.RunAt = JobStatusCancelled, "", time.Time{}, nil
		reserved = nil
		if r.Kind == fileExtractJobKind && r.FileExtract != nil && r.FileExtract.Batch != nil {
			reserved, r.FileExtract.Batch = r.FileExtract.Batch.Reserved, nil // its answers are never filed now
		}
		return false, nil
	})
	if err == nil {
		// The batch's reserved ledger rows close as unanswered calls, so they stop holding the day's cap.
		s.releaseExtractCalls(context.WithoutCancel(ctx), in.Scope, reserved)
	}
	return out, err
}

// RetryOperation requeues failed work with fresh attempts, at most
// maxManualJobRetries times per job.
func (s *Service) RetryOperation(ctx context.Context, token, id string, in OperationChangeInput) (OperationView, error) {
	return s.changeOperation(ctx, token, id, in, "transition:jobs", "retry:jobs", false, func(r *Record) (bool, error) {
		if r.Kind == fileExtractJobKind {
			return false, ErrInvalid
		}
		if r.Status != JobStatusFailed || r.ManualRetries >= maxManualJobRetries {
			return false, ErrConflict
		}
		requeueJob(r, time.Now().UTC())
		r.ManualRetries++
		return false, nil
	})
}

// DeleteOperation removes finished work only.
func (s *Service) DeleteOperation(ctx context.Context, token, id string, in OperationChangeInput) (OperationView, error) {
	return s.changeOperation(ctx, token, id, in, "delete:jobs", "delete:jobs", false, func(r *Record) (bool, error) {
		if r.Status != JobStatusDone && r.Status != JobStatusFailed && r.Status != JobStatusCancelled {
			return false, ErrConflict
		}
		return true, nil
	})
}

// ownsJob: people and services see every job, an agent only the ones it started.
func ownsJob(d Decision, r Record) bool { return d.Role != roleAgent || r.CreatedBy == d.PrincipalID }

// changeOperation applies one change; agentOwn lets an agent change a job it started itself.
func (s *Service) changeOperation(ctx context.Context, token, id string, in OperationChangeInput, action, auditAction string, agentOwn bool, apply func(*Record) (bool, error)) (OperationView, error) {
	if in.ExpectedRevision < 0 {
		return OperationView{}, ErrInvalid
	}
	roles := []string{roleHuman}
	if agentOwn {
		roles = append(roles, roleAgent)
	}
	d, err := s.opsAuthorize(ctx, token, action, in.Scope, id, roles...)
	if err != nil {
		return OperationView{}, err
	}
	if s.Repo == nil {
		return OperationView{}, ErrUnavailable
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = changeOperationTx(ctx, tx, d, id, in, auditAction, apply)
		return err
	})
	if err != nil {
		return OperationView{}, err
	}
	return viewOperation(out), nil
}

func changeOperationTx(ctx context.Context, tx Tx, d Decision, id string, in OperationChangeInput, auditAction string, apply func(*Record) (bool, error)) (Record, error) {
	r, err := tx.Get(ctx, colJobs, in.Scope, id)
	if err != nil {
		return Record{}, err
	}
	if !ownsJob(d, r) {
		return Record{}, ErrNotFound
	}
	if !operableJobKinds[r.Kind] {
		return Record{}, ErrInvalid
	}
	if in.ExpectedRevision > 0 && r.Revision != in.ExpectedRevision {
		return Record{}, ErrConflict
	}
	prev := r.Revision
	remove, err := apply(&r)
	if err != nil {
		return Record{}, err
	}
	if remove {
		err = tx.Delete(ctx, colJobs, in.Scope, id, prev)
	} else {
		r.Revision++
		r.UpdatedAt = time.Now().UTC()
		err = tx.Replace(ctx, colJobs, r, prev)
	}
	if err != nil {
		return Record{}, err
	}
	return r, audit(ctx, tx, in.Scope, d.PrincipalID, auditAction, id)
}
