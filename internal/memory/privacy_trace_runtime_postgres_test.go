package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// privacyTraceFixture ingests one completed trace with two events in scope and
// a twin trace (IDs suffixed "-other"; trace IDs are globally unique) in a second project.
func privacyTraceFixture(t *testing.T) (*Postgres, *Service, Scope, Scope) {
	t.Helper()
	p := isolatedPostgres(t)
	if err := p.initTraceSchema(testCtx); err != nil {
		t.Fatal(err)
	}
	auth := &fakeAuth{actors: map[string]fakeActor{
		"owner":  actor("owner", "human", false, append(allVerbs, "hold", "release", "retention")...),
		"worker": actor("worker", "service", false, "erase", "purge", "read"),
		"agent":  actor("agent", "agent", false, "read"),
	}}
	svc := &Service{Repo: p, Auth: auth}
	scope := Scope{WorkspaceID: "org-privacy", ProjectID: "project-privacy", UserID: "owner", AgentID: "agent-privacy"}
	other := scope
	other.ProjectID = "project-untouched"
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	for s, suffix := range map[Scope]string{scope: "", other: "-other"} {
		// event-1 used an older source as memory context: a context_uses edge.
		ctxSource := Record{ID: "ctx-src" + suffix, Scope: s, Kind: "document", Status: statusActive, Revision: 1, Text: "older evidence", CreatedAt: start, UpdatedAt: start}
		if err := p.Insert(testCtx, "sources", ctxSource); err != nil {
			t.Fatal(err)
		}
		payload := `{"message":"private-trace-marker","memory_context":{"items":[{"memory_provenance":{"source_event_id":"` + ctxSource.ID + `","trace_id":"trace-historical"}}]}}`
		in := TraceIngestInput{Scope: s, TraceID: "trace-1" + suffix, ThreadID: "thread-1",
			AgentRun: CompletedAgentRunInput{ID: "run-1" + suffix, TaskType: "review", Outcome: "success", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "run text"},
			Events: []TraceEventInput{
				{ID: "event-1" + suffix, Kind: "message", OccurredAt: start.Add(100 * time.Millisecond), Payload: json.RawMessage(payload)},
				{ID: "event-2" + suffix, Kind: "tool_result", OccurredAt: start.Add(200 * time.Millisecond), Payload: json.RawMessage(`{"result":"sibling-marker"}`)},
			}}
		if _, err := svc.IngestCompletedTrace(testCtx, "owner", in); err != nil {
			t.Fatal("ingest:", err)
		}
	}
	return p, svc, scope, other
}

func nativeTraceState(t *testing.T, p *Postgres, s Scope, table, id string) (int, bool) {
	t.Helper()
	var size int
	var erased bool
	if err := p.q().QueryRow(testCtx, `SELECT octet_length(payload_raw),content_erased FROM `+table+` WHERE organization_id=$1 AND project_id=$2 AND id=$3`, s.WorkspaceID, s.ProjectID, id).Scan(&size, &erased); err != nil {
		t.Fatal(table, id, err)
	}
	return size, erased
}

