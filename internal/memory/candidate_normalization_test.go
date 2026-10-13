package memory

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func exerciseCandidateNormalization(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	src := f.source()
	a := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "  Prefer\tSHORT\nanswers  ", Sources: refs(src)}))
	b := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "prefer short answers", Sources: refs(src)}))
	if a.NormalizedKey == "" || a.NormalizedKey != b.NormalizedKey || a.ID == b.ID || a.Text != "  Prefer\tSHORT\nanswers  " {
		t.Fatal("normalization changed original or merged", a, b)
	}
	stored, e := repo.Get(testCtx, "memory_candidates", scopeA, a.ID)
	if e != nil || stored.NormalizedKey != a.NormalizedKey {
		t.Fatal("key not stored", e)
	}
	approved, e := f.svc.ReviewCandidate(testCtx, f.human, a.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: a.Revision, Action: "edit_approve", Content: "edited decision"})
	if e != nil {
		t.Fatal(e)
	}
	memory, e := repo.Get(testCtx, colMemories, scopeA, approved.ID)
	if e != nil || memory.NormalizedKey != a.NormalizedKey || memory.NormalizedKey == candidateNormalizedKey(memory) {
		t.Fatal("lost original key after editing", e, memory)
	}
	if strings.Contains(structuredText(HitFromRecord(colMemories, memory)), "normalized_key") {
		t.Fatal("key in model context")
	}
	if _, e = repo.Get(testCtx, "memory_candidates", scopeB, a.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("foreign key leak", e)
	}
	redacted := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "password=not-public", OnSecret: "redact", Sources: refs(src)}))
	if redacted.NormalizedKey != candidateNormalizedKey(redacted) || strings.Contains(redacted.Text, "not-public") {
		t.Fatal("key derived before sanitization")
	}
	payload := strings.TrimSuffix(structuredText(candidateInput(CreateInput{Scope: scopeA, Text: "proposal", Sources: refs(src)})), "}") + `,"normalized_key":"` + a.NormalizedKey + `"}`
	req := httptest.NewRequest("POST", "/v1/memory_candidates", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatal("caller supplied server key", w.Code)
	}

	if e = f.svc.Delete(testCtx, f.human, colMemories, memory.ID, ChangeInput{Scope: scopeA, ExpectedRevision: memory.Revision}); e != nil {
		t.Fatal(e)
	}
	for _, col := range []string{colMemories, "memory_candidates"} {
		if _, e = repo.Get(testCtx, col, scopeA, a.ID); !errors.Is(e, ErrNotFound) {
			t.Fatal("deleted original key remains", col, e)
		}
	}
	kept, e := repo.Get(testCtx, "memory_candidates", scopeA, b.ID)
	if e != nil || kept.NormalizedKey != b.NormalizedKey {
		t.Fatal("same-key sibling deleted", e)
	}

}
func TestCandidateNormalization(t *testing.T) { exerciseCandidateNormalization(t, newFakeRepo()) }
func TestCandidateNormalizationRecipe(t *testing.T) {
	r := Record{Scope: scopeA, Kind: "semantic", Text: " A\u2003B "}
	key := candidateNormalizedKey(r)
	if key != "v1:ca19a2a7a2f43d06b98ee7cf7ca9373cc1bb2fe7ea6780ffe87bbdcd15b657c5" {
		t.Fatal("v1 recipe changed; use a new version", key)
	}
	for _, tc := range []struct {
		scope      Scope
		kind, text string
		equal      bool
	}{{scopeA, "semantic", "a b", true}, {scopeB, "semantic", "a b", false}, {scopeA, "decision", "a b", false}, {scopeA, "semantic", "ab", false}, {scopeA, "semantic", "a c", false}} {
		got := candidateNormalizedKey(Record{Scope: tc.scope, Kind: tc.kind, Text: tc.text})
		if (got == key) != tc.equal {
			t.Fatal(tc, got, key)
		}
	}
	if len(key) != 67 || !sourceHashPattern.MatchString(strings.TrimPrefix(key, "v1:")) {
		t.Fatal("key format", key)
	}
}
