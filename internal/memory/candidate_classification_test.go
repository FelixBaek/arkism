package memory

import (
	"errors"
	"testing"
	"time"
)

func classifierFixture(t *testing.T) (*fixture, string) {
	f := newFixture(t)
	// The classifier is permitted every verb, so only its role can keep it from approving.
	f.auth.actors["tok-classifier"] = actor("arkism-memory-reviewer", roleClassifier, true, append(allVerbs, "classify")...)
	f.svc.MemoryReviewPolicies = []MemoryReviewPolicy{{PrincipalID: "arkism-memory-reviewer", Scope: scopeA, Kinds: []string{"semantic"}, ExpiresAt: time.Now().Add(time.Hour)}}
	return f, "tok-classifier"
}

func TestClassifyCandidateRecordsRiskOnly(t *testing.T) {
	f, cls := classifierFixture(t)
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "proposal", Sources: refs(f.source()),
		Classification: &CandidateClassification{Risk: "low", EvidenceSpan: "user said so"}}))
	if c.Classification == nil || c.Classification.By != "human-1" || c.Classification.At == nil {
		t.Fatal(c.Classification)
	}
	in := CandidateClassifyInput{Scope: scopeA, ExpectedRevision: c.Revision, Risk: "high", RiskReasons: []string{"기존 기억과 모순"}, DupHint: "m1"}
	if _, e := f.svc.ClassifyCandidate(testCtx, f.work, c.ID, in); !errors.Is(e, ErrDenied) {
		t.Fatal("no classify permission", e)
	}
	r, e := f.svc.ClassifyCandidate(testCtx, cls, c.ID, in)
	if e != nil || r.Status != "candidate" || r.Text != c.Text || r.Revision != c.Revision+1 {
		t.Fatal(r, e)
	}
	k := r.Classification
	if k.Risk != "high" || k.By != "arkism-memory-reviewer" || k.EvidenceSpan != "user said so" || k.DupHint != "m1" {
		t.Fatal(k)
	}
	if _, e := f.svc.ClassifyCandidate(testCtx, cls, c.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal("stale revision", e)
	}
	bad := in
	bad.ExpectedRevision, bad.Risk = r.Revision, "none"
	if _, e := f.svc.ClassifyCandidate(testCtx, cls, c.ID, bad); !errors.Is(e, ErrInvalid) {
		t.Fatal("bad risk", e)
	}
	if _, e := f.svc.Create(testCtx, f.human, "memories", CreateInput{Scope: scopeA, Kind: "semantic", Text: "x",
		Classification: &CandidateClassification{Risk: "low"}}); !errors.Is(e, ErrInvalid) {
		t.Fatal("classification outside candidates", e)
	}
}

func TestClassifierCannotApproveAndOverturnsAreCounted(t *testing.T) {
	f, cls := classifierFixture(t)
	src := f.source()
	mk := func(text, risk string) Record {
		return f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: text, Sources: refs(src),
			Classification: &CandidateClassification{Risk: risk}}))
	}
	hi, lo, mid := mk("risky", "high"), mk("safe", "low"), mk("plain", "medium")
	if _, e := f.svc.ReviewCandidate(testCtx, cls, hi.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: hi.Revision, Action: "approve"}); !errors.Is(e, ErrDenied) {
		t.Fatal("classifier approved", e)
	}
	if _, e := f.svc.ReviewCandidates(testCtx, cls, CandidateReviewBatchInput{Scope: scopeA,
		Items: []CandidateReviewBatchItem{{ID: hi.ID, ExpectedRevision: hi.Revision, Action: "approve"}}}); !errors.Is(e, ErrDenied) {
		t.Fatal("classifier batch approved", e)
	}
	review := func(c Record, action string) Record {
		t.Helper()
		in := CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: action}
		if action == "reject" {
			in.Reason = "no"
		}
		if _, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, in); e != nil {
			t.Fatal(e)
		}
		return f.get(f.human, "memory_candidates", scopeA, c.ID)
	}
	if r := review(hi, "approve"); r.Review.AIRisk != "high" || !r.Review.Overturned {
		t.Fatal(r.Review)
	}
	if r := review(lo, "reject"); r.Review.AIRisk != "low" || !r.Review.Overturned {
		t.Fatal(r.Review)
	}
	if r := review(mid, "reject"); r.Review.AIRisk != "medium" || r.Review.Overturned {
		t.Fatal(r.Review)
	}
	rep, e := f.svc.ClassificationReport(testCtx, f.human, scopeA)
	if e != nil || rep.Decided["high"] != 1 || rep.Overturned["high"] != 1 || rep.Overturned["low"] != 1 || rep.Decided["medium"] != 1 || rep.Overturned["medium"] != 0 {
		t.Fatal(rep, e)
	}
}

