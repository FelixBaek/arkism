package memory

import (
	"context"
	"errors"
	"testing"
	"time"
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

func TestMaintenanceForgetKeepsMemoriesAsHistoryWithReason(t *testing.T) {
	f, older, newer, in := maintenanceFixture(t)
	p, _ := f.svc.ProposeMaintenance(testCtx, f.human, in)
	for _, bad := range []MaintenanceDecision{
		{Scope: scopeA, ExpectedRevision: 1, Decision: "forget_both"},                        // no reason
		{Scope: scopeA, ExpectedRevision: 1, Decision: "forget_both", ReasonCode: "someday"}, // unknown reason
		{Scope: scopeA, ExpectedRevision: 1, Decision: "dismiss", ReasonCode: "stale"},       // reason without forgetting
		{Scope: scopeA, ExpectedRevision: 1, Decision: "forget", ReasonCode: "stale"},        // signal-only decision
	} {
		if _, err := f.svc.DecideMaintenance(testCtx, f.human, p.ID, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal(bad, err)
		}
	}
	if out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "한국어"}); e != nil || len(out.Items) == 0 {
		t.Fatal("the pair is not recalled before forgetting", e)
	}
	// A duplicate pair can lose both: each keeps the reason, who and when, and leaves recall.
	d, err := f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "forget_both", ReasonCode: "stale", Reason: "디자인을 새 파일로 옮김"})
	if err != nil || d.Status != "forgotten" || d.Maintenance.Decision != "forget_both" {
		t.Fatal("forget_both", d.Status, err)
	}
	for _, id := range []string{older.ID, newer.ID} {
		got := f.get(f.human, "memories", scopeA, id)
		if got.Status != "rejected" || got.ConflictID != "" || got.Removal == nil || got.Removal.Code != "stale" || got.Removal.Note != "디자인을 새 파일로 옮김" || got.Removal.By == "" || got.Removal.At.IsZero() {
			t.Fatal("forgotten memory", id, got.Status, got.Removal)
		}
	}
	if out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "한국어"}); e != nil || len(out.Items) != 0 {
		t.Fatal("a forgotten memory is still recalled", out.Items, e)
	}
	if done, _ := f.svc.ListMaintenance(testCtx, f.human, scopeA, "forgotten"); len(done.Items) != 1 {
		t.Fatal("forgotten list")
	}

	// Forgetting one side leaves the other active.
	f, older, newer, in = maintenanceFixture(t)
	in.Proposal.Type, in.Proposal.Reason = "contradiction", "newer_replaces_older"
	p, _ = f.svc.ProposeMaintenance(testCtx, f.human, in)
	if _, err := f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "forget_retire", ReasonCode: "wrong"}); err != nil {
		t.Fatal(err)
	}
	if got := f.get(f.human, "memories", scopeA, older.ID); got.Status != "rejected" || got.Removal.Code != "wrong" {
		t.Fatal("retire side", got.Status)
	}
	if got := f.get(f.human, "memories", scopeA, newer.ID); got.Status != "active" || got.Removal != nil {
		t.Fatal("keep side changed", got.Status)
	}
}

