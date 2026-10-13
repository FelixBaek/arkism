package memory

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEffectiveAtBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, offset := range []time.Duration{-time.Millisecond, 0, time.Millisecond} {
		start := now.Add(offset)
		r := Record{Status: "active", EffectiveAt: &start}
		want := offset <= 0
		if liveAt(r, now) != want || searchableLive(r, colMemories, "", now, nil) != want {
			t.Fatalf("start boundary %v", offset)
		}
		expiry := now
		r.ExpiresAt = &expiry
		if liveAt(r, now) {
			t.Fatal("expiry is exclusive")
		}
	}
	if !liveAt(Record{Status: "active"}, now) {
		t.Fatal("legacy absent time")
	}
}

func exerciseEffectiveAt(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	src := f.source()
	future := time.Now().Add(24 * time.Hour).In(time.FixedZone("KST", 9*3600))
	input := candidateInput(CreateInput{Scope: scopeA, Text: "effectiveneedle", Keywords: []string{"effectiveneedle"}, Sources: refs(src), EffectiveAt: &future})
	c := f.create(f.human, "memory_candidates", input)
	want := future.UTC().Truncate(time.Millisecond)
	if c.EffectiveAt == nil || !c.EffectiveAt.Equal(want) {
		t.Fatal("creation lost time", c.EffectiveAt)
	}
	m, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "edit_approve", Content: "effectiveneedle reviewed"})
	if err != nil {
		t.Fatal(err)
	}
	if m.EffectiveAt == nil || !m.EffectiveAt.Equal(want) || m.Status != "active" {
		t.Fatal("approval lost time")
	}
	// The actual worker must publish an embedding before the start time, not drop
	// the job or require a scheduler to create another job when the time arrives.
	if err = f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal("future embedding", err)
	}
	m = f.get(f.human, colMemories, scopeA, m.ID)
	if m.Embedding == nil || len(m.Embedding.Vector) != VectorDimension || !m.EffectiveAt.Equal(want) {
		t.Fatal("future embedding was discarded")
	}
	// Native Mongo has no $vectorSearch. Inspect the persisted vector above,
	// then use the same native search path in both fixtures.
	f.svc.Embedder = nil
	search := SearchInput{Scope: scopeA, Query: "effectiveneedle", BudgetTokens: 2000, TokenEncoding: "cl100k_base"}
	if got := f.search(f.human, search); len(got.Items) != 0 {
		t.Fatal("future search exposure", got)
	}
	page, err := f.svc.ListMemories(testCtx, f.human, MemoryListInput{Scope: scopeA})
	if err != nil || len(page.Items) != 0 {
		t.Fatal("future default list", page, err)
	}
	page, err = f.svc.ListMemories(testCtx, f.human, MemoryListInput{Scope: scopeA, IncludeInactive: true})
	if err != nil || len(page.Items) != 1 || !page.Items[0].EffectiveAt.Equal(want) {
		t.Fatal("future history", page, err)
	}
	// An immediate replacement cannot retire the old memory in favour of a
	// still-future one. Creating new replacement content starts it immediately.
	old := f.activeMemory("old unrelated")
	_, err = f.svc.Supersede(testCtx, f.human, old.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "replace", ReplacementMemoryID: m.ID, ReplacementExpectedRevision: m.Revision})
	if !errors.Is(err, ErrConflict) {
		t.Fatal("future immediate replacement", err)
	}
	marked, err := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Action: "mark", Reason: "conflicting schedule", Memories: []Ref{{Collection: colMemories, ID: old.ID, Revision: old.Revision}, {Collection: colMemories, ID: m.ID, Revision: m.Revision}}})
	if err != nil {
		t.Fatal(err)
	}
	resolve := ConflictInput{Scope: scopeA, Action: "resolve", Reason: "resolve schedule", Memories: marked.Relation.Relations, ConflictID: marked.Relation.ID, ExpectedRevision: marked.Relation.Revision, Resolution: "second_supersedes_first"}
	if _, err = f.svc.Conflict(testCtx, f.human, resolve); !errors.Is(err, ErrConflict) {
		t.Fatal("conflict bypassed future replacement guard", err)
	}
	resolve.Resolution = "keep_both"
	resolved, err := f.svc.Conflict(testCtx, f.human, resolve)
	if err != nil {
		t.Fatal(err)
	}
	old, m = resolved.Memories[0], resolved.Memories[1]
	if m.EffectiveAt == nil || !m.EffectiveAt.Equal(want) || m.Status != "active" {
		t.Fatal("conflict resolution lost start time")
	}
	// Set the test fixture's time in the past without touching status/embedding;
	// the existing vector must immediately become usable, with no new job.
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	m.EffectiveAt = &past
	if err = repo.Replace(testCtx, colMemories, m, m.Revision); err != nil {
		t.Fatal(err)
	}
	got := f.search(f.human, search)
	if len(got.Items) != 1 || got.Items[0].ID != m.ID || got.Items[0].EffectiveAt == nil || !got.Items[0].EffectiveAt.Equal(past) {
		t.Fatal("effective context", got)
	}
	if !strings.Contains(got.ContextData, "effective_at") {
		t.Fatal("context data lost start time")
	}
	next, err := f.svc.Supersede(testCtx, f.human, old.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "replace", NewContent: "replacement content", Sources: refs(src)})
	if err != nil || next.New.EffectiveAt == nil || !next.New.EffectiveAt.Equal(next.New.CreatedAt) {
		t.Fatal("new replacement start", next, err)
	}
}
func TestEffectiveAtLifecycle(t *testing.T) { exerciseEffectiveAt(t, newFakeRepo()) }

func TestEffectiveAtHTTPValidation(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	future := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name       string
		at, expiry *time.Time
		code       int
	}{
		{"default", nil, nil, 201}, {"future", &future, nil, 201},
		{"zero", new(time.Time), nil, 400}, {"expiry_equals_start", &future, &future, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := candidateInput(CreateInput{Scope: scopeA, Text: "candidate", Sources: refs(src), EffectiveAt: tc.at, ExpiresAt: tc.expiry})
			req := httptest.NewRequest("POST", "/v1/memory_candidates", strings.NewReader(structuredText(in)))
			req.Header.Set("Authorization", "Bearer "+f.human)
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			if w.Code != tc.code {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.code == 201 && !strings.Contains(w.Body.String(), "effective_at") {
				t.Fatal("missing response time")
			}
		})
	}
	in := sourceInput(CreateInput{Scope: scopeA, Text: "source", EffectiveAt: &future})
	if _, err := f.svc.Create(testCtx, f.human, "sources", in); !errors.Is(err, ErrInvalid) {
		t.Fatal("source accepted memory time", err)
	}
}