func TestCaptureClaimCarriesItsRiskCall(t *testing.T) {
	f := newFixture(t)
	claim := CaptureClaim{Subject: "s", Text: "병합 뒤 테스트를 돌린다", Kind: "semantic", Basis: "inferred", Reason: "r",
		Risk: "medium", RiskReasons: []string{"출처가 사용자 발화 아님"}, EvidenceSpan: "테스트 안 돌렸잖아"}
	items := f.capture(f.human, captureInput(scopeA, "e-risk", claim))
	if len(items) != 1 || items[0].CandidateID == "" {
		t.Fatal(items)
	}
	c := f.get(f.human, "memory_candidates", scopeA, items[0].CandidateID)
	if c.Classification == nil || c.Classification.Risk != "medium" || c.Classification.EvidenceSpan != "테스트 안 돌렸잖아" {
		t.Fatal(c.Classification)
	}
	claim.Risk = ""
	if items := f.capture(f.human, captureInput(scopeA, "e-risk2", claim)); items[0].Reason != "invalid_claim" {
		t.Fatal("risk reasons without a risk", items)
	}
}

func TestCaptureOwnerCredentialAndHighRiskCannotSelfApprove(t *testing.T) {
	f := newFixture(t)
	claim := CaptureClaim{Subject: "build", Text: "이 저장소는 pnpm으로 빌드한다.", Kind: "decision", Basis: "explicit_user", Reason: "user decided", Risk: "high"}
	items := f.capture(f.human, captureInput(scopeA, "e-auto", claim))
	if len(items) != 1 || items[0].Status != "candidate" {
		t.Fatal(items)
	}
	c := f.get(f.human, "memory_candidates", scopeA, items[0].CandidateID)
	if c.Review != nil || len(activeMemories(f, scopeA)) != 0 {
		t.Fatal(c.Review)
	}
	if rep, e := f.svc.ClassificationReport(testCtx, f.human, scopeA); e != nil || len(rep.Decided) != 0 || len(rep.Overturned) != 0 {
		t.Fatal(rep, e)
	}
}

func TestClassifyCandidateKeepsVerdictAndSemanticAdvice(t *testing.T) {
	f, cls := classifierFixture(t)
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "proposal", Sources: refs(f.source())}))
	in := CandidateClassifyInput{Scope: scopeA, ExpectedRevision: c.Revision, Risk: "medium",
		Verdict:  &CandidateVerdict{Action: "fix", Text: "검수 전에는 올리지 않는다", Reason: "저장소 언급이 빠졌어요"},
		Semantic: []SemanticCheck{{Check: "same_meaning", OK: true}, {Check: "missing_content", OK: false, Note: "저장소"}}}
	r, e := f.svc.ClassifyCandidate(testCtx, cls, c.ID, in)
	if e != nil || r.Status != "candidate" || r.Text != c.Text {
		t.Fatal(r, e)
	}
	if k := r.Classification; k.Verdict == nil || k.Verdict.Action != "fix" || len(k.Semantic) != 2 || k.Semantic[1].OK {
		t.Fatal(k)
	}
	for name, bad := range map[string]CandidateClassifyInput{
		"action": {Verdict: &CandidateVerdict{Action: "approve"}},
		"check":  {Semantic: []SemanticCheck{{Check: "vibes"}}},
	} {
		bad.Scope, bad.ExpectedRevision, bad.Risk = scopeA, r.Revision, "low"
		if _, e := f.svc.ClassifyCandidate(testCtx, cls, c.ID, bad); !errors.Is(e, ErrInvalid) {
			t.Fatal(name, e)
		}
	}
}

// 다시 보기 (764:61615): a person drops who judged it, so the next AI run judges it again; nobody else may.
func TestRequestReclassifyClearsTheJudge(t *testing.T) {
	f, cls := classifierFixture(t)
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "proposal", Sources: refs(f.source()),
		Classification: &CandidateClassification{Risk: "low", EvidenceSpan: "user said so"}}))
	r, e := f.svc.ClassifyCandidate(testCtx, cls, c.ID, CandidateClassifyInput{Scope: scopeA, ExpectedRevision: c.Revision, Risk: "high",
		Verdict: &CandidateVerdict{Action: "fix", Text: "better"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.RequestReclassify(testCtx, cls, c.ID, ReclassifyInput{Scope: scopeA, ExpectedRevision: r.Revision}); !errors.Is(e, ErrDenied) {
		t.Fatal("classifier asked for its own re-judge", e)
	}
	out, e := f.svc.RequestReclassify(testCtx, f.human, c.ID, ReclassifyInput{Scope: scopeA, ExpectedRevision: r.Revision})
	if e != nil || out.Revision != r.Revision+1 || out.Classification.By != "" || out.Classification.At != nil ||
		out.Classification.Verdict == nil || out.Classification.EvidenceSpan != "user said so" {
		t.Fatal(out.Classification, e)
	}
	if _, e := f.svc.RequestReclassify(testCtx, f.human, c.ID, ReclassifyInput{Scope: scopeA, ExpectedRevision: r.Revision}); !errors.Is(e, ErrConflict) {
		t.Fatal("stale revision", e)
	}
}
