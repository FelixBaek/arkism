package memory

import (
	"slices"
	"testing"
	"time"
)

// TC-FUNC-004/011/016: a duplicate pointer must not suppress a new review of
// expired evidence, cross a repository boundary, or discard explicit sources.
func TestCaptureRestatementAfterApprovedMemoryExpiry(t *testing.T) {
	f := newFixture(t)
	claim := explicit("만료된 규칙은 다시 검토한다.")
	first := f.captureAndReview(f.human, captureInput(scopeA, "expiry-first", claim))[0]
	memory := f.get(f.human, "memories", scopeA, first.MemoryID)
	past := time.Now().UTC().Add(-time.Hour)
	memory.ExpiresAt = &past
	if err := f.repo.Replace(testCtx, "memories", memory, memory.Revision); err != nil {
		t.Fatal(err)
	}
	next := f.capture(f.human, captureInput(scopeA, "expiry-second", claim))[0]
	if next.Status != "candidate" || next.CandidateID == "" || next.CandidateID == first.MemoryID {
		t.Fatalf("expired memory suppressed a new review: %+v", next)
	}
	if got := f.get(f.human, "memory_candidates", scopeA, next.CandidateID); got.Status != "candidate" {
		t.Fatal("restatement automatically approved")
	}
}

func TestCaptureSameTextInDifferentRepositories(t *testing.T) {
	f := newFixture(t)
	claim := explicit("배포 전에 전체 테스트를 실행한다.")
	claim.Kind, claim.Repo = "procedural", "owner/first"
	first := f.capture(f.human, captureInput(scopeA, "repo-first", claim))[0]
	claim.Repo = "owner/second"
	second := f.capture(f.human, captureInput(scopeA, "repo-second", claim))[0]
	if first.Status != "candidate" || second.Status != "candidate" || first.CandidateID == second.CandidateID {
		t.Fatalf("repository evidence lost: first=%+v second=%+v", first, second)
	}
	a := f.get(f.human, "memory_candidates", scopeA, first.CandidateID)
	b := f.get(f.human, "memory_candidates", scopeA, second.CandidateID)
	if !slices.Contains(a.Tags, "repo:owner/first") || !slices.Contains(b.Tags, "repo:owner/second") || a.Sources[0].SourceID == b.Sources[0].SourceID {
		t.Fatal("repository provenance mixed")
	}
	if again := f.capture(f.human, captureInput(scopeA, "repo-second-again", claim))[0]; again.Status != "merged" || again.CandidateID != second.CandidateID {
		t.Fatalf("same repository no longer deduplicated: %+v", again)
	}
}

func TestCaptureUserPreferenceStillDeduplicatesAcrossRepositories(t *testing.T) {
	f := newFixture(t)
	claim := explicit("답변은 간결하게 한다.")
	claim.Repo = "owner/first"
	first := f.capture(f.human, captureInput(scopeA, "user-first", claim))[0]
	claim.Repo = "owner/second"
	second := f.capture(f.human, captureInput(scopeA, "user-second", claim))[0]
	if second.Status != "merged" || second.CandidateID != first.CandidateID {
		t.Fatalf("user preference became repository specific: %+v", second)
	}
}

func TestCaptureDistinctCanonicalSourcesPreserveSeparateReviews(t *testing.T) {
	f := newFixture(t)
	one := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Kind: "tool_result", Text: "First test execution failed."}))
	two := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Kind: "tool_result", Text: "Second test execution failed."}))
	first := f.capture(f.agent, captureInput(scopeA, "evidence-first", canonicalToolClaim(one)))[0]
	second := f.capture(f.agent, captureInput(scopeA, "evidence-second", canonicalToolClaim(two)))[0]
	if second.Status != "candidate" || first.CandidateID == second.CandidateID {
		t.Fatalf("new canonical evidence discarded: %+v", second)
	}
	a := f.get(f.agent, "memory_candidates", scopeA, first.CandidateID)
	b := f.get(f.agent, "memory_candidates", scopeA, second.CandidateID)
	if len(a.Sources) != 1 || a.Sources[0].SourceID != one.ID || len(b.Sources) != 1 || b.Sources[0].SourceID != two.ID {
		t.Fatal("immutable provenance was overwritten")
	}
}

