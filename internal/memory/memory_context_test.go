package memory

import (
	"strings"
	"testing"
	"time"
)

func TestMemoryContextMetadataValidation(t *testing.T) {
	now := time.Now().UTC()
	base := MemoryContext{Domains: []string{"work", "personal"}, EvidenceKind: "inference", SemanticKind: "preference", Applicability: "업무 발표에는 간결하게, 개인 일기에는 자세하게", ValidFrom: "다음 발표부터"}
	if !validMemoryContext(&base, now) {
		t.Fatal("valid mixed context rejected")
	}
	cases := []func(*MemoryContext){func(c *MemoryContext) { c.Domains = []string{"admin"} }, func(c *MemoryContext) { c.Domains = []string{"work", "work"} }, func(c *MemoryContext) { c.EvidenceKind = "verified" }, func(c *MemoryContext) { c.SemanticKind = "instruction" }, func(c *MemoryContext) { c.Applicability = strings.Repeat("가", 2001) }, func(c *MemoryContext) { future := now.Add(time.Hour); c.ObservedAt = &future }}
	for i, change := range cases {
		c := *copyMemoryContext(&base)
		change(&c)
		if validMemoryContext(&c, now) {
			t.Fatalf("invalid metadata %d accepted", i)
		}
	}
	first := copyMemoryContext(&base)
	first.ObservedAt = &now
	later := copyMemoryContext(&base)
	at := now.Add(time.Minute)
	later.ObservedAt = &at
	later.Domains = []string{"personal", "work"}
	if memoryContextKey(first) != memoryContextKey(later) {
		t.Fatal("observation/order duplicated semantic claim")
	}
	later.Applicability = "개인 일기에만"
	if memoryContextKey(first) == memoryContextKey(later) {
		t.Fatal("distinct condition lost")
	}
	if memoryContextKey(nil) != "" {
		t.Fatal("legacy metadata must be omitted")
	}
}

func TestMemoryContextCapturePreservesConditionsAndHostTime(t *testing.T) {
	f := newFixture(t)
	src := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Kind: "tool_result", Text: "checker failed in personal notebook and succeeded in work report"}))
	claim := canonicalToolClaim(src)
	claim.MemoryContext = &MemoryContext{Domains: []string{"personal", "work"}, Applicability: "업무 보고서에서만", EvidenceKind: "tool_observation", SemanticKind: "failure_recovery", ValidFrom: "다음 실행부터", Uncertainty: "다른 프로젝트는 확인하지 않음"}
	old := time.Now().Add(-24 * time.Hour)
	claim.MemoryContext.ObservedAt = &old
	in := captureInput(scopeA, "context-capture-1", claim)
	got := f.capture(f.agent, in)[0]
	if got.Status != "candidate" {
		t.Fatal(got)
	}
	c := f.get(f.agent, "memory_candidates", scopeA, got.CandidateID)
	if c.MemoryContext == nil || c.MemoryContext.Applicability != claim.MemoryContext.Applicability || !c.MemoryContext.ObservedAt.Equal(in.Event.OccurredAt.Truncate(time.Millisecond)) || c.Scope != scopeA || c.TrustLevel == "user_verified" {
		t.Fatal("context or authority changed", c)
	}
	second := f.capture(f.agent, captureInput(scopeA, "context-capture-2", claim))[0]
	if second.Status != "merged" {
		t.Fatal("same semantic context failed to dedupe", second)
	}
	claim.MemoryContext.Applicability = "개인 노트에서만"
	third := f.capture(f.agent, captureInput(scopeA, "context-capture-3", claim))[0]
	if third.CandidateID == got.CandidateID || third.Status != "candidate" {
		t.Fatal("different applicability merged", third)
	}
	approved, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "edit_approve", Content: "수정한 복구 절차"})
	if err != nil {
		t.Fatal(err)
	}
	if approved.MemoryContext == nil || approved.MemoryContext.ValidFrom != "다음 실행부터" || !approved.EffectiveAt.Before(time.Now()) {
		t.Fatal("review discarded context or interpreted descriptive date", approved)
	}
	hit := HitFromRecord("memories", approved)
	if hit.MemoryContext == nil || hit.Authority != "data_only_untrusted" {
		t.Fatal("retrieval lost data context")
	}
	if err = f.svc.Delete(testCtx, f.human, "memories", approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Get(testCtx, f.human, "memories", scopeA, approved.ID); err == nil {
		t.Fatal("deleted context still readable")
	}
}

