package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestConversationQueryExcerptKeepsRelevantLateWholeTurns(t *testing.T) {
	early := "Rowan: " + strings.Repeat("ordinary morning report ", 30)
	fact := "Rowan: The copper pump failed because the gasket cracked."
	text := early + "\nAvery: lunch was quiet.\n" + fact + "\nRowan: The replacement passed the pressure test."
	got := conversationQueryExcerpt("Why did the copper pump fail?", text, 200)
	if !strings.Contains(got, fact) || utf8.RuneCountInString(got) > 200 {
		t.Fatal(got)
	}
	// The returned turn is exact source text and keeps its qualification.
	if !strings.Contains(text, got) {
		t.Fatal("non-verbatim excerpt", got)
	}
}

func TestConversationQueryExcerptRedactsBeforeScoring(t *testing.T) {
	text := strings.Repeat("ordinary start. ", 50) + "\n<private>copper pump secret</private>\nAvery: copper pump repair is public."
	got := conversationQueryExcerpt("copper pump secret", text, 180)
	if strings.Contains(got, "secret") || !strings.Contains(got, "repair is public") || utf8.RuneCountInString(got) > 180 {
		t.Fatal(got)
	}
}

func TestLocalRecallQueriesOnlyReuseQuestionTerms(t *testing.T) {
	for _, q := range []string{"What activities do Rowan and Avery enjoy?", "Which equipment did Rowan repair and which tools did Avery replace?", "어제 배포 서버와 백업 서버 상태는?"} {
		queries := localRecallQueries(q)
		if len(queries) > recallExpandMax {
			t.Fatal(queries)
		}
		allowed := SearchTokens(q)
		for _, sub := range queries {
			for _, term := range SearchTokens(sub) {
				if !slices.Contains(allowed, term) {
					t.Fatalf("invented term: %q -> %q", q, sub)
				}
			}
		}
	}
	if len(localRecallQueries("copper pump")) != 0 {
		t.Fatal("identical query expanded")
	}
}

func TestLocalExpansionOptInPreservesBoundaries(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	setCaptureDensity(t, f, captureDensityConversation)
	conversationTurn(f, "Rowan: the copper pump repair is complete.")
	q := SearchInput{Scope: scopeA, Query: "What did Rowan say about copper pump repair?", Modes: []string{"fts"}, Include: []string{"sources"}, Explain: true}
	baseline := f.search(f.agent, q)
	if baseline.Explain.Expand != "" {
		t.Fatal("enabled by default", baseline.Explain)
	}
	cfg, e := f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: cfg.Revision, RecallQuality: &SpaceRecallQuality{LocalExpand: true}}, false)
	if e != nil {
		t.Fatal(e)
	}
	out := f.search(f.agent, q)
	if out.Explain.Expand != "local_applied" || len(out.Conversation) == 0 {
		t.Fatalf("expand=%+v conversation=%+v", out.Explain, out.Conversation)
	}
	pinned := q
	pinned.space = &SpaceConfig{RecallQuality: &SpaceRecallQuality{LocalExpand: true}}
	pinned.contextView = &ContextInvocationView{}
	if localExpandOn(pinned, colMemories) {
		t.Fatal("expanded frozen context")
	}
	historical := pinned
	historical.contextView = nil
	historical.IncludeInactive = true
	if localExpandOn(historical, colMemories) {
		t.Fatal("expanded history")
	}
}

func TestConversationMiddleExcerptNeverStitchesAcrossOmission(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	setLocalExpansionForTest(t, f, true)
	body := strings.Repeat("unrelated warmup. ", 45) + "\nRowan: copper pump is ready.\n" + strings.Repeat("later unrelated discussion. ", 45)
	anchor := conversationBatch(f, body, "excerpt-middle", 0)
	conversationBatch(f, "Avery: continue with the next steps.", "excerpt-middle", 1)
	in := SearchInput{Scope: scopeA, Query: "copper pump", Modes: []string{"fts"}, Include: []string{"sources"}}
	out := f.search(f.agent, in)
	if len(out.Conversation) != 1 || out.Conversation[0].ID != anchor.ID || !strings.Contains(out.Conversation[0].Text, "copper pump is ready") || strings.Contains(out.Conversation[0].Text, "next steps") {
		t.Fatal(out.Conversation)
	}
}

type failingLocalSideRepo struct{ Repository }

func (r failingLocalSideRepo) SearchCollection(ctx context.Context, col string, in SearchInput, v []float64, space string) ([][]Hit, error) {
	if in.Query == "expanded failure" {
		return nil, errors.New("synthetic optional search failure")
	}
	return r.Repository.SearchCollection(ctx, col, in, v, space)
}
func TestLocalSideFailurePreservesOriginalConversation(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	turn := conversationTurn(f, "Avery: copper pump service is complete.")
	f.svc.Repo = failingLocalSideRepo{f.svc.Repo}
	in := SearchInput{Scope: scopeA, Query: "copper pump", Modes: []string{"fts"}, Include: []string{"sources"}, space: &SpaceConfig{CaptureDensity: captureDensityConversation}, expandStatus: "local_applied", expanded: []expandedQuery{{query: "expanded failure"}}}
	var out ContextBundle
	f.svc.conversationTurns(testCtx, f.agent, in, colMemories, &out)
	if len(out.Conversation) != 1 || out.Conversation[0].ID != turn.ID || !slices.Contains(out.Degraded, "local_expansion") {
		t.Fatal(out)
	}
}

