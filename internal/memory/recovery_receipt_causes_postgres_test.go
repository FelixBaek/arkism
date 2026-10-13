package memory

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func receiptCauseInventory(t *testing.T, p *Postgres) RecoverySnapshot {
	t.Helper()
	raw, err := p.RecoveryReviewSnapshot(testCtx, false, "")
	if err != nil {
		t.Fatal(err)
	}
	var result RecoverySnapshot
	if err = json.Unmarshal(raw, &result); err != nil || result.Version != 5 || result.ReceiptCauses == nil {
		t.Fatal("cause inventory", err)
	}
	return result
}

func TestPostgresRecoveryReceiptCascadesAndHistoricalOrigins(t *testing.T) {
	p := isolatedPostgres(t)
	f := deletionReplayFixture(t, p)
	source := f.source()
	candidate := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "receipt-cause-private-body", Sources: refs(source)}))
	memory, err := f.svc.ReviewCandidate(testCtx, f.human, candidate.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Action: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	pkg := workCheckpointPackage()
	pkg.SourceRefs = []Ref{{Collection: "memories", ID: memory.ID, Revision: memory.Revision}}
	if _, err = f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "cause-work", "initial", 0, pkg)); err != nil {
		t.Fatal(err)
	}
	request, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "cause-source", TargetType: "source_event", TargetIDs: []string{source.ID}, Reason: "private-reason-sentinel"})
	if err != nil {
		t.Fatal(err)
	}
	drainDeletionReplayFixture(t, f, p)
	inventory := receiptCauseInventory(t, p)
	all := map[string]RecoveryReceiptCause{}
	for _, group := range *inventory.ReceiptCauses {
		for _, cause := range group.Items {
			if cause.OriginType != "privacy_request" || cause.RequestID != request.ID || cause.Basis != "parent_chain" {
				t.Fatalf("incorrect actual cascade: %+v", cause)
			}
			all[cause.Target.Collection] = cause
		}
	}
	for _, collection := range []string{"sources", "memories", "memory_candidates", colWorkCheckpoints} {
		if _, ok := all[collection]; !ok {
			t.Fatal("missing cascade", collection)
		}
	}
	if len(all) != 4 {
		t.Fatal("unexpected cascade count", len(all))
	}
	// Simulate a pre-link historical receipt while preserving its original time.
	receipt, err := p.Get(testCtx, colJobs, scopeA, workDeletionID(scopeA, "cause-work"))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ErasureParentID != request.ID {
		t.Fatal("new receipt omitted immutable parent")
	}
	if err = p.Delete(testCtx, colJobs, scopeA, receipt.ID, receipt.Revision); err != nil {
		t.Fatal(err)
	}
	receipt.ErasureParentID = ""
	if err = p.Insert(testCtx, colJobs, receipt); err != nil {
		t.Fatal(err)
	}
	findWork := func() RecoveryReceiptCause {
		t.Helper()
		inventory := receiptCauseInventory(t, p)
		for _, g := range *inventory.ReceiptCauses {
			for _, c := range g.Items {
				if c.Target.Collection == colWorkCheckpoints {
					return c
				}
			}
		}
		t.Fatal("missing work receipt")
		return RecoveryReceiptCause{}
	}
	if cause := findWork(); cause.Basis != "request_impact" || cause.RequestID != request.ID {
		t.Fatal("unique historical impact not found", cause)
	}
	// A second equally old impact is ambiguous, even if the target IDs coincide.
	original, err := p.Get(testCtx, colJobs, scopeA, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	other := original
	pd := *original.PrivacyDeletion
	pd.IdempotencyKey = "ambiguous-cause"
	other.PrivacyDeletion = &pd
	other.ID = privacyJobID(scopeA, pd.RequesterID, pd.IdempotencyKey)
	if err = p.Insert(testCtx, colJobs, other); err != nil {
		t.Fatal(err)
	}
	if cause := findWork(); cause.OriginType != "unresolved" || cause.Basis != "ambiguous_request" {
		t.Fatal("ambiguous impact guessed", cause)
	}
	if err = p.Delete(testCtx, colJobs, scopeA, other.ID, other.Revision); err != nil {
		t.Fatal(err)
	}
	// Moving the parent into a different scope must not let an administrator
	// accidentally join it globally just because its identifier is still present.
	parent, err := p.Get(testCtx, colJobs, scopeA, sourceMemoryErasureID(scopeA, source.ID))
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Delete(testCtx, colJobs, scopeA, parent.ID, parent.Revision); err != nil {
		t.Fatal(err)
	}
	parent.Scope = scopeB
	if err = p.Insert(testCtx, colJobs, parent); err != nil {
		t.Fatal(err)
	}
	inventory = receiptCauseInventory(t, p)
	unknown := 0
	for _, g := range *inventory.ReceiptCauses {
		for _, c := range g.Items {
			if c.Target.Collection == "memories" || c.Target.Collection == "memory_candidates" {
				if c.OriginType != "unresolved" {
					t.Fatal("foreign parent used", c)
				}
				unknown++
			}
		}
	}
	if unknown != 2 {
		t.Fatal("missing derived receipts", unknown)
	}
}

func TestPostgresRecoveryReceiptCauseCLIWithWriteGate(t *testing.T) {
	p := isolatedPostgres(t)
	snapshotReceipt(t, p, scopeA, "historical-source")
	if err := enqueueSourceMemoryErasure(testCtx, p, scopeA, "historical-source", 1, "human-owner"); err != nil {
		t.Fatal(err)
	}
	paused := gateChange(t, p, "pause", gateStatus(t, p))
	binary := filepath.Join(t.TempDir(), "memoryd")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/memoryd").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	invoke := func(args ...string) ([]byte, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "MEMORY_STORE=postgres", "MEMORY_DATABASE_URL="+recoveryGateDSN(p), "MEMORY_LOCAL_STANDALONE=true")
		return cmd.Output()
	}
	for _, mode := range []string{"--export-recovery-snapshot", "--inspect-restored-recovery"} {
		if mode == "--inspect-restored-recovery" {
			if err := p.RequireRestoreReview(testCtx, ContentHash("cause-cli-restore")); err != nil {
				t.Fatal(err)
			}
		}
		out, err := invoke(mode, "--receipt-causes", "--expected-write-generation="+paused.Gate.Generation)
		if err != nil {
			t.Fatal("cause CLI", mode, err)
		}
		var document struct {
			Version int                    `json:"version"`
			Causes  []RecoveryReceiptScope `json:"receipt_causes"`
		}
		if err = json.Unmarshal(out, &document); err != nil || len(document.Causes) != 1 || len(document.Causes[0].Items) != 1 || document.Causes[0].Items[0].OriginType != "legacy_delete" {
			t.Fatal("cause CLI output", err)
		}
		want := 5
		if mode == "--inspect-restored-recovery" {
			want = 6
		}
		if document.Version != want {
			t.Fatal("cause CLI version", document.Version)
		}
	}
	for _, args := range [][]string{{"--receipt-causes"}, {"--receipt-causes", "--worker"}, {"--inspect-restored-recovery", "--receipt-causes", "--worker"}} {
		if out, err := invoke(args...); err == nil || len(out) != 0 {
			t.Fatal("mixed cause CLI")
		}
	}
}
