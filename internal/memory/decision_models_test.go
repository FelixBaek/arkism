//go:build linux

package memory

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// decisionServer answers every decision request with relation = choice.
func decisionServer(t *testing.T, choice *string, calls *atomic.Int32) *JevJudge {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "new_notes") || !strings.Contains(string(body), "existing_observations") {
			t.Errorf("decision request without the notes and observations: %s", body)
		}
		io.WriteString(w, `{"model":"jev-1.13.0","answers":{"relation":{"type":"choice","choice":"`+*choice+`","probabilities":{"`+*choice+`":0.9}}},"usage":{"input_tokens":40}}`)
	}))
	t.Cleanup(srv.Close)
	return &JevJudge{APIKey: "decision-key", Endpoint: srv.URL}
}

func TestConsolidationDecisionModelSettlesRestatements(t *testing.T) {
	f, llm, ms := obsFixture(t)
	// One observation already covers two of the three staging notes.
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID))}
	if out, err := f.consolidate(f.human, false); err != nil || len(out.Proposed) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	choice, calls := "restates", &atomic.Int32{}
	f.svc.Judges = map[string]SkillJudge{"jev": decisionServer(t, &choice, calls)}
	// Off by default: the group goes to the model, the engine is not asked.
	llm.answers = append(llm.answers, obsAnswer(t))
	if out, err := f.consolidate(f.human, false); err != nil || out.Calls != 1 || calls.Load() != 0 {
		t.Fatalf("off: %+v %v calls=%d", out, err, calls.Load())
	}
	bad := map[string]string{"consolidation": "fixed"}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: -1, DecisionModels: &bad}, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown engine is refused: %v", err)
	}
	on := map[string]string{"consolidation": "jev"}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: -1, DecisionModels: &on}, false); err != nil {
		t.Fatal(err)
	}
	prompts := len(llm.prompts)
	out, err := f.consolidate(f.human, false)
	if err != nil || out.Calls != 0 || len(llm.prompts) != prompts || calls.Load() != 1 || len(out.Rejected) != 1 || out.Rejected[0].Code != "decision_restates" || out.Rejected[0].Target != "jev" {
		t.Fatalf("restates: %+v %v calls=%d", out, err, calls.Load())
	}
	// Anything else still goes to the model.
	choice = "changes"
	llm.answers = append(llm.answers, obsAnswer(t))
	if out, err = f.consolidate(f.human, false); err != nil || out.Calls != 1 || calls.Load() != 2 {
		t.Fatalf("changes: %+v %v calls=%d", out, err, calls.Load())
	}
	// An engine the server cannot run leaves the model in charge.
	f.svc.Judges = nil
	llm.answers = append(llm.answers, obsAnswer(t))
	if out, err = f.consolidate(f.human, false); err != nil || out.Calls != 1 {
		t.Fatalf("no engine: %+v %v", out, err)
	}
	view, err := f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA)
	if err != nil || view.Config.DecisionModels["consolidation"] != "jev" || view.Sources["decision_models"] != spaceSourceSpace {
		t.Fatalf("effective: %+v %v", view, err)
	}
}

func TestPickedChoice(t *testing.T) {
	if got := pickedChoice(jevAnswer{Choice: "new", Probabilities: map[string]float64{"new": 0.3, "chatter": 0.7}}); got != "chatter" {
		t.Fatal(got)
	}
	if got := pickedChoice(jevAnswer{Choice: "new"}); got != "new" {
		t.Fatal(got)
	}
}

// itemDecisionServer answers each per-item question with pick(item text) and every other
// question (enough) with other; requests counts the calls.
func itemDecisionServer(t *testing.T, pick func(text string) string, other string, requests *atomic.Int32) *JevJudge {
	return itemQuestionServer(t, func(text, suffix string) string {
		if suffix != "" {
			return other
		}
		return pick(text)
	}, other, requests)
}

