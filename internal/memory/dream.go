package memory

// Dreaming: while a scope is idle, the worker applies the consolidation the AI
// already judged, without a person approving each item. It decides open
// maintenance proposals (a duplicate or a newer memory supersedes the other),
// settles classified review candidates (approve, reject a duplicate or a discard,
// approve a newer version and supersede what it replaces) and retires memories
// whose expiry passed. Nothing is deleted: every change is a status change that
// keeps the old row as history.
//
// Every change is logged on the run with the rows it touched (their earlier
// state, the revision it left them at), its reason and its confidence, so a
// person can undo one change or the whole run (dream_undo.go). A change the AI is
// not sure about, a contradiction with no known order, or one that would retire a
// memory the user stated, is skipped and logged instead.
//
// It is on by default (MEMORY_DREAM=auto); MEMORY_DREAM=manual returns to
// approving every item by hand. The time-limited AI review grant
// (MEMORY_REVIEW_POLICIES) does not gate it: that grant is for a reviewer
// calling the review API, and the dream runs inside the worker under this switch.
//
// Scheduling: every capture or proposal in a scope moves the scope's one dream
// job to idle-after-now (the user is still working), but never later than
// every-after-the-first-unprocessed-write (the schedule fallback for a scope that
// is never quiet). A run that hits its limits continues after the next idle
// window; one that skipped items looks again after every.

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
	dreamJobKind   = "dream"
	dreamRunStatus = "dream_run" // run reports live in jobs under this status, which the worker never claims
	dreamUndoneTag = "dream-undone"
	dreamRunsMax   = 50
	dreamScanMax   = 1000
)

// dreamGrants are the worker actions a dream needs (guide/server.md, MEMORY_DREAM).
var dreamGrants = []string{"transition:memory_candidates", "transition:memories", "read:maintenance_proposals", "write:maintenance_proposals"}

// DreamJob is the state of a scope's dream scheduler job.
type DreamJob struct {
	FirstActivity *time.Time `json:"first_activity,omitempty" bson:"first_activity,omitempty"`
	LastActivity  *time.Time `json:"last_activity,omitempty" bson:"last_activity,omitempty"`
	LastRun       *time.Time `json:"last_run,omitempty" bson:"last_run,omitempty"`
	Outcome       string     `json:"outcome,omitempty" bson:"outcome,omitempty"`
	// Skipped remembers what earlier runs left for a person, by target and revision, so a run does not
	// log it again until the item itself changes.
	Skipped map[string]int64 `json:"-" bson:"skipped,omitempty"`
	// Since is the start of the last run that finished every stage: the generative stages look at
	// memories changed after it.
	Since *time.Time `json:"-" bson:"since,omitempty"`
}

// DreamRun is one run's report: what it changed, what it skipped and why.
type DreamRun struct {
	Trigger    string        `json:"trigger" bson:"trigger"` // idle | schedule
	StartedAt  time.Time     `json:"started_at" bson:"started_at"`
	FinishedAt *time.Time    `json:"finished_at,omitempty" bson:"finished_at,omitempty"`
	Outcome    string        `json:"outcome" bson:"outcome"` // running | ok | item_limit | time_limit | denied:<action>
	Changes    []DreamChange `json:"changes" bson:"changes"`
	Skipped    []DreamSkip   `json:"skipped" bson:"skipped"`
	// SkippedMore counts new skips past the logged ones (MEMORY_DREAM_ITEMS of them).
	SkippedMore int `json:"skipped_more,omitempty" bson:"skipped_more,omitempty"`
	// Model is the dream model ("" when none is configured: the generative stages did not run);
	// ModelCalls counts the calls this run made (at most MEMORY_DREAM_MODEL_CALLS).
	Model      string `json:"model,omitempty" bson:"model,omitempty"`
	ModelCalls int    `json:"model_calls" bson:"model_calls"`
	seen       map[string]int64
	since      *time.Time // the previous run: generative stages look at memories changed after it
}

