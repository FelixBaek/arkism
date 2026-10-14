package memory

import (
	"errors"
	"testing"
)

// Records that point at a source only through source_event_id (no sources
// array), like synthetic consultations, must be purged too, not just hidden.
func TestPostgresPrivacySourceDeletionPurgesSourceEventIDOnlyRecords(t *testing.T) {
	p, svc, scope, other := privacyTraceFixture(t)
	for s, suffix := range map[Scope]string{scope: "", other: "-other"} {
		for col, text := range map[string]string{colConsultations: "consult-secret", colMemories: "mem-secret"} {
			r := Record{ID: col + "-ref" + suffix, Scope: s, Kind: "note", Status: statusActive, Revision: 1, Text: text, SourceEventID: "ctx-src" + suffix}
			if err := p.Insert(testCtx, col, r); err != nil {
				t.Fatal(err)
			}
		}
	}
	status, err := svc.RequestPrivacyDeletion(testCtx, "owner", PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "event-ref", TargetType: "source_event", TargetIDs: []string{"ctx-src"}, Reason: "owner_request", RetentionPolicyRef: retentionSynthetic})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := svc.RunPrivacyRuntimeStep(testCtx, "worker"); err != nil || n != 1 {
		t.Fatalf("runtime step = %d %v", n, err)
	}
	for i := 0; i < 200 && status.PrivacyDeletion.State != privacyCompleted; i++ {
		if status.PrivacyDeletion.State == privacyPurgingDerived || status.PrivacyDeletion.State == privacyVerifying {
			if status, err = svc.AdvancePrivacyDeletion(testCtx, "worker", status.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: status.Revision}); err != nil && !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
		}
		if err := svc.WorkOnce(testCtx, "worker"); err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
		if status, err = svc.GetPrivacyDeletion(testCtx, "owner", status.ID, scope); err != nil {
			t.Fatal(err)
		}
	}
	if status.PrivacyDeletion.State != privacyCompleted {
		t.Fatalf("deletion did not complete: %+v", status.PrivacyDeletion)
	}
	for _, col := range []string{colConsultations, colMemories} {
		if r, err := p.Get(testCtx, col, scope, col+"-ref"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s still stored after completed deletion: %q %v", col, r.Text, err)
		}
		if _, err := p.Get(testCtx, col, other, col+"-ref-other"); err != nil {
			t.Fatal("other project record removed", col, err)
		}
	}
}
