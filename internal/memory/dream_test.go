package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDreamQueueWaitsForQuietButNotForever(t *testing.T) {
	repo := newFakeRepo()
	set := dreamSettings{auto: true, idle: 30 * time.Minute, every: 24 * time.Hour}
	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	queue := func(at time.Time) Record {
		t.Helper()
		if err := repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error { return queueDream(ctx, tx, scopeA, set, at) }); err != nil {
			t.Fatal(err)
		}
		job, err := repo.Get(testCtx, colJobs, scopeA, dreamJobID(scopeA))
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	if job := queue(t0); job.Status != JobStatusPending || !job.RunAt.Equal(t0.Add(30*time.Minute)) {
		t.Fatal("first activity", job.Status, job.RunAt)
	}
	if job := queue(t0.Add(20 * time.Minute)); !job.RunAt.Equal(t0.Add(50 * time.Minute)) {
		t.Fatal("activity did not push the run past the next quiet window", job.RunAt)
	}
	if job := queue(t0.Add(23*time.Hour + 50*time.Minute)); !job.RunAt.Equal(t0.Add(24*time.Hour)) || !job.Dream.FirstActivity.Equal(t0) {
		t.Fatal("a never-quiet scope waited past the schedule", job.RunAt)
	}
}

func TestDreamJudgeCandidate(t *testing.T) {
	cl := func(risk, relation, verdict string, failed bool) Record {
		c := &CandidateClassification{Risk: risk, Relation: relation, DupHint: "m1"}
		if verdict != "" {
			c.Verdict = &CandidateVerdict{Action: verdict, Text: "reworded"}
		}
		if failed {
			c.Semantic = []SemanticCheck{{Check: "negation", OK: false}}
		}
		return Record{Classification: c}
	}
	for name, tc := range map[string]struct {
		in   Record
		want string
	}{
		"unclassified":       {Record{}, "skip"},
		"keep low":           {cl("low", "", "keep", false), "approve"},
		"keep medium failed": {cl("medium", "", "keep", true), "skip"},
		"high risk":          {cl("high", "", "keep", false), "skip"},
		"duplicate":          {cl("low", "duplicate", "", false), "reject"},
		"contradiction":      {cl("low", "contradiction", "keep", false), "contradiction"},
		"newer":              {cl("low", "newer_version", "", false), "supersede"},
		"discard":            {cl("low", "", "discard", false), "reject"},
		"fix":                {cl("low", "", "fix", false), "edit_approve"},
	} {
		if got := judgeCandidate(tc.in, 0.7); got.action != tc.want {
			t.Errorf("%s: %s (%s), want %s", name, got.action, got.reason, tc.want)
		}
	}
}

// dreamSeed is a fixture with a worker that holds the dream grants and an AI review grant that already
// expired, plus one of each thing a dream handles.
type dreamSeed struct {
	f                                *fixture
	worker                           string
	keep, retire, stale, old         Record
	dup, fresh, contra, risky, newer Record
}

func newDreamSeed(t *testing.T, pg *Postgres) dreamSeed {
	f := newFixture(t)
	if pg != nil {
		f.svc.Repo = pg
	}
	s := dreamSeed{f: f, worker: testPrincipalToken("tok-dreamer")}
	f.auth.actors[s.worker] = actor("dream-worker", roleService, false, allVerbs...)
	f.svc.MemoryReviewPolicies = []MemoryReviewPolicy{{PrincipalID: "dream-worker", Scope: scopeA, Kinds: []string{"semantic"}, ExpiresAt: time.Now().Add(-time.Hour)}}
	s.retire = f.activeMemory("answers in Korean")
	s.keep = f.activeMemory("answers in Korean, in plain words")
	if _, err := f.svc.ProposeMaintenance(testCtx, f.human, MaintenanceInput{Scope: scopeA, Proposal: MaintenanceProposal{Type: "duplicate", Reason: "same_meaning",
		Keep: Ref{Collection: colMemories, ID: s.keep.ID, Revision: s.keep.Revision}, Retire: Ref{Collection: colMemories, ID: s.retire.ID, Revision: s.retire.Revision}}}); err != nil {
		t.Fatal(err)
	}
	s.stale = f.activeMemory("the release freeze ends Friday")
	s.old = f.activeMemory("the deploy window is 9am")
	past := time.Now().Add(-time.Hour)
	s.stale = dreamPatch(t, f, colMemories, s.stale.ID, func(r *Record) { r.ExpiresAt = &past })
	src := f.source()
	candidate := func(text string, c CandidateClassification) Record {
		r := f.create(f.agent, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: refs(src), Generator: &Generator{Model: "m", PromptVersion: "v1", PipelineVersion: "v1"}}))
		return dreamPatch(t, f, "memory_candidates", r.ID, func(r *Record) { r.Classification = &c })
	}
	s.dup = candidate("answer in Korean please", CandidateClassification{Risk: "low", Relation: "duplicate", DupHint: s.keep.ID})
	s.fresh = candidate("deploys go out on Tuesdays", CandidateClassification{Risk: "low", Verdict: &CandidateVerdict{Action: "keep"}})
	s.contra = candidate("answers in English", CandidateClassification{Risk: "low", Relation: "contradiction", DupHint: s.keep.ID})
	s.newer = candidate("the deploy window moved to 2pm", CandidateClassification{Risk: "low", Relation: "newer_version", DupHint: s.old.ID})
	s.risky = candidate("the root password is in the wiki", CandidateClassification{Risk: "high"})
	return s
}

