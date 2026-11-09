//go:build linux

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
	if got, ok := parseModelRerank("```json\n{\"order\":[3,1]}\n```", 3, RerankOptions{}); !ok || !slices.Equal(got.Order, []int{3, 1, 2}) {
		t.Fatal(got, ok)
	}
	for _, bad := range []string{`{"order":[1,1]}`, `{"order":[4]}`, `{"order":[0]}`, `{"order":[]}`, `{"order":[1],"why":"x"}`, `order: 1,2`, `{"order":[1,2,3,1]}`} {
		if got, ok := parseModelRerank(bad, 3, RerankOptions{}); ok {
			t.Fatal(bad, got)
		}
	}
	// drop and duplicates count only when asked for, are checked like order, and a number in both is removed once.
	both := RerankOptions{Prune: true, Dedup: true}
	if got, ok := parseModelRerank(`{"order":[2,1,3],"drop":[3],"duplicates":[1,3]}`, 3, both); !ok || !slices.Equal(got.Drop, []int{3}) || !slices.Equal(got.Duplicates, []int{1}) {
		t.Fatal(got, ok)
	}
	if got, ok := parseModelRerank(`{"order":[2],"drop":[3]}`, 3, RerankOptions{}); !ok || got.Drop != nil {
		t.Fatal(got, ok)
	}
	for _, bad := range []string{`{"order":[1],"drop":[4]}`, `{"order":[1],"duplicates":[2,2]}`, `{"order":[1],"drop":"all"}`, `{"order":["1"]}`, `{"order":[1.5]}`,
		`{"order":[1],"drop":[-1]}`, `{"order":[9223372036854775807]}`, `{"order":[1]} trailing`, `null`, ``, `[1,2,3]`} {
		if got, ok := parseModelRerank(bad, 3, both); ok {
			t.Fatal(bad, got)
		}
	}
	// Dropping and repeating every snippet still keeps the model's first pick.
	if got := modelRemovals([]int{1, 2, 3}, []int{1, 2, 3}, []int{4, 7, 9}, 7); len(got) != 2 || got[7] != "" || got[4] != "model_prune" {
		t.Fatal(got)
	}
	if !strings.Contains(modelRerankPrompt(both), `"drop"`) || strings.Contains(modelRerankPrompt(RerankOptions{}), `"drop"`) {
		t.Fatal("prompt")
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
	if status, order, _ := f.svc.modelRerankOrder(testCtx, f.human, in, rows); status != "denied" || order != nil || len(llm.prompts) != 0 {
		t.Fatal(status, order)
	}
	a.verbs["generate"] = true
	// A query that may not leave the host is not sent either.
	if status, _, _ := f.svc.modelRerankOrder(testCtx, f.human, SearchInput{Scope: scopeA, Query: "token sk-ant-api03-" + strings.Repeat("a", 40), Rerank: rerankModel}, rows); status != "denied" {
		t.Fatal(status)
	}
	if status, _, _ := f.svc.modelRerankOrder(testCtx, f.human, in, rows); status != "applied" {
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
	if status, _, _ := f.svc.modelRerankOrder(testCtx, f.human, in, rows); status != "budget_exceeded" || len(llm.prompts) != 1 {
		t.Fatal(status, len(llm.prompts))
	}
}

func TestRecallModelRerankPruneDedup(t *testing.T) {
	f, llm := modelRerankFixture(t, `{"order":[2,1],"drop":[2],"duplicates":[1]}`, `{"order":[1,2],"drop":[2]}`)
	base, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true})
	if err != nil || len(base.Items) < 2 {
		t.Fatal(ids(base.Items), err)
	}
	// The model's first pick stays even when it also listed it to drop; the other one goes.
	opts := &RerankOptions{Prune: true, Dedup: true}
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true, Rerank: rerankModel, RerankOptions: opts})
	if err != nil || out.Explain.ModelRerank != "applied" || len(out.Items) != len(base.Items)-1 || out.Explain.Dropped["model_dedup"] != 1 || out.Explain.Dropped["model_prune"] != 0 {
		t.Fatal(ids(out.Items), out.Explain, err)
	}
	if !strings.Contains(llm.prompts[0], "Snippets:") {
		t.Fatal(llm.prompts)
	}
	// Without prune the drop list is ignored.
	kept, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true, Rerank: rerankModel})
	if err != nil || len(kept.Items) != len(base.Items) || kept.Explain.Dropped != nil {
		t.Fatal(ids(kept.Items), kept.Explain, err)
	}
}
