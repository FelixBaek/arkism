package memory

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestMemoryReviewReceiptDoesNotPromoteLegacyApproval(t *testing.T) {
	f := newFixture(t)
	old := f.activeMemory("legacy record")
	if memoryApprovalOrigin(old) != "legacy_human_confirmation_unproven" {
		t.Fatal("legacy promoted")
	}
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "review this wording", Sources: refs(f.source())}))
	m, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
	if err != nil || m.Review == nil || m.Review.ReceiptID == "" || m.Review.ActorRole != roleHuman || m.Review.Channel != "explicit_review_api" {
		t.Fatal(m.Review, err)
	}
	if memoryApprovalOrigin(m) != "explicit_human_role_review" || HitFromRecord(colMemories, m).ApprovalOrigin != "explicit_human_role_review" {
		t.Fatal("receipt missing from delivery")
	}
	if memoryApprovalOrigin(f.get(f.human, colMemories, scopeA, old.ID)) != "legacy_human_confirmation_unproven" {
		t.Fatal("other record changed")
	}
}

func TestMemoryReviewDueIsSeparateFromExpiryAndApproval(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("permanent rule")
	at := time.Now().Add(24 * time.Hour)
	in := MemoryReviewDueInput{Scope: scopeA, ExpectedRevision: m.Revision, ReviewDueAt: &at}
	if _, err := f.svc.SetMemoryReviewDue(testCtx, f.agent, m.ID, in); !errors.Is(err, ErrDenied) {
		t.Fatal("agent scheduled review", err)
	}
	set, err := f.svc.SetMemoryReviewDue(testCtx, f.human, m.ID, in)
	if err != nil || set.ReviewDueAt == nil || set.ExpiresAt != nil || set.Status != m.Status || set.Trust != m.Trust || set.ApprovedBy != m.ApprovedBy {
		t.Fatal("reminder changed lifecycle", set, err)
	}
	if _, err = f.svc.SetMemoryReviewDue(testCtx, f.human, m.ID, in); !errors.Is(err, ErrConflict) {
		t.Fatal("stale write", err)
	}
	cleared, err := f.svc.SetMemoryReviewDue(testCtx, f.human, m.ID, MemoryReviewDueInput{Scope: scopeA, ExpectedRevision: set.Revision})
	if err != nil || cleared.ReviewDueAt != nil || !live(cleared) {
		t.Fatal("clear reminder", cleared, err)
	}
	status, err := f.svc.MemoryManagement(testCtx, f.human, m.ID, scopeA)
	if err != nil || status.ActualUseStatus != "not_measured" || status.SemanticCompleteness != "not_measured" || status.OriginalsUnknown == 0 || status.Complete {
		t.Fatal("unknown evidence claimed verified", status, err)
	}
}

func TestCandidateEvidenceCountsIndependentUserTurnsAndRetriesOnce(t *testing.T) {
	f, now := archiveSearchFixture(t, nil)
	raw := append(jsonLine(map[string]any{"role": "user", "content": "briefmarker first request"}), jsonLine(map[string]any{"role": "user", "content": "briefmarker repeated request"})...)
	id := ContentHash("candidate-evidence")
	archiveSearchUpload(t, f, *now, id, raw, "jsonl")
	page, err := f.svc.SearchSessionArchiveMessages(testCtx, f.human, ArchiveSearchInput{Scope: scopeA, ArchiveID: id, Query: "briefmarker", Limit: 10})
	if err != nil || len(page.Items) != 2 {
		t.Fatal(page, err)
	}
	refsToMessages := []ArchiveMessageRef{page.Items[0].Ref, page.Items[1].Ref}
	source := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "briefmarker first request", ArchiveMessages: refsToMessages}))
	copySource := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "projection: briefmarker first request", ArchiveMessages: []ArchiveMessageRef{page.Items[0].Ref}}))
	second := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "briefmarker repeated request", ArchiveMessages: []ArchiveMessageRef{page.Items[1].Ref}}))
	copyRefs := refs(copySource)
	copyRefs[0].Span.Start = len("projection: ")
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "brief replies", Sources: append(append(refs(source), copyRefs...), refs(second)...), MemoryContext: &MemoryContext{Domains: []string{"personal"}, EvidenceKind: "user_statement", SemanticKind: "preference", Applicability: "replies"}}))
	got, err := f.svc.CandidateEvidence(testCtx, f.human, c.ID, scopeA)
	if err != nil || len(got.UserMentionKeys) != 2 || len(got.SourceKeys) != 3 || got.Complete {
		t.Fatal("turns or retries miscounted", got, err)
	}
	if _, err = f.svc.CandidateEvidence(testCtx, f.agent, c.ID, scopeA); !errors.Is(err, ErrDenied) {
		t.Fatal("agent evidence access", err)
	}
	unknown := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "legacy inference", Sources: refs(f.source())}))
	got, err = f.svc.CandidateEvidence(testCtx, f.human, unknown.ID, scopeA)
	if err != nil || got.Complete || len(got.UserMentionKeys) != 0 {
		t.Fatal("inference became user request", got, err)
	}
}

