package memory

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type nativeStageLanesRepo struct {
	Repository
	lanes [][]Hit
	calls int
}

func (r *nativeStageLanesRepo) SearchCollection(_ context.Context, _ string, _ SearchInput, _ []float64, _ string) ([][]Hit, error) {
	r.calls++
	return nativeClone(r.lanes)
}
func nativeStageFixture(t *testing.T) (*fixture, Record, Record, *nativeStageLanesRepo) {
	t.Helper()
	f := newFixture(t)
	f.svc.Embedder = nil
	a := viewMemory(t, f, scopeA, "viewcanary stage-a raw-stage-body-canary")
	b := viewMemory(t, f, scopeA, "viewcanary stage-b raw-stage-body-canary")
	ah := HitFromRecord(colMemories, a)
	bh := HitFromRecord(colMemories, b)
	ah.Reasons = []string{reasonExactKeyword}
	bh.Reasons = []string{reasonExactKeyword}
	af := ah
	bf := bh
	af.Reasons = []string{reasonTextSearch}
	bf.Reasons = []string{reasonTextSearch}
	repo := &nativeStageLanesRepo{Repository: f.repo, lanes: [][]Hit{{ah, ah, bh}, {bf, af}}}
	f.svc.Repo = repo
	f.svc.NativeSearchShadow = nativeSearchTestRuntime(t, "B0")
	return f, a, b, repo
}
func TestNativeSearchStagesRankLimitPackingAndPrivacy(t *testing.T) {
	f, a, b, repo := nativeStageFixture(t)
	in := nativeSearchTestInput()
	in.Limit = 1
	repo.lanes = [][]Hit{{repo.lanes[0][0]}, {repo.lanes[1][0]}}
	in.IncludeInactive = false
	out := f.search(f.human, in)
	records := f.svc.NativeSearchShadow.StagesV2()
	if len(records) != 1 || repo.calls != 1 {
		t.Fatal("single candidate construction")
	}
	v := records[0]
	da := contextScopedRefSHA(scopeA, Ref{Collection: colMemories, ID: a.ID, Revision: a.Revision})
	db := contextScopedRefSHA(scopeA, Ref{Collection: colMemories, ID: b.ID, Revision: b.Revision})
	if !reflect.DeepEqual(v.FullRankedRefs, []string{da, db}) || !reflect.DeepEqual(v.PrepackRefs, []string{da}) || !reflect.DeepEqual(v.EmittedCurrentRefs, []string{da}) || len(v.EmittedHistoryRefs) != 0 || !v.BaselineReturned || !reflect.DeepEqual(v.BaselineCurrentRefs, []string{da}) || len(out.Items) != 1 {
		t.Fatal("rank/limit/emission conflated", v)
	}
	if !reflect.DeepEqual(f.svc.NativeSearchShadow.Observations()[0].SelectedRefs, v.FullRankedRefs) {
		t.Fatal("old selected field meaning changed")
	}
	raw, _ := json.Marshal(v)
	for _, canary := range []string{"raw-stage-body-canary", "viewcanary", a.ID, b.ID, in.Query} {
		if strings.Contains(string(raw), canary) {
			t.Fatal("raw telemetry leaked")
		}
	}
	records[0].FullRankedRefs[0] = "tampered"
	if f.svc.NativeSearchShadow.StagesV2()[0].FullRankedRefs[0] != da {
		t.Fatal("telemetry alias")
	}
}
func TestNativeSearchStagesCharAndEmptyTokenEmission(t *testing.T) {
	for _, kind := range []string{"chars", "tokens"} {
		t.Run(kind, func(t *testing.T) {
			f, _, _, repo := nativeStageFixture(t)
			in := nativeSearchTestInput()
			in.Limit = 2
			repo.lanes[0] = []Hit{repo.lanes[0][0], repo.lanes[0][2]}
			in.IncludeInactive = false
			if kind == "chars" {
				in.MaxChars = 1
			} else {
				in.BudgetTokens = 20
			}
			out := f.search(f.human, in)
			v := f.svc.NativeSearchShadow.StagesV2()[0]
			if len(v.FullRankedRefs) != 2 || len(v.PrepackRefs) != 2 {
				t.Fatal("prepack lost candidates")
			}
			want := 1
			if kind == "tokens" {
				want = 0
			}
			if len(v.EmittedCurrentRefs) != want || len(v.BaselineCurrentRefs) != want || len(out.Items) != want {
				t.Fatal("packing loss not exact", kind, v)
			}
		})
	}
}
func TestNativeSearchStagesHistoryMetadataNotReaderInput(t *testing.T) {
	f, old, _, repo := nativeStageFixture(t)
	row := f.transition(f.human, colMemories, old.ID, ChangeInput{Scope: scopeA, ExpectedRevision: old.Revision, Status: "rejected"})
	h := HitFromRecord(colMemories, row)
	h.Reasons = []string{reasonExactKeyword}
	repo.lanes = [][]Hit{{h}}
	in := nativeSearchTestInput()
	out := f.search(f.human, in)
	v := f.svc.NativeSearchShadow.StagesV2()[0]
	if len(out.History) != 1 || len(v.EmittedHistoryRefs) != 1 || len(v.EmittedCurrentRefs) != 0 || len(v.BaselineHistoryRefs) != 1 || len(v.BaselineCurrentRefs) != 0 {
		t.Fatal("history separation", v)
	}
	if strings.Contains(out.ContextData, old.Text) || v.ReaderAcknowledgement != "unknown" || v.ActualUse != "unknown" {
		t.Fatal("history promoted to delivery")
	}
}

