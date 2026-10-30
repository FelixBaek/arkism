package memory

import (
	"context"
	"encoding/json"
	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func nativeSearchTestRuntime(t *testing.T, policy string) *NativeSearchShadowRuntime {
	t.Helper()
	raw, e := os.ReadFile("../../docs/research/memory-harness/policy_registry.json")
	if e != nil {
		t.Fatal(e)
	}
	var registry dyn.PolicyRegistryV2
	if e = json.Unmarshal(raw, &registry); e != nil {
		t.Fatal(e)
	}
	r, e := NewNativeSearchShadowRuntime(NativeSearchShadowConfig{Mode: "shadow", Registry: registry, Policy: dyn.PolicyID(policy), NativeVersion: "search-fixture", Dimension: "work_task", Deadline: time.Second, MaxSteps: 100000, Capacity: 4})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func nativeSearchTestInput() SearchInput {
	return SearchInput{Scope: scopeA, Query: "searchcanary", Modes: []string{"exact", "fts"}, Limit: 10, MaxChars: 4000, BudgetTokens: 2000, TokenEncoding: "cl100k_base", IncludeInactive: true}
}

type nativeSearchCountRepo struct {
	Repository
	calls int
}

func (r *nativeSearchCountRepo) SearchCollection(ctx context.Context, col string, in SearchInput, v []float64, space string) ([][]Hit, error) {
	r.calls++
	return r.Repository.SearchCollection(ctx, col, in, v, space)
}

func TestNativeSearchShadowServiceBaseline(t *testing.T) {
	for _, policy := range []string{"B0", "B1", "B2", "V3-P1-IR"} {
		t.Run(policy, func(t *testing.T) {
			nativeSearchCheckServiceBaseline(t, policy)
		})
	}
}
func TestNativeSearchShadowSnapshotIsolation(t *testing.T) {
	h := Hit{Scope: scopeA, ID: "one", Collection: colMemories, Origin: "memory", Revision: 1, Text: "canary", Sources: []SourceRef{{SourceID: "source"}}}
	lanes := [][]Hit{{h, h}, {h}}
	history := map[string]Record{"x": {Text: "old"}}
	snap, code := nativeFreezeSearch(scopeA, lanes, history)
	if code != "" || snap.entries != 3 || len(snap.unique) != 1 {
		t.Fatal(code)
	}
	lanes[0][0].Text = "new"
	lanes[0][1].Sources[0].SourceID = "changed"
	history["x"] = Record{Text: "new"}
	if snap.lanes[0][0].Text != "canary" || snap.lanes[0][1].Sources[0].SourceID != "source" || snap.history["x"].Text != "old" {
		t.Fatal("aliased")
	}
	h.Score = math.NaN()
	if _, code = nativeFreezeSearch(scopeA, [][]Hit{{h}}, nil); code != "invalid_candidate" {
		t.Fatal(code)
	}
	h.Score = 0
	h.Scope = Scope{}
	if _, code = nativeFreezeSearch(scopeA, [][]Hit{{h}}, nil); code != "candidate_scope_mismatch" {
		t.Fatal(code)
	}
}
func TestNativeSearchShadowCanceledAndOff(t *testing.T) {
	f := newFixture(t)
	in := nativeSearchTestInput()
	r := nativeSearchTestRuntime(t, "B0")
	f.svc.NativeSearchShadow = r
	ctx, cancel := context.WithCancel(testCtx)
	cancel()
	a := f.svc.beginNativeSearchShadow(ctx, in, colMemories, nil, nil)
	a.compute(ctx, f.svc, f.human, in, Decision{}, ContextBundle{})
	a.finish()
	if got := r.Observations()[0]; got.Status != "fallback" || got.Reason != "caller_canceled" || got.CandidateCount != 0 {
		t.Fatal(got)
	}
	off, e := NewNativeSearchShadowRuntime(NativeSearchShadowConfig{Mode: "off"})
	if e != nil {
		t.Fatal(e)
	}
	f.svc.NativeSearchShadow = off
	if f.svc.beginNativeSearchShadow(testCtx, in, colMemories, nil, nil) != nil || len(off.Observations()) != 0 {
		t.Fatal("off work")
	}
}

func nativeSearchMatchingConfig(t *testing.T, r *NativeSearchShadowRuntime) NativeDynamicsConfig {
	t.Helper()
	entry, ok := nativeSearchPolicy(r.config)
	if !ok || entry.Dynamics == nil {
		t.Fatal("fixture dynamics absent")
	}
	d := entry.Dynamics
	out := nativeTestConfig(r.config.NativeVersion)
	*out.InitialValue = d.Initial
	*out.TauNS = d.TauNS
	*out.EtaPositive = d.EtaPositive
	*out.UnitStrength = d.UnitStrength
	*out.DecayEnabled = d.DecayEnabled
	*out.PositiveLearning = d.OutcomeJumpEnabled
	return out
}
func TestNativeSearchShadowStateService(t *testing.T) {
	for _, policy := range []string{"B2", "V3-P1-FD", "V3-P1-NOTIME", "V3-P1-NOOUT"} {
		t.Run(policy, func(t *testing.T) {
			nativeSearchCheckStateService(t, policy)
		})
	}
}

type nativeSearchBlockedSourceRepo struct {
	Repository
	cancel      context.CancelFunc
	sawDeadline bool
}

func (r *nativeSearchBlockedSourceRepo) Get(ctx context.Context, col string, scope Scope, id string) (Record, error) {
	if col == colJobs && strings.HasPrefix(id, "nd_state_") {
		_, r.sawDeadline = ctx.Deadline()
		if r.cancel != nil {
			r.cancel()
		}
		<-ctx.Done()
		return Record{}, ctx.Err()
	}
	return r.Repository.Get(ctx, col, scope, id)
}
func TestNativeSearchShadowDeadlineAndCallerCancel(t *testing.T) {
	for _, callerCancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "shadow_timeout", true: "caller_cancel"}[callerCancel], func(t *testing.T) {
			nativeSearchCheckCancellation(t, callerCancel)
		})
	}
}
func TestNativeSearchShadowMissingAndActorFallback(t *testing.T) {
	for _, who := range []string{"native", "human", "agent"} {
		t.Run(who, func(t *testing.T) {
			f, _ := nativeFixture(t)
			r := nativeSearchTestRuntime(t, "B2")
			f.svc.NativeSearchShadow = r
			q := nativeSearchTestInput()
			q.Query = "설계"
			token := who
			if who == "human" {
				token = f.human
			}
			if who == "agent" {
				token = f.agent
			}
			out, e := f.svc.Search(testCtx, token, q)
			if e != nil || len(out.Items) != 1 {
				t.Fatal(e)
			}
			got := r.Observations()[0]
			want := "native_service_role_required"
			if who == "native" {
				want = "missing_state"
			}
			if got.Status != "fallback" || got.Reason != want || !got.BaselineReturned {
				t.Fatal(got)
			}
		})
	}
}

