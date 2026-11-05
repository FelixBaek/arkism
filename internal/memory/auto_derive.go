package memory

// Automatic derivation: approved memories trigger the existing model passes
// that turn them into proposals: observation consolidation (which also
// refreshes due mental models), causal link proposals and the model entity
// pass. Each pass only proposes; a person approves what becomes active.
//
// It is off unless the operator sets MEMORY_AUTO_DERIVE=on: an upgrade must not
// start spending the server model's daily calls on its own.
//
// One deduped job per scope (kind auto_derive in the jobs collection) counts
// memories that became active (afterMemoryApproval, every approval path). It
// runs once threshold approvals arrived, or interval after the first
// unprocessed one, whichever is sooner. The shared worker (WorkOnce)
// claims it like any job and runs one pass per claim, so a slow model never
// holds the embedding queue for more than one bounded pass. Nothing is queued
// or run without a server model, and every pass goes through the per-scope
// daily call limit and run token cap (wikiCall).

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"time"
)

const (
	autoDeriveJobKind          = "auto_derive"
	autoDeriveDefaultThreshold = 10
	autoDeriveDefaultInterval  = 6 * time.Hour
	autoDeriveGroups           = 1 // one model group per pass keeps a claim inside the worker iteration
)

var autoDerivePasses = []string{"observations", "causal", "entities"}

// AutoDeriveJob is the state of a scope's automatic derivation job.
type AutoDeriveJob struct {
	Approved int        `json:"approved" bson:"approved"`
	Pass     int        `json:"pass" bson:"pass"`
	Outcome  string     `json:"outcome,omitempty" bson:"outcome,omitempty"`
	LastRun  *time.Time `json:"last_run,omitempty" bson:"last_run,omitempty"`
	// Started is when the current run's first pass began; approvals after it are queued again at the end.
	Started *time.Time `json:"started,omitempty" bson:"started,omitempty"`
}

type autoDeriveSettings struct {
	Enabled   bool
	Threshold int
	Interval  time.Duration
}

// autoDeriveSettingsFor returns the server defaults, set by MEMORY_AUTO_DERIVE (on
// enables; off by default), MEMORY_AUTO_DERIVE_THRESHOLD (approvals, 1-1000) and
// MEMORY_AUTO_DERIVE_INTERVAL (a duration, 1m-720h). Invalid values keep the default.
// Per-space overrides plug in here: read the scope's SpaceConfig and let set fields win.
func autoDeriveSettingsFor(Scope) autoDeriveSettings {
	out := autoDeriveSettings{Enabled: os.Getenv("MEMORY_AUTO_DERIVE") == "on", Threshold: autoDeriveDefaultThreshold, Interval: autoDeriveDefaultInterval}
	if n, err := strconv.Atoi(os.Getenv("MEMORY_AUTO_DERIVE_THRESHOLD")); err == nil && n >= 1 && n <= 1000 {
		out.Threshold = n
	}
	if d, err := time.ParseDuration(os.Getenv("MEMORY_AUTO_DERIVE_INTERVAL")); err == nil && d >= time.Minute && d <= 720*time.Hour {
		out.Interval = d
	}
	return out
}

func autoDeriveJobID(scope Scope) string {
	return "auto-derive-" + ContentHash(structuredText(scope))
}

// noteApprovedMemory counts one approval toward the scope's job. Best effort: an
// approval never fails on it. One arriving while the job runs leaves the job alone
// (the run holds its lease); the run queues itself again when it ends (Started).
func (s *Service) noteApprovedMemory(ctx context.Context, scope Scope) {
	set := autoDeriveSettingsFor(scope)
	if s.Wiki == nil || !set.Enabled || s.Repo == nil || ctx.Err() != nil {
		return
	}
	count := func() error {
		return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			return countApproval(ctx, tx, scope, set, time.Now().UTC())
		})
	}
	err := count()
	if errors.Is(err, ErrConflict) {
		err = count() // two first approvals raced to insert the job; count on the one that won
	}
	if err != nil {
		slog.Debug("automatic derivation not queued", "error", err)
	}
}