// DreamChange is one automatic change. Rows hold what undo needs; the rest is the readable summary.
type DreamChange struct {
	Op         string     `json:"op" bson:"op"` // approve | edit_approve | reject | supersede | expire | resolve | merge | insight | promote_skill
	Target     string     `json:"target" bson:"target"`
	Reason     string     `json:"reason" bson:"reason"`
	Confidence float64    `json:"confidence" bson:"confidence"`
	Before     string     `json:"before" bson:"before"` // status before ("" when the change created the row)
	After      string     `json:"after" bson:"after"`   // status after
	UndoneAt   *time.Time `json:"undone_at,omitempty" bson:"undone_at,omitempty"`
	UndoneBy   string     `json:"undone_by,omitempty" bson:"undone_by,omitempty"`
	Rows       []dreamRow `json:"-" bson:"rows"`
}

// dreamRow is one row a change wrote: its status before ("" when the change created it) and the revision
// the change left, read in the changing transaction. No text: reports live in jobs, which erasure skips.
type dreamRow struct {
	Collection    string `bson:"collection"`
	ID            string `bson:"id"`
	BeforeStatus  string `bson:"before_status,omitempty"`
	AfterRevision int64  `bson:"after_revision"`
	// BeforeConflict is the open conflict a resolved member was held in; undo holds it there again.
	BeforeConflict string `bson:"before_conflict,omitempty"`
	// BeforeActive is a skill header's active version before a promotion (Header marks such a row).
	Header       bool   `bson:"header,omitempty"`
	BeforeActive string `bson:"before_active,omitempty"`
}

// DreamSkip is an item the run left for a person, with why.
type DreamSkip struct {
	Target     string  `json:"target" bson:"target"`
	Reason     string  `json:"reason" bson:"reason"`
	Confidence float64 `json:"confidence,omitempty" bson:"confidence,omitempty"`
}

type dreamSettings struct {
	auto                   bool
	idle, every, timeLimit time.Duration
	items, modelCalls      int
	minConfidence          float64
}

type dreamRunKey struct{}

// dreamRunning reports whether ctx belongs to a dream run (set only by runDream, never by a request).
func dreamRunning(ctx context.Context) bool {
	id, _ := ctx.Value(dreamRunKey{}).(string)
	return id != ""
}

// dreamConfig reads MEMORY_DREAM (auto by default, manual turns it off), MEMORY_DREAM_IDLE (30m),
// MEMORY_DREAM_EVERY (24h), MEMORY_DREAM_ITEMS (50 changes and 50 skips per run), MEMORY_DREAM_TIME
// (60s, at most 80s so a run ends inside one worker iteration), MEMORY_DREAM_MIN_CONFIDENCE (0.7) and
// MEMORY_DREAM_MODEL_CALLS (12 model calls per run, 0 turns the generative stages off). Invalid values
// keep the default.
func dreamConfig() dreamSettings {
	set := dreamSettings{auto: os.Getenv("MEMORY_DREAM") != "manual", idle: 30 * time.Minute, every: 24 * time.Hour, timeLimit: time.Minute, items: 50, modelCalls: 12, minConfidence: 0.7}
	envDuration := func(name string, low, high time.Duration, into *time.Duration) {
		if d, err := time.ParseDuration(os.Getenv(name)); err == nil && d >= low && d <= high {
			*into = d
		}
	}
	envDuration("MEMORY_DREAM_IDLE", time.Minute, 24*time.Hour, &set.idle)
	envDuration("MEMORY_DREAM_EVERY", time.Hour, 30*24*time.Hour, &set.every)
	envDuration("MEMORY_DREAM_TIME", time.Second, 80*time.Second, &set.timeLimit)
	if n, err := strconv.Atoi(os.Getenv("MEMORY_DREAM_ITEMS")); err == nil && n >= 1 && n <= 500 {
		set.items = n
	}
	if f, err := strconv.ParseFloat(os.Getenv("MEMORY_DREAM_MIN_CONFIDENCE"), 64); err == nil && f >= 0.5 && f <= 1 {
		set.minConfidence = f
	}
	if n, err := strconv.Atoi(os.Getenv("MEMORY_DREAM_MODEL_CALLS")); err == nil && n >= 0 && n <= 100 {
		set.modelCalls = n
	}
	return set
}

