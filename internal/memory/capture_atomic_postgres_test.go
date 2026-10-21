package memory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresCaptureAtomicRecovery(t *testing.T) {
	for _, stage := range []string{"source-preparation", "candidate-pointer", "preparation-completion", "source-commit-unknown", "candidate-commit-unknown"} {
		t.Run(stage, func(t *testing.T) {
			p := isolatedPostgres(t)
			f := newFixture(t)
			f.svc.CandidateDailyLimit = 1
			switch stage {
			case "source-preparation":
				f.svc.Repo = failCapturePreparationRepo{p}
			case "candidate-pointer":
				f.svc.Repo = &captureFailureRepo{Repository: p, pointerError: ErrUnavailable}
			case "preparation-completion":
				f.svc.Repo = failCaptureCompleteRepo{p}
			case "source-commit-unknown":
				f.svc.Repo = &captureCommitUnknownRepo{Repository: p, collection: "sources", once: true}
			case "candidate-commit-unknown":
				f.svc.Repo = &captureCommitUnknownRepo{Repository: p, collection: "memory_candidates", once: true}
			}
			in := captureInput(scopeA, "atomic-pg-event", explicit("실제 데이터베이스에서도 캡처를 한 번만 저장한다."))
			first := f.capture(f.agent, in)[0]
			if first.Status != "rejected" || first.Reason != ErrUnavailable.Error() {
				t.Fatal(first)
			}
			if stage != "candidate-commit-unknown" && len(pgCollection(t, p, "memory_candidates")) != 0 {
				t.Fatal("partial candidate committed")
			}
			if stage == "source-preparation" && len(pgCollection(t, p, "sources")) != 0 {
				t.Fatal("source committed without preparation")
			}
			f.svc.Repo = p
			next := f.capture(f.agent, in)[0]
			if next.Status != "candidate" && next.Status != "merged" {
				t.Fatal("recovery failed", next)
			}
			if len(pgCollection(t, p, "sources")) != 1 || len(pgCollection(t, p, "memory_candidates")) != 1 {
				t.Fatal("recovery duplicated rows")
			}
			if len(pgCollection(t, p, "memories")) != 0 {
				t.Fatal("capture autoapproved")
			}
		})
	}
}

func TestPostgresCaptureConcurrentClaimsCreateOneCandidate(t *testing.T) {
	for _, evidence := range []string{"synthetic", "canonical"} {
		t.Run(evidence, func(t *testing.T) {
			p := isolatedPostgres(t)
			f := newFixture(t)
			f.svc.Repo = p
			claim := explicit("동시에 저장해도 한 후보로 검토한다.")
			if evidence == "canonical" {
				claim = canonicalToolClaim(f.source())
			}
			original := f.svc.Auth
			var reached atomic.Int32
			ready := make(chan struct{})
			f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
				if req.Action == "write:memory_candidates" {
					if reached.Add(1) == 2 {
						close(ready)
					}
					select {
					case <-ready:
					case <-ctx.Done():
						return Decision{}, ctx.Err()
					}
				}
				return original.Authorize(ctx, token, req)
			})
			ctx, cancel := context.WithTimeout(testCtx, 15*time.Second)
			defer cancel()
			results := make(chan CaptureResult, 2)
			errs := make(chan error, 2)
			for _, event := range []string{"pg-race-a", "pg-race-b"} {
				go func(event string) {
					out, err := f.svc.Capture(ctx, f.agent, captureInput(scopeA, event, claim))
					results <- out
					errs <- err
				}(event)
			}
			for i := 0; i < 2; i++ {
				select {
				case out := <-results:
					if len(out.Items) != 1 || (out.Items[0].Status != "candidate" && out.Items[0].Status != "merged") {
						t.Fatal(out)
					}
				case <-ctx.Done():
					t.Fatal("capture race timed out")
				}
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
			}
			if len(pgCollection(t, p, "sources")) != 1 || len(pgCollection(t, p, "memory_candidates")) != 1 {
				t.Fatal("concurrent captures duplicated records")
			}
		})
	}
}

func TestPostgresCapturePreparedResumeKeepsApplicationRLS(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	app := restrictedPostgres(t, p)
	token := testPrincipalToken("capture-atomic-agent")
	actions := []string{"db:read", "db:write", "read:memory_candidates", "write:memory_candidates", "read:sources", "write:sources"}
	if err := p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "capture-agent", Role: roleAgent, Token: token, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: actions}); err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.Repo = app
	f.svc.Auth = auth
	ctx := ContextWithPostgresToken(testCtx, token)
	original := f.svc.Auth
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		if req.Action == "write:memory_candidates" {
			return Decision{}, ErrUnavailable
		}
		return original.Authorize(ctx, token, req)
	})
	claim := explicit("권한 범위 안에서만 중단된 캡처를 이어간다.")
	in := captureInput(scopeA, "rls-prepare", claim)
	first, err := f.svc.Capture(ctx, token, in)
	if err != nil || first.Items[0].Status != "rejected" {
		t.Fatal(first, err)
	}
	if len(pgCollection(t, p, "sources")) != 1 {
		t.Fatal("source preparation did not commit")
	}
	f.svc.Auth = original
	if _, err := f.svc.Capture(ctx, token, captureInput(scopeB, "rls-foreign", claim)); err == nil {
		t.Fatalf("cross-scope capture accepted: %v", err)
	}
	second, err := f.svc.Capture(ctx, token, captureInput(scopeA, "rls-resume", claim))
	if err != nil || second.Items[0].Status != "candidate" {
		t.Fatal(second, err)
	}
	if len(pgCollection(t, p, "memory_candidates")) != 1 || len(pgCollection(t, p, "memories")) != 0 {
		t.Fatal("RLS resume duplicated or approved")
	}
	// A pooled connection must not retain the previous caller's RLS identity.
	if rows, err := app.ListRecords(testCtx, scopeA, "sources", 10); err != nil || len(rows) != 0 {
		t.Fatal("request actor leaked through pool", err, len(rows))
	}
}

