package memory

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPrivacyLargeSubjectPagesAndDurablePurgeResume(t *testing.T) {
	p, svc, scope, other := privacyTraceFixture(t)
	for i := 0; i < 1005; i++ {
		if err := p.Insert(testCtx, "sources", Record{ID: fmt.Sprintf("large-%04d", i), Scope: scope, Kind: "document", Status: statusActive, Revision: 1, Text: "synthetic evidence", CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.DiscoverPrivacyImpact(testCtx, scope, "subject", []string{scope.UserID}, privacyImpactLimit); !errors.Is(err, ErrUnavailable) {
		t.Fatal("legacy cap must fail closed", err)
	}
	request := PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "large-subject", TargetType: "subject", TargetIDs: []string{scope.UserID}, Reason: "owner_request", RetentionPolicyRef: retentionSynthetic}
	cancelled, cancel := context.WithCancel(testCtx)
	cancel()
	if _, err := svc.RequestPrivacyDeletion(cancelled, "owner", request); err == nil {
		t.Fatal("cancelled discovery accepted")
	}
	status, err := svc.RequestPrivacyDeletion(testCtx, "owner", request)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.PrivacyDeletion.Impact.SourceEventIDs) != 1008 || len(status.PrivacyDeletion.Impact.Records) < 1008 {
		t.Fatal("truncated impact", len(status.PrivacyDeletion.Impact.SourceEventIDs), len(status.PrivacyDeletion.Impact.Records))
	}
	checkPrivacyQuarantined(t, p, scope, "large-0000", "large-1004")
	if n, e := svc.RunPrivacyRuntimeStep(testCtx, "worker"); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	status, err = svc.GetPrivacyDeletion(testCtx, "owner", status.ID, scope)
	if err != nil {
		t.Fatal(err)
	}
	status, err = svc.AdvancePrivacyDeletion(testCtx, "worker", status.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: status.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if status.PrivacyDeletion.PurgeSourceOffset != 100 || status.PrivacyDeletion.State != privacyPurgingDerived {
		t.Fatal("unbounded or premature purge", status.PrivacyDeletion.PurgeSourceOffset, status.PrivacyDeletion.State)
	}
	// No process-local cursor survives; a fresh service resumes persisted progress.
	fresh := &Service{Repo: p, Auth: svc.Auth}
	status = resumePrivacyPurgeInBatches(t, fresh, scope, status)
	if status.PrivacyDeletion.State != privacyVerifying || status.PrivacyDeletion.PurgeSourceOffset != 1008 || status.PrivacyDeletion.PurgeRecordOffset != len(status.PrivacyDeletion.Impact.Records) {
		t.Fatal("resume incomplete")
	}
	if _, e := p.Get(testCtx, "sources", scope, "large-1004"); !errors.Is(e, ErrNotFound) {
		t.Fatal("tail remains", e)
	}
	if _, e := p.Get(testCtx, "sources", other, "ctx-src-other"); e != nil {
		t.Fatal("cross-scope purge", e)
	}
	status, err = fresh.AdvancePrivacyDeletion(testCtx, "worker", status.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: status.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if status.PrivacyDeletion.State == privacyCompleted {
		t.Fatal("pending child erasures reported complete")
	}
}

func checkPrivacyQuarantined(t *testing.T, p *Postgres, scope Scope, ids ...string) {
	t.Helper()
	for _, id := range ids {
		r, e := p.Get(testCtx, "sources", scope, id)
		if e != nil || !r.PrivacyQuarantined {
			t.Fatal("tail not quarantined", id, e)
		}
	}
}

// resumePrivacyPurgeInBatches re-reads persisted progress before every step and
// checks each step stays within the shared source/record batch budget.
func resumePrivacyPurgeInBatches(t *testing.T, fresh *Service, scope Scope, status PrivacyDeletionStatus) PrivacyDeletionStatus {
	t.Helper()
	var err error
	for i := 0; i < 40 && status.PrivacyDeletion.State == privacyPurgingDerived; i++ {
		status, err = fresh.GetPrivacyDeletion(testCtx, "owner", status.ID, scope)
		if err != nil {
			t.Fatal(err)
		}
		before := status.PrivacyDeletion.PurgeSourceOffset + status.PrivacyDeletion.PurgeRecordOffset
		status, err = fresh.AdvancePrivacyDeletion(testCtx, "worker", status.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: status.Revision})
		if err == nil && status.PrivacyDeletion.PurgeSourceOffset+status.PrivacyDeletion.PurgeRecordOffset-before > 100 {
			t.Fatal("source/record boundary exceeded shared batch budget")
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return status
}

func TestPrivacyCompleteDiscoveryFollowsMoreThan64Edges(t *testing.T) {
	p := isolatedPostgres(t)
	scope := Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u"}
	for i := 0; i < 70; i++ {
		id := fmt.Sprintf("chain-%02d", i)
		if err := p.Insert(testCtx, "sources", Record{ID: id, Scope: scope, Kind: "document", Status: statusActive, Revision: 1}); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			if err := p.insertGraphRelation(testCtx, scope, "source_event", id, "source_event", fmt.Sprintf("chain-%02d", i-1), "context_uses"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, e := p.DiscoverPrivacyImpact(testCtx, scope, "source_event", []string{"chain-00"}, 1000); !errors.Is(e, ErrUnavailable) {
		t.Fatal("legacy depth cap changed", e)
	}
	err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		impact, e := tx.(*Postgres).DiscoverPrivacyImpactComplete(ctx, scope, "source_event", []string{"chain-00"})
		if e != nil {
			return e
		}
		if len(impact.SourceEventIDs) != 70 || len(impact.Records) != 70 {
			t.Fatal("fixed point truncated", len(impact.SourceEventIDs), len(impact.Records))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
