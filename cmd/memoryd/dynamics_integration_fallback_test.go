package main

import (
	"bytes"
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/FelixBaek/arkism/internal/memory"
)

// daemonFallbackRig is one disposable database with a worker and two shadow
// HTTP APIs: "api" with the normal deadline, "tight" with a 1ns deadline.
type daemonFallbackRig struct {
	db            testDB
	scope         memory.Scope
	target        string
	worker        *memory.Service
	api, tight    *memory.Service
	apiAddr       string
	tightAddr     string
	haltWorker    func()
	startWorkerFn func()
}

func newDaemonFallbackRig(t *testing.T) *daemonFallbackRig {
	t.Helper()
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
	r := &daemonFallbackRig{db: db, worker: &memory.Service{Repo: workerRepo, Auth: auth}, api: &memory.Service{Repo: app, Auth: auth}, tight: &memory.Service{Repo: app, Auth: auth}}
	load, c := daemonDynamicsFixture(t)
	c.Scopes = []memory.Scope{{WorkspaceID: "default", ProjectID: "default"}}
	// The worker is halted after one publication, so the cells run on that snapshot; a 1s freshness window
	// expired before the later cells on slow runners and turned them into snapshot_unavailable.
	load = bytes.Replace(load, []byte(`"allowed_freshness_ns":1000000000`), []byte(`"allowed_freshness_ns":60000000000`), 1)
	c.Limits.AllowedFreshnessNS, c.LoadConfigSHA256 = 60_000_000_000, memory.ContentHash(string(load))
	rc, lc := daemonDynamicsFiles(t, load, c)
	t.Setenv("MEMORY_WORKER_TOKEN", db.token)
	t.Setenv("MEMORY_DYNAMICS_READER_TOKEN", reader)
	prepared := func(mode string) daemonOptions {
		cfg, e := prepareDaemonDynamics(daemonOptions{DynamicsRuntimeConfig: rc, DynamicsLoadConfig: lc, Listen: freeAddress(t), DynamicsSearchConfig: mode})
		if e != nil {
			t.Fatal(e)
		}
		return cfg
	}
	apiCfg := prepared(daemonSearchModeFiles(t, nil))
	tightCfg := prepared(daemonSearchModeFiles(t, func(m map[string]any) { m["deadline_ns"] = int64(1) }))
	workerCfg := prepared("")
	workerCfg.Worker = true
	for _, s := range []struct {
		svc *memory.Service
		cfg daemonOptions
	}{{r.worker, workerCfg}, {r.api, apiCfg}, {r.tight, tightCfg}} {
		if err = configureDaemonDynamics(ctx, s.svc, s.cfg); err != nil {
			t.Fatal(err)
		}
	}
	r.scope, r.apiAddr, r.tightAddr = c.Scopes[0], apiCfg.Listen, tightCfg.Listen
	in := daemonDynamicsCanonicalSource(t, app, auth, human, r.scope)
	r.target = in.Target.ID
	if _, err = r.worker.ActivateNativeMemoryDynamics(ctx, db.token, memory.NativeDynamicsActivationInput{Scope: in.Scope, Target: in.Target, Dimension: in.Dimension, Config: daemonShadowNativeConfig(), RequestID: "daemon-activate"}); err != nil {
		t.Fatal(err)
	}
	if _, err = r.worker.RecordMemoryOutcome(ctx, db.token, in); err != nil {
		t.Fatal("durable outcome", err)
	}
	r.serve(t, ctx, workerCfg, apiCfg, tightCfg)
	return r
}

// serve starts both APIs now and gives the caller startWorkerFn/haltWorker for the worker.
func (r *daemonFallbackRig) serve(t *testing.T, ctx context.Context, workerCfg, apiCfg, tightCfg daemonOptions) {
	t.Helper()
	run, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	var done []chan error
	for _, s := range []struct {
		svc *memory.Service
		cfg daemonOptions
	}{{r.api, apiCfg}, {r.tight, tightCfg}} {
		d := make(chan error, 1)
		done = append(done, d)
		go func() { d <- serveDaemonHTTP(run, s.svc, s.cfg, nil) }()
	}
	var stop context.CancelFunc
	var workerDone chan error
	var once sync.Once
	r.startWorkerFn = func() {
		var workerRun context.Context
		workerRun, stop = context.WithCancel(run)
		workerDone = make(chan error, 1)
		go func() { workerDone <- serveDaemonWorker(workerRun, r.worker, workerCfg, nil) }()
	}
	r.haltWorker = func() { once.Do(func() { stop(); daemonDynamicsWaitStop(t, workerDone) }) }
	t.Cleanup(func() {
		if stop != nil {
			r.haltWorker()
		}
		cancel()
		for _, d := range done {
			daemonDynamicsWaitStop(t, d)
		}
	})
}

