package memory

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func TestSearchQueryConceptsCountAliasesOnce(t *testing.T) {
	for _, query := range []string{"사본 백업 복원 복구 서버", "백업 사본 복구 복원 서버"} {
		got := searchQueryConcepts(searchQueryTerms(query))
		want := []string{"('사본' | '백업')", "('복원' | '복구')", "('서버')"}
		if !slices.Equal(got, want) {
			t.Fatalf("%q: got %v, want %v", query, got, want)
		}
	}
}

func TestPostgresLexicalAliasesRemainOneConcept(t *testing.T) {
	p := isolatedPostgres(t)
	scope := scopeA
	scope.ProjectID = "lexical-aliases"
	now := time.Now().UTC()
	for id, text := range map[string]string{
		"alias-only":     "사본 백업 사본 백업",
		"backup-server":  "백업 서버",
		"restore-server": "복구 서버",
	} {
		r := Record{ID: id, Scope: scope, Status: statusActive, Revision: 1, Kind: "fact", Text: text,
			ApprovedBy: "reviewer", CreatedAt: now, UpdatedAt: now}
		if err := p.Insert(testCtx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"사본 백업 서버 배포", "사본 서버 배포"} {
		lanes, err := p.Search(testCtx, scope, query, nil, "", 10, "fts")
		if err != nil {
			t.Fatal(err)
		}
		if hits := Fuse(lanes, 10); len(hits) != 1 || hits[0].ID != "backup-server" {
			t.Fatalf("aliases supplied independent evidence: %q -> %v", query, hits)
		}
	}
	lanes, err := p.Search(testCtx, scope, "복원 서버 배포", nil, "", 10, "fts")
	if err != nil {
		t.Fatal(err)
	}
	if hits := Fuse(lanes, 10); len(hits) != 1 || hits[0].ID != "restore-server" {
		t.Fatal("bounded restore alias lost", hits)
	}
}

func TestPostgresLongLexicalQueryNeedsDistinctEvidence(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC()
	scope := scopeA
	scope.ProjectID = "lexical-precision"
	insert := func(id, text string, keywords []string) Record {
		t.Helper()
		r := Record{ID: id, Scope: scope, Status: statusActive, Revision: 1, Kind: "fact", Text: text,
			ApprovedBy: "reviewer", Keywords: keywords, CreatedAt: now, UpdatedAt: now}
		if err := p.Insert(testCtx, colMemories, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for i := range 12 {
		insert(fmt.Sprintf("generic-%02d", i), "release release release notes", nil)
	}
	good := insert("supported", "release server deployment", nil)
	lookup := func(query, mode string) []Hit {
		t.Helper()
		lanes, err := p.Search(testCtx, scope, query, nil, "", 50, mode)
		if err != nil {
			t.Fatal(err)
		}
		return Fuse(lanes, 50)
	}
	for _, query := range []string{"release server deploy", "release release server deploy"} {
		if hits := lookup(query, "fts"); len(hits) != 1 || hits[0].ID != good.ID {
			t.Fatalf("generic one-word overlap admitted: %q -> %v", query, hits)
		}
	}
	// Existing short keyword lookups stay permissive; repeated words are one term.
	for _, query := range []string{"release", "release release release", "release unavailable"} {
		if hits := lookup(query, "fts"); len(hits) != 13 {
			t.Fatalf("short lookup changed: %q -> %d", query, len(hits))
		}
	}
	insert("explicit-keyword", "a separately tagged reference", []string{"deploy"})
	if hits := lookup("release server deploy", "exact"); len(hits) != 1 || hits[0].ID != "explicit-keyword" {
		t.Fatal("exact keyword lane changed", hits)
	}
	// Unbackfilled rows use the same evidence threshold through the legacy expression.
	if _, err := p.pool.Exec(testCtx, `UPDATE memory_records SET search_tokens=NULL WHERE workspace_id=$1 AND project_id=$2`, scope.WorkspaceID, scope.ProjectID); err != nil {
		t.Fatal(err)
	}
	if hits := lookup("release server deploy", "fts"); len(hits) != 1 || hits[0].ID != good.ID {
		t.Fatal("legacy fallback bypassed the evidence threshold", hits)
	}
}
