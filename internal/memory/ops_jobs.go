package memory

import (
	"context"
	"time"
)

// Only background work kinds are operable. Erasure, privacy, quota, receipt,
// approval and counter rows in jobs keep their own state machines untouched.
var operableJobKinds = map[string]bool{"embedding": true, wikiJobKind: true}

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
	return v
}

func (s *Service) ListOperations(ctx context.Context, token string, in OpsListInput) (OperationPage, error) {
	out := OperationPage{Items: []OperationView{}}
	if in.Action != "" {
		return out, ErrInvalid
	}
	if _, err := s.opsAuthorize(ctx, token, "read:jobs", in.Scope, "", roleHuman, roleService); err != nil {
		return out, err
	}
	rows, next, err := s.opsPage(ctx, in, colJobs)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Items = append(out.Items, viewOperation(r))
	}
	out.NextCursor = next
	return out, nil
}

func (s *Service) GetOperation(ctx context.Context, token string, scope Scope, id string) (OperationView, error) {
	if _, err := s.opsAuthorize(ctx, token, "read:jobs", scope, id, roleHuman, roleService); err != nil {
		return OperationView{}, err
	}
	if s.Repo == nil {
		return OperationView{}, ErrUnavailable
	}
	r, err := s.Repo.Get(ctx, colJobs, scope, id)
	if err != nil {
		return OperationView{}, err
	}
	return viewOperation(r), nil
}

// CancelOperation stops pending or running work. A running job loses its
// lease: the revision moves on, so the holder's finish is refused and the
// claim queries (which only take pending/retry/expired-running) skip it.
func (s *Service) CancelOperation(ctx context.Context, token, id string, in OperationChangeInput) (OperationView, error) {
	return s.changeOperation(ctx, token, id, in, "transition:jobs", "cancel:jobs", func(r *Record) (bool, error) {
		if r.Status != JobStatusPending && r.Status != JobStatusRetry && r.Status != JobStatusRunning {
			return false, ErrConflict
		}
		r.Status, r.LeaseToken, r.LeaseUntil, r.RunAt = JobStatusCancelled, "", time.Time{}, nil
		return false, nil
	})
}

// RetryOperation requeues failed work with fresh attempts, at most
// maxManualJobRetries times per job.
func (s *Service) RetryOperation(ctx context.Context, token, id string, in OperationChangeInput) (OperationView, error) {
	return s.changeOperation(ctx, token, id, in, "transition:jobs", "retry:jobs", func(r *Record) (bool, error) {
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
	return s.changeOperation(ctx, token, id, in, "delete:jobs", "delete:jobs", func(r *Record) (bool, error) {
		if r.Status != JobStatusDone && r.Status != JobStatusFailed && r.Status != JobStatusCancelled {
			return false, ErrConflict
		}
		return true, nil
	})
}

func (s *Service) changeOperation(ctx context.Context, token, id string, in OperationChangeInput, action, auditAction string, apply func(*Record) (bool, error)) (OperationView, error) {
	if in.ExpectedRevision < 0 {
		return OperationView{}, ErrInvalid
	}
	d, err := s.opsAuthorize(ctx, token, action, in.Scope, id, roleHuman)
	if err != nil {
		return OperationView{}, err
	}
	if s.Repo == nil {
		return OperationView{}, ErrUnavailable
	}
	var out Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, colJobs, in.Scope, id)
		if err != nil {
			return err
		}
		if !operableJobKinds[r.Kind] {
			return ErrInvalid
		}
		if in.ExpectedRevision > 0 && r.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		prev := r.Revision
		remove, err := apply(&r)
		if err != nil {
			return err
		}
		if remove {
			err = tx.Delete(ctx, colJobs, in.Scope, id, prev)
		} else {
			r.Revision++
			r.UpdatedAt = time.Now().UTC()
			err = tx.Replace(ctx, colJobs, r, prev)
		}
		if err != nil {
			return err
		}
		out = r
		return audit(ctx, tx, in.Scope, d.PrincipalID, auditAction, id)
	})
	if err != nil {
		return OperationView{}, err
	}
	return viewOperation(out), nil
}
