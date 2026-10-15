package memory

import (
	"errors"
	"strings"
	"testing"
)

// This is an offline service contract, not a model-quality benchmark or a test
// of the Claude/Codex adapters. Independent callers share only durable storage.
func TestCrossAgentMemoryReuseLifecycle(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	const secondToken = "tok-second-agent"
	f.auth.actors[secondToken] = actor("agent-2", "agent", true, allVerbs...)
	reader := &Service{Repo: f.repo, Auth: f.auth}
	const rule = "For reusefixture, run the focused package tests before the full suite."
	src := f.create(f.human, "sources", sourceInput(CreateInput{
		Scope: scopeA, Text: rule, TrustLevel: "user_authored",
	}))
	candidate := f.create(f.agent, "memory_candidates", candidateInput(CreateInput{
		Scope: scopeA, Text: rule, Keywords: []string{"reusefixture"}, Sources: refs(src),
		Generator: &Generator{Model: "test-model", PromptVersion: "p1", PipelineVersion: "v1"},
	}))
	search := func(scope Scope, wantID string) {
		t.Helper()
		out, err := reader.Search(testCtx, secondToken, SearchInput{
			Scope: scope, Query: "reusefixture", Modes: []string{"exact"},
			BudgetTokens: 2000, TokenEncoding: "cl100k_base",
		})
		if err != nil {
			t.Fatal(err)
		}
		if wantID == "" {
			if len(out.Items) != 0 || strings.Contains(out.ContextData, rule) {
				t.Fatalf("unavailable memory leaked into context: %+v", out)
			}
			return
		}
		if len(out.Items) != 1 || out.Items[0].ID != wantID ||
			out.Items[0].TrustLevel != "user_verified" ||
			out.Items[0].Authority != AuthorityDataOnly ||
			!strings.Contains(out.ContextData, rule) || out.ContextID == "" {
			t.Fatalf("approved memory was not reusable with its trust labels: %+v", out)
		}
		if len(out.Items[0].Sources) != 1 || out.Items[0].Sources[0].SourceID != src.ID {
			t.Fatalf("source provenance lost: %+v", out.Items[0])
		}
	}

	search(scopeA, "")
	for _, token := range []string{f.agent, secondToken} {
		_, err := f.svc.Transition(testCtx, token, "memory_candidates", candidate.ID, ChangeInput{
			Scope: scopeA, ExpectedRevision: candidate.Revision, Status: "approved",
		})
		if !errors.Is(err, ErrDenied) {
			t.Fatalf("agent approval = %v, want ErrDenied", err)
		}
	}
	search(scopeA, "")
	approved := f.transition(f.human, "memory_candidates", candidate.ID, ChangeInput{
		Scope: scopeA, ExpectedRevision: candidate.Revision, Status: "approved",
	})
	active := f.transition(f.human, "memories", approved.ID, ChangeInput{
		Scope: scopeA, ExpectedRevision: approved.Revision, Status: "active",
	})
	search(scopeA, active.ID)
	search(scopeB, "")

	// Simulate an index that still returns the old active row after its evidence
	// is removed. Publication must recheck the source, including for a new caller.
	f.repo.stale = []Hit{HitFromRecord("memories", active)}
	if err := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{
		Scope: scopeA, ExpectedRevision: src.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	reader = &Service{Repo: f.repo, Auth: f.auth}
	search(scopeA, "")
}
