package memory

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type nativePGFixture struct {
	admin, app, checkerRepo, workerRepo *Postgres
	checker, worker                     *Service
	in                                  MemoryOutcomeInput
	checkerToken, workerToken           string
}

func nativePGSetup(t *testing.T) *nativePGFixture {
	t.Helper()
	admin := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = admin
	m := reviewedForEvaluation(t, f)
	target := Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}
	f.auth.actors["native_pg_producer"] = actor("native-pg-producer", roleService, true, allVerbs...)
	raw, e := json.Marshal(map[string]any{"retrieved": []Ref{target}})
	if e != nil {
		t.Fatal(e)
	}
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	trace, e := f.svc.IngestCompletedTrace(testCtx, "native_pg_producer", TraceIngestInput{Scope: scopeA, TraceID: "native-pg-trace", ThreadID: "native-pg-thread", AgentRun: CompletedAgentRunInput{AgentID: "native-pg-agent", TaskType: "agent_session", Outcome: "partial", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "synthetic native source setup"}, Events: []TraceEventInput{{ID: "native-pg-source", Kind: "context", OccurredAt: start.Add(time.Second), Payload: raw}}})
	if e != nil {
		t.Fatal("canonical trace", e)
	}
	common := []string{"db:read", "db:write", "read:evaluations", "read:agent_runs", "read:execution_traces", "read:sources", "read:memories"}
	checkerToken, workerToken := testPrincipalToken("native-checker-"+ID()), testPrincipalToken("native-worker-"+ID())
	if e := admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "native-pg-checker", Role: roleService, Token: checkerToken, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: append(append([]string{}, common...), "write:evaluations")}); e != nil {
		t.Fatal(e)
	}
	if e := admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "native-pg-worker", Role: roleService, Token: workerToken, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: append(append([]string{}, common...), "read:jobs", "write:jobs")}); e != nil {
		t.Fatal(e)
	}
	app := restrictedPostgres(t, admin)
	checkerRepo, e := app.WithToken(checkerToken)
	if e != nil {
		t.Fatal(e)
	}
	workerRepo, e := app.WithToken(workerToken)
	if e != nil {
		t.Fatal(e)
	}
	auth, e := NewPostgresAuthorizer(app)
	if e != nil {
		t.Fatal(e)
	}
	runtime := &NativeDynamicsRuntime{Enabled: true}
	checker := &Service{Repo: checkerRepo, Auth: auth, NativeDynamics: runtime}
	worker := &Service{Repo: workerRepo, Auth: auth, NativeDynamics: runtime}
	in := MemoryOutcomeInput{Scope: scopeA, Target: target, Trace: trace.Trace, Evidence: trace.SourceEvents, EventID: "native-pg-outcome", CheckerSHA256: strings.Repeat("a", 64), Dimension: "work_task", Passed: true}
	return &nativePGFixture{admin, app, checkerRepo, workerRepo, checker, worker, in, checkerToken, workerToken}
}
func (p *nativePGFixture) activate(t *testing.T, version string) NativeDynamicsReceipt {
	t.Helper()
	r, e := p.worker.ActivateNativeMemoryDynamics(testCtx, p.workerToken, NativeDynamicsActivationInput{p.in.Scope, p.in.Target, p.in.Dimension, nativeTestConfig(version), "activate_" + version})
	if e != nil {
		t.Fatal("PG activate", e)
	}
	return r
}
func (p *nativePGFixture) accept(t *testing.T) (Ref, Ref) {
	t.Helper()
	ref, e := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, p.in)
	if e != nil {
		t.Fatal("PG accept", e)
	}
	return ref, Ref{Collection: colJobs, ID: nativeCaptureID(p.in.Scope, ref), Revision: 1}
}
func (p *nativePGFixture) process(t *testing.T, captureRef Ref) NativeDynamicsReceipt {
	t.Helper()
	r, e := p.worker.ProcessNativeDynamicsCapture(testCtx, p.workerToken, p.in.Scope, captureRef)
	if e != nil {
		t.Fatal("PG process", e)
	}
	return r
}
func (p *nativePGFixture) rows(t *testing.T, role string) []Record {
	t.Helper()
	out := []Record{}
	for _, r := range pgCollection(t, p.admin, colJobs) {
		if r.Kind == nativeDynamicsPrefix+role {
			out = append(out, r)
		}
	}
	return out
}
func (p *nativePGFixture) view(t *testing.T, version string) NativeDynamicsView {
	t.Helper()
	r, e := p.worker.ReadNativeDynamicsSnapshot(testCtx, p.workerToken, NativeDynamicsQuery{p.in.Scope, p.in.Target, p.in.Dimension, version, time.Now().UnixNano()})
	if e != nil {
		t.Fatal("PG read", e)
	}
	return r
}

