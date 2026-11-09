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
}

// DreamRun is one run's report: what it changed, what it skipped and why.
type DreamRun struct {
	Trigger    string        `json:"trigger" bson:"trigger"` // idle | schedule
	StartedAt  time.Time     `json:"started_at" bson:"started_at"`
	FinishedAt *time.Time    `json:"finished_at,omitempty" bson:"finished_at,omitempty"`
	Outcome    string        `json:"outcome" bson:"outcome"` // running | ok | item_limit | time_limit | denied:<action>
	Changes    []DreamChange `json:"changes" bson:"changes"`
	Skipped    []DreamSkip   `json:"skipped" bson:"skipped"`
}

// DreamChange is one automatic change. Rows hold what undo needs; the rest is the readable summary.
type DreamChange struct {
	Op         string     `json:"op" bson:"op"` // approve | edit_approve | reject | supersede | expire
	Target     string     `json:"target" bson:"target"`
	Reason     string     `json:"reason" bson:"reason"`
	Confidence float64    `json:"confidence" bson:"confidence"`
	Before     string     `json:"before" bson:"before"` // status and text before
	After      string     `json:"after" bson:"after"`
	UndoneAt   *time.Time `json:"undone_at,omitempty" bson:"undone_at,omitempty"`
	UndoneBy   string     `json:"undone_by,omitempty" bson:"undone_by,omitempty"`
	Rows       []dreamRow `json:"-" bson:"rows"`
}

