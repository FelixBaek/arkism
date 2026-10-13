package memory

import (
	"errors"
	"testing"
)

func TestNewSourceAndCandidateKinds(t *testing.T) {
	f := newFixture(t)
	source := f.source()
	for _, kind := range []string{"", "memory", "Semantic", " semantic", "skill_file"} {
		in := sourceInput(CreateInput{Scope: scopeA, Text: "kind source"})
		in.Kind = kind
		if _, e := f.svc.Create(testCtx, f.human, "sources", in); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid source kind accepted", kind, e)
		}
		if _, e := f.svc.Create(testCtx, f.human, "memory_candidates", CreateInput{Kind: kind, Scope: scopeA, Text: "kind candidate", Sources: refs(source)}); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid candidate kind accepted", kind, e)
		}
	}
	for _, kind := range []string{"conversation", "message", "note", "document", "url", "image", "tool_result", "design_page"} {
		in := sourceInput(CreateInput{Kind: kind, Scope: scopeA, Text: "source kind " + kind})
		r, e := f.svc.Create(testCtx, f.human, "sources", in)
		if e != nil || r.Kind != kind {
			t.Fatal("source kind lost", kind, e)
		}
	}
	for _, kind := range []string{"raw", "semantic", "episodic", "procedural", "user", "decision", "lesson"} {
		r, e := f.svc.Create(testCtx, f.human, "memory_candidates", CreateInput{Kind: kind, Scope: scopeA, Text: "candidate kind " + kind, Sources: refs(source)})
		if e != nil || r.Kind != kind {
			t.Fatal("candidate kind lost", kind, e)
		}
	}
}

func TestCandidateKindSurvivesApprovalAndSearch(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	r := f.create(f.human, "memory_candidates", CreateInput{Kind: "decision", Scope: scopeA, Text: "recorded choice", Sources: refs(src)})
	approved := f.transition(f.human, "memory_candidates", r.ID, ChangeInput{Scope: scopeA, ExpectedRevision: 1, Status: "approved"})
	f.transition(f.human, "memories", approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision, Status: "active"})
	out := f.search(f.human, SearchInput{Scope: scopeA, Query: "recorded"})
	if len(out.Items) != 1 || out.Items[0].Kind != "decision" {
		t.Fatal("classified decision lost")
	}
}

func TestLegacyKindsAreNotReclassified(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("legacy classification")
	r.Kind = "old-memory-kind"
	f.repo.store["memories"][recKey(scopeA, r.ID)] = r
	sourceID := r.Sources[0].SourceID
	source := f.repo.store["sources"][recKey(scopeA, sourceID)]
	source.Kind = "old-source-kind"
	f.repo.store["sources"][recKey(scopeA, sourceID)] = source
	if got := f.get(f.human, "sources", scopeA, sourceID); got.Kind != "old-source-kind" {
		t.Fatal("legacy source reclassified")
	}
	result := f.search(f.human, SearchInput{Scope: scopeA, Query: "legacy"})
	if len(result.Items) != 1 || result.Items[0].Kind != "old-memory-kind" {
		t.Fatal("legacy memory reclassified")
	}
}
