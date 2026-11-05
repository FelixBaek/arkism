package memory

// The derive scheduler runs mental-model triggers (daily, cron, full_refresh_cron,
// on_change, after_consolidation) and the space's consolidation_schedule
// (daily, weekly) without anyone calling consolidate. Each run only proposes:
// mental models get drafts and observations proposals, which a person reviews.
//
// It is off unless the operator sets MEMORY_DERIVE_SCHEDULE=on: it spends the
// server model's daily calls.
//
// One job per scope (kind derive_schedule in the jobs collection) waits until the
// earliest time anything in the scope is due. Writes that change that time
// (a model created, edited or cleared, a space config saved, a memory approved)
// move it sooner; a run moves it to the next due time. A trigger inside a model's
// min_interval holds the job until the interval ends, so a burst of triggers folds
// into one refresh. The worker claims it like any job (WorkOnce), and the hourly
// seed (SeedDeriveSchedules) covers scopes whose records predate the scheduler.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"
)

const (
	deriveScheduleKind = "derive_schedule"
	deriveFloor        = time.Minute // a run never wakes again sooner than this
	deriveBackoff      = time.Hour   // after a run that hit a budget, permission or model stop
	deriveSeedLimit    = 500
)

var consolidationEvery = map[string]time.Duration{"daily": 24 * time.Hour, "weekly": 7 * 24 * time.Hour}

// DeriveScheduleJob is the state of a scope's derive scheduler job.
type DeriveScheduleJob struct {
	LastConsolidation *time.Time `json:"last_consolidation,omitempty" bson:"last_consolidation,omitempty"`
	LastRun           *time.Time `json:"last_run,omitempty" bson:"last_run,omitempty"`
	Outcome           string     `json:"outcome,omitempty" bson:"outcome,omitempty"`
}

func deriveScheduleOn() bool { return os.Getenv("MEMORY_DERIVE_SCHEDULE") == "on" }

func deriveScheduleJobID(scope Scope) string {
	return "derive-schedule-" + ContentHash(structuredText(scope))
}

// consolidationDue is when the scope's consolidation_schedule next runs (space scope only).
func consolidationDue(c SpaceConfig, scope Scope, state DeriveScheduleJob, now time.Time) (time.Time, bool) {
	every, ok := consolidationEvery[c.ConsolidationSchedule]
	if !ok || scope != spaceScope(scope) {
		return time.Time{}, false
	}
	if state.LastConsolidation == nil {
		return now, true
	}
	return state.LastConsolidation.Add(every), true
}

// deriveNextAt is the earliest time anything in the scope is due.
func (s *Service) deriveNextAt(ctx context.Context, scope Scope, state DeriveScheduleJob, now time.Time) (time.Time, bool, error) {
	c, err := s.spaceSettings(ctx, scope)
	if err != nil {
		return time.Time{}, false, err
	}
	next, ok := consolidationDue(c, scope, state, now)
	rows, err := s.Repo.ListRecordsByStatus(ctx, scope, colMentalModels, statusActive, mmListLimit)
	if err != nil {
		return time.Time{}, false, err
	}
	latest, latestObs, gap := latestApprovedMemory(ctx, s.Repo, scope), latestObservationChange(ctx, s.Repo, scope), mentalModelSpaceGap(c)
	for _, r := range rows {
		if r.MentalModel == nil {
			continue
		}
		if at, due := mentalModelNextAt(r.MentalModel, latest, latestObs, now, gap); due && (!ok || at.Before(next)) {
			next, ok = at, true
		}
	}
	return next, ok, nil
}