func dreamPatch(t *testing.T, f *fixture, col, id string, change func(*Record)) Record {
	t.Helper()
	var out Record
	if err := f.svc.Repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, col, scopeA, id)
		if err != nil {
			return err
		}
		change(&r)
		prev := r.Revision
		r.Revision++
		out = r
		return tx.Replace(ctx, col, r, prev)
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func (s dreamSeed) run(t *testing.T) DreamRunItem {
	t.Helper()
	job, err := s.f.svc.Repo.Get(testCtx, colJobs, scopeA, dreamJobID(scopeA))
	if err != nil || job.Status != JobStatusPending {
		t.Fatal("no dream queued by the writes", job.Status, err)
	}
	if err = s.f.svc.runDream(testCtx, s.worker, job); err != nil {
		t.Fatal(err)
	}
	page, err := s.f.svc.ListDreamRuns(testCtx, s.f.human, scopeA, 5)
	if err != nil || len(page.Items) != 1 || page.Mode != "auto" {
		t.Fatal("dream runs", page, err)
	}
	return page.Items[0]
}

func (s dreamSeed) status(t *testing.T, col, id string) string {
	t.Helper()
	r, err := s.f.svc.Repo.Get(testCtx, col, scopeA, id)
	if err != nil {
		t.Fatal(col, id, err)
	}
	return r.Status
}

// expect checks each row's status: want maps collection to id to status.
func (s dreamSeed) expect(t *testing.T, when string, want map[string]map[string]string) {
	t.Helper()
	for col, rows := range want {
		for id, st := range rows {
			if got := s.status(t, col, id); got != st {
				t.Errorf("%s %s/%s = %s, want %s", when, col, id, got, st)
			}
		}
	}
}

func TestDreamAppliesJudgedChangesAndLogsThem(t *testing.T) {
	s := newDreamSeed(t, nil)
	run := s.run(t).Run
	if run.Outcome != "ok" || run.FinishedAt == nil {
		t.Fatal("outcome", run.Outcome)
	}
	ops := map[string]DreamChange{}
	for _, c := range run.Changes {
		ops[c.Op+" "+c.Target] = c
		if c.Reason == "" || c.Confidence < 0.7 || c.After == "" {
			t.Errorf("change not explained: %+v", c)
		}
	}
	for _, want := range []string{"supersede memories/" + s.retire.ID, "expire memories/" + s.stale.ID, "reject memory_candidates/" + s.dup.ID, "approve memory_candidates/" + s.fresh.ID} {
		if _, ok := ops[want]; !ok {
			t.Errorf("missing change %s in %v", want, run.Changes)
		}
	}
	skipped := map[string]string{}
	for _, k := range run.Skipped {
		skipped[k.Target] = k.Reason
	}
	if skipped["memory_candidates/"+s.contra.ID] != "contradiction_undecidable" || skipped["memory_candidates/"+s.risky.ID] != "low_confidence" {
		t.Fatal("skips", skipped)
	}
	s.expect(t, "after", map[string]map[string]string{
		colMemories:         {s.retire.ID: "superseded", s.keep.ID: "active", s.stale.ID: "expired", s.fresh.ID: "active", s.old.ID: "superseded", s.newer.ID: "active"},
		"memory_candidates": {s.dup.ID: "rejected", s.contra.ID: "candidate", s.risky.ID: "candidate"},
	})
	job, _ := s.f.svc.Repo.Get(testCtx, colJobs, scopeA, dreamJobID(scopeA))
	if job.Status != JobStatusPending || job.Dream.Outcome != "ok" {
		t.Fatal("skipped items must be looked at again on the schedule", job.Status)
	}
}