func TestCaptureDuplicateRevalidatesCanonicalSource(t *testing.T) {
	for _, mode := range []string{"deleted", "expired", "quarantined", "revision"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			claim := canonicalToolClaim(src)
			first := f.capture(f.agent, captureInput(scopeA, "source-live", claim))[0]
			if first.Status != "candidate" {
				t.Fatal(first)
			}
			switch mode {
			case "deleted":
				src.Status = "deleted"
			case "expired":
				past := time.Now().UTC().Add(-time.Hour)
				src.ExpiresAt = &past
			case "quarantined":
				src.PrivacyQuarantined = true
			case "revision":
				src.Revision++
			}
			if err := f.repo.Replace(testCtx, "sources", src, 1); err != nil {
				t.Fatal(err)
			}
			next := f.capture(f.agent, captureInput(scopeA, "source-invalid", claim))[0]
			if next.Status != "rejected" {
				t.Fatalf("invalid source accepted through duplicate path: %+v", next)
			}
		})
	}
}

func TestCaptureLegacyPointerPreservesMatchingContext(t *testing.T) {
	for _, mode := range []string{"repository", "canonical-source"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			claim := explicit("호환성 확인용 재진술 규칙.")
			if mode == "repository" {
				claim.Kind, claim.Repo = "procedural", "owner/existing"
			} else {
				claim = canonicalToolClaim(f.source())
			}
			first := f.capture(f.human, captureInput(scopeA, "legacy-first", claim))[0]
			pointer, err := f.repo.Get(testCtx, "jobs", scopeA, first.DedupeKey)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.repo.Delete(testCtx, "jobs", scopeA, pointer.ID, pointer.Revision); err != nil {
				t.Fatal(err)
			}
			pointer.ID = captureKey(scopeA, claim.Kind, claim.Text, claim.Supersedes)
			if err := f.repo.Insert(testCtx, "jobs", pointer); err != nil {
				t.Fatal(err)
			}
			second := f.capture(f.human, captureInput(scopeA, "legacy-second", claim))[0]
			if second.Status != "merged" || second.CandidateID != first.CandidateID || len(f.repo.rows("memory_candidates")) != 1 {
				t.Fatalf("legacy pointer duplicated a review: %+v", second)
			}
			if mode == "repository" {
				claim.Repo = "owner/new"
			} else {
				claim = canonicalToolClaim(f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "New canonical source."})))
			}
			third := f.capture(f.human, captureInput(scopeA, "legacy-third", claim))[0]
			if third.Status != "candidate" || third.CandidateID == first.CandidateID {
				t.Fatalf("legacy context leaked: %+v", third)
			}
		})
	}
}

func TestCaptureDuplicateValidatesArchiveSession(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	claim := explicit("대화의 출처를 확인한다.")
	first, err := f.svc.Capture(testCtx, f.agent, captureInput(scopeA, "archive-first", claim))
	if err != nil || first.Items[0].Status != "candidate" {
		t.Fatal(first, err)
	}
	raw := jsonLine(map[string]string{"role": "user", "content": "대화의 출처를 확인한다."})
	id := ContentHash("duplicate-invalid-session")
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	claim.ArchiveMessages = []ArchiveMessageRef{{ArchiveID: id, SHA256: ContentHash(string(raw))}}
	// The archive is bound to search-session, not the capture fixture's sess-1.
	next, err := f.svc.Capture(testCtx, f.agent, captureInput(scopeA, "archive-second", claim))
	if err != nil || next.Items[0].Status != "rejected" {
		t.Fatalf("duplicate bypassed archive session validation: %+v %v", next, err)
	}
}

