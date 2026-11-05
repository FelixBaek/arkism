package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRerankChoiceJSON(t *testing.T) {
	for raw, want := range map[string]RerankChoice{`true`: rerankOn, `false`: rerankOff, `"model"`: rerankModel, `null`: rerankDefault} {
		var in SearchInput
		if err := json.Unmarshal([]byte(`{"rerank":`+raw+`}`), &in); err != nil || in.Rerank != want {
			t.Fatal(raw, in.Rerank, err)
		}
		if out, _ := json.Marshal(in); want != rerankDefault && !strings.Contains(string(out), `"rerank":`+raw) {
			t.Fatal(string(out))
		}
	}
	var in SearchInput
	if err := json.Unmarshal([]byte(`{"rerank":"llm"}`), &in); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	on := true
	if modelRerankOn(SearchInput{}) || !modelRerankOn(SearchInput{Rerank: rerankModel}) || !modelRerankOn(SearchInput{space: &SpaceConfig{RerankModel: &on}}) ||
		modelRerankOn(SearchInput{Rerank: rerankOff, space: &SpaceConfig{RerankModel: &on}}) {
		t.Fatal("modelRerankOn")
	}
}

func TestParseModelOrder(t *testing.T) {
	if got, ok := parseModelOrder("```json\n{\"order\":[3,1]}\n```", 3); !ok || !slices.Equal(got, []int{3, 1, 2}) {
		t.Fatal(got, ok)
	}
	for _, bad := range []string{`{"order":[1,1]}`, `{"order":[4]}`, `{"order":[0]}`, `{"order":[]}`, `{"order":[1],"why":"x"}`, `order: 1,2`, `{"order":[1,2,3,1]}`} {
		if got, ok := parseModelOrder(bad, 3); ok {
			t.Fatal(bad, got)
		}
	}
}

// modelRerankFixture: memories the transfer gate lets out plus a restricted and a private one, all matching "alpha".
func modelRerankFixture(t *testing.T, answers ...string) (*fixture, *fakeWikiLLM) {
	f, _ := recallFixture(t)
	a := f.auth.actors[f.human]
	a.verbs["generate"] = true
	f.auth.actors[f.human] = a
	internal := func(r *Record) { r.Sensitivity = "internal" }
	f.editRecord("sources", f.editRecord(colMemories, "alpha", internal).Sources[0].SourceID, internal)
	f.editRecord(colMemories, "delta", internal)
	secret, _ := f.repo.Get(testCtx, colMemories, scopeA, "alpha")
	secret.ID, secret.Text, secret.Sensitivity = "secret", "alpha deploy secret plan", "restricted"
	if err := f.repo.Insert(testCtx, colMemories, secret); err != nil {
		t.Fatal(err)
	}
	personal := secret
	personal.ID, personal.Text, personal.Sensitivity = "personal", "alpha deploy personal note", "private"
	if err := f.repo.Insert(testCtx, colMemories, personal); err != nil {
		t.Fatal(err)
	}
	llm := &fakeWikiLLM{answers: answers}
	f.svc.Wiki = llm
	return f, llm
}

func TestRecallModelRerankAppliesAndFallsBack(t *testing.T) {
	f, llm := modelRerankFixture(t, `{"order":[2,1]}`, `not json`)
	base, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true})
	if err != nil || len(base.Items) < 2 || len(llm.prompts) != 0 || base.Explain.ModelRerank != "" {
		t.Fatal(ids(base.Items), llm.prompts, err)
	}
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true, Rerank: rerankModel})
	if err != nil || out.Explain.ModelRerank != "applied" || len(llm.prompts) != 1 {
		t.Fatal(out.Explain, llm.prompts, err)
	}
	// Only the two shareable memories were sent; the model put the second first.
	prompt := llm.prompts[0]
	if strings.Contains(prompt, "secret plan") || strings.Contains(prompt, "personal note") || !strings.Contains(prompt, "Query: alpha deploy") || !strings.Contains(prompt, "[2]") || strings.Contains(prompt, "[3]") {
		t.Fatal(prompt)
	}
	sent := []string{}
	for _, h := range base.Items {
		if h.ID != "secret" && h.ID != "personal" && len(sent) < 2 {
			sent = append(sent, h.ID)
		}
	}
	if pos := slices.Index(ids(out.Items), sent[1]); pos > slices.Index(ids(out.Items), sent[0]) || out.Items[pos].Explain.ModelRank != 1 {
		t.Fatal(ids(base.Items), ids(out.Items), out.Items[pos].Explain)
	}
	if usage := ledgerOf(f, modelRerankOperation); len(usage) != 1 {
		t.Fatalf("%+v", usage)
	}
	// An unreadable answer keeps the fused order.
	failed, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true, Rerank: rerankModel})
	if err != nil || failed.Explain.ModelRerank != "failed" || !slices.Equal(ids(failed.Items), ids(base.Items)) {
		t.Fatal(failed.Explain, ids(failed.Items), err)
	}
	// rerank:false wins over everything; no model: the fused order with the reason.
	f.svc.Wiki = nil
	none, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true, Rerank: rerankModel})
	if err != nil || none.Explain.ModelRerank != "model_not_configured" || !slices.Equal(ids(none.Items), ids(base.Items)) {
		t.Fatal(none.Explain, err)
	}
}

func TestRecallModelRerankGates(t *testing.T) {
	f, llm := modelRerankFixture(t, `{"order":[1,2]}`)
	in := SearchInput{Scope: scopeA, Query: "alpha deploy", Limit: 5, Rerank: rerankModel}
	rows := []ScoredHit{}
	for _, id := range []string{"alpha", "delta"} {
		r, _ := f.repo.Get(testCtx, colMemories, scopeA, id)
		rows = append(rows, ScoredHit{Hit: searchHit(r, colMemories, reasonExactKeyword), Score: 0.03})
	}
	// Without the generate permission nothing is sent.
	a := f.auth.actors[f.human]
	a.verbs["generate"] = false
	f.auth.actors[f.human] = a
	if status, order := f.svc.modelRerankOrder(testCtx, f.human, in, rows); status != "denied" || order != nil || len(llm.prompts) != 0 {
		t.Fatal(status, order)
	}
	a.verbs["generate"] = true
	// A query that may not leave the host is not sent either.
	if status, _ := f.svc.modelRerankOrder(testCtx, f.human, SearchInput{Scope: scopeA, Query: "token sk-ant-api03-" + strings.Repeat("a", 40), Rerank: rerankModel}, rows); status != "denied" {
		t.Fatal(status)
	}
	if status, _ := f.svc.modelRerankOrder(testCtx, f.human, in, rows); status != "applied" {
		t.Fatal(status)
	}
	// The day's cap counts this project's ledger rows; at the cap nothing more is sent.
	for i := range modelRerankDailyCalls {
		usage := Record{ID: fmt.Sprintf("usage-%d", i), Scope: projectScope(scopeA), Kind: providerUsageKind, Cost: &ProviderCost{Operation: modelRerankOperation}, CreatedAt: time.Now()}
		if err := f.repo.Insert(testCtx, colProviderUsage, usage); err != nil {
			t.Fatal(err)
		}
	}
	if calls, err := f.svc.dailyModelCalls(testCtx, projectScope(scopeA), modelRerankOperation, time.Now()); err != nil || calls != modelRerankDailyCalls+1 {
		t.Fatal(calls, err)
	}
	if status, _ := f.svc.modelRerankOrder(testCtx, f.human, in, rows); status != "budget_exceeded" || len(llm.prompts) != 1 {
		t.Fatal(status, len(llm.prompts))
	}
}
