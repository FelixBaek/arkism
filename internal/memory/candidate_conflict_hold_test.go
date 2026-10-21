package memory

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestCandidateConflictHoldAtomicServiceAndReplay(t *testing.T) {
	f, c, _ := autoReviewFixture(t)
	old := reviewedForEvaluation(t, f)
	in := CandidateConflictHoldInput{Scope: scopeA, ExpectedRevision: c.Revision, Memory: Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}, OperationID: "correction-1"}
	got, e := f.svc.HoldCandidateConflict(testCtx, testPrincipalToken("reviewer"), c.ID, in)
	if e != nil {
		t.Fatal(e)
	}
	rel, _ := f.repo.Get(testCtx, "memory_relations", scopeA, got.Conflict.ID)
	if rel.CreatedByType != roleService {
		t.Fatal("service marked as human")
	}
	current, _ := f.repo.Get(testCtx, colMemories, scopeA, old.ID)
	// Until a person decides, agents keep reading the memory the correction targets.
	if !live(current) || current.ConflictID != got.Conflict.ID {
		t.Fatal("corrected memory left retrieval or lost its conflict link")
	}
	again, e := f.svc.HoldCandidateConflict(testCtx, testPrincipalToken("reviewer"), c.ID, in)
	if e != nil || again != got {
		t.Fatal("replay", again, e)
	}
	current.Status = "deleted"
	if e = f.repo.Replace(testCtx, colMemories, current, current.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.HoldCandidateConflict(testCtx, testPrincipalToken("reviewer"), c.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal("deleted replay", e)
	}
}
func TestCandidateConflictHoldAuthorizationAndAtomicFailure(t *testing.T) {
	for _, mode := range []string{"human", "agent", "expired", "audit"} {
		t.Run(mode, func(t *testing.T) {
			f, c, _ := autoReviewFixture(t)
			old := reviewedForEvaluation(t, f)
			token := testPrincipalToken("reviewer")
			switch mode {
			case "human":
				token = f.human
			case "agent":
				token = f.agent
			case "expired":
				f.svc.MemoryReviewPolicies[0].ExpiresAt = time.Now().Add(-time.Hour)
			case "audit":
				f.repo.failAudit = true
			}
			_, e := f.svc.HoldCandidateConflict(testCtx, token, c.ID, CandidateConflictHoldInput{Scope: scopeA, ExpectedRevision: c.Revision, Memory: Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}, OperationID: "hold"})
			if e == nil {
				t.Fatal("accepted", mode)
			}
			current, _ := f.repo.Get(testCtx, colMemories, scopeA, old.ID)
			if current.Status != "active" || current.Revision != old.Revision {
				t.Fatal("partial mutation")
			}
		})
	}
}
func TestCaptureAutomatedCorrectionRetriesFailedHold(t *testing.T) {
	f, _, _ := autoReviewFixture(t)
	old := reviewedForEvaluation(t, f)
	claim := explicit("한국어 답변을 이전보다 상세하게 해줘")
	claim.Supersedes = &Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}
	in := captureInput(scopeA, "correction-event", claim)
	in.Automated = true
	f.svc.MemoryReviewPolicies[0].ExpiresAt = time.Now().Add(-time.Hour)
	out := f.capture(testPrincipalToken("reviewer"), in)
	if out[0].Status != "rejected" {
		t.Fatal(out)
	}
	key := captureKey(scopeA, claim.Kind, claim.Text, claim.Supersedes)
	pointer, e := f.repo.Get(testCtx, colJobs, scopeA, key)
	if e != nil || pointer.Status != "correction_pending" || pointer.Target == nil {
		t.Fatal("lost pending", pointer, e)
	}
	c, _ := f.repo.Get(testCtx, "memory_candidates", scopeA, pointer.Target.ID)
	if !slices.Contains(c.Tags, pendingCaptureCorrectionTag) {
		t.Fatal("missing approval fence")
	}
	f.svc.MemoryReviewPolicies[0].ExpiresAt = time.Now().Add(time.Hour)
	if _, e = f.svc.ReviewCandidate(testCtx, testPrincipalToken("reviewer"), c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve", OperationID: "premature"}); !errors.Is(e, ErrDenied) {
		t.Fatal("pending correction approved", e)
	}
	out = f.capture(testPrincipalToken("reviewer"), in)
	if out[0].Reason != "conflict_review" || out[0].CandidateID != c.ID {
		t.Fatal("retry lost correction", out)
	}
	current, _ := f.repo.Get(testCtx, colMemories, scopeA, old.ID)
	if current.Status != "active" || current.ConflictID == "" {
		t.Fatal("old memory left retrieval or lost its conflict link", current.Status, current.ConflictID)
	}
	if again := f.capture(testPrincipalToken("reviewer"), in); again[0].Reason != "duplicate_event" {
		t.Fatal(again)
	}
}
func TestCaptureKeyBindsCorrectionTarget(t *testing.T) {
	a, b := Ref{Collection: colMemories, ID: "a", Revision: 1}, Ref{Collection: colMemories, ID: "b", Revision: 1}
	if captureKey(scopeA, "user", "same", &a) == captureKey(scopeA, "user", "same", &b) || captureKey(scopeA, "user", "same", &a) == captureKey(scopeA, "user", "same") {
		t.Fatal("correction keys collide")
	}
}