// dreamReview: an automated review from inside a dream run, by the worker (a service principal). The
// time-limited review grant (memoryReviewerAllowed) is not asked: MEMORY_DREAM governs the dream.
func dreamReview(ctx context.Context, review *CandidateReviewInput, d Decision) bool {
	return review.Automated && d.Role == roleService && dreamRunning(ctx) && dreamConfig().auto
}

// reviewerMayDecide: an explicit review by a person, or an automated one from the worker's dream run.
func reviewerMayDecide(ctx context.Context, review *CandidateReviewInput, d Decision) bool {
	return dreamReview(ctx, review, d) || !review.Automated && d.Role == roleHuman
}

func dreamJobID(scope Scope) string { return "dream-" + ContentHash(structuredText(scope)) }

// noteDreamActivity moves the scope's dream past the next idle window. Best effort: the write that
// called it never fails on it. Activity while a run holds the lease is picked up when the run ends.
func (s *Service) noteDreamActivity(ctx context.Context, scope Scope) {
	set := dreamConfig()
	if !set.auto || s.Repo == nil || ctx.Err() != nil || dreamRunning(ctx) {
		return
	}
	queue := func() error {
		return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
			return queueDream(ctx, tx, scope, set, time.Now().UTC())
		})
	}
	err := queue()
	if errors.Is(err, ErrConflict) {
		err = queue() // two first writes raced to insert the job
	}
	if err != nil {
		slog.Debug("dream not queued", "error", err)
	}
}

func queueDream(ctx context.Context, tx Tx, scope Scope, set dreamSettings, now time.Time) error {
	id := dreamJobID(scope)
	job, err := tx.Get(ctx, colJobs, scope, id)
	if errors.Is(err, ErrNotFound) {
		job = Record{ID: id, Scope: scope, Kind: dreamJobKind, Status: JobStatusDone, CreatedAt: now, Dream: &DreamJob{}}
	} else if err != nil {
		return err
	}
	if job.Kind != dreamJobKind || job.Dream == nil || job.Status == JobStatusRunning {
		return nil
	}
	state := *job.Dream
	if job.Status != JobStatusPending || state.FirstActivity == nil {
		state.FirstActivity = &now
		job.Status, job.Attempts = JobStatusPending, 0
	}
	state.LastActivity = &now
	at := now.Add(set.idle)
	if late := state.FirstActivity.Add(set.every); late.Before(at) {
		at = late // never quiet: the schedule runs it anyway
	}
	job.RunAt, job.Dream, job.UpdatedAt = &at, &state, now
	if job.Revision == 0 {
		job.Revision = 1
		return tx.Insert(ctx, colJobs, job)
	}
	prev := job.Revision
	job.Revision++
	return tx.Replace(ctx, colJobs, job, prev)
}

// runDream runs one dream with the worker's credentials and stores the job: pending again after the
// next idle window when a stage hit its limit or new work arrived, after every when it left new items
// for a person, else done.
func (s *Service) runDream(ctx context.Context, workerToken string, job Record) error {
	set := dreamConfig()
	state := DreamJob{}
	if job.Dream != nil {
		state = *job.Dream
	}
	started := time.Now().UTC()
	run := &DreamRun{Outcome: "manual", StartedAt: started, seen: state.Skipped, since: state.Since}
	if set.auto {
		run.Trigger = "idle"
		if state.LastActivity != nil && state.LastActivity.Add(set.idle).After(started) {
			run.Trigger = "schedule"
		}
		var err error
		if run, err = s.dream(ctx, workerToken, job.Scope, set, run); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	state.LastRun, state.Outcome, state.FirstActivity, state.Skipped = &now, run.Outcome, nil, run.seen
	if run.Outcome == "ok" {
		state.Since = &started // the generative stages saw everything changed before this run began
	}
	next, due := now.Add(set.every), set.auto && len(run.Skipped) > 0
	if set.auto && (run.Outcome == "item_limit" || run.Outcome == "time_limit" || run.Outcome == "model_limit" || s.newCandidateSince(ctx, job.Scope, started)) {
		next, due = now.Add(set.idle), true
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
		if due {
			state.FirstActivity = &now
		} else {
			cur.Status, cur.RunAt = JobStatusDone, nil
		}
		cur.Attempts, cur.Dream = 0, &state
		prev := cur.Revision
		cur.Revision++
		return tx.Replace(ctx, colJobs, cur, prev)
	})
}