func TestPostgresPrivacyTraceDeletionRunsRuntimeStepAndErasesOnlyTarget(t *testing.T) {
	p, svc, scope, other := privacyTraceFixture(t)
	if _, err := svc.RequestPrivacyDeletion(testCtx, "owner", PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "missing", TargetType: "trace", TargetIDs: []string{"trace-missing"}, Reason: "owner_request"}); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown trace accepted", err)
	}
	status, err := svc.RequestPrivacyDeletion(testCtx, "owner", PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "trace", TargetType: "trace", TargetIDs: []string{"trace-1"}, Reason: "owner_request"})
	if err != nil {
		t.Fatal(err)
	}
	impact := status.PrivacyDeletion.Impact
	if !sameStrings(impact.SourceEventIDs, []string{"event-1", "event-2"}) || !sameStrings(impact.TraceIDs, []string{"trace-1"}) || !sameStrings(impact.ExpectedRunIDs, []string{"run-1"}) || !sameStrings(impact.ThreadIDs, []string{"thread-1"}) {
		t.Fatalf("trace impact = %+v", impact)
	}
	fence, err := svc.CheckPrivacyFence(testCtx, "worker", PrivacyFenceInput{Scope: scope, TraceIDs: []string{"trace-1"}})
	if err != nil || fence.Allowed || len(fence.DeletionRequestIDs) != 1 {
		t.Fatalf("quarantined trace fence = %+v %v", fence, err)
	}
	if fence, err = svc.CheckPrivacyFence(testCtx, "worker", PrivacyFenceInput{Scope: other, TraceIDs: []string{"trace-1-other"}}); err != nil || !fence.Allowed {
		t.Fatalf("other project trace fenced = %+v %v", fence, err)
	}
	if _, err := svc.RunPrivacyRuntimeStep(testCtx, "agent"); !errors.Is(err, ErrDenied) {
		t.Fatal("agent ran the runtime step", err)
	}
	if n, err := svc.RunPrivacyRuntimeStep(testCtx, "worker"); err != nil || n != 1 {
		t.Fatalf("runtime step = %d %v", n, err)
	}
	if n, err := svc.RunPrivacyRuntimeStep(testCtx, "worker"); err != nil || n != 0 {
		t.Fatalf("second runtime step = %d %v", n, err)
	}
	status, err = svc.GetPrivacyDeletion(testCtx, "owner", status.ID, scope)
	if err != nil || status.PrivacyDeletion.State != privacyPurgingDerived || !status.PrivacyDeletion.Report.RuntimePurged || !sameStrings(status.PrivacyDeletion.RuntimeReceipt.AbsentRunIDs, []string{"run-1"}) {
		t.Fatalf("after runtime step = %+v %v", status, err)
	}
	for i := 0; i < 100 && status.PrivacyDeletion.State != privacyWaiting; i++ {
		if status.PrivacyDeletion.State == privacyPurgingDerived || status.PrivacyDeletion.State == privacyVerifying {
			if status, err = svc.AdvancePrivacyDeletion(testCtx, "worker", status.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: status.Revision}); err != nil {
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
	if status.PrivacyDeletion.State != privacyWaiting || !status.PrivacyDeletion.Report.DerivedPurged || !status.PrivacyDeletion.Report.BackupExpiryPending {
		t.Fatalf("deletion did not finish: %+v", status)
	}
	for _, target := range []struct{ col, id string }{{"sources", "event-1"}, {"sources", "event-2"}, {"execution_traces", "trace-1"}} {
		if _, err := p.Get(testCtx, target.col, scope, target.id); !errors.Is(err, ErrNotFound) {
			t.Fatal("erased row still readable", target, err)
		}
		if row, err := p.Get(testCtx, target.col, other, target.id+"-other"); err != nil || row.PrivacyQuarantined {
			t.Fatal("other project row changed", target, err)
		}
	}
	if row, err := p.Get(testCtx, "sources", scope, "ctx-src"); err != nil || row.PrivacyQuarantined {
		t.Fatal("evidence the trace only read was deleted", err)
	}
	for _, native := range []struct{ table, id string }{{"source_events", "event-1"}, {"source_events", "event-2"}, {"execution_traces", "trace-1"}} {
		if size, erased := nativeTraceState(t, p, scope, native.table, native.id); size != 0 || !erased {
			t.Fatal("native bytes survived", native, size, erased)
		}
		if size, erased := nativeTraceState(t, p, other, native.table, native.id+"-other"); size == 0 || erased {
			t.Fatal("other project native bytes erased", native)
		}
	}
	report, err := verifyInTx(p, scope, status.PrivacyDeletion.Impact)
	if err != nil || !report.DerivedPurged {
		t.Fatalf("verify after purge = %+v %v", report, err)
	}
}

func verifyInTx(p *Postgres, scope Scope, impact PrivacyImpact) (PrivacyPurgeReport, error) {
	var report PrivacyPurgeReport
	err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		var err error
		report, err = tx.(*Postgres).VerifyPrivacyPurge(ctx, scope, impact)
		return err
	})
	return report, err
}

