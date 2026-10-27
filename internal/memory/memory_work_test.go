package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"testing"
	"time"
)

// MemoryWorkCounts mirrors the Postgres join over trace sources: context events
// name the delivered memories, the behavior event of the same trace its work.
func (f *fakeRepo) MemoryWorkCounts(_ context.Context, scope Scope, ids []string) ([]MemoryWorkCount, error) {
	if checkScope(scope) != nil || len(ids) == 0 || len(ids) > memoryGraphMapNodes {
		return nil, ErrInvalid
	}
	delivered, work := map[string][]string{}, map[string]map[string]any{}
	for _, r := range f.rows("sources") {
		var p struct {
			Retrieved []Ref          `json:"retrieved"`
			Behavior  string         `json:"behavior"`
			Work      map[string]any `json:"work"`
		}
		if r.Scope != scope || !live(r) || r.ContentErased || r.TraceID == "" || json.Unmarshal([]byte(r.Text), &p) != nil {
			continue
		}
		for _, ref := range p.Retrieved {
			if ref.Collection == colMemories && slices.Contains(ids, ref.ID) && !slices.Contains(delivered[r.TraceID], ref.ID) {
				delivered[r.TraceID] = append(delivered[r.TraceID], ref.ID)
			}
		}
		if p.Behavior != "" && p.Work != nil {
			work[r.TraceID] = p.Work
		}
	}
	by := map[string]*MemoryWorkCount{}
	for trace, mids := range delivered {
		w, ok := work[trace]
		if !ok {
			continue
		}
		for _, id := range mids {
			c := by[id]
			if c == nil {
				c = &MemoryWorkCount{MemoryID: id}
				by[id] = c
			}
			c.Sessions++
			if w["diff"] == true {
				c.Diff++
			}
			c.TestsPassed += boolInt(w["tests"] == "passed")
			c.TestsFailed += boolInt(w["tests"] == "failed")
			c.CIPassed += boolInt(w["ci"] == "passed")
			c.CIFailed += boolInt(w["ci"] == "failed")
		}
	}
	out := []MemoryWorkCount{}
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MemoryID < out[j].MemoryID })
	return out, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestWorkFactor(t *testing.T) {
	for _, c := range []struct {
		w    *MemoryWorkCounts
		want float64
	}{
		{nil, 1},
		{&MemoryWorkCounts{Sessions: 3}, 1}, // sessions without a result move nothing
		{&MemoryWorkCounts{Sessions: 1, Diff: 1}, 1.05},
		{&MemoryWorkCounts{Sessions: 1, Diff: 1, TestsPassed: 1, CIPassed: 1}, 1.09},
		{&MemoryWorkCounts{Sessions: 1, TestsFailed: 1}, 0.95},
		{&MemoryWorkCounts{Sessions: 2, Diff: 1, TestsFailed: 1}, 1},                 // G = B
		{&MemoryWorkCounts{Sessions: 2, Diff: 2, TestsPassed: 1, CIFailed: 1}, 1.05}, // G=3, B=1
	} {
		if got := workFactor(c.w); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%+v: got %v, want %v", c.w, got, c.want)
		}
	}
	// The bound holds however many sessions agree.
	for _, n := range []int{1, 10, 1000, 1 << 20} {
		up := workFactor(&MemoryWorkCounts{Sessions: n, Diff: n, TestsPassed: n, CIPassed: n})
		down := workFactor(&MemoryWorkCounts{Sessions: n, TestsFailed: n, CIFailed: n})
		if up <= 1 || up >= 1.15 || down >= 1 || down <= 0.85 {
			t.Fatalf("n=%d escaped (0.85, 1.15): %v %v", n, up, down)
		}
	}
}

func TestOutcomeWeightsAddWorkAndLeaveUnobservedAlone(t *testing.T) {
	f := newFixture(t)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u"}
	for id, sig := range map[string]MemorySignal{
		"worked":     {Work: &MemoryWorkCounts{Sessions: 1, Diff: 1, TestsPassed: 1, CIPassed: 1}},
		"judged":     {Samples: 1, Passed: 1, Work: &MemoryWorkCounts{Sessions: 1, TestsFailed: 1}},
		"unobserved": {Uses: 4},
		"even":       {Work: &MemoryWorkCounts{Sessions: 2}},
	} {
		sig.Memory = Ref{Collection: colMemories, ID: id, Revision: 1}
		if e := f.repo.Insert(testCtx, colJobs, Record{ID: memorySignalID(scope, id), Scope: scope, Kind: memorySignalKind, Status: JobStatusDone, Revision: 1, MemorySignal: &sig}); e != nil {
			t.Fatal(e)
		}
	}
	rows := []ScoredHit{{Hit: Hit{ID: "worked"}}, {Hit: Hit{ID: "judged"}}, {Hit: Hit{ID: "unobserved"}}, {Hit: Hit{ID: "even"}}, {Hit: Hit{ID: "none"}}}
	got := f.svc.outcomeWeights(testCtx, scope, rows)
	if len(got) != 2 || math.Abs(got["worked"]-1.09) > 1e-9 || math.Abs(got["judged"]-1.05*0.95) > 1e-9 {
		t.Fatalf("weights = %v", got)
	}
	// Rows without outcomes keep their fused order and score exactly.
	now := time.Now()
	plain := []ScoredHit{{Hit: Hit{ID: "unobserved"}, Score: 0.3}, {Hit: Hit{ID: "none"}, Score: 0.2}, {Hit: Hit{ID: "even"}, Score: 0.1}}
	before := decayByRecency(slices.Clone(plain), now, nil, 3)
	after := decayByRecency(slices.Clone(plain), now, f.svc.outcomeWeights(testCtx, scope, plain), 3)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("no-outcome rows moved: %v -> %v", before, after)
	}
}

