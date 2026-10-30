package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
	"github.com/FelixBaek/arkism/internal/memory"
)

func TestDaemonDynamicsOptionsRemainOffAndRejectOtherModes(t *testing.T) {
	if _, err := prepareDaemonDynamics(daemonOptions{}); err != nil {
		t.Fatal(err)
	}
	load, c := daemonDynamicsFixture(t)
	r, l := daemonDynamicsFiles(t, load, c)
	for name, cfg := range map[string]daemonOptions{
		"partial_runtime": {DynamicsRuntimeConfig: r}, "partial_load": {DynamicsLoadConfig: l},
		"recovery":               {DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, Recovery: true},
		"admin":                  {DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, CollectBlobs: true},
		"init":                   {DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, InitDB: true},
		"reindex":                {DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, Reindex: true},
		"recall":                 {DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, RecallBaseline: "self"},
		"repair_without_configs": {DynamicsRepair: true},
		"repair_partial":         {DynamicsRuntimeConfig: r, DynamicsRepair: true},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareDaemonDynamics(cfg); !errors.Is(err, memory.ErrInvalid) {
				t.Fatal("unsupported combination", err)
			}
		})
	}
}

func TestDaemonDynamicsEntryRejectsBeforeDatabase(t *testing.T) {
	addr, dials := fakeDatabase(t)
	load, c := daemonDynamicsFixture(t)
	r, l := daemonDynamicsFiles(t, load, c)
	env := map[string]string{"MEMORY_DATABASE_URL": "postgres://arkism@" + addr.String() + "/arkism_none?sslmode=disable&connect_timeout=2"}
	cases := [][]string{{"-dynamics-runtime-config", r}, {"-dynamics-load-config", l},
		{"-dynamics-runtime-config", r, "-dynamics-load-config", l},
		{"-dynamics-runtime-config", r, "-dynamics-load-config", l, "-worker"},
		{"-dynamics-runtime-config", r, "-dynamics-load-config", l, "-recovery"},
		{"-dynamics-runtime-config", r, "-dynamics-load-config", l, "-init-db"},
		{"-dynamics-runtime-config", "absent", "-dynamics-load-config", l},
	}
	for i, args := range cases {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			if _, code := runDaemon(t, env, args...); code != 1 {
				t.Fatal("invalid startup", code)
			}
			if dials.Load() != 0 {
				t.Fatal("database reached before config/token rejection")
			}
		})
	}
}

type daemonDynamicsAuth struct {
	role    string
	allowed bool
	tokens  []string
}

