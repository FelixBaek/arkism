package memory

// Automatic derivation: approved memories trigger the existing model passes
// that turn them into proposals: observation consolidation (which also
// refreshes due mental models), causal link proposals, the model entity pass
// and a summary draft for each person whose facts or relations were approved
// (DraftEntitySummary). Each pass only proposes; a person approves what becomes active.
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
	"slices"
	"strconv"
	"time"
)

const (
	autoDeriveJobKind          = "auto_derive"
	autoDeriveDefaultThreshold = 10
	autoDeriveDefaultInterval  = 6 * time.Hour
	autoDeriveGroups           = 1 // one model group per pass keeps a claim inside the worker iteration
)

var autoDerivePasses = []string{"observations", "causal", "entities", "summaries"}

// autoDeriveEntitiesMax caps the people a run drafts summaries for.
const autoDeriveEntitiesMax = 16

// AutoDeriveJob is the state of a scope's automatic derivation job.
type AutoDeriveJob struct {
	Approved int        `json:"approved" bson:"approved"`
	Pass     int        `json:"pass" bson:"pass"`
	Outcome  string     `json:"outcome,omitempty" bson:"outcome,omitempty"`
	LastRun  *time.Time `json:"last_run,omitempty" bson:"last_run,omitempty"`
	// Started is when the current run's first pass began; approvals after it are queued again at the end.
	Started *time.Time `json:"started,omitempty" bson:"started,omitempty"`
	// Entities are the people whose approved facts or relations wait for a summary draft.
	Entities []string `json:"entities,omitempty" bson:"entities,omitempty"`
}

type autoDeriveSettings struct {
	Enabled   bool
	Threshold int
	Interval  time.Duration
}

// serverAutoDerive is the server layer of the auto_derive space setting, set by
// MEMORY_AUTO_DERIVE (on enables; off by default), MEMORY_AUTO_DERIVE_THRESHOLD
// (approvals, 1-1000) and MEMORY_AUTO_DERIVE_INTERVAL (a duration, 1m-720h).
// Invalid values keep the default.
func serverAutoDerive() *SpaceAutoDerive {
	on := os.Getenv("MEMORY_AUTO_DERIVE") == "on"
	out := &SpaceAutoDerive{Enabled: &on, Threshold: autoDeriveDefaultThreshold, Interval: autoDeriveDefaultInterval.String()}
	if n, err := strconv.Atoi(os.Getenv("MEMORY_AUTO_DERIVE_THRESHOLD")); err == nil && n >= 1 && n <= 1000 {
		out.Threshold = n
	}
	if _, ok := autoDeriveInterval(os.Getenv("MEMORY_AUTO_DERIVE_INTERVAL")); ok {
		out.Interval = os.Getenv("MEMORY_AUTO_DERIVE_INTERVAL")
	}
	return out
}

func autoDeriveInterval(raw string) (time.Duration, bool) {
	d, err := time.ParseDuration(raw)
	return d, err == nil && d >= time.Minute && d <= 720*time.Hour
}