// dreamRow is one row a change wrote: its earlier state (nil when the change created it) and the revision it left.
type dreamRow struct {
	Collection    string  `bson:"collection"`
	ID            string  `bson:"id"`
	Before        *Record `bson:"before,omitempty"`
	AfterRevision int64   `bson:"after_revision"`
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
	items                  int
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
// (60s, at most 80s so a run ends inside one worker iteration) and MEMORY_DREAM_MIN_CONFIDENCE (0.7).
// Invalid values keep the default.
func dreamConfig() dreamSettings {
	set := dreamSettings{auto: os.Getenv("MEMORY_DREAM") != "manual", idle: 30 * time.Minute, every: 24 * time.Hour, timeLimit: time.Minute, items: 50, minConfidence: 0.7}
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
// next idle window when work remains or new work arrived, after every when it skipped items, else done.
func (s *Service) runDream(ctx context.Context, workerToken string, job Record) error {
	set := dreamConfig()
	state := DreamJob{}
	if job.Dream != nil {
		state = *job.Dream
	}
	started := time.Now().UTC()
	run := &DreamRun{Outcome: "manual", StartedAt: started}
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
	state.LastRun, state.Outcome, state.FirstActivity = &now, run.Outcome, nil
	next, due := now.Add(set.every), set.auto && len(run.Skipped) > 0
	if set.auto && (run.Outcome == "item_limit" || run.Outcome == "time_limit" || s.newCandidateSince(ctx, job.Scope, started)) {
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
	deadline  time.Time
}

// dream checks the worker's grants, stores the run report and applies what it may, within the run's limits.
func (s *Service) dream(ctx context.Context, token string, scope Scope, set dreamSettings, run *DreamRun) (*DreamRun, error) {
	run.Outcome, run.Changes, run.Skipped = "running", []DreamChange{}, []DreamSkip{}
	d := &dreamer{s: s, token: token, scope: scope, set: set, run: run, runID: "dream-run-" + ID(), deadline: run.StartedAt.Add(set.timeLimit)}
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
	ctx = context.WithValue(ctx, dreamRunKey{}, d.runID)
	for _, step := range []func(context.Context) error{d.maintenance, d.candidates, d.expire} {
		if err := step(ctx); err != nil {
			return nil, err
		}
	}
	if run.Outcome == "running" {
		run.Outcome = "ok"
	}
	now := time.Now().UTC()
	run.FinishedAt = &now
	return run, d.save(ctx, false)
}

// stop says whether the run reached a limit, and names it in the outcome.
func (d *dreamer) stop() bool {
	switch {
	case d.run.Outcome != "running":
		return true
	case len(d.run.Changes) >= d.set.items || len(d.run.Skipped) >= d.set.items:
		d.run.Outcome = "item_limit"
	case time.Now().After(d.deadline):
		d.run.Outcome = "time_limit"
	default:
		return false
	}
	return true
}

// save writes the run report; it is written after every change so a crash leaves what was applied logged.
func (d *dreamer) save(ctx context.Context, first bool) error {
	return d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		now := time.Now().UTC()
		r := Record{ID: d.runID, Scope: d.scope, Kind: dreamJobKind, Status: dreamRunStatus, Revision: 1, CreatedAt: d.run.StartedAt, UpdatedAt: now, CreatedBy: d.principal, CreatedByType: roleService, DreamRun: d.run}
		if first {
			return tx.Insert(ctx, colJobs, r)
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

func (d *dreamer) skip(target, reason string, confidence float64) {
	d.run.Skipped = append(d.run.Skipped, DreamSkip{Target: target, Reason: reason, Confidence: confidence})
}

// snapshot reads the rows a change is about to write; a missing row is one the change creates.
func (d *dreamer) snapshot(ctx context.Context, refs ...Ref) ([]dreamRow, error) {
	rows := make([]dreamRow, len(refs))
	for i, ref := range refs {
		rows[i] = dreamRow{Collection: ref.Collection, ID: ref.ID}
		r, err := d.s.Repo.Get(ctx, ref.Collection, d.scope, ref.ID)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		r.Embedding = nil // undo keeps the row's current vector
		rows[i].Before = &r
	}
	return rows, nil
}

// record logs an applied change with the revisions it left, and saves the report.
func (d *dreamer) record(ctx context.Context, c DreamChange, rows []dreamRow) error {
	for i := range rows {
		r, err := d.s.Repo.Get(ctx, rows[i].Collection, d.scope, rows[i].ID)
		if err != nil {
			return err
		}
		rows[i].AfterRevision = r.Revision
		if i == 0 {
			c.After = r.Status + ": " + cutUTF8(r.Text, 300)
		}
	}
	if rows[0].Before != nil && c.Before == "" {
		c.Before = rows[0].Before.Status + ": " + cutUTF8(rows[0].Before.Text, 300)
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
		if d.stop() {
			return nil
		}
		p := r.Maintenance
		if p == nil || slices.Contains(r.Tags, dreamUndoneTag) {
			continue
		}
		if agreementOnly(*p) {
			d.skip(colMaintenance+"/"+r.ID, "needs_person:"+p.Type, 0)
			continue
		}
		if err := d.acceptPair(ctx, r, *p); err != nil {
			return err
		}
	}
	return nil
}

func (d *dreamer) acceptPair(ctx context.Context, r Record, p MaintenanceProposal) error {
	target, confidence := colMaintenance+"/"+r.ID, maintenanceConfidence[p.Reason]
	if confidence < d.set.minConfidence {
		d.skip(target, "low_confidence", confidence)
		return nil
	}
	rows, err := d.snapshot(ctx, p.Retire, p.Keep, Ref{Collection: colMaintenance, ID: r.ID})
	if err != nil {
		return err
	}
	if rows[0].Before == nil || rows[1].Before == nil {
		d.skip(target, "memory_missing", confidence)
		return nil
	}
	if userStated(*rows[0].Before) && !userStated(*rows[1].Before) {
		d.skip(target, "would_retire_user_statement", confidence)
		return nil
	}
	mark := supersedeInput(d.scope, r, "accept")
	if _, err = d.s.decideTx(ctx, d.scope, r, &mark, nil, nil, "accepted", "accept", d.principal, roleService); changed(err) {
		d.skip(target, "changed", confidence)
		return nil
	} else if err != nil {
		return err
	}
	return d.record(ctx, DreamChange{Op: "supersede", Target: colMemories + "/" + p.Retire.ID, Reason: p.Type + ":" + p.Reason + " kept " + p.Keep.ID, Confidence: confidence}, rows)
}

// candidateJudgement is what the dream does with one classified candidate.
type candidateJudgement struct {
	action     string // approve | edit_approve | reject | supersede | skip
	content    string
	reason     string
	confidence float64
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
		j.action, j.reason = "skip", "contradiction_needs_person"
	case cl.Relation == "duplicate" || verdict == "merge" && cl.DupHint != "":
		j.action, j.reason = "reject", "duplicate of "+cl.DupHint
	case cl.Relation == "newer_version":
		j.action, j.reason = "supersede", "newer version of "+cl.DupHint
	case verdict == "discard":
		j.action, j.reason = "reject", "not worth keeping: "+cl.Verdict.Reason
	case verdict == "fix" && cl.Verdict.Text != "":
		j.action, j.content, j.reason = "edit_approve", cl.Verdict.Text, "reworded: "+cl.Verdict.Reason
	case verdict == "keep" || verdict == "":
		j.action, j.reason = "approve", "classified "+cl.Risk+" risk"
	default:
		j.action, j.reason = "skip", "no_verdict"
	}
	if j.action != "skip" && j.confidence < minConfidence {
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
		if d.stop() {
			return nil
		}
		if slices.Contains(c.Tags, dreamUndoneTag) {
			continue
		}
		j := judgeCandidate(c, d.set.minConfidence)
		if j.action == "skip" {
			d.skip("memory_candidates/"+c.ID, j.reason, j.confidence)
			continue
		}
		if err := d.settle(ctx, c, j); err != nil {
			return err
		}
	}
	return nil
}

func (d *dreamer) settle(ctx context.Context, c Record, j candidateJudgement) error {
	target := "memory_candidates/" + c.ID
	refs := []Ref{{Collection: "memory_candidates", ID: c.ID}}
	if j.action != "reject" {
		refs = []Ref{{Collection: colMemories, ID: c.ID}} // the approved memory takes the candidate's id
	}
	var old *Record
	if j.action == "supersede" {
		refs = append(refs, Ref{Collection: colMemories, ID: c.Classification.DupHint})
	}
	rows, err := d.snapshot(ctx, refs...)
	if err != nil {
		return err
	}
	if j.action == "supersede" {
		if old = rows[1].Before; old == nil || old.Status != statusActive {
			d.skip(target, "replaced_memory_gone", j.confidence)
			return nil
		}
		if userStated(*old) && !userStated(c) {
			d.skip(target, "would_retire_user_statement", j.confidence)
			return nil
		}
	}
	in := CandidateReviewInput{Automated: true, Scope: d.scope, ExpectedRevision: c.Revision, Action: j.action, Content: j.content, OperationID: d.runID + "." + strconv.Itoa(len(d.run.Changes)+len(d.run.Skipped))}
	switch j.action {
	case "reject":
		in.Reason = "dream: " + j.reason
	case "supersede":
		in.Action = "approve"
	}
	approved, err := d.s.ReviewCandidate(ctx, d.token, c.ID, in)
	if changed(err) || errors.Is(err, ErrSecretDetected) {
		d.skip(target, "review_refused", j.confidence)
		return nil
	} else if err != nil {
		return err
	}
	if old != nil {
		if err = d.supersede(ctx, *old, approved, j.reason); changed(err) {
			d.skip(colMemories+"/"+old.ID, "supersede_refused", j.confidence) // the approval stands and is logged below
			rows = rows[:1]
		} else if err != nil {
			return err
		}
	}
	return d.record(ctx, DreamChange{Op: j.action, Target: target, Reason: j.reason, Confidence: j.confidence, Before: c.Status + ": " + cutUTF8(c.Text, 300)}, rows)
}

// supersede retires old for next through the conflict path, as a person's accept of a pair does.
func (d *dreamer) supersede(ctx context.Context, old, next Record, reason string) error {
	mark := ConflictInput{Scope: d.scope, Memories: []Ref{{Collection: colMemories, ID: old.ID, Revision: old.Revision}, {Collection: colMemories, ID: next.ID, Revision: next.Revision}},
		Action: "mark", Hold: "both", Reason: "dream: " + reason}
	return d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return supersedeInTx(ctx, tx, mark, d.principal, roleService)
	})
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
		if d.stop() {
			return nil
		}
		if r.ExpiresAt == nil || r.ExpiresAt.After(now) || r.ConflictID != "" || slices.Contains(r.Tags, dreamUndoneTag) {
			continue
		}
		if err := d.expireOne(ctx, r, now); err != nil {
			return err
		}
	}
	return nil
}

// expireOne moves one expired memory to expired and logs it; a memory changed meanwhile is skipped.
func (d *dreamer) expireOne(ctx context.Context, r Record, now time.Time) error {
	snap, err := d.snapshot(ctx, Ref{Collection: colMemories, ID: r.ID})
	if err != nil {
		return err
	}
	err = d.s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
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
		d.skip(colMemories+"/"+r.ID, "changed", 1)
		return nil
	} else if err != nil {
		return err
	}
	return d.record(ctx, DreamChange{Op: "expire", Target: colMemories + "/" + r.ID, Reason: "expired " + r.ExpiresAt.Format(time.RFC3339), Confidence: 1}, snap)
}