func (a *daemonDynamicsAuth) Authorize(_ context.Context, token string, in memory.Authorization) (memory.Decision, error) {
	a.tokens = append(a.tokens, token)
	if in.Action != "read:jobs" {
		return memory.Decision{}, memory.ErrDenied
	}
	return memory.Decision{Allowed: a.allowed, Role: a.role, Scope: in.Scope}, nil
}
func TestDaemonDynamicsFrozenPlanAndReaderAuthority(t *testing.T) {
	load, c := daemonDynamicsFixture(t)
	r, l := daemonDynamicsFiles(t, load, c)
	cfg := daemonOptions{DynamicsRuntimeConfig: r, DynamicsLoadConfig: l}
	t.Setenv("MEMORY_DYNAMICS_READER_TOKEN", "explicit-reader")
	cfg, err := prepareDaemonDynamics(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if os.WriteFile(r, []byte("null"), 0600) != nil || os.WriteFile(l, []byte("null"), 0600) != nil {
		t.Fatal("fixture overwrite")
	}
	auth := &daemonDynamicsAuth{role: "service", allowed: true}
	svc := &memory.Service{Auth: auth}
	if err = configureDaemonDynamics(context.Background(), svc, cfg); err != nil {
		t.Fatal("reread mutable files", err)
	}
	if svc.NativeDynamics == nil || !svc.NativeDynamics.Enabled || svc.DynamicsLoad == nil || svc.NativeSearchShadow != nil {
		t.Fatal("wrong enabled features")
	}
	if len(auth.tokens) != 1 || auth.tokens[0] != "explicit-reader" {
		t.Fatal("reader token fallback")
	}
	for _, bad := range []*daemonDynamicsAuth{{role: "human", allowed: true}, {role: "service", allowed: false}} {
		if err = configureDaemonDynamics(context.Background(), &memory.Service{Auth: bad}, cfg); !errors.Is(err, memory.ErrDenied) {
			t.Fatal("invalid reader role/grant", err)
		}
	}
	off := &memory.Service{}
	if configureDaemonDynamics(context.Background(), off, daemonOptions{}) != nil || off.NativeDynamics != nil || off.DynamicsLoad != nil {
		t.Fatal("off enabled runtime")
	}
	cfg.Worker = true
	cfg.dynamicsReaderToken = ""
	worker := &memory.Service{}
	if err = configureDaemonDynamics(context.Background(), worker, cfg); err != nil || worker.DynamicsLoad == svc.DynamicsLoad {
		t.Fatal("API and worker share process cache", err)
	}
}

type daemonDynamicsRefreshFixture struct {
	mu    sync.Mutex
	calls []memory.Scope
	token string
}

func (f *daemonDynamicsRefreshFixture) RefreshDynamicsSnapshot(ctx context.Context, token string, scope memory.Scope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, scope)
	f.token = token
	return ctx.Err()
}
func TestDaemonDynamicsRoundUsesExplicitScopesAndCancelledDeadline(t *testing.T) {
	_, c := daemonDynamicsFixture(t)
	c.Scopes = append(c.Scopes, memory.Scope{WorkspaceID: "fixture", ProjectID: "second"})
	p := &daemonDynamicsPlan{config: c}
	f := &daemonDynamicsRefreshFixture{}
	daemonDynamicsRefreshRound(context.Background(), f, "reader", p, 0)
	if len(f.calls) != 2 || f.calls[0] != c.Scopes[0] || f.calls[1] != c.Scopes[1] || f.token != "reader" {
		t.Fatal("unbounded/mismatched scope refresh")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	daemonDynamicsRefreshRound(ctx, f, "reader", p, 0)
	if len(f.calls) != 2 {
		t.Fatal("refresh after cancellation")
	}
}

func TestDaemonDynamicsBackgroundWaitsForStartAndJoins(t *testing.T) {
	entered, leaving, allowFinish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enterOnce sync.Once
	b := daemonDynamicsBackground(context.Background(), nil, time.Hour, func(ctx context.Context) {
		enterOnce.Do(func() { close(entered) })
		<-ctx.Done()
		close(leaving)
		<-allowFinish
	})
	select {
	case <-entered:
		t.Fatal("refresh before server/control startup")
	case <-time.After(time.Millisecond):
	}
	b.start()
	<-entered
	stopped := make(chan error, 1)
	go func() { stopped <- b.stop(context.Background()) }()
	<-leaving
	select {
	case <-stopped:
		t.Fatal("stop returned before join")
	default:
	}
	close(allowFinish)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if err := b.stop(context.Background()); err != nil {
		t.Fatal("repeat stop", err)
	}
}

func TestDaemonDynamicsControlPauseStopsRefreshAndStopReceiptWaitsJoin(t *testing.T) {
	f := newMemoryControlFixture(t, "server")
	var calls atomic.Int64
	b := daemonDynamicsBackground(context.Background(), f.control.Admission(), time.Millisecond, func(context.Context) { calls.Add(1) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	joined, allowStop := make(chan struct{}), make(chan struct{})
	originalStop := b.stopWork
	var joinOnce sync.Once
	b.stopWork = func(ctx context.Context) error {
		if err := originalStop(ctx); err != nil {
			return err
		}
		joinOnce.Do(func() { close(joined) })
		select {
		case <-allowStop:
			return nil
		case <-ctx.Done():
			return desktopcontrol.ErrTimeout
		}
	}
	var stopOnce sync.Once
	t.Cleanup(func() { stopOnce.Do(func() { close(allowStop) }) })
	done := make(chan error, 1)
	go func() {
		done <- serveMemoryServer(context.Background(), &http.Server{Handler: http.NotFoundHandler()}, ln, f.control, b)
	}()
	f.receive("ready")
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("refresh did not run")
		}
		time.Sleep(time.Millisecond)
	}
	lease := daemonDynamicsPause(t, f, 1)
	before := calls.Load()
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != before {
		t.Fatal("refresh DB work after idle pause")
	}
	f.request(lease+1, "stop", lease)
	<-joined
	if err := f.parent.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	err = f.decoder.Decode(&raw)
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatal("stop receipt arrived before background join", err, raw)
	}
	if err = f.parent.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	stopOnce.Do(func() { close(allowStop) })
	// A Decoder may retain its timeout; use a fresh one over the same stream.
	f.decoder = json.NewDecoder(f.parent)
	f.receive("stopped")
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func daemonDynamicsPause(t *testing.T, f *memoryControlFixture, id uint64) uint64 {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		f.request(id, "pause", 0)
		var row struct{ State string }
		if err := f.decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		if row.State == "idle" {
			return id
		}
		if row.State != "busy" || time.Now().After(deadline) {
			t.Fatal("pause did not reach idle", row)
		}
		id++
		time.Sleep(time.Millisecond)
	}
}