func TestPostgresCapturePreparedSourceDeletionBlocksResume(t *testing.T) {
	p := isolatedPostgres(t)
	f := deletionReplayFixture(t, p)
	original := f.svc.Auth
	f.svc.Auth = authorizerFunc(func(ctx context.Context, token string, req Authorization) (Decision, error) {
		if req.Action == "write:memory_candidates" {
			return Decision{}, ErrUnavailable
		}
		return original.Authorize(ctx, token, req)
	})
	in := captureInput(scopeA, "prepared-before-purge", explicit("삭제한 출처는 중단된 캡처에서도 되살리지 않는다."))
	first := f.capture(f.agent, in)[0]
	if first.Status != "rejected" {
		t.Fatal(first)
	}
	sources := pgCollection(t, p, "sources")
	if len(sources) != 1 {
		t.Fatal("missing prepared source")
	}
	f.svc.Auth = original
	deletion, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "prepared-source-purge", TargetType: "source_event", TargetIDs: []string{sources[0].ID}, Reason: "owner_request"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.capture(f.agent, in)[0]; got.Status != "rejected" {
		t.Fatal("quarantined source resumed", got)
	}
	if len(pgCollection(t, p, "memory_candidates")) != 0 {
		t.Fatal("quarantine created candidate")
	}
	drainDeletionReplayFixture(t, f, p)
	status, err := f.svc.GetPrivacyDeletion(testCtx, f.human, deletion.ID, scopeA)
	if err != nil || status.PrivacyDeletion.State != privacyWaiting || !status.PrivacyDeletion.Report.DerivedPurged {
		t.Fatal("preparation blocked live purge", status, err)
	}
	if _, err := p.Get(testCtx, "sources", scopeA, sources[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("source not purged", err)
	}
	// A new event is used after the terminal source deletion rejection.
	if got := f.capture(f.agent, captureInput(scopeA, "prepared-after-purge", in.Claims...))[0]; got.Status != "rejected" {
		t.Fatal("purged source resurrected", got)
	}
	if len(pgCollection(t, p, "sources")) != 0 || len(pgCollection(t, p, "memory_candidates")) != 0 {
		t.Fatal("purge retry recreated content")
	}
	for _, job := range pgCollection(t, p, "jobs") {
		if job.Kind == "capture_preparation" && (job.Text != "" || job.Summary != "") {
			t.Fatal("body retained in preparation")
		}
	}
}

func TestPostgresCaptureAtomicCorrectionRecovery(t *testing.T) {
	p := isolatedPostgres(t)
	f, _, _ := autoReviewFixture(t)
	f.svc.Repo = p
	old := reviewedForEvaluation(t, f)
	before := len(pgCollection(t, p, "memory_candidates"))
	claim := explicit("이전 기억을 상세한 새 기준으로 정정한다.")
	claim.Supersedes = &Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}
	in := captureInput(scopeA, "atomic-correction", claim)
	in.Automated = true
	f.svc.Repo = &captureFailureRepo{Repository: p, pointerError: ErrUnavailable}
	first := f.capture(testPrincipalToken("reviewer"), in)[0]
	if first.Status != "rejected" || first.Reason != ErrUnavailable.Error() {
		t.Fatal(first)
	}
	if len(pgCollection(t, p, "memory_candidates")) != before {
		t.Fatal("failed correction left an untracked candidate")
	}
	current, err := p.Get(testCtx, colMemories, scopeA, old.ID)
	if err != nil || current.Revision != old.Revision || current.ConflictID != "" {
		t.Fatal("failed correction changed approved memory", err)
	}
	f.svc.Repo = p
	next := f.capture(testPrincipalToken("reviewer"), in)[0]
	if next.Status != "candidate" || next.Reason != "conflict_review" {
		t.Fatal(next)
	}
	candidate, err := p.Get(testCtx, "memory_candidates", scopeA, next.CandidateID)
	if err != nil || candidate.Status != "conflicted" || candidate.ApprovedBy != "" {
		t.Fatal("correction activated without review", err)
	}
	if len(pgCollection(t, p, "memory_candidates")) != before+1 || len(pgCollection(t, p, "memories")) != 1 {
		t.Fatal("correction retry duplicated or approved")
	}
	if again := f.capture(testPrincipalToken("reviewer"), in)[0]; again.Reason != "duplicate_event" {
		t.Fatal("correction replay duplicated", again)
	}
}