// newCandidateSince: a candidate waits that was written or reclassified after the run began.
func (s *Service) newCandidateSince(ctx context.Context, scope Scope, at time.Time) bool {
	rows, err := s.Repo.ListRecordsByStatus(ctx, scope, "memory_candidates", "candidate", 1)
	return err == nil && len(rows) == 1 && rows[0].UpdatedAt.After(at)
}

// dreamer is one run in progress.
type dreamer struct {
	s         *Service
	token     string
	scope     Scope
	set       dreamSettings
	principal string
	runID     string
	run       *DreamRun
	known     map[string]int64 // skips earlier runs logged
	deadline  time.Time
	stageFrom int        // changes before the current stage
	stageEnd  time.Time  // the current stage's share of the time limit
	limit     string     // the first limit a stage hit
	since     *time.Time // the previous run, nil before the first
}

// dream checks the worker's grants, stores the run report and applies what it may. Each stage (pair
// proposals, open contradictions, review entries, merging paraphrases, insights, skill promotion,
// expiry) gets its own item limit and an even share of the time left, so a full first stage never
// starves the others. The generative stages share the run's model calls (dream_model.go).
func (s *Service) dream(ctx context.Context, token string, scope Scope, set dreamSettings, run *DreamRun) (*DreamRun, error) {
	run.Outcome, run.Changes, run.Skipped = "running", []DreamChange{}, []DreamSkip{}
	d := &dreamer{s: s, token: token, scope: scope, set: set, run: run, known: run.seen, since: run.since, runID: "dream-run-" + ID(), deadline: run.StartedAt.Add(set.timeLimit)}
	run.seen = map[string]int64{}
	for _, action := range dreamGrants {
		who, err := s.authorize(ctx, token, action, scope, "", "")
		if errors.Is(err, ErrDenied) || err == nil && who.Role != roleService {
			slog.Warn("dream: the worker token lacks a grant; see guide/server.md", "action", action)
			run.Outcome = "denied:" + action
			return run, d.save(ctx, true)
		}
		if err != nil {
			return nil, err
		}
		d.principal = who.PrincipalID
	}
	if err := d.save(ctx, true); err != nil {
		return nil, err
	}
	// A worker without db:read sees no rows at all and would report an empty ok run.
	if _, err := s.Repo.Get(ctx, colJobs, scope, d.runID); errors.Is(err, ErrNotFound) {
		slog.Warn("dream: the worker cannot read the rows it writes; grant its principal db:read (guide/server.md)", "scope", scope.ProjectID)
		return nil, errors.Join(ErrDenied, errors.New("dream worker lacks db:read"))
	} else if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, dreamRunKey{}, d.runID)
	if d.modelReady() {
		run.Model = s.DreamModel.Model
	}
	steps := []func(context.Context) error{d.maintenance, d.contradictions, d.candidates, d.consolidate, d.insights, d.skills, d.expire}
	for i, step := range steps {
		now := time.Now()
		d.stageFrom, d.stageEnd = len(run.Changes), now.Add(d.deadline.Sub(now)/time.Duration(len(steps)-i))
		if err := step(ctx); err != nil {
			return nil, err
		}
	}
	run.Outcome = "ok"
	if d.limit != "" {
		run.Outcome = d.limit
	}
	now := time.Now().UTC()
	run.FinishedAt = &now
	return run, d.save(ctx, false)
}

// stop says whether the current stage reached its item limit or its time; skips never count.
func (d *dreamer) stop() bool {
	limit := ""
	switch {
	case len(d.run.Changes)-d.stageFrom >= d.set.items:
		limit = "item_limit"
	case time.Now().After(d.stageEnd):
		limit = "time_limit"
	default:
		return false
	}
	d.hit(limit)
	return true
}

// hit remembers the first limit the run reached: its outcome.
func (d *dreamer) hit(limit string) {
	if d.limit == "" {
		d.limit = limit
	}
}

