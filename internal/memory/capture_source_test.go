package memory

import "testing"

func canonicalToolClaim(source Record) CaptureClaim {
	return CaptureClaim{Subject: "test-recovery", Text: "실패한 검사를 수정 후 다시 실행한다", Kind: "lesson", Basis: "inferred", Reason: "canonical tool observation", SourceRefs: []Ref{{Collection: "sources", ID: source.ID, Revision: source.Revision}}}
}

func TestCaptureCanonicalSourcePreservesEvidenceAndReplaysOnce(t *testing.T) {
	f := newFixture(t)
	source := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Kind: "tool_result", Text: "go test: failed before correction"}))
	before := len(f.repo.rows("sources"))
	in := captureInput(scopeA, "canonical-tool-event", canonicalToolClaim(source))
	first := f.capture(f.agent, in)[0]
	if first.Status != "candidate" || first.CandidateID == "" {
		t.Fatalf("canonical result was not a candidate: %+v", first)
	}
	candidate := f.get(f.agent, "memory_candidates", scopeA, first.CandidateID)
	if len(candidate.Sources) != 1 || candidate.Sources[0].SourceID != source.ID || len(f.repo.rows("sources")) != before {
		t.Fatal("tool source was replaced with a synthetic user source")
	}
	if candidate.TrustLevel == "user_verified" || len(activeMemories(f, scopeA)) != 0 {
		t.Fatal("tool output gained user trust or automatic activation")
	}
	second := f.capture(f.agent, in)[0]
	if second.Status != "merged" || second.Reason != "duplicate_event" || len(f.repo.rows("memory_candidates")) != 1 {
		t.Fatalf("canonical event replay duplicated work: %+v", second)
	}
}

func TestCaptureCanonicalSourceRejectsStaleDeletedForeignAndFalseAuthorship(t *testing.T) {
	for _, mode := range []string{"stale", "deleted", "foreign", "explicit-user", "wrong-collection", "quarantined"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			source := f.source()
			claim := canonicalToolClaim(source)
			switch mode {
			case "stale":
				claim.SourceRefs[0].Revision++
			case "deleted":
				if err := f.repo.Delete(testCtx, "sources", scopeA, source.ID, source.Revision); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				if err := f.repo.Delete(testCtx, "sources", scopeA, source.ID, source.Revision); err != nil {
					t.Fatal(err)
				}
				source.Scope = scopeB
				if err := f.repo.Insert(testCtx, "sources", source); err != nil {
					t.Fatal(err)
				}
			case "explicit-user":
				claim.Basis = "explicit_user"
			case "wrong-collection":
				claim.SourceRefs[0].Collection = "memories"
			case "quarantined":
				source.PrivacyQuarantined = true
				source.Revision++
				if err := f.repo.Replace(testCtx, "sources", source, source.Revision-1); err != nil {
					t.Fatal(err)
				}
				claim.SourceRefs[0].Revision = source.Revision
			}
			out := f.capture(f.agent, captureInput(scopeA, "canonical-invalid", claim))[0]
			if out.Status != "rejected" || len(f.repo.rows("memory_candidates")) != 0 || len(activeMemories(f, scopeA)) != 0 {
				t.Fatalf("unsafe canonical source %s accepted: %+v", mode, out)
			}
		})
	}
}