func TestDreamUndoOneChangeThenTheRun(t *testing.T) {
	s := newDreamSeed(t, nil)
	item := s.run(t)
	approve, reject := -1, -1
	for i, c := range item.Run.Changes {
		switch c.Op {
		case "approve":
			approve = i
		case "reject":
			reject = i
		}
	}
	if _, err := s.f.svc.UndoDream(testCtx, s.f.agent, item.ID, DreamUndoInput{Scope: scopeA}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent undid a dream", err)
	}
	out, err := s.f.svc.UndoDream(testCtx, s.f.human, item.ID, DreamUndoInput{Scope: scopeA, Change: &approve})
	if err != nil || len(out.Undone) != 1 || out.Run.Run.Changes[approve].UndoneBy != "human-1" {
		t.Fatal("undo one", out, err)
	}
	if got := s.status(t, colMemories, s.fresh.ID); got != "rejected" {
		t.Fatal("the approval the dream made stands", got)
	}
	// A person touches the rejected duplicate after the dream: undo must not overwrite that.
	dreamPatch(t, s.f, "memory_candidates", s.dup.ID, func(r *Record) { r.Tags = append(r.Tags, "seen") })
	out, err = s.f.svc.UndoDream(testCtx, s.f.human, item.ID, DreamUndoInput{Scope: scopeA})
	if err != nil || len(out.Conflicts) != 1 || out.Conflicts[0] != reject || len(out.Undone) != len(item.Run.Changes)-2 {
		t.Fatal("undo run", out, err)
	}
	s.expect(t, "after undo", map[string]map[string]string{
		colMemories:         {s.retire.ID: "active", s.keep.ID: "active", s.stale.ID: "active", s.old.ID: "active", s.newer.ID: "rejected"},
		"memory_candidates": {s.dup.ID: "rejected"},
	})
	// The next dream leaves what a person undid alone.
	next, err := s.f.svc.dream(testCtx, s.worker, scopeA, dreamConfig(), &DreamRun{StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range next.Changes {
		t.Errorf("redid an undone change: %+v", c)
	}
}

func TestDreamManualAndMissingGrants(t *testing.T) {
	t.Run("manual", func(t *testing.T) {
		t.Setenv("MEMORY_DREAM", "manual")
		s := newDreamSeed(t, nil)
		if _, err := s.f.svc.Repo.Get(testCtx, colJobs, scopeA, dreamJobID(scopeA)); !errors.Is(err, ErrNotFound) {
			t.Fatal("manual mode queued a dream", err)
		}
		in := CandidateReviewInput{Automated: true, Scope: scopeA, ExpectedRevision: s.fresh.Revision, Action: "approve", OperationID: "op-1"}
		if _, err := s.f.svc.ReviewCandidate(context.WithValue(testCtx, dreamRunKey{}, "run-1"), s.worker, s.fresh.ID, in); !errors.Is(err, ErrDenied) {
			t.Fatal("manual mode let the worker approve", err)
		}
	})
	t.Run("outside a run", func(t *testing.T) {
		s := newDreamSeed(t, nil)
		in := CandidateReviewInput{Automated: true, Scope: scopeA, ExpectedRevision: s.fresh.Revision, Action: "approve", OperationID: "op-1"}
		if _, err := s.f.svc.ReviewCandidate(testCtx, s.worker, s.fresh.ID, in); !errors.Is(err, ErrDenied) {
			t.Fatal("a service approved outside a dream run", err)
		}
	})
	t.Run("grant", func(t *testing.T) {
		s := newDreamSeed(t, nil)
		s.f.auth.actors[s.worker] = actor("dream-worker", roleService, false, "read", "write")
		run := s.run(t).Run
		if run.Outcome != "denied:transition:memory_candidates" || len(run.Changes) != 0 {
			t.Fatal("outcome", run.Outcome)
		}
	})
}

// One dream end to end on a scratch Postgres database, claimed by the worker loop: a duplicate pair,
// a contradiction, a newer version, an expired fact and a risky candidate, then undo of the whole run.
// logTable prints every seeded row's status and text, for the before/after record of the e2e run.
func (s dreamSeed) logTable(t *testing.T, when string) {
	for _, ref := range []struct{ col, id, what string }{
		{colMemories, s.retire.ID, "duplicate (older)"}, {colMemories, s.keep.ID, "duplicate (kept)"}, {colMemories, s.old.ID, "replaced by a newer one"},
		{colMemories, s.stale.ID, "expired fact"}, {"memory_candidates", s.dup.ID, "duplicate candidate"}, {"memory_candidates", s.contra.ID, "contradiction"},
		{"memory_candidates", s.risky.ID, "high risk"}, {"memory_candidates", s.fresh.ID, "new fact"}, {colMemories, s.fresh.ID, "new fact"}, {colMemories, s.newer.ID, "newer version"},
	} {
		r, err := s.f.svc.Repo.Get(testCtx, ref.col, scopeA, ref.id)
		st := r.Status
		if errors.Is(err, ErrNotFound) {
			st = "-"
		}
		t.Logf("%-6s %-24s %-18s %-10s %q", when, ref.what, ref.col, st, r.Text)
	}
}

// workUntilDreamed makes the idle window pass and runs the worker loop until it ran the dream.
func (s dreamSeed) workUntilDreamed(t *testing.T) DreamRunItem {
	now := time.Now().UTC().Add(-time.Second)
	dreamPatch(t, s.f, colJobs, dreamJobID(scopeA), func(r *Record) { r.RunAt = &now })
	var runs DreamRunPage
	for i := 0; i < 40 && len(runs.Items) == 0; i++ {
		if err := s.f.svc.WorkOnce(testCtx, s.worker); err != nil {
			t.Log("worker:", err) // other queued jobs (embeddings, evaluations) may fail here
		}
		runs, _ = s.f.svc.ListDreamRuns(testCtx, s.f.human, scopeA, 5)
	}
	if len(runs.Items) != 1 || runs.Items[0].Run.Outcome != "ok" {
		t.Fatal("the worker did not run the dream", runs)
	}
	return runs.Items[0]
}

// One dream end to end on a scratch Postgres database, claimed by the worker loop: a duplicate pair,
// a contradiction, a newer version, an expired fact and a risky candidate, then undo of the whole run.
func TestPostgresDreamEndToEndAndUndo(t *testing.T) {
	s := newDreamSeed(t, isolatedPostgres(t))
	s.logTable(t, "before")
	run := s.workUntilDreamed(t)
	for _, c := range run.Run.Changes {
		t.Logf("change %-12s %-48s conf %.2f  %s | %s -> %s", c.Op, c.Target, c.Confidence, c.Reason, c.Before, c.After)
	}
	for _, k := range run.Run.Skipped {
		t.Logf("skip   %-48s %s", k.Target, k.Reason)
	}
	s.logTable(t, "after")
	if len(run.Run.Changes) != 5 {
		t.Fatal("changes", len(run.Run.Changes))
	}
	s.expect(t, "after", map[string]map[string]string{
		colMemories:         {s.retire.ID: "superseded", s.old.ID: "superseded", s.stale.ID: "expired", s.newer.ID: "active", s.fresh.ID: "active"},
		"memory_candidates": {s.dup.ID: "rejected", s.contra.ID: "candidate", s.risky.ID: "candidate"},
	})
	out, err := s.f.svc.UndoDream(testCtx, s.f.human, run.ID, DreamUndoInput{Scope: scopeA})
	if err != nil || len(out.Undone) != 5 || len(out.Conflicts) != 0 {
		t.Fatal("undo", out, err)
	}
	s.logTable(t, "undone")
	s.expect(t, "after undo", map[string]map[string]string{
		colMemories:         {s.retire.ID: "active", s.keep.ID: "active", s.old.ID: "active", s.stale.ID: "active", s.fresh.ID: "rejected", s.newer.ID: "rejected"},
		"memory_candidates": {s.dup.ID: "candidate", s.contra.ID: "candidate", s.risky.ID: "candidate"},
	})
}