// save writes the run report; it is written after every change so a crash leaves what was applied logged.
// The first save also drops the scope's oldest reports past dreamRunsMax.
func (d *dreamer) save(ctx context.Context, first bool) error {
	var old []Record
	if first {
		runs, err := d.s.Repo.ListRecordsByStatus(ctx, d.scope, colJobs, dreamRunStatus, dreamScanMax)
		if err != nil {
			return err
		}
		slices.SortFunc(runs, func(a, b Record) int { return b.CreatedAt.Compare(a.CreatedAt) })
		if len(runs) >= dreamRunsMax {
			old = runs[dreamRunsMax-1:]
		}
	}
	return d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		now := time.Now().UTC()
		if first {
			for _, r := range old {
				if err := tx.Delete(ctx, colJobs, d.scope, r.ID, r.Revision); err != nil && !errors.Is(err, ErrNotFound) {
					return err
				}
			}
			return tx.Insert(ctx, colJobs, Record{ID: d.runID, Scope: d.scope, Kind: dreamJobKind, Status: dreamRunStatus, Revision: 1, CreatedAt: d.run.StartedAt, UpdatedAt: now, CreatedBy: d.principal, CreatedByType: roleService, DreamRun: d.run})
		}
		cur, err := tx.Get(ctx, colJobs, d.scope, d.runID)
		if err != nil {
			return err
		}
		prev := cur.Revision
		cur.DreamRun, cur.UpdatedAt, cur.Revision = d.run, now, prev+1
		return tx.Replace(ctx, colJobs, cur, prev)
	})
}

// skipped: an earlier run already left this item, at this revision, for a person. It stays remembered.
func (d *dreamer) skipped(target string, revision int64) bool {
	if rev, ok := d.known[target]; ok && rev == revision {
		d.run.seen[target] = revision
		return true
	}
	return false
}

// skip leaves an item for a person: logged (up to the item limit, then counted) and remembered.
func (d *dreamer) skip(target string, revision int64, reason string, confidence float64) {
	if len(d.run.seen) < 2*dreamScanMax {
		d.run.seen[target] = revision
	}
	if len(d.run.Skipped) >= d.set.items {
		d.run.SkippedMore++
		return
	}
	d.run.Skipped = append(d.run.Skipped, DreamSkip{Target: target, Reason: reason, Confidence: confidence})
}

// snapshot reads the rows a change is about to write: their status for undo ("" for a row the change
// creates) and the records themselves for the run's checks, which are not stored.
func (d *dreamer) snapshot(ctx context.Context, refs ...Ref) ([]dreamRow, []*Record, error) {
	rows, recs := make([]dreamRow, len(refs)), make([]*Record, len(refs))
	for i, ref := range refs {
		rows[i] = dreamRow{Collection: ref.Collection, ID: ref.ID}
		r, err := d.s.Repo.Get(ctx, ref.Collection, d.scope, ref.ID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		rows[i].BeforeStatus, recs[i] = r.Status, &r
	}
	return rows, recs, nil
}

// afterRevisions reads the revisions a change left, inside the changing transaction, so a later edit
// is never taken for the dream's own.
func afterRevisions(ctx context.Context, tx Tx, scope Scope, rows []dreamRow) error {
	for i := range rows {
		r, err := tx.Get(ctx, rows[i].Collection, scope, rows[i].ID)
		if err != nil {
			return err
		}
		rows[i].AfterRevision = r.Revision
	}
	return nil
}

// record logs an applied change (rows carry their after revisions) and saves the report.
func (d *dreamer) record(ctx context.Context, c DreamChange, rows []dreamRow) error {
	if c.Before == "" {
		c.Before = rows[0].BeforeStatus
	}
	c.Rows = rows
	d.run.Changes = append(d.run.Changes, c)
	return d.save(ctx, false)
}

// changed reports an error that means the item moved under the run: skip it, never retry the run.
func changed(err error) bool {
	return errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, errMemoryChanged) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrDenied)
}

// userStated: the user said it in their own words. The dream never retires one for a memory that is
// only an inference. (Approved-by-a-person is not enough: until now every memory was.)
func userStated(r Record) bool {
	return r.MemoryContext != nil && r.MemoryContext.EvidenceKind == "user_statement"
}

