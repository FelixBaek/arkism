package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
	dyn "github.com/FelixBaek/arkism/internal/memoryharness/dynamicsv2"
	"github.com/jackc/pgx/v5"
)

// This is development wiring acceptance, using the already sealed test rates.
// It starts the actual daemon HTTP/worker loops in the test process, not a
// product binary or an approved serving policy.
type daemonFastSlowHTTPRig struct {
	db     testDB
	scope  memory.Scope
	target string
	other  string
	worker *memory.Service
	apis   []*memory.Service
	cfgs   []daemonOptions
}

func newDaemonFastSlowHTTPRig(t *testing.T) *daemonFastSlowHTTPRig {
	t.Helper()
	db := newTestDB(t, true)
	app := daemonDynamicsLogin(t, db)
	auth, err := memory.NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	human, reader := daemonDynamicsPrincipals(t, db)
	workerRepo, err := app.WithToken(db.token)
	if err != nil {
		t.Fatal(err)
	}
	r := &daemonFastSlowHTTPRig{db: db, worker: &memory.Service{Repo: workerRepo, Auth: auth}}
	load, c := daemonDynamicsFixture(t)
	c.Scopes = []memory.Scope{{WorkspaceID: "default", ProjectID: "default"}}
	// Hold a settled generation while checking HTTP behavior, not freshness latency.
	load = bytes.Replace(load, []byte(`"allowed_freshness_ns":1000000000`), []byte(`"allowed_freshness_ns":60000000000`), 1)
	c.Limits.AllowedFreshnessNS, c.LoadConfigSHA256 = 60_000_000_000, memory.ContentHash(string(load))
	rc, lc := daemonDynamicsFiles(t, load, c)
	t.Setenv("MEMORY_WORKER_TOKEN", db.token)
	t.Setenv("MEMORY_DYNAMICS_READER_TOKEN", reader)
	block := daemonFastSlowBlock(t)
	for _, mode := range []string{"off", "shadow", "on"} {
		path := daemonSearchModeFiles(t, daemonBareMode("off"))
		if mode != "off" {
			path = daemonSearchModeFiles(t, func(m map[string]any) { m["mode"], m["fast_slow"] = mode, block })
		}
		cfg, e := prepareDaemonDynamics(daemonOptions{DynamicsRuntimeConfig: rc, DynamicsLoadConfig: lc, DynamicsSearchConfig: path, Listen: freeAddress(t)})
		if e != nil {
			t.Fatal(e)
		}
		api := &memory.Service{Repo: app, Auth: auth}
		if e = configureDaemonDynamics(context.Background(), api, cfg); e != nil {
			t.Fatal(e)
		}
		r.apis, r.cfgs = append(r.apis, api), append(r.cfgs, cfg)
	}
	workerCfg, err := prepareDaemonDynamics(daemonOptions{DynamicsRuntimeConfig: rc, DynamicsLoadConfig: lc, Worker: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = configureDaemonDynamics(context.Background(), r.worker, workerCfg); err != nil {
		t.Fatal(err)
	}
	r.scope = c.Scopes[0]
	in := daemonDynamicsCanonicalSource(t, app, auth, human, r.scope)
	r.target = in.Target.ID
	first := daemonFastSlowTimelessMemory(t, app, human, in)
	other := daemonFastSlowOtherMemory(t, app, auth, human, first)
	r.other = other.ID
	native := daemonShadowNativeConfig()
	slow := block["slow_dynamics"].(dyn.DynamicsConfigV2)
	native.SlowTauNS, native.SlowEtaPositive = &slow.TauNS, &slow.EtaPositive
	for _, target := range []memory.Ref{in.Target, other} {
		if _, err = r.worker.ActivateNativeMemoryDynamics(context.Background(), db.token, memory.NativeDynamicsActivationInput{Scope: r.scope, Target: target, Dimension: in.Dimension, Config: native, RequestID: "http-fastslow-" + target.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = r.worker.RecordMemoryOutcome(context.Background(), db.token, in); err != nil {
		t.Fatal(err)
	}
	r.start(t, workerCfg)
	return r
}

func (r *daemonFastSlowHTTPRig) start(t *testing.T, workerCfg daemonOptions) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	var done []chan error
	for i, api := range r.apis {
		d := make(chan error, 1)
		done = append(done, d)
		cfg := r.cfgs[i]
		go func() { d <- serveDaemonHTTP(ctx, api, cfg, nil) }()
	}
	go func() { workerDone <- serveDaemonWorker(workerCtx, r.worker, workerCfg, nil) }()
	var once sync.Once
	halt := func() { once.Do(func() { stopWorker(); daemonDynamicsWaitStop(t, workerDone) }) }
	// Register joining before any assertion can abort the setup.
	t.Cleanup(func() {
		halt()
		cancel()
		for _, d := range done {
			daemonDynamicsWaitStop(t, d)
		}
	})
	daemonSearchWaitPublished(t, r.scope, r.apis...)
	halt()
	for _, api := range r.apis {
		daemonSearchSettledGeneration(t, api, r.scope)
	}
}

func (r *daemonFastSlowHTTPRig) search(t *testing.T, index int) memory.ContextBundle {
	t.Helper()
	body, err := json.Marshal(memory.SearchInput{Scope: r.scope, Query: "searchcanary", Modes: []string{"fts"}, Limit: 10, MaxChars: 4000, BudgetTokens: 2000, TokenEncoding: "cl100k_base"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+r.cfgs[index].Listen+"/v1/search", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+r.db.token)
	req.Header.Set("Content-Type", "application/json")
	before := daemonRecordKinds(t, r.db.url)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatal("HTTP search failed", resp.StatusCode, err)
	}
	var out memory.ContextBundle
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 2 || out.ContextData == "" || out.TokensUsed == 0 {
		t.Fatalf("missing actual packed memory: items=%d context_bytes=%d tokens=%d", len(out.Items), len(out.ContextData), out.TokensUsed)
	}
	want := map[string]int{"jobs/context_usage": 1, "audit_events/search": 1}
	if !reflect.DeepEqual(daemonRecordDelta(before, daemonRecordKinds(t, r.db.url)), want) {
		t.Fatal("unexpected search row changes")
	}
	out.ContextID = ""
	return out
}

func (r *daemonFastSlowHTTPRig) state(t *testing.T) string {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), r.db.url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	rows, err := conn.Query(context.Background(), `SELECT payload,doc#>>'{native_dynamics,key,target,id}',(doc#>>'{native_dynamics,state,value}')::float8,(doc#>>'{native_dynamics,state,slow_value}')::float8 FROM arkism.memory_records WHERE collection='jobs' AND kind='native_dynamics_state' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var all []byte
	count := 0
	for rows.Next() {
		var payload []byte
		var target string
		var fast, slow *float64
		if err = rows.Scan(&payload, &target, &fast, &slow); err != nil {
			t.Fatal(err)
		}
		wantFast, wantSlow := 0.5, 0.5
		if target == r.target {
			wantFast, wantSlow = 0.5625, 0.525
		}
		if fast == nil || slow == nil || math.Abs(*fast-wantFast) > 1e-14 || math.Abs(*slow-wantSlow) > 1e-14 {
			t.Fatal("worker did not advance both lanes to hand-calculated values")
		}
		all = append(all, payload...)
		count++
	}
	if rows.Err() != nil || count != 2 {
		t.Fatal("missing paired native states", rows.Err(), count)
	}
	return memory.ContentHash(string(all))
}

func TestDynamicsIntegrationFastSlowHTTPPG(t *testing.T) {
	r := newDaemonFastSlowHTTPRig(t)
	before := r.state(t)
	off := r.search(t, 0)
	shadow := r.search(t, 1)
	if !reflect.DeepEqual(off, shadow) || r.apis[0].NativeSearchShadow != nil {
		t.Fatal("off/shadow changed baseline")
	}
	on := r.search(t, 2)
	// Base FTS order is other, learned; state order is learned, other.
	// Equal outer weights give both 1/61+1/62; trust1 beats trust0.5.
	// Rendered scores deliberately retain their original base reciprocal ranks.
	if off.Items[0].ID != r.other || on.Items[0].ID != r.target || on.ContextData == off.ContextData {
		t.Fatal("HTTP did not serve the hand-calculated reordered packed context")
	}
	if math.Abs(off.Items[0].Score-1.0/61) > 1e-14 || math.Abs(on.Items[0].Score-1.0/62) > 1e-14 {
		t.Fatal("rendered reference scores changed")
	}
	daemonFastSlowHTTPObservation(t, r.apis[1], "shadow", false)
	daemonFastSlowHTTPObservation(t, r.apis[2], "on", true)
	if r.state(t) != before {
		t.Fatal("search mutated committed native state")
	}
	r.removeState(t)
	fallback := r.search(t, 2)
	if !reflect.DeepEqual(fallback, off) {
		t.Fatal("missing state did not return HTTP baseline")
	}
	obs := r.apis[2].NativeSearchShadow.Observations()
	last := obs[len(obs)-1]
	if last.Status != "fallback" || last.Served || !last.BaselineReturned || last.Mode != "on" {
		t.Fatal("fallback fabricated native return")
	}
}

func daemonFastSlowHTTPObservation(t *testing.T, api *memory.Service, mode string, served bool) {
	t.Helper()
	obs := api.NativeSearchShadow.Observations()
	if len(obs) != 1 {
		t.Fatal("expected one HTTP observation")
	}
	rec := obs[0]
	if rec.Protocol != "sys-native-service-fast-slow-v1" || rec.Mode != mode || rec.Status != "selected" || !rec.FastSlow || rec.Served != served || rec.BaselineReturned == served || rec.SourceCount != 2 {
		t.Fatal("HTTP mode/return observation mismatch")
	}
}

func (r *daemonFastSlowHTTPRig) removeState(t *testing.T) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), r.db.url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	tag, err := conn.Exec(context.Background(), `DELETE FROM arkism.memory_records WHERE collection='jobs' AND kind='native_dynamics_state' AND doc#>>'{native_dynamics,key,target,id}'=$1`, r.target)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatal("isolated missing-state fault", err)
	}
}

// A timeless preference makes the literal FTS/RRF oracle independent of wall
// clock recency. The existing scalar daemon tests cover recency drift separately.
func daemonFastSlowTimelessMemory(t *testing.T, app *memory.Postgres, human string, in memory.MemoryOutcomeInput) memory.Record {
	t.Helper()
	repo, err := app.WithToken(human)
	if err != nil {
		t.Fatal(err)
	}
	row, err := repo.Get(context.Background(), "memories", in.Scope, in.Target.ID)
	if err != nil {
		t.Fatal(err)
	}
	row.Kind = "user"
	if err = repo.Replace(context.Background(), "memories", row, row.Revision); err != nil {
		t.Fatal(err)
	}
	return row
}

func daemonFastSlowOtherMemory(t *testing.T, app *memory.Postgres, auth memory.Authorizer, human string, first memory.Record) memory.Ref {
	t.Helper()
	repo, err := app.WithToken(human)
	if err != nil {
		t.Fatal(err)
	}
	svc := &memory.Service{Repo: repo, Auth: auth}
	row, err := svc.Create(context.Background(), human, "memory_candidates", memory.CreateInput{Scope: first.Scope, Kind: "user", Text: "searchcanary searchcanary searchcanary second candidate", Keywords: []string{"searchcanary"}, Sources: first.Sources})
	if err != nil {
		t.Fatal("create second candidate", err)
	}
	row, err = svc.Transition(context.Background(), human, "memory_candidates", row.ID, memory.ChangeInput{Scope: first.Scope, ExpectedRevision: row.Revision, Status: "approved"})
	if err != nil {
		t.Fatal(err)
	}
	row, err = svc.Transition(context.Background(), human, "memories", row.ID, memory.ChangeInput{Scope: first.Scope, ExpectedRevision: row.Revision, Status: "active"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Trust != 1 || row.Trust != 0.5 {
		t.Fatal("unexpected fixture trust defaults")
	}
	return memory.Ref{Collection: "memories", ID: row.ID, Revision: row.Revision}
}
