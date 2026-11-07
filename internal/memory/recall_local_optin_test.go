package memory

import (
	"context"
	"strings"
	"testing"
)

func setLocalExpansionForTest(t *testing.T, f *fixture, enabled bool) {
	t.Helper()
	cfg, err := f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: cfg.Revision, RecallQuality: &SpaceRecallQuality{LocalExpand: enabled}}, false); err != nil {
		t.Fatal(err)
	}
}

func TestLocalQualityExcerptIsOptIn(t *testing.T) {
	f := newFixture(t)
	setCaptureDensity(t, f, captureDensityConversation)
	body := "Rowan: copper notes " + strings.Repeat("routine status ", 60) + "\nRowan: copper pump repair succeeded."
	conversationTurn(f, body)
	in := SearchInput{Scope: scopeA, Query: "copper pump", Modes: []string{"fts"}, Include: []string{"sources"}}
	legacy := f.search(f.agent, in)
	if len(legacy.Conversation) != 1 || legacy.Conversation[0].Text != runeCutString(body, sessionLaneRunes) {
		t.Fatal("default excerpt changed", legacy.Conversation)
	}
	setLocalExpansionForTest(t, f, true)
	on := f.search(f.agent, in)
	if len(on.Conversation) != 1 || on.Conversation[0].Text != "Rowan: copper pump repair succeeded." {
		t.Fatal("opt-in lost relevant late turn", on.Conversation)
	}
	setLocalExpansionForTest(t, f, false)
	again := f.search(f.agent, in)
	if len(again.Conversation) != 1 || again.Conversation[0].Text != legacy.Conversation[0].Text {
		t.Fatal("disabling did not restore excerpt", again.Conversation)
	}
}

type localQualitySideFixture struct{ Repository }

func (r localQualitySideFixture) SearchCollection(context.Context, string, SearchInput, []float64, string) ([][]Hit, error) {
	return [][]Hit{{
		{ID: "partial", Collection: "sources", Text: "Rowan", Reasons: []string{reasonTextSearch}},
		{ID: "full", Collection: "sources", Text: "Rowan repair", Reasons: []string{reasonTextSearch}},
	}}, nil
}

func TestLocalQualitySideWeightIsOptIn(t *testing.T) {
	s := Service{Repo: localQualitySideFixture{}}
	in := SearchInput{Scope: scopeA, Query: "Rowan repair", Modes: []string{"fts"}}
	legacy, err := s.sideLane(testCtx, "sources", in, SearchCriteria{})
	if err != nil || len(legacy) != 2 || legacy[0].ID != "partial" {
		t.Fatal("default side weighting changed", legacy, err)
	}
	in.space = &SpaceConfig{RecallQuality: &SpaceRecallQuality{LocalExpand: true}}
	on, err := s.sideLane(testCtx, "sources", in, SearchCriteria{})
	if err != nil || len(on) != 2 || on[0].ID != "full" {
		t.Fatal("opt-in lost compound-query weighting", on, err)
	}
	in.contextView = &ContextInvocationView{}
	frozen, err := s.sideLane(testCtx, "sources", in, SearchCriteria{})
	if err != nil || len(frozen) != 2 || frozen[0].ID != "partial" {
		t.Fatal("frozen context changed side weighting", frozen, err)
	}
}

func TestLocalQualityExcludedContextsKeepLegacyWeights(t *testing.T) {
	base := SearchInput{Query: "Rowan repair", space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{LocalExpand: true}}}
	history, related, frozen := base, base, base
	history.IncludeInactive = true
	related.RelatedTo = &Ref{Collection: colMemories, ID: "related", Revision: 1}
	frozen.contextView = &ContextInvocationView{}
	for _, in := range []SearchInput{history, related, frozen} {
		exact := Hit{ID: "partial", Text: "Rowan", Reasons: []string{reasonExactKeyword}}
		keyword := Hit{ID: "full", Text: "Rowan repair", Reasons: []string{reasonTextSearch}}
		lanes := weighKeywordCoverage(in, [][]Hit{{exact}, {keyword}})
		if lanes[0][0].termShare != 0 || lanes[1][0].keywordQueryKey != "" {
			t.Fatal("excluded context received local quality weighting")
		}
	}
}