// maintenanceConfidence is how sure a nightly pair proposal is, by its reason code.
var maintenanceConfidence = map[string]float64{"same_meaning": 0.9, "newer_replaces_older": 0.8, "more_specific": 0.75}

// maintenance accepts open pair proposals: the kept memory supersedes the retired one, as a person's accept does.
func (d *dreamer) maintenance(ctx context.Context) error {
	rows, err := d.s.Repo.ListRecordsByStatus(ctx, d.scope, colMaintenance, maintenanceOpen, maintenanceListLimit)
	if err != nil {
		return err
	}
	for _, r := range rows {
		target := colMaintenance + "/" + r.ID
		if r.Maintenance == nil || slices.Contains(r.Tags, dreamUndoneTag) || d.skipped(target, r.Revision) {
			continue
		}
		if d.stop() {
			return nil
		}
		if agreementOnly(*r.Maintenance) {
			d.skip(target, r.Revision, "needs_person:"+r.Maintenance.Type, 0)
			continue
		}
		if err := d.acceptPair(ctx, r, *r.Maintenance); err != nil {
			return err
		}
	}
	return nil
}

func (d *dreamer) acceptPair(ctx context.Context, r Record, p MaintenanceProposal) error {
	target, confidence := colMaintenance+"/"+r.ID, maintenanceConfidence[p.Reason]
	if confidence < d.set.minConfidence {
		d.skip(target, r.Revision, "low_confidence", confidence)
		return nil
	}
	rows, recs, err := d.snapshot(ctx, p.Retire, p.Keep, Ref{Collection: colMaintenance, ID: r.ID})
	if err != nil {
		return err
	}
	if recs[0] == nil || recs[1] == nil {
		d.skip(target, r.Revision, "memory_missing", confidence)
		return nil
	}
	if userStated(*recs[0]) && !userStated(*recs[1]) {
		d.skip(target, r.Revision, "would_retire_user_statement", confidence)
		return nil
	}
	mark := supersedeInput(d.scope, r, "accept")
	err = d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := decideInTx(ctx, tx, d.scope, r, &mark, nil, nil, "accepted", "accept", d.principal, roleService); err != nil {
			return err
		}
		return afterRevisions(ctx, tx, d.scope, rows)
	})
	if changed(err) {
		d.skip(target, r.Revision, "changed", confidence)
		return nil
	} else if err != nil {
		return err
	}
	return d.record(ctx, DreamChange{Op: "supersede", Target: colMemories + "/" + p.Retire.ID, Reason: p.Type + ":" + p.Reason + " kept " + p.Keep.ID, Confidence: confidence, After: "superseded"}, rows)
}

// candidateJudgement is what the dream does with one classified candidate.
type candidateJudgement struct {
	action     string // approve | edit_approve | reject | supersede | skip (contradiction until decided)
	content    string
	reason     string // a code and ids only: reports keep no memory text
	confidence float64
	op         string   // the logged op when it differs from action (merge, insight)
	replaces   []string // memories the approved one supersedes
}

var riskConfidence = map[string]float64{"low": 0.9, "medium": 0.7, "high": 0.3}

// judgeCandidate turns the classifier's stored advice into an action. A failed meaning check lowers
// confidence; a contradiction with no known order always waits for a person.
func judgeCandidate(c Record, minConfidence float64) candidateJudgement {
	cl := c.Classification
	if cl == nil {
		return candidateJudgement{action: "skip", reason: "not_classified"}
	}
	j := candidateJudgement{confidence: riskConfidence[cl.Risk]}
	for _, check := range cl.Semantic {
		if !check.OK {
			j.confidence -= 0.2
			break
		}
	}
	verdict := ""
	if cl.Verdict != nil {
		verdict = cl.Verdict.Action
	}
	switch {
	case cl.Relation == "contradiction":
		j.action, j.reason = "contradiction", "contradicts "+cl.DupHint // dream_contradiction.go decides which is current
	case cl.Relation == "duplicate" || verdict == "merge" && cl.DupHint != "":
		j.action, j.reason = "reject", "duplicate of "+cl.DupHint
	case cl.Relation == "newer_version":
		j.action, j.reason, j.replaces = "supersede", "newer version of "+cl.DupHint, []string{cl.DupHint}
	case verdict == "discard":
		j.action, j.reason = "reject", "not_worth_keeping"
	case verdict == "fix" && cl.Verdict.Text != "":
		j.action, j.content, j.reason = "edit_approve", cl.Verdict.Text, "reworded"
	case verdict == "keep" || verdict == "":
		j.action, j.reason = "approve", "classified "+cl.Risk+" risk"
	default:
		j.action, j.reason = "skip", "no_verdict"
	}
	if j.action != "skip" && j.action != "contradiction" && j.confidence < minConfidence {
		j.action, j.reason = "skip", "low_confidence"
	}
	return j
}

