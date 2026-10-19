package memory

import (
	"slices"
	"testing"
	"time"
)

func TestPostgresLexicalProcedureWordsNeedQuerySubject(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "indexed"
		if pending {
			name = "pending"
		}
		t.Run(name, func(t *testing.T) {
			p := isolatedPostgres(t)
			now := time.Now().UTC()
			rows := []Record{
				{ID: "generic", Text: "분류 규칙 절차 classification rules procedure", Keywords: []string{"규칙"}},
				{ID: "dated", Text: "2026 분류 규칙 classification rules"},
				{ID: "subject", Text: "월면 기지 분류 규칙 quasar classification rules"},
				{ID: "context", Text: "분류 규칙 classification rules", MemoryContext: &MemoryContext{Applicability: "월면 quasar"}},
				{ID: "waiting", Text: "작업 대기 분류 규칙 이동 경로"},
				{ID: "atomic", Text: "원자 갱신 이동 경로 규칙"},
				{ID: "registration", Text: "Registration after term begins follows these rules"},
				{ID: "graduation", Text: "Graduated from college at age 25."},
				{ID: "current-age", Text: "At 32 years old, I work from home."},
			}
			for _, r := range rows {
				r.Scope, r.Status, r.Kind = scopeA, statusActive, "fact"
				r.Revision, r.ApprovedBy, r.CreatedAt, r.UpdatedAt = 1, "reviewer", now, now
				if err := p.Insert(testCtx, colMemories, r); err != nil {
					t.Fatal(err)
				}
			}
			if pending {
				if _, err := p.pool.Exec(testCtx, `UPDATE memory_records SET search_tokens=NULL, search_context_indexed=false WHERE collection=$1`, colMemories); err != nil {
					t.Fatal(err)
				}
			}
			lookup := func(query, mode string) []string {
				t.Helper()
				lanes, err := p.Search(testCtx, scopeA, query, nil, "", 10, mode)
				if err != nil {
					t.Fatal(err)
				}
				ids := []string{}
				for _, hit := range Fuse(lanes, 10) {
					ids = append(ids, hit.ID)
				}
				return ids
			}
			for _, query := range []string{"월면 분류 규칙", "2026 월면 분류 규칙", "quasar classification rules"} {
				ids := lookup(query, "fts")
				slices.Sort(ids)
				if !slices.Equal(ids, []string{"context", "subject"}) {
					t.Fatalf("%q: expected subject evidence in body or applicability, got %v", query, ids)
				}
			}
			for _, query := range []string{
				"해왕성 분류 규칙", "2026 해왕성 분류 규칙", "nebula classification rules",
				"해왕성 대기 메탄 이동 경로", "헬륨 원자 분광학 이동 경로",
			} {
				if ids := lookup(query, "fts"); len(ids) != 0 {
					t.Fatalf("%q: unrelated procedure returned: %v", query, ids)
				}
			}
			for _, query := range []string{"분류 규칙", "분류 규칙 절차", "classification rules procedure"} {
				if ids := lookup(query, "fts"); !slices.Contains(ids, "generic") {
					t.Fatalf("generic lookup %q was suppressed: %v", query, ids)
				}
			}
			if ids := lookup("해왕성 분류 규칙", "exact"); !slices.Contains(ids, "generic") {
				t.Fatalf("lexical filter affected independent keyword lane: %v", ids)
			}
			ids := lookup("How many years older am I than when I graduated from college?", "fts")
			if !slices.Contains(ids, "graduation") || !slices.Contains(ids, "current-age") {
				t.Fatalf("complementary temporal evidence was suppressed: %v", ids)
			}
		})
	}
}
