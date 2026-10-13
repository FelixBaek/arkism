package memory

import (
	"context"
	"errors"
	"time"
)

// A separate successful batch persisted the cleanup intent with the detach.
// Keep that intent until GC and the acknowledgement both succeed; replay is safe.
func (s *Service) erasePendingArtifacts(ctx context.Context, token string, job Record, actor Decision, action string) error {
	// One file per lease keeps remote authorization/GC within the worker budget.
	pending := job.SourceMemoryErasure.PendingArtifacts[0]
	d, err := s.authorize(ctx, token, "erase:"+colArtifacts, job.Scope, pending.ID, "")
	if err != nil {
		return err
	}
	if d.Role != "service" || d.PrincipalID != actor.PrincipalID {
		return ErrDenied
	}
	if s.BlobDirectory == "" {
		return ErrUnavailable
	}
	deferIfHeld := func() (bool, error) {
		deferred := false
		err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			deferred = false // A serialization retry must recompute the decision.
			lease, err := erasureLease(ctx, tx, job)
			if err != nil {
				return err
			}
			if err = tx.CheckArtifactPrivacyHold(ctx, pending.Digest); err != nil {
				if !errors.Is(err, ErrConflict) {
					return err
				}
				deferred = true
				return deferHeldErasure(ctx, tx, lease, actor.PrincipalID, action)
			}
			return nil
		})
		return deferred, err
	}
	return s.withBlobs(ctx, func(b *lockedBlobs) error {
		if deferred, err := deferIfHeld(); err != nil || deferred {
			return err
		}
		err := b.collect(s.Repo, pending.Digest)
		if errors.Is(err, ErrConflict) {
			// Only an actual surviving reference permits skipping GC. Other conflicts
			// (including an uncertain marker cleanup) must retain the retry intent.
			count, e := s.Repo.BlobReferences(ctx, pending.Digest)
			if e != nil {
				return e
			}
			if count > 0 {
				err = nil
			} else if deferred, holdErr := deferIfHeld(); holdErr != nil || deferred {
				// A hold may have committed after the first check, before GC's
				// marker transaction. Preserve the intent without charging a failure.
				return holdErr
			}
		}
		if err != nil {
			return err
		}
		return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			lease, err := erasureLease(ctx, tx, job)
			if err != nil {
				return err
			}
			progress := *lease.SourceMemoryErasure
			progress.PendingArtifacts = progress.PendingArtifacts[1:]
			lease.SourceMemoryErasure = &progress
			lease.Status = JobStatusPending
			lease.LeaseToken = ""
			lease.LeaseUntil = time.Time{}
			lease.Attempts = 0
			lease.UpdatedAt = time.Now().UTC()
			lease.Revision++
			if err = tx.Replace(ctx, "jobs", lease, job.Revision); err != nil {
				return err
			}
			return audit(ctx, tx, job.Scope, actor.PrincipalID, action+"_artifact_cleanup", job.ID)
		})
	})
}