// candidates settles classified review candidates through the review path a person uses.
func (d *dreamer) candidates(ctx context.Context) error {
	rows, err := d.s.Repo.ListRecordsByStatus(ctx, d.scope, "memory_candidates", "candidate", dreamScanMax)
	if err != nil {
		return err
	}
	for _, c := range rows {
		target := "memory_candidates/" + c.ID
		// Lessons wait as candidates for skill promotion (dream_skill.go); held ones for their conflict.
		if slices.Contains(c.Tags, dreamUndoneTag) || c.Kind == "lesson" || c.ConflictID != "" || d.skipped(target, c.Revision) {
			continue
		}
		if d.stop() {
			return nil
		}
		j := judgeCandidate(c, d.set.minConfidence)
		if j.action == "contradiction" {
			j = d.judgeContradiction(ctx, c, j)
		}
		if j.action == "skip" {
			d.skip(target, c.Revision, j.reason, j.confidence)
			continue
		}
		if err := d.settle(ctx, c, j); err != nil {
			return err
		}
	}
	return nil
}

var dreamAfter = map[string]string{"approve": "active", "edit_approve": "active", "supersede": "active", "reject": "rejected"}

// settle applies one judgement through the review path a person uses; an approval that replaces
// memories then supersedes each of them.
func (d *dreamer) settle(ctx context.Context, c Record, j candidateJudgement) error {
	target := "memory_candidates/" + c.ID
	refs := []Ref{{Collection: "memory_candidates", ID: c.ID}}
	if j.action != "reject" {
		refs = []Ref{{Collection: colMemories, ID: c.ID}} // the approved memory takes the candidate's id
	}
	for _, id := range j.replaces {
		refs = append(refs, Ref{Collection: colMemories, ID: id})
	}
	rows, recs, err := d.snapshot(ctx, refs...)
	if err != nil {
		return err
	}
	if reason := retireRefusal(c, recs[1:]); reason != "" {
		d.skip(target, c.Revision, reason, j.confidence)
		return nil
	}
	in := CandidateReviewInput{Automated: true, Scope: d.scope, ExpectedRevision: c.Revision, Action: j.action, Content: j.content, OperationID: d.runID + "." + strconv.Itoa(len(d.run.Changes)+len(d.run.Skipped)+d.run.SkippedMore)}
	switch j.action {
	case "reject":
		in.Reason = "dream: " + j.reason
	case "supersede":
		in.Action = "approve"
	}
	settled, err := d.s.ReviewCandidate(ctx, d.token, c.ID, in)
	if changed(err) || errors.Is(err, ErrSecretDetected) {
		d.skip(target, c.Revision, "review_refused", j.confidence)
		return nil
	} else if err != nil {
		return err
	}
	rows[0].AfterRevision = settled.Revision // the row the review wrote, as its transaction left it
	if len(j.replaces) > 0 {
		if rows, err = d.replaceAll(ctx, settled, recs[1:], j, rows); err != nil || rows == nil {
			return err
		}
	}
	op := j.op
	if op == "" {
		op = j.action
	}
	return d.record(ctx, DreamChange{Op: op, Target: target, Reason: j.reason, Confidence: j.confidence, Before: c.Status, After: dreamAfter[j.action]}, rows)
}

// retireRefusal: why the memories an approval would replace must stay ("" when none). The dream
// never retires a memory the user stated for one that is not.
func retireRefusal(c Record, olds []*Record) string {
	for _, old := range olds {
		if old == nil || old.Status != statusActive || old.ConflictID != "" {
			return "replaced_memory_gone"
		}
		if userStated(*old) && !userStated(c) {
			return "would_retire_user_statement"
		}
	}
	return ""
}

