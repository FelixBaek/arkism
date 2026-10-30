package memory

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type sys04ReadPauseRepository struct {
	Repository
	store            restoreReviewStore
	entered, release chan struct{}
	once             sync.Once
}

func (p *sys04ReadPauseRepository) RequireRestoreReview(ctx context.Context, manifest string) error {
	return p.store.RequireRestoreReview(ctx, manifest)
}
func (p *sys04ReadPauseRepository) CheckRestorePublication(ctx context.Context) error {
	return p.store.CheckRestorePublication(ctx)
}
func (p *sys04ReadPauseRepository) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return p.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		p.once.Do(func() {
			close(p.entered)
			select {
			case <-p.release:
			case <-ctx.Done():
			}
		})
		return ctx.Err()
	})
}

func sys04ExpectWithheld(t *testing.T, p *nativePGFixture, version string, capture Ref) {
	t.Helper()
	view, err := p.worker.ReadNativeDynamicsSnapshot(testCtx, p.workerToken, NativeDynamicsQuery{p.in.Scope, p.in.Target, p.in.Dimension, version, time.Now().UnixNano()})
	if err == nil && view.Value != nil {
		t.Fatal("invalidated state remained readable")
	}
	receipt, err := p.worker.ProcessNativeDynamicsCapture(testCtx, p.workerToken, p.in.Scope, capture)
	if err == nil || receipt.AppliedNow || receipt.LearningConsumed {
		t.Fatal("old capture retry was accepted", err)
	}
}

func sys04WaitActualLock(t *testing.T, p *Postgres) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		err := p.q().QueryRow(testCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%memory_records%')`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("writer never reached an actual PostgreSQL lock wait")
}

func TestDynamicsLifecycleActualReaderRevisionRacePG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "revision_race")
	_, capture := p.accept(t)
	p.process(t, capture)
	wrapper := &sys04ReadPauseRepository{Repository: p.workerRepo, store: p.workerRepo, entered: make(chan struct{}), release: make(chan struct{})}
	reader := &Service{Repo: wrapper, Auth: p.worker.Auth, NativeDynamics: p.worker.NativeDynamics}
	readDone := make(chan error, 1)
	go func() {
		view, err := reader.ReadNativeDynamicsSnapshot(testCtx, p.workerToken, NativeDynamicsQuery{p.in.Scope, p.in.Target, p.in.Dimension, "revision_race", time.Now().UnixNano()})
		if err == nil && (view.Value == nil || *view.Value != 0.55) {
			err = errors.New("pre-revision read lost its value")
		}
		readDone <- err
	}()
	select {
	case <-wrapper.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not enter locked transaction")
	}
	// Always release the reader, including an assertion failure in the lock observer.
	defer func() {
		select {
		case <-wrapper.release:
		default:
			close(wrapper.release)
		}
	}()
	record, err := p.admin.Get(testCtx, colMemories, p.in.Scope, p.in.Target.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Revision++
	record.Summary = "synthetic confirmed correction"
	record.UpdatedAt = time.Now().UTC()
	writeDone := make(chan error, 1)
	go func() { writeDone <- p.admin.Replace(testCtx, colMemories, record, p.in.Target.Revision) }()
	sys04WaitActualLock(t, p.admin)
	close(wrapper.release)
	if err = <-readDone; err != nil {
		t.Fatal("pre-commit reader", err)
	}
	if err = <-writeDone; err != nil {
		t.Fatal("actual revision", err)
	}
	sys04ExpectWithheld(t, p, "revision_race", capture)
	if len(p.rows(t, "purge")) != 1 {
		t.Fatal("revision and purge fence did not commit together")
	}
	sys04AssertBoundedScrub(t, p, "revision_race", capture)
}

