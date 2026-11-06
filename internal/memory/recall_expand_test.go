package memory

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseExpansion(t *testing.T) {
	got, ok := parseExpansion("```json\n{\"queries\":[\" gamma  incident \",\"Gamma incident\",\"what broke\",\"beta rollback\",\"a\",\"b\",\"c\"]}\n```", "What broke")
	if !ok || !slices.Equal(got, []string{"gamma incident", "beta rollback", "a", "b"}) {
		t.Fatal(got, ok)
	}
	for _, bad := range []string{`{"queries":[]}`, `{"queries":["what broke"]}`, `{"queries":[""]}`, `{"queries":"x"}`, `{"queries":["x"],"why":"y"}`, `not json`, ``,
		`{"queries":["` + strings.Repeat("x", recallExpandRunes+1) + `"]}`, `{"queries":["token sk-ant-api03-` + strings.Repeat("a", 40) + `"]}`, `{"queries":["<private>x</private>"]}`} {
		if got, ok := parseExpansion(bad, "what broke"); ok {
			t.Fatal(bad, got)
		}
	}
}

// The question alone matches nothing; the model's sub-queries find two memories through
// the ordinary lanes, and the result still holds at most the caller's limit.
func TestRecallExpandSearchesSubQueriesWithinTheLimit(t *testing.T) {
	f, llm := modelRerankFixture(t, `{"queries":["gamma incident","beta rollback"]}`, `{"queries":["gamma incident","beta rollback"]}`, `not json`)
	query := "what went wrong with the release"
	base, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: query, Explain: true})
	if err != nil || len(base.Items) != 0 || len(llm.prompts) != 0 || base.Explain.Expand != "" {
		t.Fatal(ids(base.Items), llm.prompts, err)
	}
	on := true
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: query, Explain: true, Expand: &on})
	if err != nil || out.Explain.Expand != "applied" || !slices.Equal(out.Explain.ExpandQueries, []string{"gamma incident", "beta rollback"}) {
		t.Fatal(out.Explain, err)
	}
	if got := ids(out.Items); !slices.Contains(got, "gamma") || !slices.Contains(got, "beta") || !slices.Contains(out.Explain.Lanes, "expanded") {
		t.Fatal(got, out.Explain.Lanes)
	}
	// Only the question was sent, and the call is in the usage ledger.
	if len(llm.prompts) != 1 || !strings.HasSuffix(llm.prompts[0], "Question: "+query) || strings.Contains(llm.prompts[0], "rollback") {
		t.Fatal(llm.prompts)
	}
	if usage := ledgerOf(f, recallExpandOperation); len(usage) != 1 {
		t.Fatalf("%+v", usage)
	}
	one, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: query, Expand: &on, Limit: 1})
	if err != nil || len(one.Items) != 1 {
		t.Fatal(ids(one.Items), err)
	}
	// An unreadable answer is plain recall.
	failed, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: query, Explain: true, Expand: &on})
	if err != nil || failed.Explain.Expand != "failed" || len(failed.Items) != 0 {
		t.Fatal(failed.Explain, ids(failed.Items), err)
	}
	// No model: plain recall with the reason.
	f.svc.Wiki = nil
	none, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: query, Explain: true, Expand: &on})
	if err != nil || none.Explain.Expand != "model_not_configured" || len(none.Items) != 0 {
		t.Fatal(none.Explain, err)
	}
}

// The space switch turns it on, a request's expand:false turns it off, and a question that may
// not leave the host, a caller without the generate permission or a spent daily cap send nothing.
func TestRecallExpandSpaceSwitchAndGates(t *testing.T) {
	f, llm := modelRerankFixture(t, `{"queries":["gamma incident"]}`, `{"queries":["gamma incident"]}`)
	setRecallQuality(t, f, SpaceRecallQuality{Expand: true})
	query := SearchInput{Scope: scopeA, Query: "what went wrong with the release", Explain: true}
	if out := f.search(f.human, query); out.Explain.Expand != "applied" || !slices.Equal(ids(out.Items), []string{"gamma"}) {
		t.Fatal(out.Explain, ids(out.Items))
	}
	off := false
	plain := query
	plain.Expand = &off
	if out := f.search(f.human, plain); out.Explain.Expand != "" || len(llm.prompts) != 1 {
		t.Fatal(out.Explain, llm.prompts)
	}
	private := query
	private.Query = "<private>what went wrong</private>"
	if out := f.search(f.human, private); out.Explain.Expand != "denied" || len(llm.prompts) != 1 {
		t.Fatal(out.Explain, llm.prompts)
	}
	secret := query
	secret.Query = "token sk-ant-api03-" + strings.Repeat("a", 40)
	if status, queries := f.svc.expansionQueries(testCtx, f.human, secret); status != "denied" || queries != nil {
		t.Fatal(status, queries)
	}
	a := f.auth.actors[f.human]
	a.verbs["generate"] = false
	f.auth.actors[f.human] = a
	if out := f.search(f.human, query); out.Explain.Expand != "denied" || len(llm.prompts) != 1 {
		t.Fatal(out.Explain, llm.prompts)
	}
	a.verbs["generate"] = true
	for i := range recallExpandDailyCalls {
		usage := Record{ID: fmt.Sprintf("usage-%d", i), Scope: projectScope(scopeA), Kind: providerUsageKind, Cost: &ProviderCost{Operation: recallExpandOperation}, CreatedAt: time.Now()}
		if err := f.repo.Insert(testCtx, colProviderUsage, usage); err != nil {
			t.Fatal(err)
		}
	}
	if out := f.search(f.human, query); out.Explain.Expand != "budget_exceeded" || len(llm.prompts) != 1 || len(out.Items) != 0 {
		t.Fatal(out.Explain, llm.prompts)
	}
}

// The sub-queries are embedded in one call and search the vector lane with their own vectors:
// a fact the question's wording leaves out of its lane comes back through them.
func TestRecallExpandSubQueryVectors(t *testing.T) {
	f := newFixture(t)
	f.emb.semantic = true
	f.svc.Repo = nearestFirstRepo{f.repo}
	a := f.auth.actors[f.human]
	a.verbs["generate"] = true
	f.auth.actors[f.human] = a
	llm := &fakeWikiLLM{answers: []string{`{"queries":["caroline adopted a dog","caroline dog oscar"]}`}}
	f.svc.Wiki = llm
	for _, text := range []string{"melanie painted the lake at sunrise", "melanie lake painting class", "melanie painted a horse", "melanie lake sunrise photo", "caroline adopted a dog named oscar"} {
		f.embedText(f.activeMemory(text))
	}
	off := false
	setRecallQuality(t, f, SpaceRecallQuality{Diversity: &off})
	query := SearchInput{Scope: scopeA, Query: "what did melanie paint at the lake", Modes: []string{"vector"}, Limit: 3, Explain: true}
	dog := func(items []Hit) bool {
		return slices.ContainsFunc(items, func(h Hit) bool { return strings.Contains(h.Text, "oscar") })
	}
	if out := f.search(f.human, query); len(out.Items) != 3 || dog(out.Items) {
		t.Fatal("the question alone already found the dog", ids(out.Items))
	}
	on := true
	query.Expand = &on
	out := f.search(f.human, query)
	if out.Explain.Expand != "applied" || len(out.Items) != 3 || !dog(out.Items) {
		t.Fatal(out.Explain, out.Items)
	}
	if !slices.Contains(f.emb.calls, "query:caroline adopted a dog") || !slices.Contains(f.emb.calls, "query:caroline dog oscar") {
		t.Fatal(f.emb.calls)
	}
}