func TestMemoryContextEvidenceLabelsCannotUpgradeSources(t *testing.T) {
	for _, kind := range []string{"user_statement", "tool_observation", "agent_statement"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			in := candidateInput(CreateInput{Scope: scopeA, Text: "unverified claim", Sources: refs(src)})
			in.MemoryContext = &MemoryContext{Domains: []string{"personal", "work"}, EvidenceKind: kind, SemanticKind: "fact"}
			if _, err := f.svc.Create(testCtx, f.human, "memory_candidates", in); err == nil {
				t.Fatal("unverified provenance accepted")
			}
		})
	}
}

func TestMemoryContextSynthesizedSourceKeepsDistinctConditions(t *testing.T) {
	f := newFixture(t)
	claim := explicit("자료를 간결하게 정리한다")
	claim.Basis = "inferred"
	claim.MemoryContext = &MemoryContext{Domains: []string{"work"}, EvidenceKind: "inference", SemanticKind: "preference", Applicability: "업무 발표에서"}
	first := f.capture(f.agent, captureInput(scopeA, "context-synthetic-1", claim))[0]
	claim.MemoryContext.Applicability = "개인 노트에서"
	second := f.capture(f.agent, captureInput(scopeA, "context-synthetic-2", claim))[0]
	if first.Status != "candidate" || second.Status != "candidate" || first.CandidateID == second.CandidateID {
		t.Fatal("source digest lost condition", first, second)
	}
	c := f.get(f.agent, "memory_candidates", scopeA, second.CandidateID)
	src := f.get(f.agent, "sources", scopeA, c.Sources[0].SourceID)
	if src.TrustLevel == "user_authored" || src.Generator == nil || c.Sources[0].Span.End != len([]rune(claim.Text)) {
		t.Fatal("derived metadata was presented as user quote", src, c.Sources)
	}
}

func TestMemoryContextArchiveRolesAreCanonicalEvidence(t *testing.T) {
	for _, role := range []string{"user", "assistant"} {
		t.Run(role, func(t *testing.T) {
			f, now := archiveSearchFixture(t, nil)
			raw := jsonLine(map[string]string{"role": role, "content": "발표 자료는 간결하게 정리해 주세요."})
			id := ContentHash("context-archive-" + role)
			archiveSearchUpload(t, f, *now, id, raw, "jsonl")
			claim := explicit("발표 자료는 간결하게 정리한다")
			claim.ArchiveMessages = []ArchiveMessageRef{{ArchiveID: id, SHA256: ContentHash(string(raw))}}
			evidence := "user_statement"
			if role == "assistant" {
				evidence = "agent_statement"
				claim.Basis = "inferred"
			}
			claim.MemoryContext = &MemoryContext{Domains: []string{"personal", "work"}, EvidenceKind: evidence, SemanticKind: "preference", Applicability: "발표 자료 작성 시"}
			in := captureInput(scopeA, "context-archive-event", claim)
			in.Event.SessionID = "search-session"
			item := f.capture(f.agent, in)[0]
			if item.Status != "candidate" {
				t.Fatal("valid archived role rejected", item)
			}
			c := f.get(f.agent, "memory_candidates", scopeA, item.CandidateID)
			if c.MemoryContext.EvidenceKind != evidence || c.TrustLevel == "user_verified" {
				t.Fatal("incorrect evidence authority", c)
			}
			wrong := copyMemoryContext(c.MemoryContext)
			if role == "assistant" {
				wrong.EvidenceKind = "user_statement"
			} else {
				wrong.EvidenceKind = "agent_statement"
			}
			_, err := f.svc.Create(testCtx, f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "mislabelled evidence", Sources: c.Sources, MemoryContext: wrong}))
			if err == nil {
				t.Fatal("wrong canonical archive role accepted")
			}
		})
	}
}

func TestMemoryContextUserAuthoredSourceAndSecretInspection(t *testing.T) {
	f := newFixture(t)
	src := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "I prefer concise work notes", TrustLevel: "user_authored"}))
	context := &MemoryContext{Domains: []string{"work"}, EvidenceKind: "user_statement", SemanticKind: "preference", Applicability: "업무 노트에만"}
	in := candidateInput(CreateInput{Scope: scopeA, Text: "Concise notes", Sources: refs(src), MemoryContext: context})
	r, err := f.svc.Create(testCtx, f.human, "memory_candidates", in)
	if err != nil || r.MemoryContext == nil {
		t.Fatal("canonical human source rejected", err)
	}
	context.Applicability = "Bearer " + strings.Repeat("a", 48)
	if _, err = f.svc.Create(testCtx, f.human, "memory_candidates", in); err == nil {
		t.Fatal("metadata secret was not inspected")
	}
}
