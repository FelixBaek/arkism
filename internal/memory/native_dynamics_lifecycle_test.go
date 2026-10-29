package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNativeDynamicsPurgeImmediateFenceAndResume(t *testing.T) {
	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	in.Passed = true
	_, captureRef := nativeAccept(t, f, in)
	if _, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef); e != nil {
		t.Fatal(e)
	}
	ref := in.Evidence[0]
	if e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return invalidateNativeDynamicsDependencyTx(ctx, tx, in.Scope, ref, "source_quarantined")
	}); e != nil {
		t.Fatal(e)
	}
	if v, e := f.svc.ReadNativeDynamicsSnapshot(testCtx, "native", NativeDynamicsQuery{in.Scope, in.Target, in.Dimension, "v1", time.Now().UnixNano()}); e == nil && v.Value != nil {
		t.Fatal("pending purge published old value")
	}
	old := in
	old.EventID = "must_not_create_during_purge"
	if _, e := f.svc.RecordMemoryOutcome(testCtx, "checker", old); !errors.Is(e, ErrConflict) {
		t.Fatal("old worker created dependency after fence", e)
	}
	job := Ref{Collection: colJobs, ID: nativePurgeID(in.Scope, ref), Revision: 1}
	nativeDrainPurge(t, f.svc, "native", in.Scope, job)
	before, _ := f.repo.Get(testCtx, colJobs, in.Scope, job.ID)
	done, e := f.svc.ProcessNativeDynamicsPurge(testCtx, "native", in.Scope, job, 64)
	after, _ := f.repo.Get(testCtx, colJobs, in.Scope, job.ID)
	if e != nil || !done || !reflect.DeepEqual(before, after) {
		t.Fatal("completed purge retry rewrote cursor", e)
	}
	if strings.Contains(structuredText(after.NativeDynamics), ref.ID) {
		t.Fatal("purge tombstone kept raw dependency id")
	}
	newer := ref
	newer.Revision++
	if e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error { return nativeRejectInvalidatedTx(ctx, tx, in.Scope, newer) }); e != nil {
		t.Fatal("new revision permanently fenced", e)
	}
}

func TestDynamicsStoreNativeBoundedPurgePG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "v1")
	_, captureRef := p.accept(t)
	p.process(t, captureRef)
	base, e := p.workerRepo.Get(testCtx, colJobs, p.in.Scope, captureRef.ID)
	if e != nil {
		t.Fatal(e)
	}
	checkNativePurgeSeedAndQuarantine(t, p, base)

	job := Ref{Collection: colJobs, ID: nativePurgeID(p.in.Scope, p.in.Evidence[0]), Revision: 1}
	// State page first, then a capture page. A lost reply commits only that bounded page and its cursor.
	if _, e := p.worker.ProcessNativeDynamicsPurge(testCtx, p.workerToken, p.in.Scope, job, 64); e != nil {
		t.Fatal(e)
	}
	checkNativePurgeCursorRollback(t, p, job)
	checkNativePurgeLostReply(t, p, job)
	checkNativePurgeCompletion(t, p, job)
	checkNativePurgeNewObservation(t, p)
}

type nativeNoOptionalTx struct{ Tx }

func TestNativeDynamicsCatalogAndDisabledMaintenance(t *testing.T) {
	f, in := nativeFixture(t)
	if e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return invalidateNativeDynamicsDependencyTx(ctx, nativeNoOptionalTx{tx}, in.Scope, in.Target, "target_deleted")
	}); e != nil {
		t.Fatal("legacy no-catalog deletion rejected", e)
	}
	nativeActivate(t, f, in, "v1")
	in.Passed = true
	_, captureRef := nativeAccept(t, f, in)
	if _, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef); e != nil {
		t.Fatal(e)
	}
	if e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return invalidateNativeDynamicsDependencyTx(ctx, nativeNoOptionalTx{tx}, in.Scope, in.Target, "target_deleted")
	}); !errors.Is(e, ErrUnavailable) {
		t.Fatal("catalog silently ignored without query seam", e)
	}
	f.svc.NativeDynamics = nil
	if e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return invalidateNativeDynamicsDependencyTx(ctx, tx, in.Scope, in.Target, "target_deleted")
	}); e != nil {
		t.Fatal(e)
	}
	nativeDrainPurge(t, f.svc, "native", in.Scope, Ref{Collection: colJobs, ID: nativePurgeID(in.Scope, in.Target), Revision: 1})
	for _, r := range nativeKindRows(f, "state") {
		if r.NativeDynamics.State.Value != nil {
			t.Fatal("off excused cleanup")
		}
	}
}