func nativeSearchCheckServiceBaseline(t *testing.T, policy string) {
	t.Helper()
	f := newFixture(t)
	f.activeMemory("searchcanary raw-body-canary", "searchcanary")
	repo := &nativeSearchCountRepo{Repository: f.repo}
	f.svc.Repo = repo
	in := nativeSearchTestInput()
	off, e := f.svc.Search(testCtx, f.human, in)
	if e != nil || len(off.Items) != 1 {
		t.Fatal(e, len(off.Items))
	}
	beforeJobs := len(f.repo.rows(colJobs))
	beforeEval := len(f.repo.rows("evaluations"))
	runtime := nativeSearchTestRuntime(t, policy)
	f.svc.NativeSearchShadow = runtime
	on, e := f.svc.Search(testCtx, f.human, in)
	if e != nil {
		t.Fatal(e)
	}
	off.ContextID = ""
	on.ContextID = ""
	if !reflect.DeepEqual(off, on) {
		t.Fatal("shadow changed actual baseline")
	}
	if repo.calls != 2 || len(f.repo.rows(colJobs)) != beforeJobs+1 || len(f.repo.rows("evaluations")) != beforeEval {
		t.Fatal("retrieval/write ledger")
	}
	nativeSearchCheckObservation(t, runtime, policy)
}

