package memory

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/prices"
)

func usageContexts(t *testing.T, f *fixture, id string) map[string]int {
	t.Helper()
	page, err := f.svc.RecentMemoryUsage(testCtx, f.human, scopeA, id)
	if err != nil {
		t.Fatal("usage", err)
	}
	out := map[string]int{}
	for _, e := range page.Items {
		if _, seen := out[e.ContextID]; seen {
			t.Fatalf("memory %s twice in receipt %s", id, e.ContextID)
		}
		out[e.ContextID] = e.Rank
	}
	return out
}

// Pinned memories a context build or a host injects are in the context_usage
// receipts like recalled ones; the GET preview writes none.
func TestPinnedMemoriesGetUsageReceipts(t *testing.T) {
	f := newFixture(t)
	style := f.activeMemory("답은 한국어로 짧게")
	both := f.activeMemory("release checklist: run the tests")
	f.activeMemory("release checklist step a")
	f.crew(pinAgent("리뷰어", memPin(style.ID, "full", 90), memPin(both.ID, "summary", 50)))
	big := 64000
	pack, err := f.svc.BuildContext(testCtx, f.human, ContextBuildInput{Scope: scopeA, Task: "release checklist", TokenEncoding: "cl100k_base",
		Modes: []string{"fts"}, Agent: "@리뷰어", BudgetTokens: &big})
	if err != nil {
		t.Fatal(err)
	}
	if got := usageContexts(t, f, style.ID); got[pack.ContextID] != 1 || len(got) != 1 {
		t.Fatalf("pinned memory not in the build's receipt: %v (%s)", got, pack.ContextID)
	}
	if got := usageContexts(t, f, both.ID); got[pack.ContextID] != 2 {
		t.Fatalf("pinned and found memory: %v", got)
	}

	if _, err = f.svc.ResolvePins(testCtx, f.human, scopeA, "@리뷰어", ""); err != nil {
		t.Fatal(err)
	}
	if got := usageContexts(t, f, style.ID); len(got) != 1 {
		t.Fatal("preview wrote a receipt", got)
	}
	res, err := f.svc.InjectPins(testCtx, f.human, PinInjectInput{Scope: scopeA, Agent: "@리뷰어"})
	if err != nil || res.ContextID == "" || len(res.Items) != 2 {
		t.Fatalf("inject: %+v %v", res, err)
	}
	if got := usageContexts(t, f, style.ID); got[res.ContextID] != 1 || len(got) != 2 {
		t.Fatalf("injected pins not recorded: %v", got)
	}

	body := `{"scope":` + structuredText(scopeA) + `,"agent":"@리뷰어"}`
	req := httptest.NewRequest("POST", "/v1/context/pins", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	var out PinResolution
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ContextID == "" || out.ContextID == res.ContextID {
		t.Fatal(w.Code, w.Body.String())
	}
}

func ledgerOf(f *fixture, operation string) []ProviderCost {
	f.repo.mu.Lock()
	defer f.repo.mu.Unlock()
	out := []ProviderCost{}
	for _, r := range f.repo.store[colProviderUsage] {
		if r.Cost != nil && r.Cost.Operation == operation {
			out = append(out, *r.Cost)
		}
	}
	return out
}

// Wiki steps and the digest and merge workers' model calls are priced, so 사용량 shows money.
func TestModelCallsArePriced(t *testing.T) {
	rates := CostRates{Revision: "deploy-1"}
	u := ProviderUsage{Provider: "x", Model: "claude-sonnet-5-5", Requests: 1, InputTokens: 1_000_000, OutputTokens: 100_000}
	for _, op := range []string{"wiki_analyse", "wiki_write", "digest_topic", "digest_user", "digest_judge", "merge_judge"} {
		amount, revision, ok := rates.priceAt(op, u)
		if !ok || revision != prices.Revision || amount != 3_000_000_000_000 {
			t.Fatalf("%s: %d %q %v", op, amount, revision, ok)
		}
	}
	if _, _, ok := rates.priceAt("digest_topic", ProviderUsage{Model: "unknown-model", InputTokens: 5}); ok {
		t.Fatal("unknown model priced")
	}
	// The wiki runs on the skill LLM: a deployment rate for it wins.
	rates.SkillLLMInputTokenPicoUSD, rates.SkillLLMOutputTokenPicoUSD = 2, 3
	if amount, revision, ok := rates.priceAt("wiki_write", u); !ok || revision != "deploy-1" || amount != 2_300_000 {
		t.Fatal(amount, revision, ok)
	}

	f := newFixture(t)
	a := f.digestMemory("decision", "배포는 항상 staging 먼저 거친다")
	_, err := f.svc.ProposeDigest(testCtx, f.agent, DigestProposalInput{Scope: scopeA, Kind: digestKindTopic, Title: "배포", Body: "staging 먼저.",
		Members: memRefs(a), Usage: &ModelCallUsage{Model: "claude-sonnet-5-5", InputTokens: 1_000_000, OutputTokens: 100_000}})
	if err != nil {
		t.Fatal(err)
	}
	got := ledgerOf(f, "digest_topic")
	if len(got) != 1 || got[0].EstimatedCostPicoUSD != 3_000_000_000_000 || !got[0].PricingAvailable || got[0].PricingRevision != prices.Revision || got[0].Provider != modelCallProvider {
		t.Fatalf("ledger: %+v", got)
	}
	// Bad usage is not recorded and never refuses the result.
	if _, err = f.svc.JudgeUserDigest(testCtx, f.agent, DigestJudgeInput{Scope: scopeA, Judged: memRefs(a), Verdict: "none",
		Usage: &ModelCallUsage{Model: "bad model id\n", InputTokens: -1}}); err != nil && !strings.Contains(err.Error(), "conflict") {
		t.Fatal(err)
	}
	if n := len(ledgerOf(f, "digest_judge")); n != 0 {
		t.Fatal("invalid usage recorded", n)
	}
}

// The removal preview reports the cascade and changes nothing.
func TestWikiSourceRemovalDryRun(t *testing.T) {
	plan2 := `{"summary":"s2","pages":[{"title":"Bleed","type":"concept","action":"update","reason":"x"},{"title":"Trim","type":"concept","action":"create","reason":"x"}],"links":[]}`
	write2 := wikiFile("wiki/concept/bleed.md", "Bleed", "concept", "Also on posters.") + wikiFile("wiki/concept/trim.md", "Trim", "concept", "Trim is the cut line, inside the [[A3 Paper]] edge.")
	f, _ := wikiFixture(t, append(wikiPrintAnswers(), plan2, write2)...)
	s1 := wikiSource(t, f, "print", wikiPrintSource)
	scope := s1.Scope
	wikiBuild(t, f, s1)
	bleed, a3, trim := wikiPageID(scope, "bleed"), wikiPageID(scope, "a3-paper"), wikiPageID(scope, "trim")
	wikiApprove(t, f, scope, a3)
	s2 := wikiSource(t, f, "trim", "Trim and Bleed: Trim is the cut line. Bleed also applies to posters.")
	wikiBuild(t, f, s2)
	wikiApprove(t, f, scope, bleed)
	wikiApprove(t, f, scope, trim)

	if _, err := f.svc.PreviewWikiSourceRemoval(testCtx, f.agent, s1.ID, WikiScopeInput{Scope: scope}); err == nil {
		t.Fatal("agent preview allowed")
	}
	h := Handler(f.svc)
	sc := structuredText(scope)
	var preview WikiCleanupResult
	// The UI sends both; each alone must also be a dry run.
	for _, c := range []struct{ query, body string }{
		{"?dry_run=1", `{"scope":` + sc + `}`},
		{"", `{"scope":` + sc + `,"dry_run":true}`},
		{"?dry_run=1", `{"scope":` + sc + `,"dry_run":true}`},
	} {
		req := httptest.NewRequest("POST", "/v1/wiki/sources/"+s1.ID+"/remove"+c.query, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var raw map[string]any
		preview = WikiCleanupResult{}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &preview) != nil || json.Unmarshal(w.Body.Bytes(), &raw) != nil || raw["dry_run"] != true ||
			!slices.Equal(preview.Deleted, []string{a3}) || !slices.Equal(preview.Detached, []string{bleed}) || !slices.Contains(preview.Unlinked, trim) {
			t.Fatal(c, w.Code, w.Body.String())
		}
		if page, _ := f.svc.GetWikiPage(testCtx, f.human, scope, a3); page.Status == "deleted" || page.Reviewed == nil {
			t.Fatalf("preview %v deleted a page: %+v", c, page)
		}
		if links, _ := f.svc.WikiGraph(testCtx, f.human, scope); len(links) != 2 {
			t.Fatalf("preview %v changed links: %+v", c, links)
		}
	}
	out, err := f.svc.RemoveWikiSource(testCtx, f.human, s1.ID, WikiScopeInput{Scope: scope})
	if err != nil || out.DryRun || !slices.Equal(out.Deleted, preview.Deleted) || !slices.Equal(out.Detached, preview.Detached) || !slices.Equal(out.Unlinked, preview.Unlinked) {
		t.Fatalf("removal differs from preview: %+v %+v %v", out, preview, err)
	}
}
