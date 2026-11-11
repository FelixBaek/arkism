package memory

import (
	"strings"
	"testing"
	"time"
)

func TestRetrievedMemoryPreservesObservationAnchor(t *testing.T) {
	observed := time.Date(2023, 8, 13, 0, 0, 0, 0, time.UTC)
	ingested := observed.AddDate(2, 0, 0)
	r := Record{ID: "anchor-memory", Text: "The workshop happened yesterday", CreatedAt: ingested,
		MemoryContext: &MemoryContext{ObservedAt: &observed},
		Occurred:      &RecallTimeWindow{From: observed.AddDate(0, 0, -1), To: observed}}
	h := HitFromRecord(colMemories, r)
	_, prompt := reflectPrompt(&reflectRun{in: ReflectInput{Query: "When was the workshop?"}, hits: []Hit{h}}, true)
	if !strings.Contains(prompt, `"recorded_at":"2023-08-13T00:00:00Z"`) || !strings.Contains(prompt, `"from":"2023-08-12T00:00:00Z"`) {
		t.Fatal("retrieval lost observation anchor or occurrence interval", prompt)
	}
	if strings.Contains(prompt, "2025-08-13") {
		t.Fatal("ingestion time used as conversation time")
	}
}

func TestRetrievedTimeAnchorDoesNotInventObservation(t *testing.T) {
	at := time.Date(2023, 8, 13, 0, 0, 0, 0, time.UTC)
	later := at.AddDate(0, 0, 3)
	for _, tc := range []struct {
		name string
		row  Record
		want *time.Time
	}{
		{"explicit collection", Record{CollectedAt: &at, MemoryContext: &MemoryContext{ObservedAt: &later}}, &at},
		{"unknown observation", Record{CreatedAt: at, EffectiveAt: &later}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := HitFromRecord(colMemories, tc.row)
			if tc.want == nil {
				if h.CollectedAt != nil {
					t.Fatal("invented observation time")
				}
				return
			}
			if h.CollectedAt == nil || !h.CollectedAt.Equal(*tc.want) {
				t.Fatal("explicit anchor lost")
			}
		})
	}
}

func TestCapturedObservationAnchorSurvivesReviewAndRetrieval(t *testing.T) {
	f := newFixture(t)
	src := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Kind: "tool_result", Text: "The workshop happened yesterday"}))
	claim := canonicalToolClaim(src)
	claim.MemoryContext = &MemoryContext{Domains: []string{"work"}, EvidenceKind: "tool_observation", SemanticKind: "fact"}
	in := captureInput(scopeA, "anchor-capture", claim)
	got := f.capture(f.agent, in)[0]
	if got.Status != "candidate" {
		t.Fatal(got)
	}
	candidate := f.get(f.agent, "memory_candidates", scopeA, got.CandidateID)
	approved, err := f.svc.ReviewCandidate(testCtx, f.human, candidate.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	hit := HitFromRecord(colMemories, approved)
	if hit.CollectedAt == nil || !hit.CollectedAt.Equal(in.Event.OccurredAt.Truncate(time.Millisecond)) {
		t.Fatal("capture/review/retrieval lost event observation anchor")
	}
}
