package memory

import (
	"errors"
	"testing"
)

func TestPostgresKnowledgeGraphEvidenceLifecycle(t *testing.T) {
	p := isolatedPostgres(t)
	if err := p.initTraceSchema(testCtx); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	f.svc.Repo = p
	r := graphMemory(t, f, p, "graph-evidence")
	in := graphClaim(r, "opaque-user", "opaque-server")
	if _, err := f.svc.PutKnowledgeRelation(testCtx, f.human, in); err != nil {
		t.Fatal(err)
	}
	// Same assertion is idempotent, not a duplicate or a silent overwrite.
	if _, err := f.svc.PutKnowledgeRelation(testCtx, f.human, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.PutKnowledgeRelation(testCtx, f.human2, in); !errors.Is(err, ErrConflict) {
		t.Fatal("overwrote author", err)
	}
	query := KnowledgeGraphInput{Scope: scopeA, Start: in.Source, MaxDepth: 3}
	for i := 0; i < 2; i++ {
		out, err := f.svc.ExpandKnowledgeGraph(testCtx, f.agent, query)
		if err != nil || len(out.Items) != 1 {
			t.Fatal("read fence removed graph", out, err)
		}
	}
	assertEmpty := func() {
		t.Helper()
		var count int
		if err := p.q().QueryRow(testCtx, `SELECT count(*) FROM relations WHERE starts_with(relation_kind,'asserted_')`).Scan(&count); err != nil || count != 0 {
			t.Fatal("relations retained", count, err)
		}
		if err := p.q().QueryRow(testCtx, `SELECT count(*) FROM entities WHERE starts_with(kind,'asserted_')`).Scan(&count); err != nil || count != 0 {
			t.Fatal("identifiers retained", count, err)
		}
	}
	// Delete a raw source directly: trigger must remove derived graph identifiers,
	// without waiting for the asynchronous memory erasure worker.
	source := r.Sources[0].SourceID
	original, err := p.Get(testCtx, "sources", scopeA, source)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Delete(testCtx, "sources", scopeA, source, original.Revision); err != nil {
		t.Fatal(err)
	}
	assertEmpty()
	// A changed memory revision also removes the corresponding assertion.
	r = graphMemory(t, f, p, "graph-evidence-2")
	in = graphClaim(r, "task-a", "task-b")
	if _, err = f.svc.PutKnowledgeRelation(testCtx, f.human, in); err != nil {
		t.Fatal(err)
	}
	r.Revision++
	if err = p.Replace(testCtx, colMemories, r, 1); err != nil {
		t.Fatal(err)
	}
	assertEmpty()
}