func TestNativeSearchStagesDuplicateOrdinalGap(t *testing.T) {
	f, a, b, repo := nativeStageFixture(t)
	in := nativeSearchTestInput()
	in.Limit = 3
	in.IncludeInactive = false
	out := f.search(f.human, in)
	v := f.svc.NativeSearchShadow.StagesV2()[0]
	da := contextScopedRefSHA(scopeA, Ref{Collection: colMemories, ID: a.ID, Revision: a.Revision})
	db := contextScopedRefSHA(scopeA, Ref{Collection: colMemories, ID: b.ID, Revision: b.Revision})
	if repo.calls != 1 || len(out.Items) != 2 || !reflect.DeepEqual(v.FullRankedRefs, []string{da, db}) || !reflect.DeepEqual(v.PrepackRefs, []string{da, db}) {
		t.Fatal("duplicate lane gap/fullC stages", v)
	}
	// Original ordinal a=2/61+1/62; b=2/63+1/61. No compression of duplicate position2.
	if out.Items[0].ID != a.ID || nativeBridgeBits(out.Items[0].Score) != "3fa90b81d3dde27d" || nativeBridgeBits(out.Items[1].Score) != "3fa8a5bcb81f712e" {
		t.Fatal("duplicate ordinal score")
	}
}
func TestNativeSearchStagesUnattributedTransactionAbort(t *testing.T) {
	f, _, _, _ := nativeStageFixture(t)
	f.repo.failAudit = true
	in := nativeSearchTestInput()
	in.Limit = 3
	in.IncludeInactive = false
	if out, e := f.svc.Search(testCtx, f.human, in); e == nil || out.ContextData != "" {
		t.Fatal("audit failure returned context")
	}
	v := f.svc.NativeSearchShadow.StagesV2()[0]
	if v.BaselineReturned || !v.FinalEntered || len(v.FinalRejected) != 0 || v.FinalErrorCode != "transaction_abort" || v.BaselineCurrentRefs != nil || len(v.BaselinePreFinalCurrentRefs) != 2 {
		t.Fatal("guessed rejected ref/return", v)
	}
}

func TestNativeSearchStagesFallbackStillObservesBaselinePreFinal(t *testing.T) {
	f, a, _, _ := nativeStageFixture(t)
	attempt := &nativeSearchAttempt{record: NativeSearchShadowObservation{Status: "fallback", Reason: "unsupported_collection"}}
	baseline := ContextBundle{ContextID: "synthetic-stage-context", Items: []Hit{HitFromRecord(colMemories, a)}, History: []HistoricalHit{}}
	attempt.compute(testCtx, f.svc, f.human, SearchInput{}, Decision{}, baseline)
	expected := []string{contextScopedRefSHA(a.Scope, Ref{Collection: colMemories, ID: a.ID, Revision: a.Revision})}
	if !reflect.DeepEqual(attempt.stages.BaselinePreFinalCurrentRefs, expected) || attempt.stages.BaselinePreFinalHistoryRefs == nil || attempt.stages.FullRankedRefs != nil || attempt.record.Status != "fallback" {
		t.Fatal("known baseline observations erased by unavailable shadow snapshot")
	}
}
