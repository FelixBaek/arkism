package memory

import (
	"context"
	"errors"
	"testing"
)

func maintenanceFixture(t *testing.T) (*fixture, Record, Record, MaintenanceInput) {
	f := newFixture(t)
	older := f.activeMemory("응답은 한국어로")
	newer := f.activeMemory("답은 한국어로, 쉬운 말로 해 줘")
	in := MaintenanceInput{Scope: scopeA, Proposal: MaintenanceProposal{Type: "duplicate", Reason: "same_meaning",
		Keep: Ref{Collection: "memories", ID: newer.ID, Revision: newer.Revision}, Retire: Ref{Collection: "memories", ID: older.ID, Revision: older.Revision}}}
	return f, older, newer, in
}

func TestMaintenanceAcceptKeepsOneAndLeavesTheOtherAsHistory(t *testing.T) {
	f, older, newer, in := maintenanceFixture(t)
	p, err := f.svc.ProposeMaintenance(testCtx, f.human, in)
	if err != nil || p.Status != "open" || p.Duplicate {
		t.Fatal("propose", p.Status, err)
	}
	if f.get(f.human, "memories", scopeA, older.ID).Status != "active" {
		t.Fatal("a proposal changed a memory before anyone decided")
	}
	if again, _ := f.svc.ProposeMaintenance(testCtx, f.human, in); !again.Duplicate || again.ID != p.ID {
		t.Fatal("the same pair was proposed twice")
	}
	swapped := in
	swapped.Proposal.Keep, swapped.Proposal.Retire = in.Proposal.Retire, in.Proposal.Keep
	if again, _ := f.svc.ProposeMaintenance(testCtx, f.human, swapped); !again.Duplicate {
		t.Fatal("the same pair in the other order was proposed again")
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.agent, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent decided", err)
	}
	done, err := f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"})
	if err != nil || done.Status != "accepted" || done.Maintenance.DecidedBy != "human-1" || done.Maintenance.DecidedAt == nil {
		t.Fatal("decide", done.Status, err)
	}
	if got := f.get(f.human, "memories", scopeA, older.ID); got.Status != "superseded" {
		t.Fatal("retired memory", got.Status)
	}
	if got := f.get(f.human, "memories", scopeA, newer.ID); got.Status != "active" {
		t.Fatal("kept memory", got.Status)
	}
	if open, _ := f.svc.ListMaintenance(testCtx, f.human, scopeA, ""); len(open.Items) != 0 {
		t.Fatal("decided proposal still open")
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 2, Decision: "dismiss"}); !errors.Is(err, ErrConflict) {
		t.Fatal("decided twice", err)
	}
}

func TestMaintenanceSwapAndDismiss(t *testing.T) {
	f, older, newer, in := maintenanceFixture(t)
	p, _ := f.svc.ProposeMaintenance(testCtx, f.human, in)
	if _, err := f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "swap"}); err != nil {
		t.Fatal(err)
	}
	if f.get(f.human, "memories", scopeA, older.ID).Status != "active" || f.get(f.human, "memories", scopeA, newer.ID).Status != "superseded" {
		t.Fatal("swap kept the wrong memory")
	}

	f, older, newer, in = maintenanceFixture(t)
	p, _ = f.svc.ProposeMaintenance(testCtx, f.human, in)
	d, err := f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "dismiss"})
	if err != nil || d.Status != "dismissed" {
		t.Fatal("dismiss", err)
	}
	if f.get(f.human, "memories", scopeA, older.ID).Status != "active" || f.get(f.human, "memories", scopeA, newer.ID).Status != "active" {
		t.Fatal("dismiss changed a memory")
	}
	if again, _ := f.svc.ProposeMaintenance(testCtx, f.human, in); !again.Duplicate || again.Status != "dismissed" {
		t.Fatal("a pair kept as two was proposed again")
	}
	if dismissed, _ := f.svc.ListMaintenance(testCtx, f.human, scopeA, "dismissed"); len(dismissed.Items) != 1 {
		t.Fatal("dismissed list")
	}
}