type failCorrectionCompletionRepo struct {
	Repository
	fail bool
}
type failCorrectionCompletionTx struct {
	Tx
	owner *failCorrectionCompletionRepo
}

func (r *failCorrectionCompletionRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, failCorrectionCompletionTx{Tx: tx, owner: r}) })
}
func (x failCorrectionCompletionTx) Replace(ctx context.Context, col string, r Record, revision int64) error {
	if x.owner.fail && col == colJobs && r.Kind == "capture_pointer" && r.Status == JobStatusDone {
		x.owner.fail = false
		return ErrUnavailable
	}
	return x.Tx.Replace(ctx, col, r, revision)
}
func TestCaptureCorrectionRecoversAfterCommittedHoldBeforePointerCompletion(t *testing.T) {
	f, _, _ := autoReviewFixture(t)
	old := reviewedForEvaluation(t, f)
	wrapped := &failCorrectionCompletionRepo{Repository: f.repo, fail: true}
	f.svc.Repo = wrapped
	c := explicit("앞으로 답변을 상세하게 작성한다")
	c.Supersedes = &Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}
	in := captureInput(scopeA, "retry-committed-hold", c)
	in.Automated = true
	first := f.capture(testPrincipalToken("reviewer"), in)
	if first[0].Status != "rejected" {
		t.Fatal(first)
	}
	current, _ := f.repo.Get(testCtx, colMemories, scopeA, old.ID)
	if current.Status != "active" || current.ConflictID == "" {
		t.Fatal("hold not committed")
	}
	conflict := current.ConflictID
	second := f.capture(testPrincipalToken("reviewer"), in)
	if second[0].Reason != "conflict_review" {
		t.Fatal("completion retry merged prematurely", second)
	}
	current, _ = f.repo.Get(testCtx, colMemories, scopeA, old.ID)
	if current.ConflictID != conflict {
		t.Fatal("replayed hold duplicated")
	}
	hits := f.search(f.human, SearchInput{Scope: scopeA, Query: old.Text, Modes: []string{"exact", "fts", "vector"}, Limit: 10, MaxChars: 4000})
	found := false
	for _, hit := range hits.Items {
		found = found || hit.ID == old.ID
	}
	if !found {
		t.Fatal("memory under correction left search")
	}
}
func TestCaptureSameCorrectionTextDifferentTargetsDoesNotMerge(t *testing.T) {
	f, _, _ := autoReviewFixture(t)
	a := reviewedForEvaluation(t, f)
	b := f.activeMemory("Different previous preference", "preference")
	ids := []string{}
	for i, old := range []Record{a, b} {
		c := explicit("새 기준으로 한국어 답변을 상세하게 작성한다")
		c.Supersedes = &Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}
		in := captureInput(scopeA, []string{"target-a", "target-b"}[i], c)
		in.Automated = true
		out := f.capture(testPrincipalToken("reviewer"), in)
		if out[0].Reason != "conflict_review" {
			t.Fatal(out)
		}
		ids = append(ids, out[0].CandidateID)
	}
	if ids[0] == ids[1] {
		t.Fatal("distinct corrections merged")
	}
}

func TestPostgresCandidateConflictHoldApplicationRole(t *testing.T) {
	f := newFixture(t)
	p := isolatedPostgres(t)
	f.svc.Repo = p
	f.auth.actors[testPrincipalToken("reviewer")] = actor("memory-reviewer", roleService, true, append(allVerbs, "auto_review")...)
	f.svc.MemoryReviewPolicies = []MemoryReviewPolicy{{PrincipalID: "memory-reviewer", Scope: scopeA, Kinds: []string{"semantic", "user", "lesson", "decision"}, ExpiresAt: time.Now().Add(time.Hour)}}
	c := f.create(f.agent, "memory_candidates", generatedCandidate(scopeA, refs(f.source())))
	old := reviewedForEvaluation(t, f)
	app := restrictedPostgres(t, p)
	if e := p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "memory-reviewer", Role: roleService, Token: testPrincipalToken("reviewer"), OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write", "transition:memory_candidates", "transition:memories", "auto_review:memory_candidates"}}); e != nil {
		t.Fatal(e)
	}
	f.svc.Repo = app
	var e error
	f.svc.Auth, e = NewPostgresAuthorizer(app)
	if e != nil {
		t.Fatal(e)
	}
	ctx := ContextWithPostgresToken(testCtx, testPrincipalToken("reviewer"))
	in := CandidateConflictHoldInput{Scope: scopeA, ExpectedRevision: c.Revision, Memory: Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}, OperationID: "pg-correction"}
	got, e := f.svc.HoldCandidateConflict(ctx, testPrincipalToken("reviewer"), c.ID, in)
	if e != nil {
		t.Fatal("application hold", e)
	}
	again, e := f.svc.HoldCandidateConflict(ctx, testPrincipalToken("reviewer"), c.ID, in)
	if e != nil || again != got {
		t.Fatal("application replay", again, e)
	}
}
