package memory

import (
	"fmt"
	"testing"
	"time"
)

type sideLaneScenario struct {
	name, col            string
	restricted, conflict bool
}

// Existing side-lane rejections must not consume its unchanged 20-row window.
func TestPostgresSideLaneEligibilityBeforeLimit(t *testing.T) {
	p := isolatedPostgres(t)
	s := &Service{Repo: p}
	vector := make([]float64, VectorDimension)
	vector[0] = 1
	now := time.Now().UTC().Add(-time.Hour)
	for _, scenario := range []sideLaneScenario{
		{"restricted-conversation", "sources", true, false},
		{"restricted-candidate", "memory_candidates", true, false},
		{"conflicted-candidate", "memory_candidates", false, true},
	} {
		t.Run(scenario.name, func(t *testing.T) { sideLaneEligibility(t, p, s, scenario, vector, now) })
	}
	// The same sensitivity remains searchable on the ordinary approved-memory path.
	scope := Scope{WorkspaceID: "side-eligibility", ProjectID: "ordinary"}
	r := Record{ID: "memory", Scope: scope, Kind: "semantic", Status: statusActive, Revision: 1, Text: "orchard", Keywords: []string{"orchard"}, Sensitivity: "restricted", ApprovedBy: "reviewer", CreatedAt: now, UpdatedAt: now}
	insertPGRecords(t, p, colMemories, r)
	lists, e := p.SearchCollection(testCtx, colMemories, SearchInput{Scope: scope, Query: "orchard", Limit: 1, Modes: []string{"exact"}}, nil, "")
	if e != nil || len(lists) == 0 || len(lists[0]) != 1 {
		t.Fatal("ordinary memory predicate changed", lists, e)
	}
}

// seedSideLaneRows fills the whole window with rejected rows ahead of one allowed row (item 20).
func seedSideLaneRows(t *testing.T, p *Postgres, scenario sideLaneScenario, scope Scope, vector []float64, now time.Time) {
	t.Helper()
	kind, status := "semantic", "candidate"
	if scenario.col == "sources" {
		kind, status = conversationSourceKind, statusActive
	}
	for i := 0; i <= provisionalLaneLimit; i++ {
		r := Record{ID: fmt.Sprintf("%s-item-%02d", scenario.name, i), Scope: scope, Kind: kind, Status: status, Revision: 1, Text: "orchard harvest", Keywords: []string{"orchard"}, Sensitivity: "internal", CreatedAt: now, UpdatedAt: now.Add(time.Duration(provisionalLaneLimit-i) * time.Second)}
		if i < provisionalLaneLimit && scenario.restricted {
			r.Sensitivity = "restricted"
		}
		if i < provisionalLaneLimit && scenario.conflict {
			r.ConflictID = "pending-conflict"
		}
		if i == provisionalLaneLimit && scenario.col == "sources" {
			r.ConflictID = "source-conflict-does-not-exclude"
		}
		r.Embedding = &Embedding{Space: "side-test", Dimension: VectorDimension, Vector: vector, ContentHash: ContentHash(recordContent(r)), EmbeddedAt: now}
		insertPGRecords(t, p, scenario.col, r)
	}
}

func sideLaneEligibility(t *testing.T, p *Postgres, s *Service, scenario sideLaneScenario, vector []float64, now time.Time) {
	scope := Scope{WorkspaceID: "side-eligibility", ProjectID: scenario.name}
	seedSideLaneRows(t, p, scenario, scope, vector, now)
	for _, mode := range []string{"exact", "fts", "vector"} {
		in := SearchInput{Scope: scope, Query: "orchard", Modes: []string{mode}, Limit: 5, queryVector: vector, querySpace: "side-test"}
		criteria := SearchCriteria{}
		if scenario.col == "sources" {
			criteria.Kinds = []string{conversationSourceKind}
		}
		got, e := s.sideLane(testCtx, scenario.col, in, criteria)
		if e != nil || len(got) != 1 || got[0].ID != scenario.name+"-item-20" {
			t.Fatalf("%s allowed row lost: count=%d error=%v", mode, len(got), e)
		}
	}
}