func nativeSearchCheckStateService(t *testing.T, policy string) {
	t.Helper()
	f, in := nativeFixture(t)
	memory := f.repo.store[colMemories][recKey(in.Scope, in.Target.ID)]
	memory.Kind = "preference" // A timeless fixture makes separate wall-clock baseline calls exactly comparable.
	f.repo.store[colMemories][recKey(in.Scope, in.Target.ID)] = memory
	r := nativeSearchTestRuntime(t, policy)
	_, e := f.svc.ActivateNativeMemoryDynamics(testCtx, "native", NativeDynamicsActivationInput{Scope: in.Scope, Target: in.Target, Dimension: in.Dimension, Config: nativeSearchMatchingConfig(t, r), RequestID: "search-state"})
	if e != nil {
		t.Fatal(e)
	}
	q := nativeSearchTestInput()
	q.Query = "설계"
	q.IncludeInactive = false
	before := nativeSearchHash("jobs", f.repo.rows(colJobs))
	off, e := f.svc.Search(testCtx, "native", q)
	if e != nil || len(off.Items) != 1 {
		t.Fatal(e, len(off.Items))
	}
	jobs := len(f.repo.rows(colJobs))
	f.svc.NativeSearchShadow = r
	on, e := f.svc.Search(testCtx, "native", q)
	if e != nil {
		t.Fatal(e)
	}
	off.ContextID = ""
	on.ContextID = ""
	if !reflect.DeepEqual(off, on) {
		t.Fatal("actual composite changed")
	}
	got := r.Observations()
	if len(got) != 1 || got[0].Status != "selected" || got[0].SourceCount != 1 || got[0].PublishedSHA == "" {
		t.Fatal(got)
	}
	if len(f.repo.rows(colJobs)) != jobs+1 || before == "" || len(nativeKindRows(f, "consumption")) != 0 {
		t.Fatal("shadow wrote learning")
	}
}

func nativeSearchCheckCancellation(t *testing.T, callerCancel bool) {
	t.Helper()
	f, _ := nativeFixture(t)
	r := nativeSearchTestRuntime(t, "B2")
	// The blocked source waits for the deadline, so it must be long enough to reach that source first.
	r.config.Deadline = 100 * time.Millisecond
	f.svc.NativeSearchShadow = r
	q := nativeSearchTestInput()
	q.Query = "설계"
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	repo := &nativeSearchBlockedSourceRepo{Repository: f.repo}
	if callerCancel {
		repo.cancel = cancel
	}
	f.svc.Repo = repo
	out, e := f.svc.Search(ctx, "native", q)
	if !repo.sawDeadline {
		t.Fatal("source has no deadline")
	}
	got := r.Observations()
	if len(got) != 1 || got[0].Status != "fallback" {
		t.Fatal(got)
	}
	if callerCancel {
		if e == nil || got[0].Reason != "caller_canceled" || got[0].BaselineReturned || len(out.Items) != 0 {
			t.Fatal(e, got)
		}
	} else {
		if e != nil || len(out.Items) != 1 || got[0].Reason != "shadow_deadline" || !got[0].BaselineReturned {
			t.Fatal(e, got)
		}
	}
}

func nativeSearchCheckObservation(t *testing.T, runtime *NativeSearchShadowRuntime, policy string) {
	t.Helper()
	records := runtime.Observations()
	if len(records) != 1 || !records[0].BaselineReturned || records[0].BaselineSHA == "" {
		t.Fatal(records)
	}
	if policy == "B2" && records[0].Reason != "native_service_role_required" {
		t.Fatal(records)
	}
	if policy == "V3-P1-IR" && records[0].Reason != "missing_feature" {
		t.Fatal(records)
	}
	if (policy == "B0" || policy == "B1") && records[0].Status != "selected" {
		t.Fatal(records)
	}
	raw, _ := json.Marshal(records)
	if strings.Contains(string(raw), "searchcanary") || strings.Contains(string(raw), "raw-body-canary") {
		t.Fatal("raw telemetry")
	}
	records[0].SelectedRefs = append(records[0].SelectedRefs, "mutated")
	if reflect.DeepEqual(records, runtime.Observations()) {
		t.Fatal("mutable observations")
	}
}

