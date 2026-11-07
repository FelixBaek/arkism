package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
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