func TestDynamicsLifecycleRevisionFenceFailureRollsBackPG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "rollback")
	_, capture := p.accept(t)
	p.process(t, capture)
	_, err := p.admin.q().Exec(testCtx, `CREATE FUNCTION sys04_fail_purge() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='native_dynamics_purge' THEN RAISE EXCEPTION 'sys04 owned failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER sys04_fail_purge BEFORE INSERT ON memory_records FOR EACH ROW EXECUTE FUNCTION sys04_fail_purge()`)
	if err != nil {
		t.Fatal(err)
	}
	record, err := p.admin.Get(testCtx, colMemories, p.in.Scope, p.in.Target.ID)
	if err != nil {
		t.Fatal(err)
	}
	record.Revision++
	record.Summary = "must roll back"
	record.UpdatedAt = time.Now().UTC()
	err = p.admin.Replace(testCtx, colMemories, record, p.in.Target.Revision)
	if err == nil {
		t.Fatal("fault injection did not reject purge insert")
	}
	current, err := p.admin.Get(testCtx, colMemories, p.in.Scope, p.in.Target.ID)
	if err != nil || current.Revision != p.in.Target.Revision || current.Summary == record.Summary {
		t.Fatal("canonical replacement escaped failed fence transaction", err)
	}
	if len(p.rows(t, "purge")) != 0 {
		t.Fatal("failed transaction retained purge")
	}
	if view := p.view(t, "rollback"); view.Value == nil || *view.Value != 0.55 {
		t.Fatal("rollback damaged current native value")
	}
	_, err = p.admin.q().Exec(testCtx, `DROP TRIGGER sys04_fail_purge ON memory_records; DROP FUNCTION sys04_fail_purge()`)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.admin.Replace(testCtx, colMemories, record, p.in.Target.Revision); err != nil {
		t.Fatal("post-fault retry", err)
	}
	sys04ExpectWithheld(t, p, "rollback", capture)
}