func TestNativeSearchShadowHistory(t *testing.T) {
	for _, policy := range []string{"B0", "B1"} {
		t.Run(policy, func(t *testing.T) {
			f := newFixture(t)
			m := f.activeMemory("searchcanary history", "searchcanary")
			f.transition(f.human, colMemories, m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision, Status: "expired"})
			in := nativeSearchTestInput()
			off := f.search(f.human, in)
			r := nativeSearchTestRuntime(t, policy)
			f.svc.NativeSearchShadow = r
			on := f.search(f.human, in)
			off.ContextID = ""
			on.ContextID = ""
			if !reflect.DeepEqual(off, on) || len(on.History) != 1 {
				t.Fatal("history changed")
			}
			got := r.Observations()[0]
			if policy == "B0" && got.Status != "selected" {
				t.Fatal(got)
			}
			if policy == "B1" && got.Reason != "unsupported_context" {
				t.Fatal(got)
			}
		})
	}
}

func TestNativeSearchShadowTwoNativeStates(t *testing.T) {
	f, in := nativeFixture(t)
	r := nativeSearchTestRuntime(t, "V3-P1-NOTIME")
	other := f.activeMemory("설계 second independent candidate", "설계")
	targets := []Ref{in.Target, {Collection: colMemories, ID: other.ID, Revision: other.Revision}}
	for _, target := range targets {
		_, e := f.svc.ActivateNativeMemoryDynamics(testCtx, "native", NativeDynamicsActivationInput{Scope: scopeA, Target: target, Dimension: in.Dimension, Config: nativeSearchMatchingConfig(t, r), RequestID: "search-two-" + target.ID})
		if e != nil {
			t.Fatal(e)
		}
	}
	in.Passed = true
	_, capture := nativeAccept(t, f, in)
	receipt, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", scopeA, capture)
	if e != nil || !receipt.LearningConsumed {
		t.Fatal("positive native fixture", e, receipt.Disposition)
	}
	snap := nativeSearchTwoStateSnapshot(t, f, targets)
	q := nativeSearchTestInput()
	q.Query = "설계"
	nativeSearchAssertTwoStateSources(t, f, q, snap, r)
	before := nativeSearchHash("learning", nativeKindRows(f, "consumption"))
	f.svc.NativeSearchShadow = r
	out, e := f.svc.Search(testCtx, "native", q)
	if e != nil || len(out.Items) != 2 {
		t.Fatal(e, len(out.Items))
	}
	got := r.Observations()
	if len(got) != 1 || got[0].SourceCount != 2 || got[0].CandidateCount != 2 || got[0].Status != "selected" || before != nativeSearchHash("learning", nativeKindRows(f, "consumption")) {
		t.Fatal(got)
	}
}

func nativeSearchTwoStateSnapshot(t *testing.T, f *fixture, targets []Ref) *nativeSearchSnapshot {
	t.Helper()
	hits := []Hit{}
	for _, target := range targets {
		m, e := f.repo.Get(testCtx, colMemories, scopeA, target.ID)
		if e != nil {
			t.Fatal(e)
		}
		hits = append(hits, HitFromRecord(colMemories, m))
	}
	snap, code := nativeFreezeSearch(scopeA, [][]Hit{hits}, nil)
	if code != "" {
		t.Fatal(code)
	}
	return snap
}

func nativeSearchAssertTwoStateSources(t *testing.T, f *fixture, q SearchInput, snap *nativeSearchSnapshot, r *NativeSearchShadowRuntime) {
	t.Helper()
	_, _, _, code := f.svc.nativePublishSearchValues(testCtx, "native", q, Decision{Role: roleService, PrincipalID: "native-consumer"}, snap, r.config)
	if code != "native_source_unauthorized" {
		t.Fatal("partial actor unexpectedly bound", code)
	}
	actor, e := f.svc.authorize(testCtx, "native", "search:memories", scopeA, "", "")
	if e != nil {
		t.Fatal(e)
	}
	values, _, sha, code := f.svc.nativePublishSearchValues(testCtx, "native", q, actor, snap, r.config)
	if code != "" || sha == "" || len(values) != 2 {
		t.Fatal(code, len(values))
	}
	if values[0].Value == nil || values[1].Value == nil || *values[0].Value <= *values[1].Value || values[0].Target.ID == values[1].Target.ID {
		t.Fatal("source factory did not bind distinct native states")
	}
}