// itemQuestionServer answers a per-item question (h3, h3_repeat) with pick(item text, suffix) and every other with other.
func itemQuestionServer(t *testing.T, pick func(text, suffix string) string, other string, requests *atomic.Int32) *JevJudge {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var in struct {
			State struct {
				Items map[string]struct {
					Text string `json:"text"`
				} `json:"items"`
			} `json:"state"`
			Questions map[string]any `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		answers := map[string]any{}
		for key := range in.Questions {
			choice := other
			if key == "h0"+decisionRepeat.suffix {
				t.Error("the first item has nothing above it to repeat")
			}
			base, _, _ := strings.Cut(key, "_")
			if item, ok := in.State.Items[base]; ok {
				choice = pick(item.Text, strings.TrimPrefix(key, base))
			}
			answers[key] = map[string]any{"type": "choice", "choice": choice, "probabilities": map[string]float64{choice: 0.9}}
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "jev-1.13.0", "answers": answers, "usage": map[string]any{"input_tokens": 10}})
	}))
	t.Cleanup(srv.Close)
	return &JevJudge{APIKey: "decision-key", Endpoint: srv.URL}
}

func setDecisionModels(t *testing.T, f *fixture, m map[string]string) {
	t.Helper()
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: -1, DecisionModels: &m}, false); err != nil {
		t.Fatal(err)
	}
}

func TestMentalModelRefreshDecisionDropsUnrelated(t *testing.T) {
	f, llm, ms := obsFixture(t)
	lunch := f.activeMemory("lunch for the staging team is at noon")
	requests := &atomic.Int32{}
	related := func(text string) string {
		if strings.Contains(text, "lunch") {
			return "unrelated"
		}
		return "relevant"
	}
	f.svc.Judges = map[string]SkillJudge{"jev": itemDecisionServer(t, related, "", requests)}
	mm, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "Deploys", Query: "staging", Refresh: MentalModelRefresh{Mode: mmRefreshManual}})
	if err != nil {
		t.Fatal(err)
	}
	answer, _ := json.Marshal(mentalModelAnswer{Summary: "Staging needs the flag.", Citations: []string{ms[0].ID}})
	// Off by default: everything recalled goes to the model, the engine is not asked.
	llm.answers = []string{string(answer)}
	if _, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA, DryRun: true}); err != nil || requests.Load() != 0 || !strings.Contains(llm.prompts[0], lunch.ID) {
		t.Fatalf("off: %v requests=%d", err, requests.Load())
	}
	setDecisionModels(t, f, map[string]string{decisionTaskRefresh: "jev"})
	llm.answers = append(llm.answers, string(answer))
	if _, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA, DryRun: true}); err != nil || requests.Load() != 1 {
		t.Fatalf("on: %v requests=%d", err, requests.Load())
	}
	if prompt := llm.prompts[1]; strings.Contains(prompt, lunch.ID) || !strings.Contains(prompt, ms[0].ID) {
		t.Fatalf("the unrelated memory must stay out of the prompt:\n%s", prompt)
	}
	// Nothing related: no model call at all.
	f.svc.Judges = map[string]SkillJudge{"jev": itemDecisionServer(t, func(string) string { return "unrelated" }, "", requests)}
	if v, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA, DryRun: true}); err != nil || v.LastOutcome == nil || v.LastOutcome.Code != mmOutcomeNoFacts || len(llm.prompts) != 2 {
		t.Fatalf("all unrelated: %v prompts=%d", err, len(llm.prompts))
	}
	// An engine that fails leaves the model in charge with everything recalled.
	f.svc.Judges = map[string]SkillJudge{"jev": &JevJudge{APIKey: "k", Endpoint: "http://127.0.0.1:1"}}
	llm.answers = append(llm.answers, string(answer))
	if _, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA, DryRun: true}); err != nil || !strings.Contains(llm.prompts[2], lunch.ID) {
		t.Fatalf("failed engine: %v", err)
	}
}

func TestMentalModelDeltaDecisionGatesNewMemories(t *testing.T) {
	f, llm, ms := obsFixture(t)
	mm, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "Staging", Query: "staging flag", Refresh: MentalModelRefresh{Mode: mmRefreshManual, Strategy: mmStrategyDelta}})
	if err != nil {
		t.Fatal(err)
	}
	answer := func(text string, ids ...string) string {
		raw, _ := json.Marshal(mentalModelAnswer{Summary: text, Citations: ids})
		return string(raw)
	}
	llm.answers = []string{answer("Staging needs the flag.", ms[0].ID)}
	v, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA})
	if err != nil {
		t.Fatal(err)
	}
	if v, err = f.svc.ReviewMentalModel(testCtx, f.human, mm.ID, MentalModelReviewInput{Scope: scopeA, ExpectedRevision: v.Revision, Action: mmReviewApprove}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	fresh := f.activeMemory("deploy to staging requires the staging flag and a second reviewer")
	verdict, requests := "no_change", &atomic.Int32{}
	f.svc.Judges = map[string]SkillJudge{"jev": itemDecisionServer(t, func(string) string { return verdict }, "", requests)}
	setDecisionModels(t, f, map[string]string{decisionTaskDelta: "jev"})
	prompts := len(llm.prompts)
	// The engine says the new memory cannot change the text: no model call, nothing to review.
	if _, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA}); err != nil || len(llm.prompts) != prompts || requests.Load() != 1 {
		t.Fatalf("no change: %v prompts=%d requests=%d", err, len(llm.prompts), requests.Load())
	}
	got, _ := f.svc.GetMentalModel(testCtx, f.human, scopeA, mm.ID)
	if got.LastOutcome == nil || got.LastOutcome.Code != mmOutcomeNoFacts {
		t.Fatalf("outcome: %+v", got.LastOutcome)
	}
	verdict = "could_change"
	llm.answers = append(llm.answers, answer("Staging needs the flag and a second reviewer.", ms[0].ID, fresh.ID))
	if v, err = f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA}); err != nil || v.Draft == nil || v.Draft.Action != "delta_refresh" {
		t.Fatalf("could change: %+v %v", v, err)
	}
}

func TestReflectDecisionPrunesAndStops(t *testing.T) {
	f, llm := reflectFixture(t)
	needle := f.activeMemory("reflect needle: the deploy runs on Fridays", "needle")
	other := f.activeMemory("reflect needle: lunch is at noon", "needle")
	requests := &atomic.Int32{}
	f.svc.Judges = map[string]SkillJudge{"jev": itemDecisionServer(t, func(text string) string {
		if strings.Contains(text, "lunch") {
			return "unrelated"
		}
		return "relevant"
	}, "enough", requests)}
	setDecisionModels(t, f, map[string]string{decisionTaskReflect: "jev"})
	llm.answers = []string{"Fridays [" + needle.ID + "]"}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle deploy day?", MaxIterations: 3})
	if err != nil || requests.Load() != 1 {
		t.Fatalf("%v requests=%d", err, requests.Load())
	}
	if len(out.Retrieved) != 1 || out.Retrieved[0].ID != needle.ID || strings.Contains(llm.prompts[0], other.ID) {
		t.Fatalf("the unrelated memory is pruned: %+v", out.Retrieved)
	}
	cfg, _ := f.svc.spaceSettings(testCtx, scopeA)
	run := &reflectRun{in: ReflectInput{Scope: scopeA, Query: "needle", MaxIterations: 3, space: &cfg}, graph: map[string]bool{other.ID: true},
		hits: []Hit{searchHit(needle, colMemories, "fts"), searchHit(other, colMemories, "fts")}}
	f.svc.decisionReflect(testCtx, f.human, run)
	if run.in.MaxIterations != 1 || len(run.hits) != 1 || run.graph[other.ID] {
		t.Fatalf("enough ends the loop after one call: %+v", run.in)
	}
	// prompt_only never asks the engine.
	if _, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true}); err != nil || requests.Load() != 2 {
		t.Fatalf("prompt only: %v requests=%d", err, requests.Load())
	}
}

// The recall decision engine prunes and dedups the fused rows only when the space picks it and the
// search (or the space's recall defaults) asks; the first row is never asked to be a repeat.
func TestRecallDecisionPruneDedup(t *testing.T) {
	f, _ := modelRerankFixture(t)
	f.svc.Wiki = nil
	requests := &atomic.Int32{}
	f.svc.Judges = map[string]SkillJudge{"jev": itemQuestionServer(t, func(text, suffix string) string {
		switch {
		case suffix == "_repeat" && strings.Contains(text, "follow up"):
			return "repeats"
		case suffix == "_repeat":
			return "adds"
		case strings.Contains(text, "note"):
			return "unrelated"
		}
		return "relevant"
	}, "", requests)}
	search := func(o *RerankOptions) ContextBundle {
		t.Helper()
		out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true, RerankOptions: o})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	both := &RerankOptions{Prune: true, Dedup: true}
	base := search(both)
	if len(base.Items) < 3 || requests.Load() != 0 {
		t.Fatalf("off by default: %v requests=%d", ids(base.Items), requests.Load())
	}
	setDecisionModels(t, f, map[string]string{decisionTaskRecall: "jev"})
	if out := search(nil); len(out.Items) != len(base.Items) || requests.Load() != 0 {
		t.Fatalf("no options, no call: %v", ids(out.Items))
	}
	dedup := search(&RerankOptions{Dedup: true})
	if requests.Load() != 1 || slices.Contains(ids(dedup.Items), "delta") || !slices.Contains(ids(dedup.Items), "alpha") || dedup.Explain.Dropped["decision_dedup"] != 1 {
		t.Fatalf("dedup: %v %+v", ids(dedup.Items), dedup.Explain.Dropped)
	}
	out := search(both)
	if slices.Contains(ids(out.Items), "alpha") || slices.Contains(ids(out.Items), "delta") || out.Explain.Dropped["decision_prune"] != 1 || !slices.Contains(ids(out.Items), "secret") || !slices.Contains(ids(out.Items), "personal") {
		t.Fatalf("prune+dedup (restricted and private rows are never sent, so they stay): %v %+v", ids(out.Items), out.Explain.Dropped)
	}
	if usage := ledgerOf(f, "wiki_decision_recall"); len(usage) != 2 {
		t.Fatalf("each call is recorded: %+v", usage)
	}
	// The space's recall defaults ask too; a failed engine keeps every row.
	var q SpaceConfigInput
	if err := json.Unmarshal([]byte(`{"expected_revision":-1,"recall_quality":{"rerank_options":{"dedup":true}}}`), &q); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, q, false); err != nil {
		t.Fatal(err)
	}
	if out := search(nil); slices.Contains(ids(out.Items), "delta") || requests.Load() != 3 {
		t.Fatalf("space default: %v", ids(out.Items))
	}
	f.svc.Judges = map[string]SkillJudge{"jev": &JevJudge{APIKey: "k", Endpoint: "http://127.0.0.1:1"}}
	if out := search(both); len(out.Items) != len(base.Items) {
		t.Fatalf("failed engine: %v", ids(out.Items))
	}
}
