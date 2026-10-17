package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func hasPostgresHit(hits []Hit, id string) bool {
	for _, h := range hits {
		if h.ID == id {
			return true
		}
	}
	return false
}

func TestPostgresSearchPredicatesUseBoundPlaceholders(t *testing.T) {
	criteria := SearchCriteria{Filters: &SearchFilters{FileDigests: []string{"sha256:abc"}}}
	w, args := pgSearchWhere(criteria, colMemories, time.Unix(0, 0).UTC(), "embedding <=> $%d::vector < 2", []any{"[0,1]"})
	if strings.Contains(w, "%!") || strings.Contains(w, "%d") {
		t.Fatalf("unbound placeholder in %q", w)
	}
	if len(args) != 7 { // status, effective, expiry, vector, project key (ST-824), digest pair
		t.Fatalf("args=%d, want 7", len(args))
	}
}

func TestVectorSQL(t *testing.T) {
	if got := vectorSQL([]float64{1, 0.25, -2}); got != "[1,0.25,-2]" {
		t.Fatalf("vectorSQL=%q", got)
	}
}

func TestPostgresSearchIntegration(t *testing.T) {
	ctx := context.Background()
	p := isolatedPostgres(t)
	var err error
	scope := Scope{WorkspaceID: "search-" + ID(), ProjectID: "integration", UserID: "u", AgentID: "a"}
	other := scope
	other.ProjectID = "other"
	now := time.Now().UTC()
	marker := "pgsearch-" + ID()
	makeRecord := func(id string, s Scope) Record {
		r := Record{ID: id, Scope: s, Kind: "memory", Status: "active", Revision: 1, Text: "verified " + marker, Keywords: []string{marker}, ApprovedBy: "reviewer", CreatedAt: now, UpdatedAt: now}
		return r
	}
	good := makeRecord("good", scope)
	good.Embedding = &Embedding{Space: "pg-test", Dimension: VectorDimension, Vector: make([]float64, VectorDimension), ContentHash: ContentHash(recordContent(good)), EmbeddedAt: now}
	good.Embedding.Vector[0] = 1
	if err := p.Insert(ctx, colMemories, good); err != nil {
		t.Fatal(err)
	}
	foreign := makeRecord("foreign", other)
	if err := p.Insert(ctx, colMemories, foreign); err != nil {
		t.Fatal(err)
	}
	expired := makeRecord("expired", scope)
	past := now.Add(-time.Minute)
	expired.ExpiresAt = &past
	if err := p.Insert(ctx, colMemories, expired); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = p.q().Exec(ctx, `DELETE FROM memory_records WHERE workspace_id=$1`, scope.WorkspaceID) }()

	lanes, err := p.Search(ctx, scope, marker, nil, "", 10, "exact", "fts")
	if err != nil {
		t.Fatal(err)
	}
	if !hasPostgresHit(lanes[0], good.ID) || hasPostgresHit(lanes[0], foreign.ID) || hasPostgresHit(lanes[0], expired.ID) {
		t.Fatalf("exact lane=%v", lanes[0])
	}
	if !hasPostgresHit(lanes[1], good.ID) {
		t.Fatalf("fts lane=%v", lanes[1])
	}
	vectorLanes, err := p.Search(ctx, scope, "", good.Embedding.Vector, "pg-test", 10, "vector")
	if err != nil {
		t.Fatal(err)
	}
	if !hasPostgresHit(vectorLanes[2], good.ID) {
		t.Fatalf("vector lane=%v", vectorLanes[2])
	}

	historical := makeRecord("historical", scope)
	historical.Status = "superseded"
	if err := p.Insert(ctx, colMemories, historical); err != nil {
		t.Fatal(err)
	}
	activeOnly, err := p.SearchCollection(ctx, colMemories, SearchInput{Scope: scope, Query: marker, Limit: 10}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if hasPostgresHit(activeOnly[0], historical.ID) {
		t.Fatal("history leaked into live exact search")
	}
	history, err := p.SearchCollection(ctx, colMemories, SearchInput{Scope: scope, Query: marker, Limit: 10, IncludeInactive: true}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !hasPostgresHit(history[0], historical.ID) {
		t.Fatal("history missing from inactive search")
	}
}

// Quarantined rows must not spend the LIMIT ahead of an older live match.
func TestPostgresSearchSkipsQuarantinedBeforeLimit(t *testing.T) {
	ctx := context.Background()
	p := isolatedPostgres(t)
	scope := Scope{WorkspaceID: "quarantine-" + ID(), ProjectID: "integration", UserID: "u", AgentID: "a"}
	defer func() { _, _ = p.q().Exec(ctx, `DELETE FROM memory_records WHERE workspace_id=$1`, scope.WorkspaceID) }()
	marker := "pgquarantine-" + ID()
	now := time.Now().UTC()
	for i := range 4 {
		at := now.Add(time.Duration(i) * time.Second)
		r := Record{ID: fmt.Sprintf("q%d", i), Scope: scope, Kind: "memory", Status: "active", Revision: 1, Text: marker, Keywords: []string{marker}, ApprovedBy: "reviewer", CreatedAt: at, UpdatedAt: at}
		r.PrivacyQuarantined = i > 0 // the three newest are quarantined
		if err := p.Insert(ctx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	lanes, err := p.Search(ctx, scope, marker, nil, "", 1, "exact")
	if err != nil {
		t.Fatal(err)
	}
	if !hasPostgresHit(lanes[0], "q0") {
		t.Fatalf("live match lost behind quarantined rows: %v", lanes[0])
	}
}

// Every scope owns its own agent tools document under the shared (collection,id) key.
func TestPostgresAgentToolsPerScope(t *testing.T) {
	ctx := context.Background()
	p := isolatedPostgres(t)
	a := Scope{WorkspaceID: "agent-tools-" + ID(), ProjectID: "integration", UserID: "u", AgentID: "a"}
	defer func() { _, _ = p.q().Exec(ctx, `DELETE FROM memory_records WHERE workspace_id=$1`, a.WorkspaceID) }()
	b := a
	b.AgentID = "b"
	s := &Service{Repo: p}
	for _, scope := range []Scope{a, b} {
		if _, err := s.updateAgentTools(ctx, scope, func(*AgentTools, time.Time) {}); err != nil {
			t.Fatalf("scope %s: %v", scope.AgentID, err)
		}
	}
}