func TestMemoryTransitionStoresRemovalReason(t *testing.T) {
	f := newFixture(t)
	m := f.activeMemory("오래된 리디자인 계획")
	for _, bad := range []ChangeInput{
		{Scope: scopeA, ExpectedRevision: m.Revision, Status: "rejected", ReasonCode: "someday"},
		{Scope: scopeA, ExpectedRevision: m.Revision, Status: "rejected", Reason: "note without a code"},
		{Scope: scopeA, ExpectedRevision: m.Revision, Status: "expired", ReasonCode: "stale"},
	} {
		if _, err := f.svc.Transition(testCtx, f.human, "memories", m.ID, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal(bad, err)
		}
	}
	got, err := f.svc.Transition(testCtx, f.human, "memories", m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision, Status: "rejected", ReasonCode: "changed", Reason: "계획이 바뀜"})
	if err != nil || got.Status != "rejected" || got.Removal == nil || got.Removal.Code != "changed" || got.Removal.Note != "계획이 바뀜" || got.Removal.By == "" {
		t.Fatal(got.Removal, err)
	}
	if stored := f.get(f.human, "memories", scopeA, m.ID); stored.Removal == nil || stored.Removal.Code != "changed" {
		t.Fatal("removal not stored", stored.Removal)
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

// signalFixture is an active memory of the given kind with a stored ST-813 signal of samples/passed.
func signalFixture(t *testing.T, kind string, samples, passed int) (*fixture, Record, MaintenanceInput) {
	f := newFixture(t)
	cand := f.create(f.human, "memory_candidates", CreateInput{Scope: scopeA, Kind: kind, Text: "PR 본문에 광고 문구를 넣지 않는다", Sources: refs(f.source())})
	approved := f.transition(f.human, "memory_candidates", cand.ID, ChangeInput{Scope: scopeA, ExpectedRevision: cand.Revision, Status: "approved"})
	m := f.transition(f.human, "memories", approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision, Status: "active"})
	ref := Ref{Collection: "memories", ID: m.ID, Revision: m.Revision}
	sig := MemorySignal{Memory: ref, Samples: samples, Passed: passed}
	now := time.Now().UTC()
	if err := f.repo.Insert(testCtx, colJobs, Record{ID: memorySignalID(scopeA, m.ID), Scope: scopeA, Kind: memorySignalKind, Status: JobStatusDone, Revision: 1,
		Target: &ref, MemorySignal: &sig, CreatedBy: "human-1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	typ := "rule_often_violated"
	if kind == "lesson" {
		typ = "lesson_often_failing"
	}
	return f, m, MaintenanceInput{Scope: scopeA, Proposal: MaintenanceProposal{Type: typ, Reason: "low_pass_rate", Memory: &ref,
		Evidence: &MaintenanceEvidence{Samples: samples, Passed: passed, PassRate: 0.99}}}
}

// ST-814 / FR-079 / TC-MAINT-010: a signal proposal carries the stored counts as evidence and a person's answer changes no memory.
func TestMaintenanceSignalProposalOnlyProposes(t *testing.T) {
	f, m, in := signalFixture(t, "procedural", 12, 3)
	p, err := f.svc.ProposeMaintenance(testCtx, f.human, in)
	if err != nil || p.Status != "open" || p.Kind != "rule_often_violated" || p.Maintenance.Evidence.PassRate != 0.25 {
		t.Fatal("propose", p.Status, p.Kind, err)
	}
	if again, _ := f.svc.ProposeMaintenance(testCtx, f.human, in); !again.Duplicate || again.ID != p.ID {
		t.Fatal("the same memory was proposed twice")
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.agent, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent decided", err)
	}
	if _, err = f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "swap"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("swap on one memory", err)
	}
	done, err := f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "accept"})
	if err != nil || done.Status != "accepted" {
		t.Fatal("accept", err)
	}
	if got := f.get(f.human, "memories", scopeA, m.ID); got.Status != "active" || got.Revision != m.Revision {
		t.Fatal("accepting a signal proposal changed the memory", got.Status, got.Revision)
	}
	if again, _ := f.svc.ProposeMaintenance(testCtx, f.human, in); !again.Duplicate || again.Status != "accepted" {
		t.Fatal("a decided memory was proposed again")
	}

	f, _, in = signalFixture(t, "lesson", 10, 2)
	if p, err = f.svc.ProposeMaintenance(testCtx, f.human, in); err != nil || p.Kind != "lesson_often_failing" {
		t.Fatal("lesson", err)
	}
	if d, err := f.svc.DecideMaintenance(testCtx, f.human, p.ID, MaintenanceDecision{Scope: scopeA, ExpectedRevision: 1, Decision: "dismiss"}); err != nil || d.Status != "dismissed" {
		t.Fatal("dismiss", err)
	}
}

func TestMaintenanceSignalProposalEvidenceMustMatch(t *testing.T) {
	f, _, in := signalFixture(t, "procedural", 12, 3)
	bad := in
	bad.Proposal.Evidence = &MaintenanceEvidence{Samples: 12, Passed: 1}
	if _, err := f.svc.ProposeMaintenance(testCtx, f.human, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("made-up evidence accepted", err)
	}
	bad = in
	bad.Proposal.Type = "lesson_often_failing"
	if _, err := f.svc.ProposeMaintenance(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("a rule proposed as a lesson", err)
	}
	for _, mut := range []func(*MaintenanceProposal){
		func(p *MaintenanceProposal) { p.Reason = "자주 어김" },
		func(p *MaintenanceProposal) { p.Evidence = nil },
		func(p *MaintenanceProposal) { p.Evidence = &MaintenanceEvidence{Samples: 2, Passed: 3} },
		func(p *MaintenanceProposal) { p.Keep = *p.Memory },
	} {
		bad = in
		mut(&bad.Proposal)
		if _, err := f.svc.ProposeMaintenance(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid signal proposal accepted", err)
		}
	}
	bad = in
	stale := *in.Proposal.Memory
	stale.Revision++
	bad.Proposal.Memory = &stale
	if _, err := f.svc.ProposeMaintenance(testCtx, f.human, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("old revision accepted", err)
	}
	if err := f.repo.Delete(testCtx, colJobs, scopeA, memorySignalID(scopeA, in.Proposal.Memory.ID), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ProposeMaintenance(testCtx, f.human, in); !errors.Is(err, ErrConflict) {
		t.Fatal("proposed without a stored signal", err)
	}
}
