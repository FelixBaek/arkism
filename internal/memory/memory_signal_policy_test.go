package memory

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

type batchedSignalRepo struct {
	Repository
	records     []Record
	calls, gets int
	fail        bool
}

func (r *batchedSignalRepo) Get(ctx context.Context, c string, s Scope, id string) (Record, error) {
	r.gets++
	return r.Repository.Get(ctx, c, s, id)
}
func (r *batchedSignalRepo) MemorySignalRecords(_ context.Context, _ Scope, ids []string) ([]Record, error) {
	r.calls++
	if len(ids) > 100 {
		panic("unbounded batch")
	}
	if r.fail {
		return nil, ErrUnavailable
	}
	return r.records, nil
}
func TestSignalBatchBoundsAndScope(t *testing.T) {
	f := newFixture(t)
	repo := &batchedSignalRepo{Repository: f.repo}
	f.svc.Repo = repo
	rows := make([]ScoredHit, 201)
	for i := range rows {
		id := fmt.Sprint(i)
		rows[i] = diverseRow(id, 1)
		repo.records = append(repo.records, Record{ID: memorySignalID(scopeA, id), Scope: scopeA, Kind: memorySignalKind, MemorySignal: &MemorySignal{Memory: Ref{ID: id}, Samples: 1}})
	}
	wrong := repo.records[0]
	wrong.Scope.ProjectID = "other"
	repo.records[0] = wrong
	got := f.svc.outcomeWeights(testCtx, scopeA, rows)
	if repo.calls != 3 || repo.gets != 0 || len(got) != 200 || got["1"] != .1 {
		t.Fatalf("calls=%d gets=%d weights=%d", repo.calls, repo.gets, len(got))
	}
	repo.fail = true
	if len(f.svc.outcomeWeights(testCtx, scopeA, rows)) != 0 {
		t.Fatal("failed batch changed weights")
	}
	f.svc.SignalRankingOff = true
	calls := repo.calls
	f.svc.outcomeWeights(testCtx, scopeA, rows)
	if repo.calls != calls {
		t.Fatal("disabled ranking read signals")
	}
}
func TestSignalPolicyContentWindowAndRecovery(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("use the current verified procedure")
	h := HitFromRecord(colMemories, m)
	now := time.Now().UTC()
	rows := []ScoredHit{{Hit: h, Score: 1}}
	in := SearchInput{Scope: scopeA, space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{SignalPolicy: &SignalPolicy{Dimension: "work_task", WindowDays: 30, MinSamples: 3}}}}
	f.repo.store["evaluations"] = map[string]Record{}
	for i := 0; i < 101; i++ {
		r := Record{ID: fmt.Sprint(i), Scope: scopeA, Kind: memoryOutcomeKind, Status: "active", Revision: 1, CreatedAt: now.Add(-time.Hour), Target: &Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}, Tags: []string{"dimension:work_task"}, Passed: i > 0, Relations: []Ref{{Collection: "execution_traces", ID: fmt.Sprintf("task-%d", i), Revision: 1}}, MemoryOutcomeContentHash: signalContentHash(h)}
		f.repo.store["evaluations"][recKey(scopeA, r.ID)] = r
	}
	got := f.svc.policyOutcomeWeights(testCtx, in, rows)
	if got[m.ID] < 1.4 {
		t.Fatal("recent successes did not recover", got)
	}
	// Metadata revisions cannot reset content-attributed observations.
	rows[0].Revision++
	if w := f.svc.policyOutcomeWeights(testCtx, in, rows)[m.ID]; w != got[m.ID] {
		t.Fatal("metadata reset outcomes")
	}
	rows[0].Text = "a different procedure"
	if len(f.svc.policyOutcomeWeights(testCtx, in, rows)) != 0 {
		t.Fatal("old content affected new content")
	}
	rows[0].Hit = h
	in.space.RecallQuality.SignalPolicy.Dimension = "personal_compliance"
	if len(f.svc.policyOutcomeWeights(testCtx, in, rows)) != 0 {
		t.Fatal("other dimension affected weight")
	}
	in.space.RecallQuality.SignalPolicy.Dimension = "work_task"
	for k, r := range f.repo.store["evaluations"] {
		r.CreatedAt = now.Add(-31 * 24 * time.Hour)
		f.repo.store["evaluations"][k] = r
	}
	if len(f.svc.policyOutcomeWeights(testCtx, in, rows)) != 0 {
		t.Fatal("old results affected recent policy")
	}
	if math.Abs(outcomeFactor(&MemorySignal{Samples: 101, Passed: 100})-.115) > 1e-9 {
		t.Fatal("legacy policy changed")
	}
}