func TestLocalExpansionNativeKeepsExpandedBaseline(t *testing.T) {
	for _, mode := range []string{"on", "dynamic"} {
		t.Run(mode, func(t *testing.T) {
			runtime := nativeFSRuntime(t, mode, "V3-P1-NOTIME")
			f, q, _ := nativeFSSearchFixture(t, runtime)
			cfg, e := f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
			if e != nil {
				t.Fatal(e)
			}
			_, e = f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: cfg.Revision, RecallQuality: &SpaceRecallQuality{LocalExpand: true}}, false)
			if e != nil {
				t.Fatal(e)
			}
			q.Query = "What 설계 방식?" // produces a local query without using model generation
			q.IncludeInactive = false
			now := time.Now().UTC()
			q.QueryTime = &now
			baseline, e := f.svc.Search(testCtx, "native", q)
			if e != nil {
				t.Fatal(e)
			}
			f.svc.NativeSearchShadow = runtime
			out, e := f.svc.Search(testCtx, "native", q)
			if e != nil {
				t.Fatal(e)
			}
			observed := runtime.Observations()
			if nativeFSStrip(out) != nativeFSStrip(baseline) || len(observed) != 1 || observed[0].Reason != "unsupported_context" || !observed[0].BaselineReturned || observed[0].Served {
				t.Fatal(out, observed)
			}
		})
	}
}

func TestLocalExpansionKeepsBothAnchorsAndSharedPredicate(t *testing.T) {
	queries := localRecallQueries("What hobbies do Rowan and Avery share?")
	if !slices.Contains(queries, "rowan hobbies share") || !slices.Contains(queries, "avery hobbies share") || slices.Contains(queries, "hobbies share") {
		t.Fatal(queries)
	}
	if got := localRecallQueries("What did Rowan repair?"); len(got) != 1 || got[0] != "rowan repair" {
		t.Fatal(got)
	}
}
func TestExpandedKeywordCoverageDoesNotManufactureFullMatches(t *testing.T) {
	partial := Hit{ID: "partial", Origin: "memory", Collection: colMemories, Text: "Rowan", Reasons: []string{reasonTextSearch}}
	full := Hit{ID: "full", Origin: "memory", Collection: colMemories, Text: "Rowan repair", Reasons: []string{reasonTextSearch}}
	in := SearchInput{Query: "Rowan repair", space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{LocalExpand: true}}, expanded: []expandedQuery{{query: "Rowan repair", lists: [][]Hit{{partial, full}}}}}
	lists := expandedLanes(in, &recallTrace{})
	if len(lists) != 1 || lists[0][0].termShare != .5 || lists[0][1].termShare != 1 {
		t.Fatal(lists)
	}
	ranked := fuseWeighted(lists, 2, nil)
	if ranked[0].ID != "full" {
		t.Fatal(ranked)
	}
	in.space = nil
	legacy := expandedLanes(in, &recallTrace{})
	if legacy[0][0].termShare != 0 || fuseWeighted(legacy, 2, nil)[0].ID != "partial" {
		t.Fatal("legacy expansion weighting changed")
	}
}

func TestConversationQueryExcerptBoundsManyEmptyTurns(t *testing.T) {
	text := strings.Repeat("\n", 10000) + "Avery: copper pump pressure is stable.\n" + strings.Repeat("ordinary note\n", 1000)
	got := conversationQueryExcerpt("copper pump pressure", text, 180)
	if !strings.Contains(got, "pressure is stable") || utf8.RuneCountInString(got) > 180 || !strings.Contains(text, got) {
		t.Fatal(got)
	}
}

func TestLocalExactCoveragePreservesIntentionalLookups(t *testing.T) {
	partial := Hit{ID: "partial", Origin: "memory", Collection: colMemories, Text: "Rowan unrelated chatter", keywords: []string{"Rowan"}, Reasons: []string{reasonExactKeyword}}
	full := Hit{ID: "full", Origin: "memory", Collection: colMemories, Text: "Rowan repaired pump", keywords: []string{"pump"}, Reasons: []string{reasonExactKeyword}}
	in := SearchInput{Query: "Rowan pump", Modes: []string{"exact"}, space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{LocalExpand: true}}}
	weighted := weighKeywordCoverage(in, [][]Hit{{partial, full}})
	if weighted[0][0].termShare != .5 || weighted[0][1].termShare != 1 || fuseWeighted(weighted, 2, nil)[0].ID != "full" {
		t.Fatal(weighted)
	}
	in.space.RecallQuality.LocalExpand = false
	weighted = weighKeywordCoverage(in, [][]Hit{{partial, full}})
	if weighted[0][0].termShare != 0 || fuseWeighted(weighted, 2, nil)[0].ID != "partial" {
		t.Fatal("legacy order changed", weighted)
	}
	in.space.RecallQuality.LocalExpand = true
	for _, q := range []string{"Rowan", "Rowan pump", "X"} {
		h := partial
		h.keywords = []string{q}
		in.Query = q
		w := weighKeywordCoverage(in, [][]Hit{{h}})
		if w[0][0].termShare != 0 {
			t.Fatal("whole keyword lookup discounted", q, w)
		}
	}
}