func TestDynamicsStoreNativeDeleteWhileDisabledPG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "v1")
	_, captureRef := p.accept(t)
	p.process(t, captureRef)
	token := testPrincipalToken("native-pg-human-delete")
	if e := p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "native-pg-human", Role: "human", Token: token, OrganizationID: p.in.Scope.WorkspaceID, ProjectID: p.in.Scope.ProjectID, Actions: []string{"db:read", "db:write", "delete:memories", "delete:memory_candidates"}}); e != nil {
		t.Fatal(e)
	}
	repo, e := p.app.WithToken(token)
	if e != nil {
		t.Fatal(e)
	}
	svc := &Service{Repo: repo, Auth: p.worker.Auth}
	if e := svc.Delete(testCtx, token, colMemories, p.in.Target.ID, ChangeInput{Scope: p.in.Scope, ExpectedRevision: p.in.Target.Revision}); e != nil {
		t.Fatal("actual Service.Delete with feature off", e)
	}
	job := Ref{Collection: colJobs, ID: nativePurgeID(p.in.Scope, p.in.Target), Revision: 1}
	marker, e := p.workerRepo.Get(testCtx, colJobs, p.in.Scope, job.ID)
	if e != nil || marker.Status != JobStatusPending {
		t.Fatal("off deletion omitted durable purge", e)
	}
	p.worker.NativeDynamics = nil
	nativeDrainPurge(t, p.worker, p.workerToken, p.in.Scope, job)
	for _, role := range nativePurgeRoles {
		for _, r := range p.rows(t, role) {
			n := r.NativeDynamics
			if n.Input != nil || n.Config != nil || n.Receipt != nil || n.Key.Target.ID != "" || len(n.Dependencies) != 0 {
				t.Fatal("off delete left native payload", role)
			}
		}
	}
	checkNativePhysicalPurge(t, p)
}

func checkNativePurgeSeedAndQuarantine(t *testing.T, p *nativePGFixture, base Record) {
	t.Helper()

	// These synthetic duplicate metadata rows exercise pagination only, never source acceptance or learning authority.
	if e := p.workerRepo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		for i := 0; i < 130; i++ {
			r := base
			r.ID = fmt.Sprintf("a_purge_fixture_%03d", i)
			r.Revision = 1
			r.NativeDynamics, _ = nativeClone(base.NativeDynamics)
			if e := nativeInsertTx(ctx, tx, r); e != nil {
				return e
			}
		}
		return quarantinePrivacyRecords(ctx, tx, p.in.Scope, PrivacyImpact{Records: []Ref{p.in.Evidence[0]}}, time.Now().UTC())
	}); e != nil {
		t.Fatal("quarantine owner transaction", e)
	}
	if v, e := p.worker.ReadNativeDynamicsSnapshot(testCtx, p.workerToken, NativeDynamicsQuery{p.in.Scope, p.in.Target, p.in.Dimension, "v1", time.Now().UnixNano()}); e == nil && v.Value != nil {
		t.Fatal("quarantine published before scrub")
	}
}

func checkNativePurgeCursorRollback(t *testing.T, p *nativePGFixture, job Ref) {
	t.Helper()

	cursorFailure := errors.New("isolated purge cursor write failure")
	p.worker.Repo = nativeFaultRepo{p.workerRepo, "purge", cursorFailure}
	if done, e := p.worker.ProcessNativeDynamicsPurge(testCtx, p.workerToken, p.in.Scope, job, 64); !errors.Is(e, cursorFailure) || done {
		t.Fatal("failed cursor write acknowledged page", e)
	}
	p.worker.Repo = p.workerRepo
	for _, r := range p.rows(t, "capture") {
		if r.NativeDynamics.Input == nil {
			t.Fatal("page scrub committed without its cursor")
		}
	}
	old := p.in
	old.EventID = "blocked_new_transport_after_invalidation"
	if _, e := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, old); !errors.Is(e, ErrConflict) && !errors.Is(e, ErrDenied) {
		t.Fatal("old worker added dependency after quarantine", e)
	}
}

func checkNativePurgeLostReply(t *testing.T, p *nativePGFixture, job Ref) {
	t.Helper()

	failure := errors.New("isolated purge reply loss")
	p.worker.Repo = nativeLostReplyRepo{p.workerRepo, failure}
	if done, e := p.worker.ProcessNativeDynamicsPurge(testCtx, p.workerToken, p.in.Scope, job, 64); !errors.Is(e, failure) || done {
		t.Fatal("lost reply exposed purge completion", e)
	}
	p.worker.Repo = p.workerRepo
	removed := 0
	for _, r := range p.rows(t, "capture") {
		if r.NativeDynamics.Input == nil {
			removed++
		}
	}
	if removed != 64 {
		t.Fatal("page was not exactly bounded", removed)
	}
	if e := p.workerRepo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		pending, e := nativePurgePendingTx(ctx, tx, p.in.Scope)
		if e != nil {
			return e
		}
		if !pending {
			return ErrConflict
		}
		return nil
	}); e != nil {
		t.Fatal("pending purge disappeared", e)
	}
}