func TestMaintenanceStaleAndInvalid(t *testing.T) {
	f, older, _, in := maintenanceFixture(t)
	p, _ := f.svc.ProposeMaintenance(testCtx, f.human, in)
	// The memory changes after the proposal (here: a person retires it another way).
	other := f.activeMemory("다른 규칙")
	_, err := f.svc.Conflict(testCtx, f.human, ConflictInput{Scope: scopeA, Action: "mark", Reason: "manual",
		Memories: []Ref{{Collection: "memories", ID: older.ID, Revision: older.Revision}, {Collection: "memories", ID: other.ID, Revision: other.Revision}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"}); !errors.Is(err, ErrConflict) {
		t.Fatal("decided on a changed memory", err)
	}
	if got, _ := f.repo.Get(testCtx, colMaintenance, scopeA, p.ID); got.Status != "stale" {
		t.Fatal("stale", got.Status)
	}
	if got := f.get(f.human, "memories", scopeA, in.Proposal.Keep.ID); got.Status != "active" || got.ConflictID != "" || got.Revision != in.Proposal.Keep.Revision {
		t.Fatal("a failed decision left the kept memory changed", got.Status, got.ConflictID)
	}

	bad := in
	bad.Proposal.Reason = "답은 한국어로"
	if _, err = f.svc.ProposeMaintenance(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("free text reason accepted", err)
	}
	bad = in
	bad.Proposal.Retire = bad.Proposal.Keep
	if _, err = f.svc.ProposeMaintenance(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("same memory twice", err)
	}
	bad = in
	bad.Proposal.Keep.Revision++
	if _, err = f.svc.ProposeMaintenance(testCtx, f.human, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("old revision accepted", err)
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "delete"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown decision", err)
	}
}

// The review finding behind one transaction: if the mark lands and the resolve
// fails, nothing may stay half done (both memories conflicted, out of search).
func TestMaintenanceDecisionIsAllOrNothing(t *testing.T) {
	f, older, newer, in := maintenanceFixture(t)
	// The kept memory points at the other one, which the resolve step refuses
	// after the mark step has already passed.
	if err := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		newer.Relations, newer.Revision = []Ref{{Collection: "memories", ID: older.ID, Revision: older.Revision}}, newer.Revision+1
		return tx.Replace(ctx, colMemories, newer, newer.Revision-1)
	}); err != nil {
		t.Fatal(err)
	}
	in.Proposal.Keep.Revision = newer.Revision
	p, err := f.svc.ProposeMaintenance(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"}); !errors.Is(err, ErrConflict) {
		t.Fatal("decide", err)
	}
	for _, m := range []Record{older, newer} {
		if got := f.get(f.human, "memories", scopeA, m.ID); got.Status != "active" || got.ConflictID != "" || got.Revision != m.Revision {
			t.Fatal("half-done decision", got.ID, got.Status, got.ConflictID)
		}
	}
	if rels, _ := f.repo.ListRecords(testCtx, scopeA, "memory_relations", 100); len(rels) != 0 {
		t.Fatal("a conflict relation was left behind", len(rels))
	}
}

// Proposed again at the memory's new revision, the pair is asked about afresh.
func TestMaintenanceEditedMemoryIsProposedAgain(t *testing.T) {
	f, older, _, in := maintenanceFixture(t)
	first, _ := f.svc.ProposeMaintenance(testCtx, f.human, in)
	if err := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		older.Text, older.Revision = "응답은 한국어로 해 줘", older.Revision+1
		return tx.Replace(ctx, colMemories, older, older.Revision-1)
	}); err != nil {
		t.Fatal(err)
	}
	in.Proposal.Retire.Revision = older.Revision
	again, err := f.svc.ProposeMaintenance(testCtx, f.human, in)
	if err != nil || again.Duplicate || again.ID != first.ID || again.Revision != 2 || again.Status != "open" || again.Maintenance.Retire.Revision != older.Revision {
		t.Fatal("an edited memory's pair was not proposed again in place", err)
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.human, first.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"}); !errors.Is(err, ErrConflict) {
		t.Fatal("decided on the replaced proposal", err)
	}
	if done, err := f.svc.DecideMaintenance(testCtx, f.human, first.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 2, Decision: "accept"}); err != nil || done.Status != "accepted" {
		t.Fatal("decide the new proposal", err)
	}
}

// Keep both on a pair whose memory changed: stale, not a silent dismissal.
func TestMaintenanceDismissOfAChangedPairGoesStale(t *testing.T) {
	f, older, _, in := maintenanceFixture(t)
	p, _ := f.svc.ProposeMaintenance(testCtx, f.human, in)
	if err := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		older.Text, older.Revision = "응답은 한국어로 해 줘", older.Revision+1
		return tx.Replace(ctx, colMemories, older, older.Revision-1)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "dismiss"}); !errors.Is(err, ErrConflict) {
		t.Fatal("dismissed a changed pair", err)
	}
	if got, _ := f.repo.Get(testCtx, colMaintenance, scopeA, p.ID); got.Status != "stale" {
		t.Fatal("changed pair not stale", got.Status)
	}
}
