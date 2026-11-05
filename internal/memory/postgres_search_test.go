package memory

import (
	"context"
	"fmt"
	"slices"
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

// The reflect observations tool searches with Kinds=[observation]; observation rows carry that kind in doc.
func TestPostgresSearchKindsFilterObservations(t *testing.T) {
	w, args := pgSearchWhere(SearchCriteria{Kinds: []string{observationKind}}, colObservations, time.Unix(0, 0).UTC(), "", nil)
	found := false
	for i, a := range args {
		if kinds, ok := a.([]string); ok && len(kinds) == 1 && kinds[0] == observationKind {
			found = strings.Contains(w, fmt.Sprintf("doc->>'kind' = ANY($%d::text[])", 6+i))
		}
	}
	if !found {
		t.Fatalf("kind predicate missing: %q %v", w, args)
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

// The provisional lane reads waiting candidates only (never erased ones), and the job
// queue gauges count each kind's backlog, running and failed jobs across scopes.
func TestPostgresPendingCandidateSearchAndJobQueueStats(t *testing.T) {
	ctx := context.Background()
	p := isolatedPostgres(t)
	scope := Scope{WorkspaceID: "pending-" + ID(), ProjectID: "integration", UserID: "u", AgentID: "a"}
	defer func() { _, _ = p.q().Exec(ctx, `DELETE FROM memory_records WHERE workspace_id=$1`, scope.WorkspaceID) }()
	now, marker := time.Now().UTC(), "pgpending-"+ID()
	for _, r := range []Record{
		{ID: "waiting", Status: "candidate"},
		{ID: "erased", Status: "candidate", ContentErased: true},
		{ID: "rejected", Status: "rejected"},
	} {
		r.Scope, r.Kind, r.Revision, r.Text, r.Keywords, r.CreatedAt, r.UpdatedAt = scope, "memory", 1, "pending "+marker, []string{marker}, now, now
		if err := p.Insert(ctx, "memory_candidates", r); err != nil {
			t.Fatal(err)
		}
	}
	lanes, err := p.SearchCollection(ctx, "memory_candidates", SearchInput{Scope: scope, Query: marker, Limit: 10}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, lane := range lanes {
		if hasPostgresHit(lane, "erased") || hasPostgresHit(lane, "rejected") {
			t.Fatal("lane", i, lane)
		}
	}
	if !hasPostgresHit(lanes[0], "waiting") {
		t.Fatal("exact lane missed the waiting candidate", lanes[0])
	}
	kind := "pgqueue_" + strings.ToLower(ID()[:8])
	old := now.Add(-time.Hour)
	for i, st := range []string{JobStatusPending, JobStatusRetry, JobStatusRunning, JobStatusFailed, "done"} {
		j := Record{ID: fmt.Sprintf("job%d", i), Scope: scope, Kind: kind, Status: st, Revision: 1, CreatedAt: old, UpdatedAt: old}
		if err := p.Insert(ctx, colJobs, j); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := p.JobQueueStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(stats, func(s JobQueueStat) bool { return s.Kind == kind })
	if i < 0 || stats[i].Backlog != 2 || stats[i].Running != 1 || stats[i].Failed != 1 || stats[i].OldestAgeSeconds < 3500 {
		t.Fatal(stats)
	}
}
