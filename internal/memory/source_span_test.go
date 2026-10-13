package memory

import (
	"errors"
	"strings"
	"testing"
)

func TestCandidateSourceSpanValidation(t *testing.T) {
	f := newFixture(t)
	src := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "가😀나다"}))
	good := SourceRef{SourceID: src.ID, Locator: "chars", Span: &SourceSpan{ContentHash: ContentHash(src.Text), Start: 1, End: 3}}
	for _, test := range []struct {
		span *SourceSpan
		want error
	}{
		{nil, ErrInvalid},
		{&SourceSpan{ContentHash: good.Span.ContentHash, Start: -1, End: 1}, ErrInvalid},
		{&SourceSpan{ContentHash: good.Span.ContentHash, Start: 1, End: 1}, ErrInvalid},
		{&SourceSpan{ContentHash: good.Span.ContentHash, End: 5}, ErrInvalid},
		{&SourceSpan{ContentHash: strings.Repeat("0", 64), End: 1}, ErrConflict},
	} {
		ref := good
		ref.Span = test.span
		if _, e := f.svc.Create(testCtx, f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "candidate", Sources: []SourceRef{ref}})); !errors.Is(e, test.want) {
			t.Fatal("invalid span accepted", e, test.want)
		}
	}
	rec := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "candidate", Sources: []SourceRef{good}}))
	if rec.Sources[0].Span.End != 3 {
		t.Fatal("span lost")
	}
	rows, e := sourceMetadata(testCtx, f.repo, scopeA, rec.Sources)
	if e != nil || len(rows) != 1 || rows[0].Text != "" {
		t.Fatal("source metadata exposed body", e)
	}
	src.Text = "changed original"
	f.repo.store["sources"][recKey(scopeA, src.ID)] = src
	if e := checkSources(testCtx, f.repo, rec); !errors.Is(e, ErrConflict) {
		t.Fatal("changed original accepted", e)
	}
}

func TestChangedSourceSpanBlocksApprovalAndSearch(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("tracked original")
	pending := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "pending claim", Sources: r.Sources}))
	sourceID := r.Sources[0].SourceID
	src := f.repo.store["sources"][recKey(scopeA, sourceID)]
	// Simulate an out-of-band administrator changing the original body.
	src.Text = "another text"
	f.repo.store["sources"][recKey(scopeA, sourceID)] = src
	if _, e := f.svc.Transition(testCtx, f.human, "memory_candidates", pending.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "approved"}); !errors.Is(e, ErrConflict) {
		t.Fatal("changed evidence approved", e)
	}
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "tracked"})
	if e != nil || len(out.Items) != 0 {
		t.Fatal("changed evidence returned", e)
	}
	if _, e := f.svc.authorizeTransfer(testCtx, f.human, "embed:memories", scopeA, r.ID, r.Sensitivity, "voyage", r.Text, r.Sources); !errors.Is(e, ErrConflict) {
		t.Fatal("changed evidence exported", e)
	}
	// Old spanless data remains readable; no inferred locations are added.
	legacy := r
	legacy.Sources = []SourceRef{{SourceID: sourceID, Locator: "legacy"}}
	if e := checkSources(testCtx, f.repo, legacy); e != nil {
		t.Fatal("legacy source rejected", e)
	}
}

func TestSourceProofUsesTwoBatchesWithoutReturningBodies(t *testing.T) {
	f := newFixture(t)
	first := f.source()
	second := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "legacy reference source"}))
	spy := &sourceBatchSpy{Repository: f.repo}
	references := append(first.Sources, SourceRef{SourceID: second.ID, Locator: "legacy"})
	rows, e := sourceMetadata(testCtx, spy, scopeA, references)
	if e != nil || len(rows) != 2 || spy.batches != 2 || spy.sourceReads != 0 {
		t.Fatal("unbounded span proof reads", e)
	}
	for _, row := range rows {
		if row.Text != "" || row.Summary != "" {
			t.Fatal("source body escaped proof checker")
		}
	}
}
