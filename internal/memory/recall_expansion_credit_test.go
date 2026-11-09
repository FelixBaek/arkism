//go:build linux

package memory

import (
	"slices"
	"testing"
)

func TestLocalExpansionEquivalentFTSDoesNotMultiplyEvidence(t *testing.T) {
	in := SearchInput{Query: "What did Rowan repair?", space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{LocalExpand: true}}}
	a := Hit{ID: "a", Origin: "memory", Collection: colMemories, Text: "Rowan repair copper pump", Reasons: []string{reasonTextSearch}}
	b := Hit{ID: "b", Origin: "memory", Collection: colMemories, Text: "Rowan repair turbine", Reasons: []string{reasonTextSearch}}
	original := weighKeywordCoverage(in, [][]Hit{{a, b}})
	want := fuseWeighted(original, 10, nil)
	in.expanded = []expandedQuery{{query: "rowan repair", lists: [][]Hit{{a, b}}}, {query: "repair rowan", lists: [][]Hit{{a, b}}}}
	got := fuseWeighted(append(original, expandedLanes(in, &recallTrace{})...), 10, nil)
	if len(got) != len(want) {
		t.Fatal("candidate union changed", got)
	}
	for i := range got {
		if got[i].ID != want[i].ID || got[i].Score != want[i].Score {
			t.Fatal("equivalent lexical queries multiplied evidence", want, got)
		}
	}
	// Legacy mode retains its prior fusion policy.
	in.space = nil
	original = weighKeywordCoverage(in, [][]Hit{{a, b}})
	want = fuseWeighted(original, 10, nil)
	got = fuseWeighted(append(original, expandedLanes(in, &recallTrace{})...), 10, nil)
	if got[0].Score <= want[0].Score*2.9 {
		t.Fatal("legacy fusion changed")
	}
}

func TestLocalExpansionCreditPreservesNewCandidatesAndDifferentEvidence(t *testing.T) {
	in := SearchInput{Query: "Rowan repair", space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{LocalExpand: true}}}
	a := Hit{ID: "a", Origin: "memory", Collection: colMemories, Text: "Rowan and Avery repair pumps", Reasons: []string{reasonTextSearch}}
	b := a
	b.ID = "b"
	// A query's gate can remove a candidate that another equivalent wording
	// admits. Deduplicating entire input lanes would lose that candidate.
	first := weighKeywordCoverage(in, [][]Hit{{a}})
	second := weighKeywordCoverage(in, [][]Hit{{b, a}})
	got := fuseWeighted(append(first, second...), 10, nil)
	if len(got) != 2 || got[0].Score != got[1].Score {
		t.Fatal("lost union or repeated a lower rank", got)
	}
	reverse := fuseWeighted(append(second, first...), 10, nil)
	if reverse[0].ID != got[0].ID || reverse[0].Score != got[0].Score {
		t.Fatal("equivalent lane order changed credit")
	}
	in.Query = "Avery repair"
	different := weighKeywordCoverage(in, [][]Hit{{a}})
	if fuseWeighted(append(first, different...), 10, nil)[0].Score <= got[0].Score {
		t.Fatal("different predicate evidence lost its credit")
	}
	for _, reason := range []string{reasonExactKeyword, reasonVector} {
		a.Reasons = []string{reason}
		lanes := weighKeywordCoverage(in, [][]Hit{{a}, {a}})
		one := fuseWeighted(lanes[:1], 10, nil)
		two := fuseWeighted(lanes, 10, nil)
		if two[0].Score != 2*one[0].Score {
			t.Fatal("non-FTS evidence was deduplicated", reason)
		}
	}
}

func TestLocalExpansionEquivalentConversationFTSPostgres(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	setCaptureDensity(t, f, captureDensityConversation)
	conversationTurn(f, "Rowan: copper pump repair is complete.")
	in := SearchInput{Scope: scopeA, Query: "What did Rowan repair?", Modes: []string{"fts"}, space: &SpaceConfig{RecallQuality: &SpaceRecallQuality{LocalExpand: true}}}
	criteria := SearchCriteria{Kinds: []string{conversationSourceKind}}
	want, err := f.svc.sideLane(testCtx, "sources", in, criteria)
	if err != nil || len(want) == 0 {
		t.Fatal("missing baseline", want, err)
	}
	in.expandStatus = "local_applied"
	in.expanded = []expandedQuery{{query: "rowan repair"}}
	got, err := f.svc.sideLane(testCtx, "sources", in, criteria)
	if err != nil || len(got) != len(want) {
		t.Fatal(got, err)
	}
	for i := range got {
		if got[i].ID != want[i].ID || got[i].Score != want[i].Score || !slices.Contains(got[i].Reasons, reasonTextSearch) {
			t.Fatal("same real FTS evidence received another vote", want, got)
		}
	}
}