func TestSignalPolicyFromRecordedOutcomeToSearch(t *testing.T) {
	f, m, in := outcomeFixture(t)
	for i := 0; i < 3; i++ {
		if i > 0 {
			in = distinctOutcomeTask(t, f, in, fmt.Sprintf("policy-task-%d", i))
		}
		in.EventID = fmt.Sprintf("policy-%d", i)
		in.Passed = true
		ref, err := f.svc.RecordMemoryOutcome(testCtx, "checker", in)
		if err != nil {
			t.Fatal(err)
		}
		ev, err := f.repo.Get(testCtx, "evaluations", scopeA, ref.ID)
		if err != nil || ev.MemoryOutcomeContentHash != signalContentHash(HitFromRecord(colMemories, m)) {
			t.Fatal("content attribution absent", err)
		}
	}
	setRecallQuality(t, f, SpaceRecallQuality{SignalPolicy: &SignalPolicy{Dimension: "work_task", WindowDays: 30, MinSamples: 3}})
	out := f.search(f.human, SearchInput{Scope: scopeA, Query: m.Text, Modes: []string{"fts"}, Limit: 10, Explain: true})
	for _, h := range out.Items {
		if h.ID == m.ID && h.Explain != nil && h.Explain.Outcome != nil && math.Abs(*h.Explain.Outcome-1.3) < 1e-9 {
			return
		}
	}
	t.Fatal("recorded outcomes did not reach actual search ranking", out.Items)
}

func TestSignalPolicyPostgresFiltersAndBatch(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := testCtx
	now := time.Now().UTC()
	for i, scope := range []Scope{scopeA, scopeB} {
		rec := Record{ID: memorySignalID(scope, "memory"), Scope: scope, Kind: memorySignalKind, Status: JobStatusDone, Revision: 1, CreatedAt: now, MemorySignal: &MemorySignal{Memory: Ref{ID: "memory", Collection: colMemories, Revision: 1}, Samples: 1, Passed: i}}
		if err := p.Insert(ctx, colJobs, rec); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := p.MemorySignalRecords(ctx, scopeA, []string{memorySignalID(scopeA, "memory"), memorySignalID(scopeB, "memory")})
	if err != nil || len(batch) != 1 || batch[0].Scope != scopeA {
		t.Fatal("batch crossed scope", batch, err)
	}
	for i, hash := range []string{"current", "old", "current"} {
		r := Record{ID: fmt.Sprintf("outcome-%d", i), Scope: scopeA, Kind: memoryOutcomeKind, Status: "active", Revision: 1, CreatedAt: now.Add(-time.Hour), Target: &Ref{Collection: colMemories, ID: "memory", Revision: 1}, Tags: []string{"dimension:work_task"}, Passed: true, MemoryOutcomeContentHash: hash}
		if i == 2 {
			r.CreatedAt = now.Add(-40 * 24 * time.Hour)
		}
		if err := p.Insert(ctx, "evaluations", r); err != nil {
			t.Fatal(err)
		}
	}
	from := now.Add(-30 * 24 * time.Hour)
	counts, err := p.MemoryOutcomeCounts(ctx, MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{"memory"}, Dimension: "work_task", From: &from, ContentHashes: map[string]string{"memory": "current"}})
	if err != nil || len(counts) != 1 || counts[0].Total != 1 {
		t.Fatal("content/time SQL filter", counts, err)
	}
	all, err := p.MemoryOutcomeCounts(ctx, MemoryOutcomeQuery{Scope: scopeA, MemoryIDs: []string{"memory"}})
	if err != nil || len(all) != 1 || all[0].Total != 3 {
		t.Fatal("historical evidence lost", all, err)
	}
}

func TestSignalPolicyPreservesHostWorkAndValidatesConfig(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("verified procedure")
	work := &MemoryWorkCounts{Sessions: 3, TestsPassed: 3}
	sig := Record{ID: memorySignalID(scopeA, m.ID), Scope: scopeA, Kind: memorySignalKind, MemorySignal: &MemorySignal{Memory: Ref{ID: m.ID}, Samples: 1, Work: work}}
	repo := &batchedSignalRepo{Repository: f.repo, records: []Record{sig}}
	f.svc.Repo = repo
	in := SearchInput{Scope: scopeA, space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{SignalPolicy: &SignalPolicy{Dimension: "work_task", WindowDays: 30, MinSamples: 3}}}}
	if w := f.svc.policyOutcomeWeights(testCtx, in, []ScoredHit{{Hit: HitFromRecord(colMemories, m)}})[m.ID]; w != workFactor(work) {
		t.Fatalf("host work was lost: %v", w)
	}
	for _, p := range []*SignalPolicy{{Dimension: "other", WindowDays: 30, MinSamples: 3}, {Dimension: "work_task", WindowDays: 0, MinSamples: 3}, {Dimension: "work_task", WindowDays: 3651, MinSamples: 3}, {Dimension: "work_task", WindowDays: 30, MinSamples: 2}, {Dimension: "work_task", WindowDays: 30, MinSamples: 1001}} {
		if p.valid() {
			t.Fatalf("invalid policy accepted: %+v", p)
		}
	}
	if !(*SignalPolicy)(nil).valid() {
		t.Fatal("legacy policy invalid")
	}
	h := HitFromRecord(colMemories, m)
	h.MemoryContext = &MemoryContext{}
	before := signalContentHash(h)
	now := time.Now()
	h.MemoryContext.ObservedAt = &now
	h.Revision++
	h.Tags = []string{"project:metadata"}
	if signalContentHash(h) != before {
		t.Fatal("observation metadata reset content evidence")
	}
	h.Tags = []string{"rule:always"}
	always := signalContentHash(h)
	h.Tags = []string{"rule:never"}
	if always == signalContentHash(h) {
		t.Fatal("rule semantics retained old evaluation identity")
	}
}

