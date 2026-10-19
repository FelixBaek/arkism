package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSearchTokensRecordContextQualifiers(t *testing.T) {
	r := Record{Text: "release notes", Summary: "short", Keywords: []string{"draft"}}
	legacy := searchTokensText(r.Text, r.Summary, r.Keywords)
	if searchTokensRecordText(r) != legacy {
		t.Fatal("legacy token input changed")
	}
	r.MemoryContext = &MemoryContext{Domains: []string{"work"}, EvidenceKind: "tool_observation", SemanticKind: "failure_recovery"}
	if searchTokensRecordText(r) != legacy {
		t.Fatal("classification labels became search terms")
	}
	r.MemoryContext.Applicability = "검수된 보고서에서만"
	r.MemoryContext.Uncertainty = "타부서는 미확인"
	r.MemoryContext.ValidFrom = "2027년부터"
	tokens := SearchTokens(searchTokensRecordText(r))
	for _, term := range []string{"보고서", "타부서", "2027"} {
		if !slices.Contains(tokens, term) {
			t.Fatalf("qualifier omitted: %s", term)
		}
	}
	for _, term := range []string{"work", "tool", "observation", "failure", "recovery"} {
		if slices.Contains(tokens, term) {
			t.Fatalf("provenance label indexed: %s", term)
		}
	}
}

func TestMemoryContextConditionSearchAfterCaptureReviewPostgres(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	src := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Kind: "tool_result", Text: "복구 절차는 검수된 보고서에서만 적용한다. 타부서는 미확인. 2027년부터 시행한다."}))
	claim := canonicalToolClaim(src)
	claim.Text = "검증한 복구 절차"
	claim.MemoryContext = &MemoryContext{Domains: []string{"work"}, EvidenceKind: "tool_observation", SemanticKind: "failure_recovery", Applicability: "검수된 보고서에서만", Uncertainty: "타부서는 미확인", ValidFrom: "2027년부터"}
	item := f.capture(f.agent, captureInput(scopeA, "condition-search", claim))[0]
	if item.Status != "candidate" {
		t.Fatal("capture bypassed human review", item)
	}
	candidate := f.get(f.agent, "memory_candidates", scopeA, item.CandidateID)
	search := func(query string, scope Scope) ContextBundle {
		t.Helper()
		in := SearchInput{Scope: scope, Query: query, Modes: []string{"fts"}, Limit: 6, MaxChars: 6000}
		req := httptest.NewRequest(http.MethodPost, "/v1/search", strings.NewReader(structuredText(in)))
		req.Header.Set("Authorization", "Bearer "+f.human)
		rr := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("search status=%d body=%s", rr.Code, rr.Body.String())
		}
		var bundle ContextBundle
		if err := json.Unmarshal(rr.Body.Bytes(), &bundle); err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	if len(search("보고서", scopeA).Items) != 0 {
		t.Fatal("pending candidate entered recall")
	}
	approved, err := f.svc.ReviewCandidate(testCtx, f.human, candidate.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	// Each query matches only metadata; body, summary and keywords omit all three.
	for _, q := range []string{"보고서", "타부서", "2027"} {
		bundle := search(q, scopeA)
		if len(bundle.Items) != 1 {
			t.Fatalf("condition %s: got %d hits", q, len(bundle.Items))
		}
		hit := bundle.Items[0]
		if hit.ID != approved.ID || hit.Authority != "data_only_untrusted" || hit.Scope != scopeA || !reflect.DeepEqual(hit.MemoryContext, approved.MemoryContext) || !slices.Contains(hit.Reasons, reasonTextSearch) {
			t.Fatal("retrieval lost condition, evidence or scope")
		}
	}
	other := scopeA
	other.WorkspaceID = "other-workspace"
	if len(search("보고서", other).Items) != 0 || len(search("없는낱말", scopeA).Items) != 0 {
		t.Fatal("unrelated or out-of-scope hit")
	}
	if err = f.svc.Delete(testCtx, f.human, colMemories, approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision}); err != nil {
		t.Fatal(err)
	}
	if len(search("보고서", scopeA).Items) != 0 {
		t.Fatal("deleted metadata still recalled")
	}
}