// autoDeriveSettingsFor is the scope's resolved auto_derive setting (space, then
// template, then server). A space read failure falls back to the server layer.
func (s *Service) autoDeriveSettingsFor(ctx context.Context, scope Scope) autoDeriveSettings {
	block := serverAutoDerive()
	if c, err := s.spaceSettings(ctx, scope); err == nil && c.AutoDerive != nil {
		block = c.AutoDerive
	}
	out := autoDeriveSettings{Enabled: block.Enabled != nil && *block.Enabled, Threshold: block.Threshold, Interval: autoDeriveDefaultInterval}
	if d, ok := autoDeriveInterval(block.Interval); ok {
		out.Interval = d
	}
	if out.Threshold < 1 || out.Threshold > 1000 {
		out.Threshold = autoDeriveDefaultThreshold
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
	if s.Wiki == nil || s.Repo == nil || ctx.Err() != nil {
		return
	}
	set := s.autoDeriveSettingsFor(ctx, scope)
	if !set.Enabled {
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
	outcome, err := s.autoDerivePass(ctx, workerToken, job.Scope, state.Pass, state.Entities)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	state.Pass++
	state.Outcome, state.LastRun = outcome, &now
	done := outcome != "ok" || state.Pass >= len(autoDerivePasses)
	drafted := []string{}
	if outcome == "ok" && autoDerivePasses[state.Pass-1] == "summaries" {
		drafted = state.Entities
	}
	// Approvals that arrived after the run began are not in every pass it made: start the next count.
	again := done && (state.Approved > 0 || latestApprovedMemory(ctx, s.Repo, job.Scope).After(*state.Started))
	set := s.autoDeriveSettingsFor(ctx, job.Scope) // read before the transaction: it reads the space outside tx
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cur, err := tx.Get(ctx, colJobs, job.Scope, job.ID)
		if err != nil {
			return err
		}
		if cur.Revision != job.Revision || cur.LeaseToken != job.LeaseToken {
			return ErrConflict // the lease was lost (cancelled or reclaimed)
		}
		cur.Status, cur.RunAt, cur.LeaseToken, cur.LeaseUntil, cur.UpdatedAt = JobStatusPending, &now, "", time.Time{}, now
		if cur.AutoDerive != nil { // people queued while the run held the lease stay queued
			state.Entities = slices.DeleteFunc(slices.Clone(cur.AutoDerive.Entities), func(id string) bool { return slices.Contains(drafted, id) })
		}
		switch {
		case again:
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
func (s *Service) autoDerivePass(ctx context.Context, token string, scope Scope, pass int, entities []string) (string, error) {
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
	case "summaries":
		err = s.draftQueuedSummaries(ctx, token, scope, entities)
	default:
		_, err = s.ExtractEntities(ctx, token, EntityExtractInput{Scope: scope, Model: true})
	}
	return derivePassOutcome(err)
}

// draftQueuedSummaries drafts a summary for each queued person. A person whose draft still waits
// for review (ErrConflict) or who is gone is skipped; any other stop ends the pass.
func (s *Service) draftQueuedSummaries(ctx context.Context, token string, scope Scope, ids []string) error {
	for _, id := range ids {
		_, err := s.DraftEntitySummary(ctx, token, id, EntitySummaryInput{Scope: scope})
		if err != nil && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// queueEntitySummaries puts the people an approved memory states facts or relations about on the
// scope's auto_derive job, which noteApprovedMemory queued. Best effort, like the count.
func (s *Service) queueEntitySummaries(ctx context.Context, r Record) {
	set := s.autoDeriveSettingsFor(ctx, r.Scope)
	if s.Wiki == nil || !set.Enabled {
		return
	}
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		store, e := entityTx(tx)
		if e != nil {
			return e
		}
		if e = store.LockNamedEntities(ctx, r.Scope); e != nil {
			return e
		}
		checked := searchReadTx{Tx: tx}
		current, e := checked.Get(ctx, colMemories, r.Scope, r.ID)
		if e != nil {
			return e
		}
		if !validKnowledgeEvidence(current, Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}, r.Scope) {
			return ErrConflict
		}
		if e = checkSources(ctx, checked, current); e != nil {
			return e
		}
		return recoverSummarySchedule(ctx, tx, store, current, set, r.ApprovedBy)
	})
	if err != nil {
		slog.Debug("person summary not queued", "memory", r.ID, "error", err)
	}
}

func approvedPeopleIn(ctx context.Context, store namedEntityStore, r Record) ([]string, error) {
	names := []string{}
	for _, e := range r.Entities {
		if e.Type == "person" {
			names = append(names, e.Name)
		}
	}
	for _, rel := range r.EntityRelations {
		names = append(names, rel.Source, rel.Target)
	}
	if len(names) == 0 {
		return nil, nil
	}
	g, err := loadEntityGraph(ctx, store, r.Scope)
	if err != nil {
		return nil, err
	}
	if g.truncated {
		return nil, ErrUnavailable
	}
	ids := []string{}
	for _, name := range names {
		if e, _ := g.resolve(name, "person"); e != nil && e.Type == "person" && !slices.Contains(ids, e.ID) {
			ids = append(ids, e.ID)
		}
	}
	return ids, nil
}