// scheduleDerive moves the scope's job to the earliest due time. Best effort: the
// write that called it never fails on it; the hourly seed catches anything missed.
func (s *Service) scheduleDerive(ctx context.Context, scope Scope) {
	if !deriveScheduleOn() || s.Wiki == nil || s.Repo == nil || ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()
	state := DeriveScheduleJob{}
	if job, err := s.Repo.Get(ctx, colJobs, scope, deriveScheduleJobID(scope)); err == nil && job.DeriveSchedule != nil {
		state = *job.DeriveSchedule
	}
	next, ok, err := s.deriveNextAt(ctx, scope, state, now)
	if err == nil {
		queue := func() error {
			return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error { return queueDerive(ctx, tx, scope, next, ok, now) })
		}
		if err = queue(); errors.Is(err, ErrConflict) {
			err = queue() // two writes raced to insert the job
		}
	}
	if err != nil {
		slog.Debug("derive schedule not queued", "error", err)
	}
}

func queueDerive(ctx context.Context, tx Tx, scope Scope, next time.Time, due bool, now time.Time) error {
	id := deriveScheduleJobID(scope)
	job, err := tx.Get(ctx, colJobs, scope, id)
	if errors.Is(err, ErrNotFound) {
		if !due {
			return nil
		}
		job = Record{ID: id, Scope: scope, Kind: deriveScheduleKind, Status: JobStatusDone, CreatedAt: now, DeriveSchedule: &DeriveScheduleJob{}}
	} else if err != nil {
		return err
	}
	pending := job.Status == JobStatusPending || job.Status == JobStatusRetry
	switch {
	case job.Kind != deriveScheduleKind || job.DeriveSchedule == nil || job.Status == JobStatusRunning:
		return nil // a running job schedules itself when it ends
	case !due && !pending, due && pending && job.RunAt != nil && !job.RunAt.After(next):
		return nil
	case !due:
		job.Status, job.RunAt = JobStatusDone, nil
	default:
		job.Status, job.RunAt, job.Attempts = JobStatusPending, &next, 0
	}
	job.UpdatedAt = now
	if job.Revision == 0 {
		job.Revision = 1
		return tx.Insert(ctx, colJobs, job)
	}
	prev := job.Revision
	job.Revision++
	return tx.Replace(ctx, colJobs, job, prev)
}

// runDeriveSchedule runs what is due with the worker's credentials, then stores the job
// pending at the next due time, or done when nothing is scheduled.
func (s *Service) runDeriveSchedule(ctx context.Context, workerToken string, job Record) error {
	state := DeriveScheduleJob{}
	if job.DeriveSchedule != nil {
		state = *job.DeriveSchedule
	}
	now := time.Now().UTC()
	outcome, err := s.derivePass(ctx, workerToken, job.Scope, &state, now)
	if err != nil {
		return err
	}
	state.LastRun, state.Outcome = &now, outcome
	next, due, err := s.deriveNextAt(ctx, job.Scope, state, now)
	if err != nil {
		return err
	}
	floor := now.Add(deriveFloor)
	if outcome != "ok" {
		floor = now.Add(deriveBackoff)
	}
	if next.Before(floor) {
		next = floor
	}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cur, err := tx.Get(ctx, colJobs, job.Scope, job.ID)
		if err != nil {
			return err
		}
		if cur.Revision != job.Revision || cur.LeaseToken != job.LeaseToken {
			return ErrConflict // the lease was lost (cancelled or reclaimed)
		}
		cur.Status, cur.RunAt, cur.LeaseToken, cur.LeaseUntil, cur.UpdatedAt = JobStatusPending, &next, "", time.Time{}, now
		if !due {
			cur.Status, cur.RunAt = JobStatusDone, nil
		}
		cur.Attempts, cur.DeriveSchedule = 0, &state
		prev := cur.Revision
		cur.Revision++
		return tx.Replace(ctx, colJobs, cur, prev)
	})
}

