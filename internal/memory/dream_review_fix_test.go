package memory

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// A pile of items the dream must leave for a person neither stops the run nor starves later stages,
// and a later run does not log the same items again.
func TestDreamSkipsNeitherStopTheRunNorRepeat(t *testing.T) {
	t.Setenv("MEMORY_DREAM_ITEMS", "2")
	s := newDreamSeed(t, nil)
	src := s.f.source()
	for _, text := range []string{"risky one", "risky two", "risky three"} {
		r := s.f.create(s.f.agent, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: refs(src), Generator: &Generator{Model: "m", PromptVersion: "v1", PipelineVersion: "v1"}}))
		dreamPatch(t, s.f, "memory_candidates", r.ID, func(r *Record) { r.Classification = &CandidateClassification{Risk: "high"} })
	}
	first := s.run(t).Run
	if len(first.Skipped) != 2 || first.SkippedMore < 1 {
		t.Fatal("skips are logged up to the limit, then counted", len(first.Skipped), first.SkippedMore)
	}
	if first.Outcome != "item_limit" || s.status(t, colMemories, s.stale.ID) != "expired" {
		t.Fatal("a full stage starved the expiry stage", first.Outcome)
	}
	job, _ := s.f.svc.Repo.Get(testCtx, colJobs, scopeA, dreamJobID(scopeA))
	if err := s.f.svc.runDream(testCtx, s.worker, job); err != nil {
		t.Fatal(err)
	}
	page, _ := s.f.svc.ListDreamRuns(testCtx, s.f.human, scopeA, 5)
	logged := map[string]bool{}
	for _, k := range first.Skipped {
		logged[k.Target] = true
	}
	for _, item := range page.Items {
		if item.Run.StartedAt.Equal(first.StartedAt) {
			continue
		}
		for _, k := range item.Run.Skipped {
			if logged[k.Target] {
				t.Errorf("logged %s again at the same revision", k.Target)
			}
		}
	}
}

// The run report keeps no memory text (reports live in jobs, which erasure does not walk) and the
// scope keeps at most dreamRunsMax reports.
func TestDreamReportHoldsNoTextAndIsBounded(t *testing.T) {
	s := newDreamSeed(t, nil)
	item := s.run(t)
	if _, err := s.f.svc.UndoDream(testCtx, s.f.human, item.ID, DreamUndoInput{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	stored, err := s.f.svc.Repo.Get(testCtx, colJobs, scopeA, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, j, err := encodeRecord(stored)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []Record{s.retire, s.keep, s.stale, s.old, s.dup, s.fresh, s.contra, s.risky, s.newer} {
		if bytes.Contains(b, []byte(r.Text)) || strings.Contains(string(j), r.Text) {
			t.Errorf("report holds memory text %q", r.Text)
		}
	}
	for i := 0; i < dreamRunsMax+3; i++ {
		if _, err = s.f.svc.dream(testCtx, s.worker, scopeA, dreamConfig(), &DreamRun{}); err != nil {
			t.Fatal(err)
		}
	}
	if runs, _ := s.f.svc.Repo.ListRecordsByStatus(testCtx, scopeA, colJobs, dreamRunStatus, 1000); len(runs) != dreamRunsMax {
		t.Fatal("dream reports kept", len(runs))
	}
}

// raceRepo lets a person's edit land right after a dream's transaction commits.
type raceRepo struct {
	*fakeRepo
	after func()
}

func (r raceRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	err := r.fakeRepo.Transaction(ctx, fn)
	if err == nil {
		r.after()
	}
	return err
}

// An edit between the dream's change and its bookkeeping is never taken for the dream's own: undo
// reports it as a conflict instead of overwriting it.
func TestDreamUndoKeepsAnEditMadeRightAfterTheChange(t *testing.T) {
	s := newDreamSeed(t, nil)
	edited := false
	s.f.svc.Repo = raceRepo{s.f.repo, func() {
		if r, err := s.f.repo.Get(testCtx, colMemories, scopeA, s.stale.ID); edited || err != nil || r.Status != "expired" {
			return
		}
		edited = true
		dreamPatch(t, s.f, colMemories, s.stale.ID, func(r *Record) { r.Tags = append(r.Tags, "person-edit") })
	}}
	item := s.run(t)
	expire := slices.IndexFunc(item.Run.Changes, func(c DreamChange) bool { return c.Op == "expire" })
	out, err := s.f.svc.UndoDream(testCtx, s.f.human, item.ID, DreamUndoInput{Scope: scopeA, Change: &expire})
	if !edited || err != nil || len(out.Conflicts) != 1 || s.status(t, colMemories, s.stale.ID) != "expired" {
		t.Fatal("undo overwrote a person's edit", edited, out, err)
	}
}

// A row deleted after the dream does not abort undoing the rest of the run.
func TestDreamUndoGoesOnPastADeletedRow(t *testing.T) {
	s := newDreamSeed(t, nil)
	item := s.run(t)
	if err := s.f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		r, err := tx.Get(ctx, "memory_candidates", scopeA, s.dup.ID)
		if err != nil {
			return err
		}
		return tx.Delete(ctx, "memory_candidates", scopeA, r.ID, r.Revision)
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.f.svc.UndoDream(testCtx, s.f.human, item.ID, DreamUndoInput{Scope: scopeA})
	if err != nil || len(out.Conflicts) != 1 || len(out.Undone) != len(item.Run.Changes)-1 {
		t.Fatal("undo stopped at a deleted row", out, err)
	}
	if s.status(t, colMemories, s.stale.ID) != "active" {
		t.Fatal("the rest of the run was not undone")
	}
}

// blindRepo hides the rows the worker writes, as Postgres does for a principal without db:read.
type blindRepo struct{ *fakeRepo }

func (r blindRepo) Get(ctx context.Context, col string, s Scope, id string) (Record, error) {
	if col == colJobs && strings.HasPrefix(id, "dream-run-") {
		return Record{}, ErrNotFound
	}
	return r.fakeRepo.Get(ctx, col, s, id)
}

func TestDreamWithoutDBReadFailsLoudly(t *testing.T) {
	s := newDreamSeed(t, nil)
	s.f.svc.Repo = blindRepo{s.f.repo}
	job, _ := s.f.repo.Get(testCtx, colJobs, scopeA, dreamJobID(scopeA))
	if err := s.f.svc.runDream(testCtx, s.worker, job); !errors.Is(err, ErrDenied) || !strings.Contains(err.Error(), "db:read") {
		t.Fatal("a blind worker reported success", err)
	}
	if s.status(t, colMemories, s.stale.ID) != "active" {
		t.Fatal("a blind worker changed memories")
	}
}