// Audit failures must stay unknown, not turn into a zero-residue assertion.
type managementAuditRepo struct {
	archiveFake
	mode   string
	called bool
}

func (r *managementAuditRepo) SessionArchiveOrphanRows(ctx context.Context, scope Scope, now time.Time, limit int) ([]Record, error) {
	r.called = true
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2*time.Second {
		return nil, errors.New("unbounded audit")
	}
	switch r.mode {
	case "error":
		return nil, ErrUnavailable
	case "cancel":
		return nil, context.DeadlineExceeded
	case "foreign":
		return []Record{{ID: "foreign", Scope: scopeFar}}, nil
	case "partial":
		rows := make([]Record, limit)
		for i := range rows {
			rows[i] = Record{ID: fmt.Sprint(i), Scope: scope}
		}
		return rows, nil
	}
	return nil, nil
}
func TestMemoryManagementAuditUnknownPartialScopeAndRole(t *testing.T) {
	f := newFixture(t)
	repo := &managementAuditRepo{archiveFake: archiveFake{f.repo}}
	f.svc.Repo = repo
	for _, mode := range []string{"error", "cancel", "partial", "empty"} {
		repo.mode = mode
		got, err := f.svc.AuditArchiveStorage(testCtx, f.human, scopeA)
		if err != nil || got.ExternalCopies != "not_verified" {
			t.Fatal(mode, got, err)
		}
		if mode == "error" || mode == "cancel" {
			if got.State != "incomplete" || got.OrphanCandidates != nil {
				t.Fatal("failure became clean", got)
			}
		} else if mode == "partial" {
			if got.State != "partial" || *got.OrphanCandidates != 25 {
				t.Fatal(got)
			}
		} else if got.State != "checked_scope" || *got.OrphanCandidates != 0 {
			t.Fatal(got)
		}
	}
	repo.called = false
	if _, err := f.svc.AuditArchiveStorage(testCtx, f.agent, scopeA); !errors.Is(err, ErrDenied) || repo.called {
		t.Fatal("agent audit", err)
	}
	repo.mode = "foreign"
	if _, err := f.svc.AuditArchiveStorage(testCtx, f.human, scopeA); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign audit", err)
	}
}
func TestCandidateSourceTraceRequiresHumanAndKeepsPrivacyFences(t *testing.T) {
	f := newFixture(t)
	source := f.source()
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "candidate citation", Sources: refs(source)}))
	got, err := f.svc.TraceCandidateSources(testCtx, f.human, scopeA, c.ID)
	if err != nil || got.Memory.Collection != "memory_candidates" || len(got.Sources) != 1 || got.Sources[0].Excerpt != source.Text {
		t.Fatal(got, err)
	}
	if _, err = f.svc.TraceCandidateSources(testCtx, f.agent, scopeA, c.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("agent trace", err)
	}
	if _, err = f.svc.TraceCandidateSources(testCtx, f.human, scopeFar, c.ID); err == nil {
		t.Fatal("foreign trace")
	}
	c.ContentErased = true
	f.repo.store["memory_candidates"][recKey(scopeA, c.ID)] = c
	got, err = f.svc.TraceCandidateSources(testCtx, f.human, scopeA, c.ID)
	if err == nil || len(got.Sources) != 0 {
		t.Fatal("erased candidate exposed", got, err)
	}
}
func TestPostgresReviewReceiptAndReminderPersistIndependently(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	source := f.source()
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "reviewed durable wording", Sources: refs(source)}))
	m, err := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Millisecond)
	updated, err := f.svc.SetMemoryReviewDue(testCtx, f.human, m.ID, MemoryReviewDueInput{Scope: scopeA, ExpectedRevision: m.Revision, ReviewDueAt: &due})
	if err != nil {
		t.Fatal(err)
	}
	reread, err := p.Get(testCtx, colMemories, scopeA, m.ID)
	if err != nil || reread.ReviewDueAt == nil || !reread.ReviewDueAt.Equal(due) || reread.Review == nil || reread.Review.ReceiptID != m.Review.ReceiptID || reread.Status != updated.Status || reread.ExpiresAt != nil {
		t.Fatal(reread, err)
	}
}