// derivePass runs a scheduled consolidation (which also refreshes due mental models),
// or else the due mental models alone, and names how it ended.
func (s *Service) derivePass(ctx context.Context, token string, scope Scope, state *DeriveScheduleJob, now time.Time) (string, error) {
	if s.Wiki == nil {
		return "model_not_configured", nil
	}
	if calls, err := s.wikiDailyCalls(ctx, scope, now); err != nil {
		return "", err
	} else if calls >= wikiDailyCallLimit {
		return "model_budget_exceeded", nil
	}
	c, err := s.spaceSettings(ctx, scope)
	if err != nil {
		return "", err
	}
	at, ok := consolidationDue(c, scope, *state, now)
	consolidate := ok && !now.Before(at)
	if missing := s.missingDeriveGrant(ctx, token, scope, consolidate); missing != "" {
		slog.Warn("derive schedule: the worker token lacks a grant; see guide/server.md", "action", missing)
		return "denied:" + missing, nil
	}
	if consolidate {
		_, err = s.ConsolidateObservations(ctx, token, ObservationConsolidateInput{Scope: scope})
		state.LastConsolidation = &now
		return derivePassOutcome(err)
	}
	d, err := s.derivedWriter(ctx, token, colMentalModels, scope, "")
	if err == nil {
		out := ObservationConsolidateResult{}
		err = s.refreshDueMentalModels(ctx, token, d, ObservationConsolidateInput{Scope: scope, MaxGroups: obsGroupsMax}, &WikiJob{MaxTotalTokens: obsRunTokens}, &out)
	}
	return derivePassOutcome(err)
}

// deriveGrants are the worker actions a scheduled pass needs (guide/server.md, MEMORY_DERIVE_SCHEDULE).
// The app cannot grant them itself: principals are registered with the schema owner's connection.
var deriveGrants = []string{"write:mental_models", "search:memories", "read:memories", "context:read"}

// missingDeriveGrant names the first needed action the worker token is denied, so the job's
// outcome says what to grant instead of a run that silently reads nothing.
func (s *Service) missingDeriveGrant(ctx context.Context, token string, scope Scope, consolidate bool) string {
	grants := deriveGrants
	if consolidate {
		grants = append([]string{"write:observations"}, grants...)
	}
	for _, action := range grants {
		if _, err := s.authorize(ctx, token, action, scope, "", ""); errors.Is(err, ErrDenied) {
			return action
		}
	}
	return ""
}

// derivePassOutcome names how a derivation pass ended; an error it does not name is returned to be retried.
func derivePassOutcome(err error) (string, error) {
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

// deriveScopeLister is the cross-scope read the hourly seed needs (Postgres).
type deriveScopeLister interface {
	DeriveScheduleScopes(ctx context.Context, limit int) ([]Scope, error)
}

// SeedDeriveSchedules queues the scheduler for every scope that has an active mental
// model or a space config, so records written before the scheduler existed are covered.
// It returns how many scopes it looked at; without the scheduler or a cross-scope store it does nothing.
func (s *Service) SeedDeriveSchedules(ctx context.Context) (int, error) {
	lister, ok := s.Repo.(deriveScopeLister)
	if !deriveScheduleOn() || s.Wiki == nil || !ok {
		return 0, nil
	}
	scopes, err := lister.DeriveScheduleScopes(ctx, deriveSeedLimit)
	if err != nil {
		return 0, err
	}
	for _, scope := range scopes {
		s.scheduleDerive(ctx, scope)
	}
	return len(scopes), nil
}

// DeriveScheduleScopes lists the scopes holding an active mental model or a space config.
func (p *Postgres) DeriveScheduleScopes(ctx context.Context, limit int) ([]Scope, error) {
	rows, e := p.q().Query(ctx, `SELECT DISTINCT workspace_id,project_id,user_id,agent_id FROM memory_records WHERE (collection=$1 AND status='active') OR collection=$2 LIMIT $3`, colMentalModels, colSpaceConfigs, limit)
	if e != nil {
		return nil, pgErr(e)
	}
	defer rows.Close()
	out := []Scope{}
	for rows.Next() {
		var sc Scope
		if e = rows.Scan(&sc.WorkspaceID, &sc.ProjectID, &sc.UserID, &sc.AgentID); e != nil {
			return nil, ErrUnavailable
		}
		out = append(out, sc)
	}
	return out, pgErr(rows.Err())
}