// This uses only the explicitly supplied disposable test cluster. API and worker
// hold distinct runtimes, while both connect through a genuine restricted LOGIN.
func TestDaemonDynamicsSeparateAPIWorkerCachesPG(t *testing.T) {
	db := newTestDB(t, true)
	app := daemonDynamicsLogin(t, db)
	ctx := context.Background()
	auth, err := memory.NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	human, reader := daemonDynamicsPrincipals(t, db)
	workerRepo, err := app.WithToken(db.token)
	if err != nil {
		t.Fatal(err)
	}
	worker := &memory.Service{Repo: workerRepo, Auth: auth}
	api := &memory.Service{Repo: app, Auth: auth}
	load, c := daemonDynamicsFixture(t)
	c.Scopes = []memory.Scope{{WorkspaceID: "default", ProjectID: "default"}}
	r, l := daemonDynamicsFiles(t, load, c)
	t.Setenv("MEMORY_WORKER_TOKEN", db.token)
	t.Setenv("MEMORY_DYNAMICS_READER_TOKEN", reader)
	apiCfg, err := prepareDaemonDynamics(daemonOptions{DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, Listen: freeAddress(t)})
	if err != nil {
		t.Fatal(err)
	}
	workerCfg := apiCfg
	workerCfg.Worker = true
	if err = configureDaemonDynamics(ctx, worker, workerCfg); err != nil {
		t.Fatal(err)
	}
	if err = configureDaemonDynamics(ctx, api, apiCfg); err != nil {
		t.Fatal(err)
	}
	in := daemonDynamicsCanonicalSource(t, app, auth, human, c.Scopes[0])
	if _, err = worker.ActivateNativeMemoryDynamics(ctx, db.token, memory.NativeDynamicsActivationInput{Scope: in.Scope, Target: in.Target, Dimension: in.Dimension, Config: daemonDynamicsNativeConfig(), RequestID: "daemon-activate"}); err != nil {
		t.Fatal(err)
	}
	ref, err := worker.RecordMemoryOutcome(ctx, db.token, in)
	if err != nil || ref.ID == "" {
		t.Fatal("durable outcome", err)
	}
	if _, err = api.DynamicsLoad.DynamicsSnapshot(in.Scope, time.Now().UnixNano()); !errors.Is(err, memory.ErrDynamicsSnapshotUnavailable) {
		t.Fatal("API unexpectedly warm before refresh", err)
	}
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	workerDone, apiDone := make(chan error, 1), make(chan error, 1)
	go func() { workerDone <- serveDaemonWorker(run, worker, workerCfg, nil) }()
	go func() { apiDone <- serveDaemonHTTP(run, api, apiCfg, nil) }()
	t.Cleanup(func() { cancel(); daemonDynamicsWaitStop(t, workerDone); daemonDynamicsWaitStop(t, apiDone) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		snapshot, e := api.DynamicsLoad.DynamicsSnapshot(in.Scope, time.Now().UnixNano())
		if e == nil && snapshot.Head == 1 && snapshot.Generation > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("separate readonly API refresh never caught worker publication", e)
		}
		time.Sleep(time.Millisecond)
	}
	if api.DynamicsLoad == worker.DynamicsLoad || api.NativeSearchShadow != nil {
		t.Fatal("cache sharing or implicit native-search activation")
	}
	daemonDynamicsActualSearch(t, apiCfg.Listen, db.token, in.Scope, in.Target.ID)
	// A reader cannot publish a durable generation; the daemon never lends the worker token.
	if err = api.PublishDynamicsSnapshot(ctx, reader, in.Scope); !errors.Is(err, memory.ErrDenied) {
		t.Fatal("readonly reader published", err)
	}
}