func workSource(t *testing.T, repo interface {
	Insert(context.Context, string, Record) error
}, id, trace string, payload any) {
	t.Helper()
	raw, _ := json.Marshal(payload)
	now := time.Now().UTC()
	if e := repo.Insert(testCtx, "sources", Record{ID: id, Scope: scopeA, Kind: "note", Status: "active", Revision: 1, Text: string(raw), TraceID: trace, SourceEventID: id, CreatedAt: now, UpdatedAt: now}); e != nil {
		t.Fatal(e)
	}
}

func TestMemorySignalRecomputeReadsSessionWork(t *testing.T) {
	f, m, in := recordOutcomes(t)
	work := func(diff bool, tests, ci string) map[string]any {
		return map[string]any{"behavior": "agent_session", "work": map[string]any{"diff": diff, "tests": tests, "ci": ci}}
	}
	delivered := map[string]any{"retrieved": []Ref{in.Target}}
	workSource(t, f.repo, "ctx-1", "work-trace-1", delivered)
	workSource(t, f.repo, "beh-1", "work-trace-1", work(true, "passed", "passed"))
	workSource(t, f.repo, "ctx-2", "work-trace-2", delivered)
	workSource(t, f.repo, "beh-2", "work-trace-2", work(false, "failed", "none"))
	workSource(t, f.repo, "ctx-3", "work-trace-3", delivered) // no behavior: no work session
	if _, e := f.svc.RecomputeMemorySignals(testCtx, f.human, MemorySignalRecomputeInput{Scope: scopeA}); e != nil {
		t.Fatal(e)
	}
	want := MemoryWorkCounts{Sessions: 2, Diff: 1, TestsPassed: 1, TestsFailed: 1, CIPassed: 1}
	if s := signalOf(t, f, scopeA, m.ID).Signal; s == nil || s.Work == nil || *s.Work != want {
		t.Fatalf("work: %+v", s)
	}
	for _, r := range memorySignalRows(f) {
		if r.MemorySignal.Memory.ID != m.ID && r.MemorySignal.Work != nil {
			t.Fatalf("undelivered memory got work: %+v", r.MemorySignal)
		}
	}
}

func TestPostgresMemoryWorkCountsJoinsDeliveryToBehavior(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	f.auth.actors["session-producer"] = actor("session-producer", roleService, true, allVerbs...)
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	ingest := func(scope Scope, id string, retrieved []Ref, work map[string]any) {
		t.Helper()
		ctxRaw, _ := json.Marshal(map[string]any{"retrieved": retrieved})
		events := []TraceEventInput{{ID: "ctx-" + id, Kind: "context", OccurredAt: start, Payload: ctxRaw}}
		if work != nil {
			raw, _ := json.Marshal(map[string]any{"behavior": "agent_session", "work": work})
			events = append(events, TraceEventInput{ID: "beh-" + id, Kind: "behavior", OccurredAt: start, Payload: raw})
		}
		if _, err := f.svc.IngestCompletedTrace(testCtx, "session-producer", TraceIngestInput{Scope: scope, TraceID: id, ThreadID: id,
			AgentRun: CompletedAgentRunInput{AgentID: "codex", TaskType: "agent_session", Outcome: "partial", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "session"},
			Events:   events}); err != nil {
			t.Fatal(err)
		}
	}
	ref := func(id string) Ref { return Ref{Collection: colMemories, ID: id, Revision: 1} }
	ingest(scopeA, "t1", []Ref{ref("mem-1"), ref("mem-2")}, map[string]any{"diff": true, "tests": "passed", "ci": "none"})
	ingest(scopeA, "t2", []Ref{ref("mem-1")}, map[string]any{"diff": false, "tests": "failed", "ci": "failed"})
	ingest(scopeA, "t3", []Ref{ref("mem-1")}, nil)
	ingest(scopeB, "t4", []Ref{ref("mem-1")}, map[string]any{"diff": true, "tests": "passed", "ci": "passed"})
	counts, err := p.MemoryWorkCounts(testCtx, scopeA, []string{"mem-1", "mem-2", "mem-3"})
	want := []MemoryWorkCount{{"mem-1", MemoryWorkCounts{Sessions: 2, Diff: 1, TestsPassed: 1, TestsFailed: 1, CIFailed: 1}}, {"mem-2", MemoryWorkCounts{Sessions: 1, Diff: 1, TestsPassed: 1}}}
	if err != nil || fmt.Sprint(counts) != fmt.Sprint(want) {
		t.Fatalf("counts: %+v %v", counts, err)
	}
}
