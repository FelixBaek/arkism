package memory

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// ST-811 / FR-020·FR-021 / TC-FUNC-033: every recalled hit carries its fused score and lanes,
// the usage receipt keeps the same score, lanes and rank without body or query text, and both
// follow the ST-824 reach filter.
func TestRecallScoreAndLanesInResponseAndUsage(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	here := f.activeMemory("scorelaneneedle body sentinel", "scorelaneneedle")
	elsewhere := f.activeMemory("scorelaneneedle other project", "scorelaneneedle")
	elsewhere, e := f.svc.SetMemoryReach(testCtx, f.human, elsewhere.ID, MemoryReachInput{Scope: scopeA, ExpectedRevision: elsewhere.Revision, Reach: ReachProject, ReachProject: "owner/other"})
	if e != nil {
		t.Fatal(e)
	}
	in := SearchInput{Scope: scopeA, Query: "scorelaneneedle"}
	in.ProjectKey = "owner/mine"
	out := f.search(f.human, in)
	if len(out.Items) != 1 || out.Items[0].ID != here.ID {
		t.Fatal("reach filter", out.Items)
	}
	hit := out.Items[0]
	if hit.Score <= 0 || len(hit.Lanes) == 0 {
		t.Fatal("score or lanes missing", hit.Score, hit.Lanes)
	}
	for _, lane := range hit.Lanes {
		if !slices.Contains([]string{"exact", "keyword", "vector", "relation"}, lane) {
			t.Fatal("unknown lane", lane)
		}
	}
	raw, _ := json.Marshal(hit)
	if !strings.Contains(string(raw), `"score":`) || !strings.Contains(string(raw), `"lanes":`) {
		t.Fatal("response fields", string(raw))
	}
	receipt, e := f.repo.Get(testCtx, "jobs", scopeA, out.ContextID)
	if e != nil || len(receipt.Usage.Items) != 1 {
		t.Fatal(receipt, e)
	}
	use := receipt.Usage.Items[0]
	if use.Memory.ID != here.ID || use.Rank != 1 || use.Score != hit.Score || !slices.Equal(use.Lanes, hit.Lanes) {
		t.Fatal("usage receipt", use, hit)
	}
	stored, _ := json.Marshal(receipt)
	if strings.Contains(string(stored), here.Text) || strings.Contains(string(stored), "scorelaneneedle") || strings.Contains(string(stored), elsewhere.ID) {
		t.Fatal("receipt leaks body, query or filtered memory", string(stored))
	}
	page, e := f.svc.RecentMemoryUsage(testCtx, f.human, scopeA, here.ID)
	if e != nil || len(page.Items) != 1 || page.Items[0].Score != hit.Score || !slices.Equal(page.Items[0].Lanes, hit.Lanes) {
		t.Fatal("usage page", page, e)
	}
}

func TestHitLanes(t *testing.T) {
	got := hitLanes([]string{reasonVector, "context_text_truncated", reasonExactKeyword, reasonTextSearch, reasonExactKeyword})
	if !slices.Equal(got, []string{"exact", "keyword", "vector"}) {
		t.Fatal(got)
	}
	if got = hitLanes([]string{"relation"}); !slices.Equal(got, []string{"relation"}) {
		t.Fatal(got)
	}
	if got = hitLanes(nil); len(got) != 0 {
		t.Fatal(got)
	}
}