func checkNativePurgeCompletion(t *testing.T, p *nativePGFixture, job Ref) {
	t.Helper()

	nativeDrainPurge(t, p.worker, p.workerToken, p.in.Scope, job)
	for _, role := range []string{"state", "capture", "event", "seen", "consumption", "checkpoint"} {
		for _, r := range p.rows(t, role) {
			n := r.NativeDynamics
			if n.Input != nil || n.Receipt != nil || n.Config != nil || n.Evaluation.ID != "" || n.Key.Target.ID != "" || len(n.Dependencies) != 0 {
				t.Fatal("stored payload still contains source data", role)
			}
		}
	}
	if e := p.workerRepo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		pending, e := nativePurgePendingTx(ctx, tx, p.in.Scope)
		if e != nil {
			return e
		}
		if pending {
			return ErrConflict
		}
		return nil
	}); e != nil {
		t.Fatal("purge completion missing", e)
	}
}

func checkNativePurgeNewObservation(t *testing.T, p *nativePGFixture) {
	t.Helper()

	// A new canonical trace/source and explicit config version are a different native observation.
	// This does not establish causal credit or detect a covert copy.
	p.activate(t, "v2")
	f := newFixture(t)
	f.svc.Repo = p.admin
	f.auth.actors["new_native_producer"] = actor("new-native-producer", roleService, true, allVerbs...)
	payload, e := json.Marshal(map[string]any{"retrieved": []Ref{p.in.Target}})
	if e != nil {
		t.Fatal(e)
	}
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	trace, e := f.svc.IngestCompletedTrace(testCtx, "new_native_producer", TraceIngestInput{Scope: p.in.Scope, TraceID: "new-native-canonical-trace", ThreadID: "new-native-canonical-thread", AgentRun: CompletedAgentRunInput{AgentID: "new-native-agent", TaskType: "agent_session", Outcome: "partial", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "new independent synthetic source fixture"}, Events: []TraceEventInput{{ID: "new-native-canonical-source", Kind: "context", OccurredAt: start.Add(time.Second), Payload: payload}}})
	if e != nil {
		t.Fatal("new canonical source", e)
	}
	p.in.Trace = trace.Trace
	p.in.Evidence = trace.SourceEvents
	p.in.EventID = "new-native-outcome-after-purge"
	_, next := p.accept(t)
	r := p.process(t, next)
	if !r.AppliedNow || !r.LearningConsumed {
		t.Fatal("old marker permanently blocked new observation")
	}
	if v := p.view(t, "v2"); v.Value == nil || *v.Value <= 0.5 {
		t.Fatal("new explicit enrollment did not advance")
	}
}

func checkNativePhysicalPurge(t *testing.T, p *nativePGFixture) {
	t.Helper()
	// Inspect both physical representations, rather than inferring purge from API JSON omission.
	inspected := 0
	if e := p.workerRepo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		rows, e := tx.(*Postgres).q().Query(ctx, "SELECT payload,doc::text FROM memory_records WHERE collection='jobs' AND workspace_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND kind LIKE 'native_dynamics_%'", p.in.Scope.WorkspaceID, p.in.Scope.ProjectID, p.in.Scope.UserID, p.in.Scope.AgentID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var payload []byte
			var doc string
			if e := rows.Scan(&payload, &doc); e != nil {
				return e
			}
			if e := nativePhysicalPurgeRowError(payload, doc, append([]Ref{p.in.Target, p.in.Trace}, p.in.Evidence...)); e != nil {
				return e
			}
			inspected++
		}
		return rows.Err()
	}); e != nil {
		t.Fatal("physical native purge", e)
	}
	if inspected == 0 {
		t.Fatal("physical purge inspected zero rows")
	}
	t.Logf("physical native payload/doc inspected rows=%d", inspected)
}
func nativePhysicalPurgeRowError(payload []byte, doc string, refs []Ref) error {
	for _, forbidden := range refs {
		if strings.Contains(string(payload), forbidden.ID) || strings.Contains(doc, forbidden.ID) {
			return errors.New("native physical payload/doc retains erased reference")
		}
	}
	return nil
}
