//go:build linux

package memory

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func distinctOutcomeTask(t *testing.T, f *fixture, in MemoryOutcomeInput, name string) MemoryOutcomeInput {
	t.Helper()
	source, e := f.repo.Get(testCtx, "sources", in.Scope, in.Evidence[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	trace, e := f.repo.Get(testCtx, "execution_traces", in.Scope, in.Trace.ID)
	if e != nil {
		t.Fatal(e)
	}
	run, e := f.repo.Get(testCtx, "agent_runs", in.Scope, trace.Relations[0].ID)
	if e != nil {
		t.Fatal(e)
	}
	run.ID = name + "-run"
	trace.ID = name + "-trace"
	source.ID = name + "-source"
	source.TraceID = trace.ID
	trace.Relations = []Ref{{Collection: "agent_runs", ID: run.ID, Revision: run.Revision}}
	trace.Sources = []SourceRef{sourceReference(source, "trace")}
	for col, r := range map[string]Record{"sources": source, "execution_traces": trace, "agent_runs": run} {
		if e := f.repo.Insert(testCtx, col, r); e != nil {
			t.Fatal(e)
		}
	}
	in.Trace = Ref{Collection: "execution_traces", ID: trace.ID, Revision: trace.Revision}
	in.Evidence = []Ref{{Collection: "sources", ID: source.ID, Revision: source.Revision}}
	return in
}

func TestIndependentOutcomeFirstCompletionAndHistory(t *testing.T) {
	f, m, in := outcomeFixture(t)
	for i := 0; i < 4; i++ {
		in.EventID = fmt.Sprintf("duplicate-%d", i)
		in.Passed = i > 0
		if _, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in); e != nil {
			t.Fatal(e)
		}
	}
	query := MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{m.ID}, Dimension: "work_task", ContentHashes: map[string]string{m.ID: signalContentHash(HitFromRecord(colMemories, m))}, IndependentFirst: true}
	first, e := f.repo.MemoryOutcomeCounts(testCtx, query)
	if e != nil || len(first) != 1 || first[0].Total != 1 || first[0].Passed != 0 {
		t.Fatal(first, e)
	}
	query.IndependentFirst = false
	all, e := f.repo.MemoryOutcomeCounts(testCtx, query)
	if e != nil || len(all) != 1 || all[0].Total != 4 || all[0].Passed != 3 {
		t.Fatal("events were lost", all, e)
	}
	setRecallQuality(t, f, SpaceRecallQuality{SignalPolicy: &SignalPolicy{Dimension: "work_task", WindowDays: 30, MinSamples: 3}})
	out := f.search(f.human, SearchInput{Scope: scopeA, Query: m.Text, Modes: []string{"fts"}, Explain: true})
	for _, h := range out.Items {
		if h.ID == m.ID && h.Explain != nil && h.Explain.Outcome != nil && *h.Explain.Outcome != 1 {
			t.Fatal("duplicates satisfied threshold", *h.Explain.Outcome)
		}
	}
}

func TestIndependentOutcomePostgresParity(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	hashes := map[string]string{"m": "content"}
	for i := 0; i < 6; i++ {
		r := independentOutcomeRow(i, now)
		if e := p.Insert(testCtx, "evaluations", r); e != nil {
			t.Fatal(e)
		}
		if e := f.repo.Insert(testCtx, "evaluations", r); e != nil {
			t.Fatal(e)
		}
	}
	for _, from := range []*time.Time{nil, ptr(now.Add(-5 * time.Hour))} {
		q := MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{"m"}, Dimension: "work_task", ContentHashes: hashes, IndependentFirst: true, From: from}
		db, e := p.MemoryOutcomeCounts(testCtx, q)
		if e != nil {
			t.Fatal(e)
		}
		fake, e := f.repo.MemoryOutcomeCounts(testCtx, q)
		if e != nil || len(db) != len(fake) || len(db) != 1 || db[0].Total != fake[0].Total || db[0].Passed != fake[0].Passed || !db[0].Last.Equal(fake[0].Last) {
			t.Fatal(db, fake, e)
		}
		if len(db) != 1 || db[0].Total != map[bool]int{false: 2, true: 1}[from != nil] || db[0].Passed != 1 {
			t.Fatal("dedup before window mismatch", db)
		}
	}
	// Original public counting remains event based.
	count, e := p.MemoryOutcomeCounts(testCtx, MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{"m"}})
	if e != nil || count[0].Total != 6 {
		t.Fatal(count, e)
	}
}

// independentOutcomeRow is the i-th outcome; rows 2-5 each differ in one dedup or filter key.
func independentOutcomeRow(i int, now time.Time) Record {
	r := Record{ID: fmt.Sprintf("r%d", i), Scope: scopeA, Kind: memoryOutcomeKind, Status: "active", Revision: 1, CreatedAt: now.Add(time.Duration(i-6) * time.Hour), Target: &Ref{Collection: colMemories, ID: "m", Revision: 1}, Tags: []string{"dimension:work_task"}, Passed: i != 0, MemoryOutcomeContentHash: "content", Relations: []Ref{{Collection: "execution_traces", ID: "task", Revision: 1}}}
	switch i {
	case 2:
		r.Relations[0].ID = "another-task"
	case 3:
		r.Tags = []string{"dimension:personal_compliance"}
	case 4:
		r.MemoryOutcomeContentHash = "old"
	case 5:
		r.Relations = nil
	}
	return r
}

type visibleBatchRepo struct {
	Repository
	signalIDs []string
	calls     int
	malformed bool
	fail      bool
}

func (r *visibleBatchRepo) MemorySignalRecords(ctx context.Context, s Scope, ids []string) ([]Record, error) {
	r.calls++
	r.signalIDs = append(r.signalIDs, ids...)
	if r.fail {
		return nil, ErrUnavailable
	}
	out := []Record{}
	for _, id := range ids {
		v, e := r.Repository.Get(ctx, colJobs, s, id)
		if e == nil {
			out = append(out, v)
		}
	}
	if r.malformed && len(out) > 0 {
		out[0].Scope = scopeB
	}
	return out, nil
}
func TestPublicSignalBatchKeepsVisibilityAndErrors(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("visible signal")
	missing := f.activeMemory("signal not computed")
	sig := Record{ID: memorySignalID(scopeA, m.ID), Scope: scopeA, Kind: memorySignalKind, Revision: 1, MemorySignal: &MemorySignal{Memory: Ref{ID: m.ID}, Samples: 1}}
	if e := f.repo.Insert(testCtx, colJobs, sig); e != nil {
		t.Fatal(e)
	}
	repo := &visibleBatchRepo{Repository: f.repo}
	f.svc.Repo = repo
	q := MemorySignalQuery{Scope: scopeA, MemoryIDs: []string{"absent", m.ID, missing.ID}}
	out, e := f.svc.MemorySignals(testCtx, f.human, q)
	if e != nil || repo.calls != 1 || len(repo.signalIDs) != 2 || out.Items[0].Available || !out.Items[1].Available || out.Items[1].Signal == nil || !out.Items[2].Available || out.Items[2].Signal != nil {
		t.Fatal(out, e, repo)
	}
	repo.fail = true
	if _, e = f.svc.MemorySignals(testCtx, f.human, q); e != ErrUnavailable {
		t.Fatal(e)
	}
	repo.fail = false
	repo.malformed = true
	if _, e = f.svc.MemorySignals(testCtx, f.human, q); e != ErrUnavailable {
		t.Fatal(e)
	}
}