func TestSchemaMigrationContextIndexUpgradePreservesRecord(t *testing.T) {
	p := isolatedPostgresSchema(t, false)
	ctx := context.Background()
	if err := p.migrateSchema(ctx, schemaMigrations()[:1]); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r := Record{ID: ID(), Scope: scopeA, Kind: "memory", Status: statusActive, ApprovedBy: "reviewer", Revision: 7, Text: "검증한 복구 절차", UpdatedAt: now, CreatedAt: now, MemoryContext: &MemoryContext{Domains: []string{"work"}, EvidenceKind: "inference", SemanticKind: "lesson", Applicability: "검수된 보고서에서만", Uncertainty: "타부서는 미확인", ValidFrom: "2027년부터"}}
	if err := p.Insert(ctx, colMemories, r); err != nil {
		t.Fatal(err)
	}
	plain := r
	plain.ID = ID()
	plain.MemoryContext = nil
	if err := p.Insert(ctx, colMemories, plain); err != nil {
		t.Fatal(err)
	}
	// Emulate the old writer: a non-NULL vector with body/summary/keywords only.
	schemaExec(t, p, `UPDATE memory_records SET search_tokens=$1::tsvector WHERE id=$2`, searchTokensVector(searchTokensText(r.Text, r.Summary, r.Keywords)), r.ID)
	before, err := p.Get(ctx, colMemories, scopeA, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var plainTokens string
	if err = p.pool.QueryRow(ctx, `SELECT search_tokens::text FROM memory_records WHERE id=$1`, plain.ID).Scan(&plainTokens); err != nil {
		t.Fatal(err)
	}
	search := func(q string) []Hit {
		t.Helper()
		lanes, err := p.Search(ctx, scopeA, q, nil, "", 10, "fts")
		if err != nil {
			t.Fatal(err)
		}
		return lanes[1]
	}
	var oldMatches int
	if err = p.pool.QueryRow(ctx, `SELECT count(*) FROM memory_records WHERE search_tokens @@ $1::tsquery`, searchTermsQuery(searchQueryTerms("보고서"))).Scan(&oldMatches); err != nil || oldMatches != 0 {
		t.Fatal("old index unexpectedly included conditions", err)
	}
	// Schema-only upgrade leaves the old non-NULL body vector in place.
	if err = p.migrateSchema(ctx, schemaMigrations()); err != nil {
		t.Fatal(err)
	}
	if hits := search("미확인"); len(hits) != 1 || hits[0].ID != r.ID {
		t.Fatal("pre-context index has no qualifier fallback")
	}
	if err = p.Init(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"보고서", "타부서", "2027"} {
		if hits := search(q); len(hits) != 1 || hits[0].ID != r.ID {
			t.Fatalf("upgrade missed %s", q)
		}
	}
	after, err := p.Get(ctx, colMemories, scopeA, r.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("reindex changed canonical record", err)
	}
	var plainAfter string
	if err = p.pool.QueryRow(ctx, `SELECT search_tokens::text FROM memory_records WHERE id=$1`, plain.ID).Scan(&plainAfter); err != nil || plainAfter != plainTokens {
		t.Fatal("unqualified index changed", err)
	}
	history := schemaSnapshot(t, p)
	if err = p.Init(ctx); err != nil || !reflect.DeepEqual(history, schemaSnapshot(t, p)) {
		t.Fatal("upgrade replayed", err)
	}
	// Pending rows also search qualifiers before backfill, using the legacy parser.
	schemaExec(t, p, `UPDATE memory_records SET search_tokens=NULL WHERE id=$1`, r.ID)
	if hits := search("미확인"); len(hits) != 1 || hits[0].ID != r.ID {
		t.Fatal("pending context has no fallback")
	}
	if n, more, err := p.BackfillSearchTokens(ctx, time.Minute); err != nil || more || n != 1 {
		t.Fatal("context backfill did not resume", n, more, err)
	}
	if hits := search("보고서"); len(hits) != 1 {
		t.Fatal("resumed backfill lost Korean qualifier")
	}
	changed := after
	changed.MemoryContext = &MemoryContext{Domains: []string{"work"}, EvidenceKind: "inference", SemanticKind: "lesson", Applicability: "새로운환경에서만"}
	changed.Revision++
	if err = p.Replace(ctx, colMemories, changed, after.Revision); err != nil {
		t.Fatal(err)
	}
	if len(search("보고서")) != 0 || len(search("새로운환경")) != 1 {
		t.Fatal("replacement kept stale context tokens")
	}
}

// A small fixture normally favors a sequential scan. Disabling that preference
// checks that every OR arm has an index path, not that production always picks
// one or meets a latency target. A concatenated legacy/context vector fails this.
func TestMemoryContextSearchHasIndexPathsPostgres(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	terms := searchQueryTerms("conditionprobe")
	q := searchTermsQuery(terms)
	var raw []byte
	query := `EXPLAIN (FORMAT JSON, COSTS OFF) SELECT id FROM memory_records WHERE ` + fmt.Sprintf(pgSearchMatch, 1, 2, 3, 4)
	if err = tx.QueryRow(ctx, query, q, q, q, terms).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan map[string]any }
	if err = json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatal("invalid explain output", err)
	}
	indexes := map[string]bool{}
	var walk func(map[string]any)
	walk = func(plan map[string]any) {
		if plan["Node Type"] == "Seq Scan" {
			t.Fatal("FTS predicate lost its index path")
		}
		if name, ok := plan["Index Name"].(string); ok {
			indexes[name] = true
		}
		if children, ok := plan["Plans"].([]any); ok {
			for _, child := range children {
				walk(child.(map[string]any))
			}
		}
	}
	walk(plans[0].Plan)
	for _, name := range []string{"memory_records_search_tokens", "memory_records_fts", "memory_records_context_fts"} {
		if !indexes[name] {
			t.Fatalf("FTS arm cannot use %s", name)
		}
	}
}

func TestMemoryContextLongBodyQualifiersPostgres(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	now := time.Now().UTC()
	r := Record{ID: ID(), Scope: scopeA, Kind: "memory", Status: statusActive, Revision: 1, ApprovedBy: "reviewer", CreatedAt: now, UpdatedAt: now,
		Text: strings.Repeat("word ", searchTokenMaxPerDoc+1), MemoryContext: &MemoryContext{Domains: []string{"work"}, EvidenceKind: "inference", SemanticKind: "lesson", Applicability: "conditiononlytoken", Uncertainty: "uncertaintoken", ValidFrom: "futuretoken"}}
	if err := p.Insert(ctx, colMemories, r); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		for _, q := range []string{"conditiononlytoken", "uncertaintoken", "futuretoken"} {
			lanes, err := p.Search(ctx, scopeA, q, nil, "", 3, "fts")
			if err != nil || len(lanes[1]) != 1 || lanes[1][0].ID != r.ID {
				t.Fatalf("long body lost qualifier %s: %v", q, err)
			}
		}
	}
	check()
	schemaExec(t, p, `UPDATE memory_records SET search_tokens=NULL,search_context_indexed=false WHERE id=$1`, r.ID)
	if n, more, err := p.BackfillSearchTokens(ctx, time.Minute); err != nil || more || n != 1 {
		t.Fatal("long context backfill", n, more, err)
	}
	check()
}