// replaceAll supersedes every replaced memory for next in one transaction. When that is refused, a
// newer version's approval stands with its supersession skipped; a merge or an insight, which only
// exists to replace, is withdrawn again (rows nil: nothing to log).
func (d *dreamer) replaceAll(ctx context.Context, next Record, olds []*Record, j candidateJudgement, rows []dreamRow) ([]dreamRow, error) {
	err := d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		for _, old := range olds {
			cur, err := tx.Get(ctx, colMemories, d.scope, next.ID)
			if err != nil {
				return err
			}
			mark := ConflictInput{Scope: d.scope, Memories: []Ref{{Collection: colMemories, ID: old.ID, Revision: old.Revision}, {Collection: colMemories, ID: cur.ID, Revision: cur.Revision}},
				Action: "mark", Hold: "both", Reason: "dream: " + j.reason}
			if err = supersedeInTx(ctx, tx, mark, d.principal, roleService); err != nil {
				return err
			}
		}
		return afterRevisions(ctx, tx, d.scope, rows)
	})
	if !changed(err) {
		return rows, err
	}
	if j.op == "" {
		d.skip(colMemories+"/"+olds[0].ID, olds[0].Revision, "supersede_refused", j.confidence) // the approval stands and is logged
		return rows[:1], nil
	}
	d.skip("memory_candidates/"+next.ID, 0, j.op+"_refused", j.confidence)
	return nil, d.withdraw(ctx, next.ID)
}

// withdraw rejects a memory the run approved a moment ago, when what it was for was refused.
func (d *dreamer) withdraw(ctx context.Context, id string) error {
	err := d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cur, err := tx.Get(ctx, colMemories, d.scope, id)
		if err != nil {
			return err
		}
		prev := cur.Revision
		cur.Status, cur.Revision, cur.UpdatedAt = "rejected", prev+1, time.Now().UTC()
		if err = tx.Replace(ctx, colMemories, cur, prev); err != nil {
			return err
		}
		return audit(ctx, tx, d.scope, d.principal, "dream:withdraw:memories", id)
	})
	if changed(err) {
		return nil
	}
	return err
}

// expire retires active memories whose expiry passed: recall already leaves them out, the status says so.
// shortcut: reads the newest dreamScanMax active memories; add an expires_at query if a scope outgrows it.
func (d *dreamer) expire(ctx context.Context) error {
	rows, err := d.s.Repo.ListRecordsByStatus(ctx, d.scope, colMemories, statusActive, dreamScanMax)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, r := range rows {
		if r.ExpiresAt == nil || r.ExpiresAt.After(now) || r.ConflictID != "" || slices.Contains(r.Tags, dreamUndoneTag) {
			continue
		}
		if d.stop() {
			return nil
		}
		if err := d.expireOne(ctx, r, now); err != nil {
			return err
		}
	}
	return nil
}

// expireOne moves one expired memory to expired and logs it; a memory changed meanwhile is skipped.
func (d *dreamer) expireOne(ctx context.Context, r Record, now time.Time) error {
	err := d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		cur, err := tx.Get(ctx, colMemories, d.scope, r.ID)
		if err != nil {
			return err
		}
		if cur.Revision != r.Revision || cur.Status != statusActive {
			return ErrConflict
		}
		cur.Status, cur.Revision, cur.UpdatedAt = "expired", r.Revision+1, now
		if err = tx.Replace(ctx, colMemories, cur, r.Revision); err != nil {
			return err
		}
		return audit(ctx, tx, d.scope, d.principal, "dream:expire:memories", r.ID)
	})
	if changed(err) {
		d.skip(colMemories+"/"+r.ID, r.Revision, "changed", 1)
		return nil
	} else if err != nil {
		return err
	}
	row := dreamRow{Collection: colMemories, ID: r.ID, BeforeStatus: r.Status, AfterRevision: r.Revision + 1}
	return d.record(ctx, DreamChange{Op: "expire", Target: colMemories + "/" + r.ID, Reason: "expired " + r.ExpiresAt.Format(time.RFC3339), Confidence: 1, After: "expired"}, []dreamRow{row})
}
