package memory

import (
	"context"
	"errors"
	"time"
)

const skillTrialCheckKind = "skill_trial_check"

func skillTrialCheckID(scope Scope, id string) string {
	return "trial-check-" + ContentHash(structuredText(scope)+"/"+id)
}
func enqueueSkillTrialCheck(ctx context.Context, tx Tx, trial Record) error {
	now := time.Now().UTC()
	ref := Ref{Collection: colSkillTrials, ID: trial.ID, Revision: trial.Revision}
	return tx.Insert(ctx, colJobs, Record{ID: skillTrialCheckID(trial.Scope, trial.ID), Scope: trial.Scope, Kind: skillTrialCheckKind, Status: JobStatusPending, Revision: 1, Target: &ref, SkillID: trial.SkillID, RunAt: &now, CreatedBy: trial.CreatedBy, CreatedByType: trial.CreatedByType, CreatedAt: now, UpdatedAt: now})
}

// Claim refuses inactive trials, so a stop must close the check job itself. A
// live lease is left for its holder, whose Finish sees the stop and closes it.
func closeSkillTrialCheck(ctx context.Context, tx Tx, trial Record) error {
	job, e := tx.Get(ctx, colJobs, trial.Scope, skillTrialCheckID(trial.Scope, trial.ID))
	if errors.Is(e, ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	now := time.Now().UTC()
	if job.Kind != skillTrialCheckKind || job.Target == nil || job.Target.ID != trial.ID {
		return ErrConflict
	}
	if job.Status == JobStatusDone || job.Status == JobStatusFailed || job.Status == JobStatusRunning && job.LeaseUntil.After(now) {
		return nil
	}
	prev := job.Revision
	job.Status = JobStatusDone
	job.Revision++
	job.LeaseToken = ""
	job.LeaseUntil = time.Time{}
	job.UpdatedAt = now
	return tx.Replace(ctx, colJobs, job, prev)
}

type SkillTrialCheckLease struct {
	ID         string    `json:"id"`
	Revision   int64     `json:"revision"`
	LeaseToken string    `json:"lease_token"`
	LeaseUntil time.Time `json:"lease_until"`
}
type SkillTrialCheckFinish struct {
	Scope            Scope  `json:"scope"`
	SkillID          string `json:"skill_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	LeaseToken       string `json:"lease_token"`
	Outcome          string `json:"outcome"`
}

func (s *Service) trialCheckActor(ctx context.Context, token string, scope Scope, skillID string) (Decision, error) {
	actor, e := s.authorize(ctx, token, "apply:skills", scope, skillID, "")
	if e != nil {
		return actor, e
	}
	_, e = s.authorizeSkillAutomation(ctx, token, actor, scope, skillID, "")
	return actor, e
}
func (s *Service) ClaimSkillTrialCheck(ctx context.Context, token, id string, in SkillTrialReadInput) (SkillTrialCheckLease, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 128) || !validRunLabel(in.SkillID, 256) {
		return SkillTrialCheckLease{}, ErrInvalid
	}
	actor, e := s.trialCheckActor(ctx, token, in.Scope, in.SkillID)
	if e != nil {
		return SkillTrialCheckLease{}, e
	}
	ctx = bindRequestActor(ctx, actor)
	out := SkillTrialCheckLease{}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		trial, e := tx.Get(ctx, colSkillTrials, in.Scope, id)
		if e != nil {
			return e
		}
		if trial.SkillID != in.SkillID || trial.SkillTrial == nil || trial.ContentErased || trial.PrivacyQuarantined || trial.Status != "active" {
			return ErrNotFound
		}
		job, e := tx.Get(ctx, colJobs, in.Scope, skillTrialCheckID(in.Scope, id))
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		if e = claimableSkillTrialCheck(job, in.SkillID, id, now); e != nil {
			return e
		}
		lease, e := newLeaseToken()
		if e != nil {
			return e
		}
		prev := job.Revision
		job.Status = JobStatusRunning
		job.Revision++
		job.LeaseToken = lease
		job.LeaseUntil = now.Add(5 * time.Minute)
		job.Attempts++
		job.UpdatedAt = now
		if e = tx.Replace(ctx, colJobs, job, prev); e != nil {
			return e
		}
		out = SkillTrialCheckLease{ID: job.ID, Revision: job.Revision, LeaseToken: lease, LeaseUntil: job.LeaseUntil}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "claim:skill_trial_check", job.ID)
	})
	if e != nil {
		return SkillTrialCheckLease{}, e
	}
	return out, nil
}
func (s *Service) FinishSkillTrialCheck(ctx context.Context, token, id string, in SkillTrialCheckFinish) error {
	if !validScope(in.Scope) || !validRunLabel(id, 128) || !validRunLabel(in.SkillID, 256) || in.ExpectedRevision < 1 || in.LeaseToken == "" || (in.Outcome != "checked" && in.Outcome != "unavailable") {
		return ErrInvalid
	}
	actor, e := s.trialCheckActor(ctx, token, in.Scope, in.SkillID)
	if e != nil {
		return e
	}
	ctx = bindRequestActor(ctx, actor)
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		job, e := tx.Get(ctx, colJobs, in.Scope, skillTrialCheckID(in.Scope, id))
		if e != nil {
			return e
		}
		now := time.Now().UTC()
		requestHash := ContentHash(structuredText(in))
		if job.Kind == skillTrialCheckKind && job.SkillID == in.SkillID && job.Revision == in.ExpectedRevision+1 && job.Status != JobStatusRunning && job.RequestHash == requestHash {
			return nil
		}
		if job.Kind != skillTrialCheckKind || job.SkillID != in.SkillID || job.Status != JobStatusRunning || job.Revision != in.ExpectedRevision || job.LeaseToken != in.LeaseToken || !job.LeaseUntil.After(now) {
			return ErrConflict
		}
		terminal, e := skillTrialCheckTerminal(ctx, tx, in.Scope, id, now)
		if e != nil {
			return e
		}
		prev := job.Revision
		job.Revision++
		job.LeaseToken = ""
		job.LeaseUntil = time.Time{}
		job.UpdatedAt = now
		job.RequestHash = requestHash
		if terminal {
			job.Status = JobStatusDone
		} else if in.Outcome == "checked" {
			job.Status = JobStatusRetry
			next := now.Add(time.Minute)
			job.RunAt = &next
			job.Attempts = 0
		} else if job.Attempts >= 8 {
			job.Status = JobStatusFailed
		} else {
			job.Status = JobStatusRetry
			next := now.Add(time.Duration(1<<min(job.Attempts, 6)) * time.Minute)
			job.RunAt = &next
		}
		if e = tx.Replace(ctx, colJobs, job, prev); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "finish:skill_trial_check", job.ID)
	})
}
func claimableSkillTrialCheck(job Record, skillID, id string, now time.Time) error {
	if job.Kind != skillTrialCheckKind || job.Target == nil || job.Target.ID != id || job.SkillID != skillID || job.ContentErased || job.PrivacyQuarantined {
		return ErrConflict
	}
	if job.Status == JobStatusDone || job.Status == JobStatusFailed || (job.RunAt != nil && job.RunAt.After(now)) || job.Status == JobStatusRunning && job.LeaseUntil.After(now) {
		return ErrNotFound
	}
	if job.Status != JobStatusPending && job.Status != JobStatusRetry && job.Status != JobStatusRunning {
		return ErrConflict
	}
	if job.Attempts >= 8 {
		return ErrUnavailable
	}
	return nil
}
func skillTrialCheckTerminal(ctx context.Context, tx Tx, scope Scope, id string, now time.Time) (bool, error) {
	trial, e := tx.Get(ctx, colSkillTrials, scope, id)
	terminal := errors.Is(e, ErrNotFound)
	if e != nil && !terminal {
		return false, e
	}
	if !terminal {
		terminal = trial.SkillTrial == nil || trial.ContentErased || trial.PrivacyQuarantined || trial.Status != "active" || !trial.SkillTrial.EndsAt.After(now)
	}
	if !terminal {
		state, err := skillTrialTerminal(ctx, tx, trial)
		if err != nil {
			return false, err
		}
		terminal = state == "rolled_back" || state == "expired"
	}
	return terminal, nil
}