// mutateState rewrites the one native state row in payload and doc together.
func (r *daemonFallbackRig) mutateState(t *testing.T, status string, corrupt bool) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, r.db.url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	q, args := `UPDATE arkism.memory_records SET doc=jsonb_set(doc,'{native_dynamics,state,status}',to_jsonb($1::text)), payload=convert_to(jsonb_set(doc,'{native_dynamics,state,status}',to_jsonb($1::text))::text,'UTF8') WHERE collection='jobs' AND kind='native_dynamics_state'`, []any{status}
	if corrupt {
		q, args = `UPDATE arkism.memory_records SET payload=convert_to('{"attempts":"invalid"}','UTF8') WHERE collection='jobs' AND kind='native_dynamics_state'`, nil
	}
	if tag, e := conn.Exec(ctx, q, args...); e != nil || tag.RowsAffected() != 1 {
		t.Fatal("native state rewrite", tag, e)
	}
}

// search runs one HTTP search, checks the baseline answer and its two records,
// and returns the newest shadow observation of svc.
func (r *daemonFallbackRig) search(t *testing.T, svc *memory.Service, addr string) memory.NativeSearchShadowObservation {
	t.Helper()
	before, seen := daemonRecordKinds(t, r.db.url), len(svc.NativeSearchShadow.Observations())
	bundle := daemonSearchBundle(t, addr, r.db.token, r.scope)
	if len(bundle.Items) == 0 || bundle.Items[0].ID != r.target {
		t.Fatal("baseline answer lost under fallback", bundle.Items)
	}
	if want := map[string]int{"jobs/context_usage": 1, "audit_events/search": 1}; !reflect.DeepEqual(daemonRecordDelta(before, daemonRecordKinds(t, r.db.url)), want) {
		t.Fatal("fallback search side effects")
	}
	obs := svc.NativeSearchShadow.Observations()
	if len(obs) != seen+1 || !obs[seen].BaselineReturned {
		t.Fatal("one shadow observation per search", seen, obs)
	}
	return obs[seen]
}

// Each shadow fallback cell of the SYS-06 matrix is forced on a real daemon
// HTTP API and must report its own reason, with the baseline answer returned.
func TestDynamicsIntegrationShadowFallbackCellsPG(t *testing.T) {
	r := newDaemonFallbackRig(t)
	check := func(cell string, o memory.NativeSearchShadowObservation, status, reason string) {
		t.Helper()
		if o.Status != status || o.Reason != reason {
			t.Fatal(cell, o.Status, o.Reason)
		}
	}
	// lag: no publication has reached the API cache yet.
	check("lag", r.search(t, r.api, r.apiAddr), "fallback", "snapshot_unavailable")
	r.startWorkerFn()
	daemonSearchWaitPublished(t, r.scope, r.api, r.tight)
	r.haltWorker()
	daemonSearchSettledGeneration(t, r.api, r.scope)
	// control: the same state, settled, is read.
	o := r.search(t, r.api, r.apiAddr)
	check("control", o, "selected", "none")
	if o.SourceCount != 1 {
		t.Fatal("control read no source", o)
	}
	check("timeout", r.search(t, r.tight, r.tightAddr), "fallback", "shadow_deadline")
	r.mutateState(t, "stale", false)
	check("stale", r.search(t, r.api, r.apiAddr), "fallback", "stale_source")
	r.mutateState(t, "active", false)
	check("restored", r.search(t, r.api, r.apiAddr), "selected", "none")
	r.mutateState(t, "", true)
	check("corrupt", r.search(t, r.api, r.apiAddr), "fallback", "invalid_source")
	got := map[string]int{}
	for _, svc := range []*memory.Service{r.api, r.tight} {
		for _, o := range svc.NativeSearchShadow.Observations() {
			got[o.Reason+"/"+o.Status]++
		}
	}
	want := map[string]int{"snapshot_unavailable/fallback": 1, "none/selected": 2, "shadow_deadline/fallback": 1, "stale_source/fallback": 1, "invalid_source/fallback": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("reported counts differ from forced cells", got, want)
	}
}