func TestPostgresPrivacyRuntimeStepKeepsHeldDeletionHeld(t *testing.T) {
	p, svc, scope, _ := privacyTraceFixture(t)
	status, err := svc.RequestPrivacyDeletion(testCtx, "owner", PrivacyDeletionRequest{Scope: scope, IdempotencyKey: "held", TargetType: "trace", TargetIDs: []string{"trace-1"}, Reason: "owner_request"})
	if err != nil {
		t.Fatal(err)
	}
	if status, err = svc.HoldPrivacyDeletion(testCtx, "owner", status.ID, PrivacyHoldInput{Scope: scope, ExpectedRevision: status.Revision, Reason: "legal_case", PolicyRef: "case_1"}); err != nil {
		t.Fatal(err)
	}
	// A held deletion is not "waiting for runtime": the step has nothing to record.
	scopes, err := p.PrivacyRuntimeScopes(testCtx)
	if err != nil || len(scopes) != 0 {
		t.Fatalf("held deletion listed for runtime purge: %v %v", scopes, err)
	}
	if n, err := svc.RunPrivacyRuntimeStep(testCtx, "worker"); err != nil || n != 0 {
		t.Fatalf("runtime step on held = %d %v", n, err)
	}
	if _, err := svc.AdvancePrivacyDeletion(testCtx, "worker", status.ID, PrivacyRevisionInput{Scope: scope, ExpectedRevision: status.Revision}); !errors.Is(err, ErrConflict) {
		t.Fatal("held deletion advanced", err)
	}
	if _, err := p.NextPrivacyAdvance(testCtx, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatal("held deletion offered to the advance worker", err)
	}
	if row, err := p.Get(testCtx, "sources", scope, "event-1"); err != nil || !row.PrivacyQuarantined || row.Text == "" {
		t.Fatal("held source lost or not quarantined", err)
	}
	if size, erased := nativeTraceState(t, p, scope, "source_events", "event-1"); size == 0 || erased {
		t.Fatal("held native bytes erased")
	}
	// Releasing makes it runnable again; the step then purges runtime and moves on.
	if status, err = svc.ReleasePrivacyDeletionHold(testCtx, "owner", status.ID, PrivacyHoldInput{Scope: scope, ExpectedRevision: status.Revision, Reason: "case_closed", PolicyRef: "case_1"}); err != nil {
		t.Fatal(err)
	}
	if scopes, err = p.PrivacyRuntimeScopes(testCtx); err != nil || len(scopes) != 1 || scopes[0] != scope {
		t.Fatalf("released deletion scopes = %v %v", scopes, err)
	}
	if n, err := svc.RunPrivacyRuntimeStep(testCtx, "worker"); err != nil || n != 1 {
		t.Fatalf("runtime step after release = %d %v", n, err)
	}
	if got, err := svc.GetPrivacyDeletion(testCtx, "owner", status.ID, scope); err != nil || got.PrivacyDeletion.State != privacyPurgingDerived {
		t.Fatalf("released state = %+v %v", got, err)
	}
	if next, err := p.NextPrivacyAdvance(testCtx, time.Now()); err != nil || next.ID != status.ID {
		t.Fatal("released deletion not offered to the advance worker", err)
	}
}

func TestPrivacyRuntimeStepRequiresPendingScopeStore(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.RunPrivacyRuntimeStep(testCtx, f.work); !errors.Is(err, ErrUnavailable) {
		t.Fatal("runtime step without a pending-scope store", err)
	}
}
