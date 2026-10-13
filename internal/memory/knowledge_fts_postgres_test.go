//go:build linux

package memory

// Real-PostgreSQL knowledge full-text tests. They skip when POSTGRES_TEST_DSN
// is not configured (see tools/check_postgres_local.sh).
import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPostgresKnowledgeEndToEndUsesFullTextSearch(t *testing.T) {
	p := isolatedPostgres(t)
	f, ing, b, req := knowledgeRealFixtureOn(t, p)
	// Writes store the knowledge tokens (with Korean bigrams) in search_tokens.
	var tokens string
	if err := p.pool.QueryRow(testCtx, `SELECT search_tokens::text FROM memory_records WHERE collection=$1 AND id=$2`, colKnowledgeChunks, ing.Chunks[1].ID).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'절차'", "'배포절차'", "'재단'"} {
		if !strings.Contains(tokens, want) {
			t.Fatalf("%s missing from %s", want, tokens)
		}
	}
	got, err := f.svc.RetrieveKnowledge(testCtx, f.work, req)
	if err != nil || len(got.Context.Items) != 1 || got.Context.Items[0].SectionPath != "인쇄 가이드 > 재단" {
		t.Fatalf("postgres retrieval: %v %+v", err, got)
	}
	if again, err := f.svc.RevalidateKnowledge(testCtx, f.work, knowledgeValidateRequest(got)); err != nil || again.ContextHash != got.ContextHash {
		t.Fatalf("revalidate: %v", err)
	}
	search, err := f.svc.SearchKnowledge(testCtx, f.work, KnowledgeSearchInput{Scope: req.Scope, ProfileID: "illustrator", Query: "인쇄 CMYK 저장", Limit: 8})
	if err != nil || len(search.BindingIDs) != 1 || search.BindingIDs[0] != b.ID || len(search.Receipt.Context.Items) < 2 || search.Receipt.Context.Items[0].SectionPath != "인쇄 가이드 > 색상 > CMYK" {
		t.Fatalf("postgres search: %v %+v", err, search)
	}
	sources, err := f.svc.ListKnowledgeSources(testCtx, f.human, req.Scope, 0)
	if err != nil || len(sources.Items) != 1 || len(sources.Items[0].Bindings) != 1 {
		t.Fatalf("list sources: %v %+v", err, sources)
	}
	docs, err := f.svc.ListKnowledgeDocuments(testCtx, f.human, req.Scope, ing.Source.ID)
	if err != nil || len(docs.Items) != 1 || len(docs.Items[0].Sections) != 4 {
		t.Fatalf("list documents: %v %+v", err, docs)
	}
}

func TestPostgresRankKnowledgeTextKeepsAllowlistAndLegacyRows(t *testing.T) {
	p := isolatedPostgres(t)
	f, ing, _, req := knowledgeRealFixtureOn(t, p)
	_ = f
	ids := []string{}
	for _, c := range ing.Chunks {
		ids = append(ids, c.ID)
	}
	var ranks []KnowledgeTextRank
	err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		var e error
		ranks, e = tx.RankKnowledgeText(ctx, req.Scope, ids[:2], knowledgeQueryTerms("절차 재단 Intro"))
		return e
	})
	if err != nil || len(ranks) != 2 || ranks[0].ID != ing.Chunks[1].ID || ranks[0].Matched <= ranks[1].Matched {
		t.Fatalf("ranks %v %+v", err, ranks)
	}
	// Nothing outside the allowlist, nothing from another scope.
	other := req.Scope
	other.AgentID = "other-agent"
	err = p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		r, e := tx.RankKnowledgeText(ctx, other, ids, knowledgeQueryTerms("절차"))
		if e == nil && len(r) != 0 {
			return errors.New("cross scope rank")
		}
		r, e = tx.RankKnowledgeText(ctx, req.Scope, ids[2:], knowledgeQueryTerms("절차"))
		if e == nil && len(r) != 0 {
			return errors.New("allowlist widened")
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	// A row written before knowledge chunks had search_tokens still matches.
	if _, err = p.pool.Exec(testCtx, `UPDATE memory_records SET search_tokens=NULL WHERE collection=$1 AND id=$2`, colKnowledgeChunks, ing.Chunks[3].ID); err != nil {
		t.Fatal(err)
	}
	err = p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		var e error
		ranks, e = tx.RankKnowledgeText(ctx, req.Scope, ids, knowledgeQueryTerms("appendix"))
		return e
	})
	if err != nil || len(ranks) != 1 || ranks[0].ID != ing.Chunks[3].ID {
		t.Fatalf("legacy row: %v %+v", err, ranks)
	}
}