func TestDynamicsStoreNativeSourceStampAndCapturePG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "v1")
	ref, captureRef := p.accept(t)
	eval, e := p.admin.Get(testCtx, "evaluations", p.in.Scope, ref.ID)
	if e != nil {
		t.Fatal(e)
	}
	row, e := p.admin.Get(testCtx, colJobs, p.in.Scope, captureRef.ID)
	if e != nil {
		t.Fatal(e)
	}
	if eval.NativeOutcomeCaptureStamp == nil || eval.NativeOutcomeCaptureStamp.Mode != "enrolled" || row.NativeDynamics == nil || row.NativeDynamics.Input == nil || !reflect.DeepEqual(*row.NativeDynamics.Input, p.in) {
		t.Fatal("durable source binding missing")
	}
	if len(p.rows(t, "event")) != 0 {
		t.Fatal("capture implicitly applied")
	}
	p.process(t, captureRef)
	if v := p.view(t, "v1"); v.Value == nil || math.Abs(*v.Value-0.55) > 1e-15 {
		t.Fatal("not actual scalar path")
	}
}
func TestDynamicsStoreNativeCodecMillisPG(t *testing.T) {
	p := nativePGSetup(t)
	activation := p.activate(t, "v1")
	// No sleep: source acceptance follows actual persisted activation immediately.
	ref, captureRef := p.accept(t)
	eval, e := p.admin.Get(testCtx, "evaluations", p.in.Scope, ref.ID)
	if e != nil {
		t.Fatal(e)
	}
	state, e := p.admin.Get(testCtx, colJobs, p.in.Scope, activation.State.ID)
	if e != nil {
		t.Fatal(e)
	}
	act, e := p.admin.Get(testCtx, colJobs, p.in.Scope, activation.Event.ID)
	if e != nil {
		t.Fatal(e)
	}
	row, e := p.admin.Get(testCtx, colJobs, p.in.Scope, captureRef.ID)
	if e != nil {
		t.Fatal(e)
	}
	if state.NativeDynamics.State.LastModelNS != act.CreatedAt.UnixNano() || !state.NativeDynamics.State.ActivationAt.Equal(act.CreatedAt) || !row.NativeDynamics.SourceAcceptedAt.Equal(eval.CreatedAt) {
		t.Fatal("stored time precision mismatch")
	}
	if eval.CreatedAt.Nanosecond()%1000000 != 0 || act.CreatedAt.Nanosecond()%1000000 != 0 {
		t.Fatal("expected actual codec-ms")
	}
	t.Logf("actual stored external time ledger: activationNS=%d sourceAcceptedNS=%d captureNS=%d sourceMinusActivationNS=%d; no sleep/clamp", act.CreatedAt.UnixNano(), eval.CreatedAt.UnixNano(), row.NativeDynamics.SourceAcceptedAt.UnixNano(), eval.CreatedAt.UnixNano()-act.CreatedAt.UnixNano())
	p.process(t, captureRef)
}
func TestDynamicsStoreNativeAtomicApplyPG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "v1")
	p.in.Passed = false
	_, captureRef := p.accept(t)
	r := p.process(t, captureRef)
	if !r.AppliedNow || r.LearningConsumed || len(p.rows(t, "seen")) != 1 || len(p.rows(t, "consumption")) != 0 || len(p.rows(t, "event")) != 1 || len(p.rows(t, "checkpoint")) != 1 {
		t.Fatal("false observation ledger mismatch")
	}
	p.in.Passed = true
	p.in.EventID = "changed-verdict"
	_, captureRef = p.accept(t)
	if _, e := p.worker.ProcessNativeDynamicsCapture(testCtx, p.workerToken, p.in.Scope, captureRef); !errors.Is(e, ErrConflict) {
		t.Fatal("changed verdict admitted", e)
	}
	if len(p.rows(t, "consumption")) != 0 || len(p.rows(t, "event")) != 1 {
		t.Fatal("conflict partial state")
	}
}
func TestDynamicsStoreNativeConcurrentWritersPG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "v1")
	_, captureRef := p.accept(t)
	secondApp := restrictedPostgres(t, p.admin)
	secondRepo, e := secondApp.WithToken(p.workerToken)
	if e != nil {
		t.Fatal(e)
	}
	secondAuth, e := NewPostgresAuthorizer(secondApp)
	if e != nil {
		t.Fatal(e)
	}
	second := &Service{Repo: secondRepo, Auth: secondAuth, NativeDynamics: p.worker.NativeDynamics}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, worker := range []*Service{p.worker, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := worker.ProcessNativeDynamicsCapture(testCtx, p.workerToken, p.in.Scope, captureRef)
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil && !errors.Is(e, ErrConflict) && !errors.Is(e, ErrUnavailable) {
			t.Fatal("unexpected race result", e)
		}
	}
	// Retry the same durable capture after any serializable loser; never issue a new source event.
	p.process(t, captureRef)
	if len(p.rows(t, "event")) != 1 || len(p.rows(t, "consumption")) != 1 || len(p.rows(t, "seen")) != 1 || len(p.rows(t, "checkpoint")) != 1 {
		t.Fatal("duplicate workers duplicated ledger")
	}
}
func TestDynamicsStoreNativeCrashAndLostReplyPG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "v1")
	_, captureRef := p.accept(t)
	failure := errors.New("native synthetic commit failure")
	p.worker.Repo = nativeCommitFailureRepo{Repository: p.workerRepo, err: failure}
	r, e := p.worker.ProcessNativeDynamicsCapture(testCtx, p.workerToken, p.in.Scope, captureRef)
	if !errors.Is(e, failure) || !reflect.DeepEqual(r, NativeDynamicsReceipt{}) {
		t.Fatal("rollback result")
	}
	if len(p.rows(t, "event")) != 0 || len(p.rows(t, "consumption")) != 0 {
		t.Fatal("partial transaction persisted")
	}
	p.worker.Repo = nativeLostReplyRepo{Repository: p.workerRepo, err: failure}
	r, e = p.worker.ProcessNativeDynamicsCapture(testCtx, p.workerToken, p.in.Scope, captureRef)
	if !errors.Is(e, failure) || !reflect.DeepEqual(r, NativeDynamicsReceipt{}) {
		t.Fatal("lost reply exposed commit")
	}
	p.worker.Repo = p.workerRepo
	p.process(t, captureRef)
	if len(p.rows(t, "event")) != 1 || len(p.rows(t, "consumption")) != 1 {
		t.Fatal("lost reply duplicated")
	}
}
func TestDynamicsStoreNativeClosureAndDeletePG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "v1")
	_, captureRef := p.accept(t)
	p.process(t, captureRef)
	if e := p.workerRepo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		return invalidateNativeDynamicsDependencyTx(ctx, tx, p.in.Scope, p.in.Evidence[0], "source_quarantined")
	}); e != nil {
		t.Fatal("Tx-local cleanup", e)
	}
	nativeDrainPurge(t, p.worker, p.workerToken, p.in.Scope, Ref{Collection: colJobs, ID: nativePurgeID(p.in.Scope, p.in.Evidence[0]), Revision: 1})
	v := p.view(t, "v1")
	if v.Status != "invalidated" || v.Value != nil {
		t.Fatal("invalidated published")
	}
	for _, role := range []string{"capture", "event", "seen", "consumption", "checkpoint"} {
		for _, row := range p.rows(t, role) {
			n := row.NativeDynamics
			if n.Input != nil || n.ObservedPassed != nil || len(n.Dependencies) != 0 || len(n.DependencyKeys) != 0 || n.Key.Target.ID != "" || n.Evaluation.ID != "" {
				t.Fatal("native body/ref cleanup incomplete", role)
			}
		}
	}
}
func TestDynamicsStoreNativeRLSFourScopePG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "v1")
	_, captureRef := p.accept(t)
	p.process(t, captureRef)
	foreign := p.in.Scope
	foreign.UserID = "other-user"
	if _, e := p.workerRepo.Get(testCtx, colJobs, foreign, captureRef.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("SQL Scope4 widened", e)
	}
	foreign = p.in.Scope
	foreign.ProjectID = "other-project"
	if _, e := p.workerRepo.Get(testCtx, colJobs, foreign, captureRef.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("crossproject RLS widened", e)
	}
	// RLS grants are org/project, while native store SQL fixes user/agent explicitly.
	if e := p.workerRepo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		q := tx.(nativeDynamicsTxQueries)
		other := p.in.Scope
		other.AgentID = "other-agent"
		rows, e := q.NativeDynamicsRows(ctx, other, "capture", "", "", 64)
		if e != nil {
			return e
		}
		if len(rows) != 0 {
			return ErrConflict
		}
		return nil
	}); e != nil {
		t.Fatal("native exact-scope query", e)
	}
	denied := testPrincipalToken("native-denied-" + ID())
	if e := p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "native-denied", Role: roleService, Token: denied, OrganizationID: p.in.Scope.WorkspaceID, ProjectID: p.in.Scope.ProjectID, Actions: []string{"db:read", "db:write", "read:jobs"}}); e != nil {
		t.Fatal(e)
	}
	deniedRepo, e := p.app.WithToken(denied)
	if e != nil {
		t.Fatal(e)
	}
	svc := &Service{Repo: deniedRepo, Auth: p.worker.Auth, NativeDynamics: p.worker.NativeDynamics}
	if _, e := svc.ProcessNativeDynamicsCapture(testCtx, denied, p.in.Scope, captureRef); !errors.Is(e, ErrDenied) {
		t.Fatal("missing action granted", e)
	}
}
func TestDynamicsStoreNativeFreshUpgradePG(t *testing.T) {
	p := nativePGSetup(t)
	if e := p.admin.Init(testCtx); e != nil {
		t.Fatal("existing schema Init", e)
	}
	if len(p.rows(t, "state")) != 0 {
		t.Fatal("implicit state on old schema")
	}
	p.activate(t, "v1")
	_, captureRef := p.accept(t)
	p.process(t, captureRef)
	if e := p.admin.Init(testCtx); e != nil {
		t.Fatal(e)
	}
	if v := p.view(t, "v1"); v.Value == nil {
		t.Fatal("upgrade lost state")
	}
}

type nativeLostReplyRepo struct {
	Repository
	err error
}

func (r nativeLostReplyRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	if e := r.Repository.Transaction(ctx, fn); e != nil {
		return e
	}
	return r.err
}