type sequentialSignalRepo struct {
	Repository
	signalReads int
}

func (r *sequentialSignalRepo) Get(ctx context.Context, c string, s Scope, id string) (Record, error) {
	if c == colJobs && strings.HasPrefix(id, "memory-signal-") {
		r.signalReads++
	}
	return r.Repository.Get(ctx, c, s, id)
}
func TestSignalBatchPreservesActualSearch(t *testing.T) {
	f := newFixture(t)
	fixed := time.Now().UTC().Add(time.Hour)
	f.svc.RecallNow = func() time.Time { return fixed }
	var signals []Record
	for i := 0; i < 12; i++ {
		m := f.activeMemory(fmt.Sprintf("deployment procedure %02d", i))
		sig := Record{ID: memorySignalID(scopeA, m.ID), Scope: scopeA, Kind: memorySignalKind, Revision: 1, MemorySignal: &MemorySignal{Memory: Ref{ID: m.ID}, Samples: 3, Passed: i % 4, Work: &MemoryWorkCounts{Sessions: 2, TestsPassed: i % 3}}}
		if err := f.repo.Insert(testCtx, colJobs, sig); err != nil {
			t.Fatal(err)
		}
		signals = append(signals, sig)
	}
	sequential := &sequentialSignalRepo{Repository: f.repo}
	f.svc.Repo = sequential
	in := SearchInput{Scope: scopeA, Query: "deployment procedure", Modes: []string{"fts"}, Limit: 12, Explain: true}
	before := f.search(f.human, in)
	batch := &batchedSignalRepo{Repository: f.repo, records: signals}
	f.svc.Repo = batch
	after := f.search(f.human, in)
	if len(before.Items) != 12 || !reflect.DeepEqual(before.Items, after.Items) {
		t.Fatalf("search rank/score/explanation changed for %d/%d items", len(before.Items), len(after.Items))
	}
	if sequential.signalReads != 12 || batch.calls != 1 {
		t.Fatalf("reads sequential=%d batch=%d", sequential.signalReads, batch.calls)
	}
}

func TestSignalBatchPostgresReadComparison(t *testing.T) {
	p := isolatedPostgres(t)
	rows := make([]ScoredHit, 201)
	for i := range rows {
		id := fmt.Sprint(i)
		rows[i] = diverseRow(id, 1)
		rec := Record{ID: memorySignalID(scopeA, id), Scope: scopeA, Kind: memorySignalKind, Status: JobStatusDone, Revision: 1, CreatedAt: time.Now(), MemorySignal: &MemorySignal{Memory: Ref{ID: id}, Samples: 3, Passed: i % 4}}
		if err := p.Insert(testCtx, colJobs, rec); err != nil {
			t.Fatal(err)
		}
	}
	sequential := &sequentialSignalRepo{Repository: p}
	svc := &Service{Repo: sequential}
	start := time.Now()
	old := svc.outcomeWeights(testCtx, scopeA, rows)
	oldTime := time.Since(start)
	counted := &countedPostgresSignals{Postgres: p}
	svc.Repo = counted
	start = time.Now()
	batch := svc.outcomeWeights(testCtx, scopeA, rows)
	batchTime := time.Since(start)
	if !reflect.DeepEqual(old, batch) || sequential.signalReads != 201 || counted.calls != 3 {
		t.Fatalf("mismatch reads=%d/%d", sequential.signalReads, counted.calls)
	}
	t.Logf("local synthetic 201 rows: sequential calls=%d duration=%s; batch calls=%d duration=%s (not a production latency claim)", sequential.signalReads, oldTime, counted.calls, batchTime)
}

type countedPostgresSignals struct {
	*Postgres
	calls int
}

func (p *countedPostgresSignals) MemorySignalRecords(ctx context.Context, s Scope, ids []string) ([]Record, error) {
	p.calls++
	return p.Postgres.MemorySignalRecords(ctx, s, ids)
}