func countApproval(ctx context.Context, tx Tx, scope Scope, set autoDeriveSettings, now time.Time) error {
	id := autoDeriveJobID(scope)
	job, err := tx.Get(ctx, colJobs, scope, id)
	if errors.Is(err, ErrNotFound) {
		job = Record{ID: id, Scope: scope, Kind: autoDeriveJobKind, Status: JobStatusDone, CreatedAt: now, AutoDerive: &AutoDeriveJob{}}
	} else if err != nil {
		return err
	}
	if job.Kind != autoDeriveJobKind || job.AutoDerive == nil || job.Status == JobStatusRunning {
		return nil
	}
	state := *job.AutoDerive
	if job.Status != JobStatusPending {
		state.Approved, state.Pass = 0, 0
		job.Status, job.Attempts = JobStatusPending, 0
		at := now.Add(set.Interval)
		job.RunAt = &at
	}
	state.Approved++
	if state.Approved >= set.Threshold {
		job.RunAt = &now
	}
	job.AutoDerive, job.UpdatedAt = &state, now
	if job.Revision == 0 {
		job.Revision = 1
		return tx.Insert(ctx, colJobs, job)
	}
	prev := job.Revision
	job.Revision++
	return tx.Replace(ctx, colJobs, job, prev)
}

// runAutoDerive runs the job's next pass with the worker's credentials and stores
// the job itself: pending again for the next pass, or done after the last one.
// A model, budget or permission stop ends the run; other errors are retried.
func (s *Service) runAutoDerive(ctx context.Context, workerToken string, job Record) error {
	state := AutoDeriveJob{}
	if job.AutoDerive != nil {
		state = *job.AutoDerive
	}
	if state.Pass == 0 || state.Started == nil {
		started := time.Now().UTC()
		state.Started, state.Approved = &started, 0 // this run consumes the approvals counted so far
	}
	outcome, err := s.autoDerivePass(ctx, workerToken, job.Scope, state.Pass)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	state.Pass++
	state.Outcome, state.LastRun = outcome, &now
	done := outcome != "ok" || state.Pass >= len(autoDerivePasses)
	// Approvals that arrived after the run began are not in every pass it made: start the next count.
	again := done && (state.Approved > 0 || latestApprovedMemory(ctx, s.Repo, job.Scope).After(*state.Started))
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cur, err := tx.Get(ctx, colJobs, job.Scope, job.ID)
		if err != nil {
			return err
		}
		if cur.Revision != job.Revision || cur.LeaseToken != job.LeaseToken {
			return ErrConflict // the lease was lost (cancelled or reclaimed)
		}
		cur.Status, cur.RunAt, cur.LeaseToken, cur.LeaseUntil, cur.UpdatedAt = JobStatusPending, &now, "", time.Time{}, now
		switch {
		case again:
			set := autoDeriveSettingsFor(job.Scope)
			at := now.Add(set.Interval)
			if state.Approved >= set.Threshold {
				at = now
			}
			state.Pass, state.Started, state.Approved, cur.RunAt = 0, nil, max(state.Approved, 1), &at
		case done:
			cur.Status, cur.RunAt = JobStatusDone, nil
		}
		cur.Attempts, cur.AutoDerive = 0, &state
		prev := cur.Revision
		cur.Revision++
		return tx.Replace(ctx, colJobs, cur, prev)
	})
}

// autoDerivePass runs one existing proposal pass and names how it ended.
func (s *Service) autoDerivePass(ctx context.Context, token string, scope Scope, pass int) (string, error) {
	if pass < 0 || pass >= len(autoDerivePasses) {
		return "invalid_pass", nil
	}
	if s.Wiki == nil {
		return "model_not_configured", nil
	}
	if calls, err := s.wikiDailyCalls(ctx, scope, time.Now()); err != nil {
		return "", err
	} else if calls >= wikiDailyCallLimit {
		return "model_budget_exceeded", nil
	}
	var err error
	switch autoDerivePasses[pass] {
	case "observations":
		_, err = s.ConsolidateObservations(ctx, token, ObservationConsolidateInput{Scope: scope, MaxGroups: autoDeriveGroups})
	case "causal":
		_, err = s.ProposeCausalLinks(ctx, token, CausalProposeInput{Scope: scope, MaxGroups: autoDeriveGroups})
	default:
		_, err = s.ExtractEntities(ctx, token, EntityExtractInput{Scope: scope, Model: true})
	}
	switch {
	case err == nil, errors.Is(err, ErrNotFound), errors.Is(err, errModelAnswer):
		return "ok", nil
	case errors.Is(err, ErrModelBudget), errors.Is(err, errWikiBudget):
		return "model_budget_exceeded", nil
	case errors.Is(err, ErrDenied):
		return "denied", nil
	case errors.Is(err, ErrModelNotConfigured):
		return "model_not_configured", nil
	}
	return "", err
}
