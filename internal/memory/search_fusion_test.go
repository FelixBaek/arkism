package memory

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestFusePreservesRankingAndReasons(t *testing.T) {
	lists := [][]Hit{
		{
			{ID: "shared", Origin: "memory", Collection: "memories", Text: "first", Reasons: []string{"fts"}},
			{ID: "shared", Origin: "memory", Collection: "memories", Text: "duplicate", Reasons: []string{"ignored"}},
			{ID: "exact", Origin: "memory", Collection: "memories", Reasons: []string{"exact_keyword"}},
		},
		{{ID: "shared", Origin: "memory", Collection: "memories", Text: "second", Reasons: []string{"vector", "fts", "relation"}}},
		{{ID: "shared", Origin: "hub", Collection: "memories", Reasons: []string{"hub"}}},
		{{ID: "shared", Origin: "memory", Collection: "skills", Reasons: []string{"skill"}}},
	}
	got := Fuse(lists, 10)
	if len(got) != 4 || got[0].ID != "shared" || got[0].Origin != "memory" || got[1].ID != "exact" {
		t.Fatalf("rank fusion changed: %+v", got)
	}
	if got[0].Text != "first" || !slices.Equal(got[0].Reasons, []string{"fts", "vector", "relation"}) {
		t.Fatalf("first hit or ordered reasons changed: %+v", got[0])
	}
	for _, hit := range got {
		if hit.Authority != AuthorityDataOnly {
			t.Fatalf("trusted output: %+v", hit)
		}
	}
	if limited := Fuse(lists, 2); !reflect.DeepEqual(limited, got[:2]) {
		t.Fatalf("limit changed ranking: %+v", limited)
	}
}

func TestFuseTieBreaks(t *testing.T) {
	now := time.Unix(100, 0)
	lists := [][]Hit{
		{{ID: "b", Origin: "memory", Trust: .5, UpdatedAt: now}},
		{{ID: "older", Origin: "memory", Trust: .5, UpdatedAt: now.Add(-time.Second)}},
		{{ID: "trusted", Origin: "memory", Trust: .9, UpdatedAt: now.Add(-time.Hour)}},
		{{ID: "a", Origin: "memory", Trust: .5, UpdatedAt: now}},
	}
	var ids []string
	for _, hit := range Fuse(lists, 10) {
		ids = append(ids, hit.ID)
	}
	if !slices.Equal(ids, []string{"trusted", "a", "b", "older"}) {
		t.Fatalf("tie order = %v", ids)
	}
}

func TestFuseDuplicateLaneHitsDoNotBoostRank(t *testing.T) {
	got := Fuse([][]Hit{{
		{ID: "repeated"},
		{ID: "repeated"},
		{ID: "exact", Reasons: []string{"exact_keyword"}},
	}}, 2)
	if len(got) != 2 || got[0].ID != "exact" {
		t.Fatalf("duplicate boosted score: %+v", got)
	}
	got = Fuse([][]Hit{{{ID: "a"}, {ID: "a"}, {ID: "b"}}, {{ID: "c"}, {ID: "d"}}}, 4)
	if len(got) != 4 || got[2].ID != "d" || got[3].ID != "b" {
		t.Fatalf("duplicates changed original lane positions: %+v", got)
	}
}

func TestFuseDoesNotShareReasonStorage(t *testing.T) {
	reasons := make([]string, 1, 4)
	reasons[0] = "fts"
	backing := reasons[:cap(reasons)]
	backing[1] = "reserved"
	lists := [][]Hit{{{ID: "a", Reasons: reasons}}, {{ID: "a", Reasons: []string{"vector"}}}}
	got := Fuse(lists, 1)
	if backing[1] != "reserved" {
		t.Fatal("fusion overwrote caller-owned backing storage")
	}
	got[0].Reasons[0] = "changed"
	if reasons[0] != "fts" {
		t.Fatal("result aliases caller-owned reasons")
	}
}

func TestFuseEmptyLimits(t *testing.T) {
	for _, limit := range []int{-1, 0, 1} {
		if got := Fuse(nil, limit); got == nil || len(got) != 0 {
			t.Fatalf("limit %d: want non-nil empty result, got %+v", limit, got)
		}
	}
}