func TestDynamicsLifecyclePendingAndConfirmedCorrectionPG(t *testing.T) {
	p := nativePGSetup(t)
	fixture := newFixture(t)
	fixture.svc.Repo = p.admin
	candidate := fixture.create(fixture.agent, "memory_candidates", generatedCandidate(p.in.Scope, refs(fixture.source())))
	marked, err := fixture.svc.Conflict(testCtx, fixture.human, ConflictInput{Scope: p.in.Scope, Memories: []Ref{p.in.Target, {Collection: "memory_candidates", ID: candidate.ID, Revision: candidate.Revision}}, Action: "mark", Reason: "pending synthetic correction", Hold: "second"})
	if err != nil {
		t.Fatal("actual pending correction", err)
	}
	var old Record
	for _, r := range marked.Memories {
		if r.ID == p.in.Target.ID {
			old = r
		} else {
			candidate = r
		}
	}
	if old.Status != "active" || candidate.Status != "conflicted" {
		t.Fatal("pending correction changed canonical status")
	}
	p.in.Target = Ref{Collection: colMemories, ID: old.ID, Revision: old.Revision}
	fixture.auth.actors["sys04-producer"] = actor("sys04-producer", roleService, true, allVerbs...)
	raw, err := json.Marshal(map[string]any{"retrieved": []Ref{p.in.Target}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	trace, err := fixture.svc.IngestCompletedTrace(testCtx, "sys04-producer", TraceIngestInput{Scope: p.in.Scope, TraceID: "pending-correction-trace", ThreadID: "pending-correction-thread", AgentRun: CompletedAgentRunInput{AgentID: "pending-correction-agent", TaskType: "agent_session", Outcome: "partial", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "synthetic pending correction"}, Events: []TraceEventInput{{ID: "pending-correction-event", Kind: "context", OccurredAt: start.Add(time.Second), Payload: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	p.in.Trace = trace.Trace
	p.in.Evidence = trace.SourceEvents
	p.activate(t, "correction")
	_, capture := p.accept(t)
	p.process(t, capture)
	if view := p.view(t, "correction"); view.Value == nil || *view.Value != 0.55 {
		t.Fatal("pending candidate incorrectly rejected current canonical memory")
	}
	replacement, err := fixture.svc.ReviewCandidate(testCtx, fixture.human, candidate.ID, CandidateReviewInput{Scope: p.in.Scope, ExpectedRevision: candidate.Revision, Action: "approve", OperationID: "sys04-confirm-replacement", Conflict: &CandidateReviewConflict{ID: marked.Relation.ID, ExpectedRevision: marked.Relation.Revision, Memory: p.in.Target, Resolution: "replace_old", Reason: "human confirmed correction"}})
	if err != nil {
		t.Fatal("actual confirmed correction", err)
	}
	current, err := p.admin.Get(testCtx, colMemories, p.in.Scope, old.ID)
	if err != nil || current.Status != "superseded" || current.SupersededBy == nil || current.SupersededBy.ID != replacement.ID {
		t.Fatal("correction did not supersede canonical old memory", err)
	}
	sys04ExpectWithheld(t, p, "correction", capture)
	view, err := p.worker.ReadNativeDynamicsSnapshot(testCtx, p.workerToken, NativeDynamicsQuery{p.in.Scope, Ref{Collection: colMemories, ID: replacement.ID, Revision: replacement.Revision}, p.in.Dimension, "correction", time.Now().UnixNano()})
	if err == nil && view.Value != nil {
		t.Fatal("replacement inherited old native value")
	}
}

func TestDynamicsLifecycleActualReaderDeleteRacePG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "deletion_race")
	_, capture := p.accept(t)
	p.process(t, capture)
	wrapper := &sys04ReadPauseRepository{Repository: p.workerRepo, store: p.workerRepo, entered: make(chan struct{}), release: make(chan struct{})}
	reader := &Service{Repo: wrapper, Auth: p.worker.Auth, NativeDynamics: p.worker.NativeDynamics}
	readDone := make(chan error, 1)
	go func() {
		view, err := reader.ReadNativeDynamicsSnapshot(testCtx, p.workerToken, NativeDynamicsQuery{p.in.Scope, p.in.Target, p.in.Dimension, "deletion_race", time.Now().UnixNano()})
		if err == nil && (view.Value == nil || *view.Value != 0.55) {
			err = errors.New("pre-revision read lost its value")
		}
		readDone <- err
	}()
	select {
	case <-wrapper.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not enter locked transaction")
	}
	// Always release the reader, including an assertion failure in the lock observer.
	defer func() {
		select {
		case <-wrapper.release:
		default:
			close(wrapper.release)
		}
	}()
	fixture := newFixture(t)
	fixture.svc.Repo = p.admin
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- fixture.svc.Delete(testCtx, fixture.human, colMemories, p.in.Target.ID, ChangeInput{Scope: p.in.Scope, ExpectedRevision: p.in.Target.Revision})
	}()
	var err error
	sys04WaitActualLock(t, p.admin)
	close(wrapper.release)
	if err = <-readDone; err != nil {
		t.Fatal("pre-commit reader", err)
	}
	if err = <-writeDone; err != nil {
		t.Fatal("actual revision", err)
	}
	sys04ExpectWithheld(t, p, "deletion_race", capture)
	if len(p.rows(t, "purge")) != 1 {
		t.Fatal("revision and purge fence did not commit together")
	}
	sys04AssertBoundedScrub(t, p, "deletion_race", capture)
}

func sys04ScopedNativeFixture(t *testing.T, base *nativePGFixture, scope Scope, label string) (*nativePGFixture, []Record) {
	t.Helper()
	fixture := newFixture(t)
	fixture.svc.Repo = base.admin
	sources := []Record{fixture.create(fixture.human, "sources", sourceInput(CreateInput{Scope: scope, Text: "same synthetic source body"})), fixture.create(fixture.human, "sources", sourceInput(CreateInput{Scope: scope, Text: "second same synthetic source body"}))}
	refs := append(refs(sources[0]), refs(sources[1])...)
	candidate := fixture.create(fixture.human, "memory_candidates", candidateInput(CreateInput{Scope: scope, Text: "identical memory body across scopes", Sources: refs}))
	memory, err := fixture.svc.ReviewCandidate(testCtx, fixture.human, candidate.ID, CandidateReviewInput{Scope: scope, ExpectedRevision: candidate.Revision, Action: "approve", OperationID: "review-" + label})
	if err != nil {
		t.Fatal(err)
	}
	target := Ref{Collection: colMemories, ID: memory.ID, Revision: memory.Revision}
	fixture.auth.actors["scope-producer"] = actor("scope-producer", roleService, true, allVerbs...)
	raw, err := json.Marshal(map[string]any{"retrieved": []Ref{target}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	traceAgent := scope.AgentID
	if traceAgent == "" {
		traceAgent = "agent-" + label
	}
	trace, err := fixture.svc.IngestCompletedTrace(testCtx, "scope-producer", TraceIngestInput{Scope: scope, TraceID: "trace-" + label, ThreadID: "thread-" + label, AgentRun: CompletedAgentRunInput{AgentID: traceAgent, TaskType: "agent_session", Outcome: "partial", StartedAt: start, FinishedAt: start.Add(time.Second), Text: "same synthetic trace body"}, Events: []TraceEventInput{{ID: "event-" + label, Kind: "context", OccurredAt: start.Add(time.Second), Payload: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	out := *base
	out.in = MemoryOutcomeInput{Scope: scope, Target: target, Trace: trace.Trace, Evidence: trace.SourceEvents, EventID: "outcome-" + label, CheckerSHA256: base.in.CheckerSHA256, Dimension: base.in.Dimension, Passed: true}
	return &out, sources
}
func TestDynamicsLifecycleMultipleSourcesAndSameBodyScopeIsolationPG(t *testing.T) {
	for _, changedSource := range []int{0, 1} {
		t.Run([]string{"first_source", "second_source"}[changedSource], func(t *testing.T) {
			base := nativePGSetup(t)
			a, sources := sys04ScopedNativeFixture(t, base, scopeA, "scope-a")
			scopeB := scopeA
			scopeB.UserID = "sys04-other-user"
			scopeB.AgentID = "sys04-other-agent"
			b, _ := sys04ScopedNativeFixture(t, base, scopeB, "scope-b")
			a.activate(t, "multi")
			_, captureA := a.accept(t)
			a.process(t, captureA)
			b.activate(t, "multi")
			_, captureB := b.accept(t)
			b.process(t, captureB)
			before := b.view(t, "multi")
			if before.Value == nil || *before.Value != 0.55 {
				t.Fatal("other scope precondition")
			}
			old := sources[changedSource]
			updated := old
			updated.Revision++
			updated.Summary = "actual source revision"
			updated.UpdatedAt = time.Now().UTC()
			if err := a.admin.Replace(testCtx, "sources", updated, old.Revision); err != nil {
				t.Fatal("actual independent source revision", err)
			}
			sys04ExpectWithheld(t, a, "multi", captureA)
			if after := b.view(t, "multi"); after.Value == nil || *after.Value != *before.Value || after.Status != before.Status {
				t.Fatal("same body in another scope was interfered with")
			}
			replay, err := b.worker.ProcessNativeDynamicsCapture(testCtx, b.workerToken, b.in.Scope, captureB)
			if err != nil || replay.AppliedNow {
				t.Fatal("unrelated scope idempotency damaged", err)
			}
			sys04DrainNativePurges(t, a)
			if after := b.view(t, "multi"); after.Value == nil || *after.Value != 0.55 {
				t.Fatal("bounded cleanup crossed scope")
			}
		})
	}
}

func sys04AssertBoundedScrub(t *testing.T, p *nativePGFixture, version string, capture Ref) {
	t.Helper()
	// Read withholding precedes bounded cleanup: the raw old state still exists.
	states := p.rows(t, "state")
	if len(states) != 1 || states[0].NativeDynamics.State == nil || states[0].NativeDynamics.State.Status != "active" {
		t.Fatal("test did not distinguish immediate withholding from later scrub")
	}
	purge := p.rows(t, "purge")[0]
	var err error
	done := false
	for n := 0; n < 100 && !done; n++ {
		done, err = p.worker.ProcessNativeDynamicsPurge(testCtx, p.workerToken, p.in.Scope, Ref{Collection: colJobs, ID: purge.ID, Revision: purge.Revision}, 1)
		if err != nil {
			t.Fatal("bounded cleanup", err)
		}
	}
	if !done {
		t.Fatal("bounded cleanup did not finish")
	}
	sys04ExpectWithheld(t, p, version, capture)
	for _, role := range nativePurgeRoles {
		for _, r := range p.rows(t, role) {
			if len(r.NativeDynamics.Dependencies) > 0 || r.NativeDynamics.Input != nil {
				t.Fatalf("%s retained erased dependency", role)
			}
		}
	}
}