func TestCaptureCanonicalSourceOrderDoesNotDuplicate(t *testing.T) {
	f := newFixture(t)
	one := f.source()
	two := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "Second canonical observation."}))
	claim := canonicalToolClaim(one)
	claim.SourceRefs = append(claim.SourceRefs, Ref{Collection: "sources", ID: two.ID, Revision: two.Revision})
	first := f.capture(f.agent, captureInput(scopeA, "ordered-first", claim))[0]
	slices.Reverse(claim.SourceRefs)
	second := f.capture(f.agent, captureInput(scopeA, "ordered-second", claim))[0]
	if second.Status != "merged" || second.CandidateID != first.CandidateID {
		t.Fatalf("source ordering duplicated review: %+v", second)
	}
}

func TestPostgresCaptureEvidenceIsolationAndExpiry(t *testing.T) {
	repo := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = repo
	claim := explicit("각 저장소에서 같은 규칙도 검토한다.")
	claim.Kind, claim.Repo = "procedural", "owner/first"
	first := f.captureAndReview(f.human, captureInput(scopeA, "pg-first", claim))[0]
	claim.Repo = "owner/second"
	second := f.capture(f.human, captureInput(scopeA, "pg-second", claim))[0]
	if first.Status != "accepted" || second.Status != "candidate" || first.MemoryID == second.CandidateID {
		t.Fatalf("repository isolation: %+v %+v", first, second)
	}
	old := f.get(f.human, "memories", scopeA, first.MemoryID)
	past := time.Now().UTC().Add(-time.Hour)
	old.EffectiveAt, old.ExpiresAt = nil, &past
	old.Revision++
	if err := repo.Replace(testCtx, "memories", old, old.Revision-1); err != nil {
		t.Fatal(err)
	}
	claim.Repo = "owner/first"
	restated := f.capture(f.human, captureInput(scopeA, "pg-restated", claim))[0]
	if restated.Status != "candidate" || restated.CandidateID == first.MemoryID {
		t.Fatalf("expired claim not recaptured: %+v", restated)
	}
	one := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "First PostgreSQL observation."}))
	two := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "Second PostgreSQL observation."}))
	a := f.capture(f.agent, captureInput(scopeA, "pg-source-one", canonicalToolClaim(one)))[0]
	b := f.capture(f.agent, captureInput(scopeA, "pg-source-two", canonicalToolClaim(two)))[0]
	if a.Status != "candidate" || b.Status != "candidate" || a.CandidateID == b.CandidateID {
		t.Fatalf("new source blocked by database: %+v %+v", a, b)
	}
	if got := f.get(f.agent, "memory_candidates", scopeA, b.CandidateID); len(got.Sources) != 1 || got.Sources[0].SourceID != two.ID || got.Status != "candidate" {
		t.Fatal("second source lost or autoapproved")
	}
	if again := f.capture(f.agent, captureInput(scopeA, "pg-source-two-repeat", canonicalToolClaim(two)))[0]; again.Status != "merged" || again.CandidateID != b.CandidateID {
		t.Fatalf("PostgreSQL evidence replay duplicated: %+v", again)
	}
}

func TestCaptureLegacyUnscopedRestatementDoesNotMergeScopedEvidence(t *testing.T) {
	f := newFixture(t)
	claim := explicit("저장소 범위 없는 재진술은 기존 범위를 넓히지 않는다.")
	claim.Kind, claim.Repo = "procedural", "owner/existing"
	first := f.capture(f.human, captureInput(scopeA, "scoped-legacy-first", claim))[0]
	pointer, err := f.repo.Get(testCtx, "jobs", scopeA, first.DedupeKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Delete(testCtx, "jobs", scopeA, pointer.ID, pointer.Revision); err != nil {
		t.Fatal(err)
	}
	pointer.ID = captureKey(scopeA, claim.Kind, claim.Text)
	if err := f.repo.Insert(testCtx, "jobs", pointer); err != nil {
		t.Fatal(err)
	}
	claim.Repo = ""
	next := f.capture(f.human, captureInput(scopeA, "scoped-legacy-unscoped", claim))[0]
	if next.Status != "rejected" || next.Reason != ErrConflict.Error() {
		t.Fatalf("legacy repository claim silently swallowed unscoped evidence: %+v", next)
	}
}