func daemonDynamicsWaitStop(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Error("daemon fixture stop", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("daemon fixture did not join")
	}
}

func daemonDynamicsNativeConfig() memory.NativeDynamicsConfig {
	initial, eta, unit, strength := 0.5, 0.2, 1.0, 0.5
	tau := int64(1000000000000)
	decay, positive := false, true
	history, deps := int64(64), int64(500)
	return memory.NativeDynamicsConfig{Version: "daemon_fixture_v1", InitialValue: &initial, TauNS: &tau, EtaPositive: &eta, UnitStrength: &unit, ObservationStrength: &strength, DecayEnabled: &decay, PositiveLearning: &positive, MaxHistory: &history, MaxDependencies: &deps}
}

func daemonDynamicsPrincipals(t *testing.T, db testDB) (string, string) {
	t.Helper()
	ctx := context.Background()
	admin, err := memory.NewPostgres(ctx, db.url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	human, err := memory.NewPrincipalToken()
	if err != nil {
		t.Fatal(err)
	}
	reader, err := memory.NewPrincipalToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range []memory.PrincipalGrant{
		{PrincipalID: "daemon-fixture-human", Role: "human", Token: human, OrganizationID: "default", ProjectID: "default", Actions: []string{"*"}},
		{PrincipalID: "daemon-fixture-reader", Role: "service", Token: reader, OrganizationID: "default", ProjectID: "default", Actions: []string{"db:read", "read:jobs", "read:memories", "read:sources", "read:evaluations", "read:execution_traces", "read:agent_runs"}},
	} {
		if err = admin.ProvisionPrincipal(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	return human, reader
}

func daemonDynamicsCanonicalSource(t *testing.T, app *memory.Postgres, auth memory.Authorizer, human string, scope memory.Scope) memory.MemoryOutcomeInput {
	t.Helper()
	ctx := context.Background()
	repo, err := app.WithToken(human)
	if err != nil {
		t.Fatal(err)
	}
	svc := &memory.Service{Repo: repo, Auth: auth}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	source, err := svc.Create(ctx, human, "sources", memory.CreateInput{Scope: scope, Kind: "document", Text: "synthetic daemon source", OriginProduct: "brain", SourceRef: "fixture/daemon", CollectedAt: &at})
	if err != nil {
		t.Fatal("create source", err)
	}
	candidate, err := svc.Create(ctx, human, "memory_candidates", memory.CreateInput{Scope: scope, Kind: "semantic", Text: "daemon fixture memory searchcanary", Keywords: []string{"searchcanary"}, Sources: []memory.SourceRef{{SourceID: source.ID, Locator: "fixture", Span: &memory.SourceSpan{ContentHash: memory.ContentHash(source.Text), End: len(source.Text)}}}})
	if err != nil {
		t.Fatal("create candidate", err)
	}
	target, err := svc.ReviewCandidate(ctx, human, candidate.ID, memory.CandidateReviewInput{Scope: scope, ExpectedRevision: candidate.Revision, Action: "approve"})
	if err != nil {
		t.Fatal("approve candidate", err)
	}
	targetRef := memory.Ref{Collection: "memories", ID: target.ID, Revision: target.Revision}
	payload, err := json.Marshal(map[string]any{"retrieved": []memory.Ref{targetRef}})
	if err != nil {
		t.Fatal(err)
	}
	trace, err := svc.IngestCompletedTrace(ctx, human, memory.TraceIngestInput{Scope: scope, TraceID: "daemon-fixture-trace", ThreadID: "daemon-fixture-thread", AgentRun: memory.CompletedAgentRunInput{AgentID: "fixture-agent", TaskType: "agent_session", Outcome: "partial", StartedAt: at, FinishedAt: at.Add(time.Second), Text: "synthetic daemon completed source"}, Events: []memory.TraceEventInput{{ID: "daemon-fixture-event", Kind: "context", OccurredAt: at.Add(time.Second), Payload: payload}}})
	if err != nil {
		t.Fatal("canonical trace", err)
	}
	return memory.MemoryOutcomeInput{Scope: scope, Target: targetRef, Trace: trace.Trace, Evidence: trace.SourceEvents, EventID: "daemon-fixture-outcome", CheckerSHA256: strings.Repeat("a", 64), Dimension: "work_task", Passed: true}
}

func daemonDynamicsActualSearch(t *testing.T, address, token string, scope memory.Scope, want string) {
	t.Helper()
	body, err := json.Marshal(memory.SearchInput{Scope: scope, Query: "searchcanary", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+address+"/v1/search", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	// The first search on a cold race runner can pass one second; this checks the result, not latency.
	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !bytes.Contains(raw, []byte(want)) {
		t.Fatal("actual baseline HTTP search did not retrieve fixture", response.StatusCode, string(raw))
	}
}

func daemonDynamicsLogin(t *testing.T, db testDB) *memory.Postgres {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, db.url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	role := "arkism_daemon_login_" + memory.ID()
	quoted := pgx.Identifier{role}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE ROLE "+quoted+" LOGIN NOINHERIT NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE NOREPLICATION"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, e := admin.Exec(context.Background(), "DROP OWNED BY "+quoted+"; DROP ROLE "+quoted); e != nil {
			t.Error(e)
		}
	})
	grants := "GRANT USAGE ON SCHEMA arkism,extensions TO " + quoted + `; GRANT SELECT ON arkism.memory_storage_metadata TO ` + quoted + `; GRANT SELECT,INSERT,UPDATE,DELETE ON arkism.memory_records,arkism.execution_traces,arkism.source_events,arkism.entities,arkism.relations TO ` + quoted + `; GRANT EXECUTE ON FUNCTION arkism.current_principal(),arkism.has_permission(text,text,text),arkism.has_any_permission(text),arkism.permitted_scopes(text),arkism.authorize_principal(text,text,text),arkism.reserve_candidate_quota(text,text,date,integer),arkism.artifact_gc_held(text),arkism.artifact_cleanup_pending(text,text,text,text,text,text) TO ` + quoted
	if _, err = admin.Exec(ctx, grants); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(db.url)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.User(role)
	login, err := pgx.Connect(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	var current, session string
	var inherit, super, bypass bool
	err = login.QueryRow(ctx, "SELECT current_user,session_user,rolinherit,rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user").Scan(&current, &session, &inherit, &super, &bypass)
	_ = login.Close(ctx)
	if err != nil || current != role || session != role || inherit || super || bypass {
		t.Fatal("not genuine restricted LOGIN", err)
	}
	app, err := memory.NewPostgres(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return app
}

type daemonDynamicsDeadlineRepo struct {
	memory.Repository
	deadline time.Time
}

func (r *daemonDynamicsDeadlineRepo) ClaimJob(ctx context.Context, _ time.Time, _ time.Duration) (memory.Record, error) {
	r.deadline, _ = ctx.Deadline()
	return memory.Record{}, memory.ErrNotFound
}
func TestDaemonDynamicsWorkerPreservesOriginalRoundDeadline(t *testing.T) {
	r := &daemonDynamicsDeadlineRepo{}
	w := daemonWorkerStep{service: &memory.Service{Repo: r}, token: "inert-worker"}
	w.run(context.Background())
	if remaining := time.Until(r.deadline); remaining < time.Minute || remaining > 90*time.Second {
		t.Fatal("original privacy/embedding round deadline changed", remaining)
	}
}

type daemonDynamicsSlowScope struct {
	slow      memory.Scope
	fastCalls int
}

func (f *daemonDynamicsSlowScope) RefreshDynamicsSnapshot(ctx context.Context, _ string, scope memory.Scope) error {
	if scope == f.slow {
		<-ctx.Done()
		return ctx.Err()
	}
	f.fastCalls++
	return nil
}
func TestDaemonDynamicsRefreshRotatesAfterSlowScopeDeadline(t *testing.T) {
	_, c := daemonDynamicsFixture(t)
	c.Scopes = append(c.Scopes, memory.Scope{WorkspaceID: "fixture", ProjectID: "second"})
	c.Limits.WorkerSubstepTimeoutNS = int64(time.Millisecond)
	f := &daemonDynamicsSlowScope{slow: c.Scopes[0]}
	step := daemonDynamicsRefreshStep(f, "reader", &daemonDynamicsPlan{config: c})
	step(context.Background())
	if f.fastCalls != 0 {
		t.Fatal("first whole-round timeout was not observed")
	}
	step(context.Background())
	if f.fastCalls != 1 {
		t.Fatal("slow first scope starved second scope")
	}
}
func TestDaemonDynamicsNormalCancellationPreservesStopFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var first atomic.Bool
	b := &memoryServerBackground{startWork: cancel, stopWork: func(context.Context) error {
		if first.CompareAndSwap(false, true) {
			return desktopcontrol.ErrTimeout
		}
		return nil
	}}
	err = serveMemoryServer(ctx, &http.Server{Handler: http.NotFoundHandler()}, ln, nil, b)
	if !errors.Is(err, desktopcontrol.ErrTimeout) {
		t.Fatal("background join failure discarded on signal/normal shutdown", err)
	}
	if err = b.stop(context.Background()); !errors.Is(err, desktopcontrol.ErrTimeout) {
		t.Fatal("earlier stop failure forgotten", err)
	}
}
func TestDaemonDynamicsServeFailurePreservesStopFailure(t *testing.T) {
	ln := &memoryIdleListener{closed: make(chan struct{}), accepted: make(chan struct{})}
	b := &memoryServerBackground{startWork: func() { _ = ln.Close() }, stopWork: func(context.Context) error { return desktopcontrol.ErrTimeout }}
	err := serveMemoryServer(context.Background(), &http.Server{Handler: http.NotFoundHandler()}, ln, nil, b)
	if !errors.Is(err, desktopcontrol.ErrTimeout) {
		t.Fatal("background join failure discarded on Serve error", err)
	}
}

type fakeDynamicsRepairer struct {
	results map[memory.Scope]error
	tokens  []string
}

func (f *fakeDynamicsRepairer) RepairDynamicsPublication(_ context.Context, token string, scope memory.Scope) error {
	f.tokens = append(f.tokens, token)
	return f.results[scope]
}

func TestDaemonDynamicsRepairReportsEveryScope(t *testing.T) {
	load, c := daemonDynamicsFixture(t)
	c.Scopes = append(c.Scopes, memory.Scope{WorkspaceID: "fixture", ProjectID: "second"})
	r, l := daemonDynamicsFiles(t, load, c)
	t.Setenv("MEMORY_WORKER_TOKEN", "")
	if _, err := prepareDaemonDynamics(daemonOptions{DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, DynamicsRepair: true}); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("repair without the worker token", err)
	}
	t.Setenv("MEMORY_WORKER_TOKEN", "worker")
	t.Setenv("MEMORY_DYNAMICS_READER_TOKEN", "")
	cfg, err := prepareDaemonDynamics(daemonOptions{DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, DynamicsRepair: true})
	if err != nil || len(cfg.dynamicsPlan.config.Scopes) != 2 {
		t.Fatal("repair needs the worker token, not a reader", err)
	}
	a, b := cfg.dynamicsPlan.config.Scopes[0], cfg.dynamicsPlan.config.Scopes[1]
	fake := &fakeDynamicsRepairer{results: map[memory.Scope]error{b: memory.ErrConflict}}
	var out bytes.Buffer
	if err = daemonDynamicsRepairRound(context.Background(), fake, "worker", cfg.dynamicsPlan, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"released":true`) || !strings.Contains(lines[1], `"released":false`) || len(fake.tokens) != 2 || fake.tokens[0] != "worker" {
		t.Fatalf("repair report %q %v", out.String(), fake.tokens)
	}
	fake = &fakeDynamicsRepairer{results: map[memory.Scope]error{a: memory.ErrDenied}}
	out.Reset()
	if err = daemonDynamicsRepairRound(context.Background(), fake, "worker", cfg.dynamicsPlan, &out); !errors.Is(err, memory.ErrDenied) || out.Len() != 0 || len(fake.tokens) != 1 {
		t.Fatal("a denied repair must stop before reporting", err, out.String())
	}
}
