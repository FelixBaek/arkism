//go:build linux

package memory

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http/httptest"
	"testing"
)

func TestSignalExplanationHTTPMatchesSearchAndDedup(t *testing.T) {
	f, m, in := outcomeFixture(t)
	for i := 0; i < 4; i++ {
		if i > 0 && i < 3 {
			in = distinctOutcomeTask(t, f, in, fmt.Sprintf("explain-%d", i))
		}
		in.EventID = fmt.Sprintf("explain-event-%d", i)
		in.Passed = i != 0
		if _, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in); e != nil {
			t.Fatal(e)
		}
	}
	setRecallQuality(t, f, SpaceRecallQuality{SignalPolicy: &SignalPolicy{Dimension: "work_task", WindowDays: 30, MinSamples: 3}})
	path := "/v1/memories/" + m.ID + "/signal-policy?workspace_id=ws-a&project_id=proj-a"
	request := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, r)
		return w
	}
	w := request(f.human)
	var v MemorySignalExplanation
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if v.Mode != "recent_first_trace" || v.HistoricalTotal != 4 || v.WindowEvents != 4 || v.EligibleTotal != 3 || v.EligiblePassed != 2 || v.ExcludedWindowEvents != 1 || !v.MinimumMet {
		t.Fatal(v)
	}
	checkSearchOutcomeMatches(t, f, m, v)
	if got := request(""); got.Code != 401 {
		t.Fatal("anonymous explanation", got.Code)
	}
	setRecallQuality(t, f, SpaceRecallQuality{SignalPolicy: &SignalPolicy{Disabled: true}})
	legacy, e := f.svc.ExplainMemorySignal(testCtx, f.human, scopeA, m.ID)
	if e != nil || legacy.Mode != "legacy" {
		t.Fatal(legacy, e)
	}
	f.svc.SignalRankingOff = true
	off, e := f.svc.ExplainMemorySignal(testCtx, f.human, scopeA, m.ID)
	if e != nil || off.Mode != "disabled" || off.EffectiveFactor != 1 {
		t.Fatal(off, e)
	}
}
func checkSearchOutcomeMatches(t *testing.T, f *fixture, m Record, v MemorySignalExplanation) {
	t.Helper()
	out := f.search(f.human, SearchInput{Scope: scopeA, Query: m.Text, Modes: []string{"fts"}, Explain: true})
	found := false
	for _, hit := range out.Items {
		if hit.ID == m.ID {
			found = true
			if hit.Explain == nil || hit.Explain.Outcome == nil || math.Abs(*hit.Explain.Outcome-v.EffectiveFactor) > 1e-9 {
				t.Fatal("explanation differs from search", hit, v)
			}
		}
	}
	if !found {
		t.Fatal("missing memory")
	}
}

func TestSignalPolicyExplicitDisableOverridesInherited(t *testing.T) {
	f := newFixture(t)
	f.svc.SpaceTemplate = &SpaceTemplate{Config: SpaceConfig{RecallQuality: &SpaceRecallQuality{SignalPolicy: &SignalPolicy{Dimension: "work_task", WindowDays: 30, MinSamples: 3}}}}
	setRecallQuality(t, f, SpaceRecallQuality{SignalPolicy: &SignalPolicy{Disabled: true}})
	settings, e := f.svc.searchSpaceSettings(testCtx, f.human, scopeA)
	if e != nil || settings.RecallQuality.SignalPolicy == nil || !settings.RecallQuality.SignalPolicy.Disabled {
		t.Fatal(settings, e)
	}
	for _, p := range []*SignalPolicy{{Disabled: true, Dimension: "work_task"}, {Disabled: true, WindowDays: 1}, {Disabled: true, MinSamples: 3}} {
		if p.valid() {
			t.Fatal("ambiguous disabled policy accepted")
		}
	}
	if !(&SignalPolicy{Disabled: true}).valid() {
		t.Fatal("explicit disabled rejected")
	}
}

func TestSignalPolicyHTTPContractEvidence(t *testing.T) {
	cases := signalPolicyHTTPResponses(t)
	if len(cases) != 2 || cases[0].Status != 401 || cases[1].Status != 200 {
		t.Fatal(cases)
	}
}

func TestSignalExplanationBaselineDoesNotPromiseSearchEligibility(t *testing.T) {
	f, m, _ := outcomeFixture(t)
	q := SearchInput{Scope: scopeA, Query: m.Text, Modes: []string{"fts"}}
	before := f.search(f.human, q)
	if len(before.Items) != 1 {
		t.Fatal("positive search control", before.Items)
	}
	source := m.Sources[0]
	src, err := f.repo.Get(testCtx, "sources", scopeA, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Delete(testCtx, "sources", scopeA, source.SourceID, src.Revision); err != nil {
		t.Fatal(err)
	}
	v, err := f.svc.ExplainMemorySignal(testCtx, f.human, scopeA, m.ID)
	if err != nil || !v.Applicable {
		t.Fatal("historical baseline explanation unexpectedly hidden", v, err)
	}
	after := f.search(f.human, q)
	if len(after.Items) != 0 {
		t.Fatal("source-ineligible memory appeared in Search", after.Items)
	}
}
