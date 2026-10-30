package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FelixBaek/arkism/internal/memory"
)

// daemonSearchModeFiles writes a search mode file next to a pinned copy of the
// repository policy registry and returns the mode file path.
func daemonSearchModeFiles(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	registry, err := os.ReadFile("../../docs/research/memory-harness/policy_registry.json")
	if err != nil {
		t.Fatal(err)
	}
	// The same registry sits beside the mode file and one level up, so the
	// absolute and parent paths fail only for their path, never for a hash.
	parent := t.TempDir()
	dir := filepath.Join(parent, "cfg")
	if err = os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{parent, dir} {
		if err = os.WriteFile(filepath.Join(d, "registry.json"), registry, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := map[string]any{"protocol": "sys06-search-mode-v1", "mode": "shadow", "registry_path": "registry.json", "registry_sha256": memory.ContentHash(string(registry)), "policy": "V3-P1-NOTIME", "native_version": "sys06_shadow_v1", "dimension": "work_task", "deadline_ns": int64(time.Second), "max_steps": 100000, "capacity": 8}
	if edit != nil {
		edit(m)
	}
	if m["registry_path"] == "<abs>" {
		m["registry_path"] = filepath.Join(dir, "registry.json")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "search-mode.json")
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// daemonSearchModeAPI is HTTP API options with both dynamics configs and the tokens set.
func daemonSearchModeAPI(t *testing.T) daemonOptions {
	t.Helper()
	load, c := daemonDynamicsFixture(t)
	r, l := daemonDynamicsFiles(t, load, c)
	t.Setenv("MEMORY_DYNAMICS_READER_TOKEN", "reader")
	t.Setenv("MEMORY_WORKER_TOKEN", "worker")
	return daemonOptions{DynamicsRuntimeConfig: r, DynamicsLoadConfig: l}
}

// daemonBareMode keeps only the protocol and sets mode.
func daemonBareMode(mode string) func(map[string]any) {
	return func(m map[string]any) {
		for k := range m {
			if k != "protocol" {
				delete(m, k)
			}
		}
		m["mode"] = mode
	}
}

func TestDynamicsIntegrationSearchModeConfig(t *testing.T) {
	api := daemonSearchModeAPI(t)
	for name, want := range map[string]bool{"absent": false, "off": false, "shadow": true} {
		t.Run(name, func(t *testing.T) {
			cfg := api
			switch name {
			case "off":
				cfg.DynamicsSearchConfig = daemonSearchModeFiles(t, daemonBareMode("off"))
			case "shadow":
				cfg.DynamicsSearchConfig = daemonSearchModeFiles(t, nil)
			}
			got, err := prepareDaemonDynamics(cfg)
			if err != nil || (got.dynamicsSearch != nil) != want {
				t.Fatal("search mode", name, err, got.dynamicsSearch != nil)
			}
			if want && (got.dynamicsSearch.Mode != "shadow" || got.dynamicsSearch.Deadline != time.Second || got.dynamicsSearch.Capacity != 8) {
				t.Fatal("shadow settings not carried", got.dynamicsSearch)
			}
		})
	}
}

func TestDynamicsIntegrationSearchModeConfigRefused(t *testing.T) {
	api := daemonSearchModeAPI(t)
	refused := map[string]func(map[string]any){
		"dynamic_without_accepted_policy": func(m map[string]any) { m["mode"] = "dynamic" },
		"unknown_mode":                    func(m map[string]any) { m["mode"] = "Shadow" },
		"empty_mode":                      func(m map[string]any) { m["mode"] = "" },
		"unknown_field":                   func(m map[string]any) { m["fallback"] = "on" },
		"other_protocol":                  func(m map[string]any) { m["protocol"] = "sys06-search-mode-v2" },
		"off_with_shadow_settings":        func(m map[string]any) { m["mode"] = "off" },
		"registry_hash":                   func(m map[string]any) { m["registry_sha256"] = strings.Repeat("0", 64) },
		"registry_absolute":               func(m map[string]any) { m["registry_path"] = "<abs>" },
		"registry_parent":                 func(m map[string]any) { m["registry_path"] = "../registry.json" },
		"unregistered_policy":             func(m map[string]any) { m["policy"] = "V9-UNKNOWN" },
		"deadline_over_bound":             func(m map[string]any) { m["deadline_ns"] = int64(6 * time.Second) },
		"zero_capacity":                   func(m map[string]any) { m["capacity"] = 0 },
		"no_dimension":                    func(m map[string]any) { m["dimension"] = "" },
		"no_native_version":               func(m map[string]any) { m["native_version"] = "" },
		"no_policy":                       func(m map[string]any) { m["policy"] = "" },
		"zero_deadline":                   func(m map[string]any) { m["deadline_ns"] = 0 },
		"zero_max_steps":                  func(m map[string]any) { m["max_steps"] = 0 },
		"capacity_over_bound":             func(m map[string]any) { m["capacity"] = 1001 },
		"bare_dynamic":                    daemonBareMode("dynamic"),
	}
	for name, edit := range refused {
		t.Run(name, func(t *testing.T) {
			cfg := api
			cfg.DynamicsSearchConfig = daemonSearchModeFiles(t, edit)
			if _, err := prepareDaemonDynamics(cfg); !errors.Is(err, memory.ErrInvalid) {
				t.Fatal("accepted", name, err)
			}
		})
	}
}

func TestDynamicsIntegrationSearchModeOnlyOnTheAPI(t *testing.T) {
	api := daemonSearchModeAPI(t)
	r, l := api.DynamicsRuntimeConfig, api.DynamicsLoadConfig
	shadow := daemonSearchModeFiles(t, nil)
	off := daemonSearchModeFiles(t, daemonBareMode("off"))
	for name, cfg := range map[string]daemonOptions{
		"no_dynamics_plan": {DynamicsSearchConfig: shadow},
		"worker_off":       {DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, Worker: true, DynamicsSearchConfig: off},
		"worker":           {DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, Worker: true, DynamicsSearchConfig: shadow},
		"repair":           {DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, DynamicsRepair: true, DynamicsSearchConfig: shadow},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := prepareDaemonDynamics(cfg); !errors.Is(err, memory.ErrInvalid) {
				t.Fatal("search mode outside the HTTP API", name, err)
			}
		})
	}
	t.Run("broken_unicode", func(t *testing.T) {
		path := daemonSearchModeFiles(t, nil)
		raw, _ := os.ReadFile(path)
		raw = bytes.Replace(raw, []byte(`"work_task"`), []byte("\"work\xff\""), 1)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := api
		cfg.DynamicsSearchConfig = path
		if _, err := prepareDaemonDynamics(cfg); !errors.Is(err, memory.ErrInvalid) {
			t.Fatal("broken Unicode accepted", err)
		}
	})
}

// daemonShadowNativeConfig matches the scalar settings of the registry's
// V3-P1-NOTIME entry so the shadow can read the native state it was activated with.
func daemonShadowNativeConfig() memory.NativeDynamicsConfig {
	c := daemonDynamicsNativeConfig()
	initial, tau, eta, unit := 0.5, int64(1000000000), 0.25, 1.0
	decay, positive := false, true
	c.Version, c.InitialValue, c.TauNS, c.EtaPositive, c.UnitStrength, c.DecayEnabled, c.PositiveLearning = "sys06_shadow_v1", &initial, &tau, &eta, &unit, &decay, &positive
	return c
}

func daemonSearchBundle(t *testing.T, address, token string, scope memory.Scope) memory.ContextBundle {
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
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal("search", response.StatusCode, err)
	}
	var out memory.ContextBundle
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func daemonRecordKinds(t *testing.T, url string) map[string]int {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	// Every record collection and kind, plus the row count of every other table in
	// the schema; a shadow write anywhere would show up here.
	var tables []string
	rows, err := conn.Query(ctx, `SELECT quote_ident(table_name) FROM information_schema.tables
		WHERE table_schema='arkism' AND table_type='BASE TABLE' AND table_name<>'memory_records' ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err = rows.Err(); err != nil || len(tables) == 0 {
		t.Fatal("schema tables", err)
	}
	q := `SELECT collection||'/'||kind, count(*) FROM arkism.memory_records GROUP BY 1`
	for _, name := range tables {
		q += ` UNION ALL SELECT '` + name + `', count(*) FROM arkism.` + name
	}
	rows, err = conn.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for rows.Next() {
		var kind string
		var n int
		if err = rows.Scan(&kind, &n); err != nil {
			t.Fatal(err)
		}
		out[kind] = n
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// Two HTTP APIs read one isolated database: one with no search mode (off), one
// in shadow. Off, shadow and off again answer the same search alike and each search leaves
// only its one usage receipt; shadow records its diagnostic in process only.
func TestDynamicsIntegrationOffShadowSameAnswerPG(t *testing.T) {
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
	offAPI, shadowAPI := &memory.Service{Repo: app, Auth: auth}, &memory.Service{Repo: app, Auth: auth}
	load, c := daemonDynamicsFixture(t)
	c.Scopes = []memory.Scope{{WorkspaceID: "default", ProjectID: "default"}}
	r, l := daemonDynamicsFiles(t, load, c)
	t.Setenv("MEMORY_WORKER_TOKEN", db.token)
	t.Setenv("MEMORY_DYNAMICS_READER_TOKEN", reader)
	offCfg, err := prepareDaemonDynamics(daemonOptions{DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, Listen: freeAddress(t)})
	if err != nil {
		t.Fatal(err)
	}
	shadowCfg, err := prepareDaemonDynamics(daemonOptions{DynamicsRuntimeConfig: r, DynamicsLoadConfig: l, Listen: freeAddress(t), DynamicsSearchConfig: daemonSearchModeFiles(t, nil)})
	if err != nil {
		t.Fatal(err)
	}
	workerCfg := offCfg
	workerCfg.Worker = true
	for _, s := range []struct {
		svc *memory.Service
		cfg daemonOptions
	}{{worker, workerCfg}, {offAPI, offCfg}, {shadowAPI, shadowCfg}} {
		if err = configureDaemonDynamics(ctx, s.svc, s.cfg); err != nil {
			t.Fatal(err)
		}
	}
	if offAPI.NativeSearchShadow != nil || shadowAPI.NativeSearchShadow == nil || worker.NativeSearchShadow != nil {
		t.Fatal("search mode wiring", offAPI.NativeSearchShadow != nil, shadowAPI.NativeSearchShadow == nil, worker.NativeSearchShadow != nil)
	}
	in := daemonDynamicsCanonicalSource(t, app, auth, human, c.Scopes[0])
	if _, err = worker.ActivateNativeMemoryDynamics(ctx, db.token, memory.NativeDynamicsActivationInput{Scope: in.Scope, Target: in.Target, Dimension: in.Dimension, Config: daemonShadowNativeConfig(), RequestID: "daemon-activate"}); err != nil {
		t.Fatal(err)
	}
	if _, err = worker.RecordMemoryOutcome(ctx, db.token, in); err != nil {
		t.Fatal("durable outcome", err)
	}
	run, cancel := context.WithCancel(ctx)
	defer cancel()
	workerRun, stopWorker := context.WithCancel(run)
	defer stopWorker()
	done := []chan error{make(chan error, 1), make(chan error, 1), make(chan error, 1)}
	go func() { done[0] <- serveDaemonWorker(workerRun, worker, workerCfg, nil) }()
	go func() { done[1] <- serveDaemonHTTP(run, offAPI, offCfg, nil) }()
	go func() { done[2] <- serveDaemonHTTP(run, shadowAPI, shadowCfg, nil) }()
	var halt sync.Once
	haltWorker := func() { halt.Do(func() { stopWorker(); daemonDynamicsWaitStop(t, done[0]) }) }
	t.Cleanup(func() {
		haltWorker()
		cancel()
		for _, d := range done[1:] {
			daemonDynamicsWaitStop(t, d)
		}
	})
	daemonSearchWaitPublished(t, in.Scope, offAPI, shadowAPI)
	// While the worker republishes every few milliseconds the shadow may read a
	// cache one generation behind and fall back; the baseline answer must not move.
	daemonSearchOffShadowOff(t, db, offCfg.Listen, shadowCfg.Listen, in.Target.ID, in.Scope)
	haltWorker()
	daemonSearchSettledGeneration(t, shadowAPI, in.Scope)
	daemonSearchOffShadowOff(t, db, offCfg.Listen, shadowCfg.Listen, in.Target.ID, in.Scope)
	daemonSearchCheckShadow(t, shadowAPI.NativeSearchShadow.Observations())
}

// daemonSearchWaitPublished waits until every API cache holds the worker's first publication.
func daemonSearchWaitPublished(t *testing.T, scope memory.Scope, apis ...*memory.Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, api := range apis {
		for {
			snapshot, e := api.DynamicsLoad.DynamicsSnapshot(scope, time.Now().UnixNano())
			if e == nil && snapshot.Head == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("API refresh never caught the worker publication", e)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

// daemonSearchCheckShadow checks the two shadow observations: under republishing, then settled.
func daemonSearchCheckShadow(t *testing.T, obs []memory.NativeSearchShadowObservation) {
	t.Helper()
	// A lagging cache may only fall back as snapshot_unavailable, never compute on stale state.
	if len(obs) == 2 && obs[0].Status != "selected" && (obs[0].Status != "fallback" || obs[0].Reason != "snapshot_unavailable") {
		t.Fatal("shadow under republishing", obs[0])
	}
	if len(obs) != 2 || !obs[0].BaselineReturned || !obs[1].BaselineReturned || obs[1].Status != "selected" || obs[1].SourceCount != 1 {
		t.Fatal("shadow diagnostic", obs)
	}
	for _, o := range obs {
		t.Logf("shadow status=%s reason=%s sources=%d", o.Status, o.Reason, o.SourceCount)
	}
}

// daemonSearchSettledGeneration waits until the API cache has held one
// generation for twenty refresh polls after the worker stopped publishing.
func daemonSearchSettledGeneration(t *testing.T, api *memory.Service, scope memory.Scope) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	last, same := int64(-1), 0
	for same < 20 {
		if time.Now().After(deadline) {
			t.Fatal("API cache never settled after the worker stopped")
		}
		time.Sleep(5 * time.Millisecond)
		snapshot, e := api.DynamicsLoad.DynamicsSnapshot(scope, time.Now().UnixNano())
		if e != nil || snapshot.Generation != last {
			same = 0
			if e == nil {
				last = snapshot.Generation
			}
			continue
		}
		same++
	}
}

// daemonSearchOffShadowOff runs the same search off, shadow, then off again.
// Each leaves only the baseline's receipt and audit event, and the shadow answer
// equals the off answers except for the recency score moving with the clock.
func daemonSearchOffShadowOff(t *testing.T, db testDB, off, shadow, target string, scope memory.Scope) {
	t.Helper()
	var answers []memory.ContextBundle
	for _, address := range []string{off, shadow, off} {
		before := daemonRecordKinds(t, db.url)
		bundle := daemonSearchBundle(t, address, db.token, scope)
		delta := daemonRecordDelta(before, daemonRecordKinds(t, db.url))
		// The baseline search writes its usage receipt and its audit event, nothing else.
		if want := map[string]int{"jobs/context_usage": 1, "audit_events/search": 1}; !reflect.DeepEqual(delta, want) {
			t.Fatal("search side effects", address, delta)
		}
		bundle.ContextID = ""
		answers = append(answers, bundle)
	}
	daemonSearchSameAnswers(t, answers, target)
}

func daemonRecordDelta(before, after map[string]int) map[string]int {
	delta := map[string]int{}
	for k, n := range after {
		if n != before[k] {
			delta[k] = n - before[k]
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			delta[k] = -before[k]
		}
	}
	return delta
}

// daemonSearchSameAnswers checks that the shadow answer (middle) equals the off answers around it.
func daemonSearchSameAnswers(t *testing.T, answers []memory.ContextBundle, target string) {
	t.Helper()
	if len(answers[0].Items) == 0 || answers[0].Items[0].ID != target {
		t.Fatal("baseline search did not retrieve the fixture", answers[0].Items)
	}
	if len(answers[0].Items) != len(answers[1].Items) || len(answers[2].Items) != len(answers[1].Items) {
		t.Fatal("shadow changed the item count")
	}
	// Recency decay reads the clock, so the shadow score must lie between the off scores before and after.
	for i := range answers[1].Items {
		lo, hi := min(answers[0].Items[i].Score, answers[2].Items[i].Score), max(answers[0].Items[i].Score, answers[2].Items[i].Score)
		if got := answers[1].Items[i].Score; got < lo || got > hi {
			t.Fatal("shadow moved a score outside the clock drift", i, lo, got, hi)
		}
		for _, a := range answers {
			a.Items[i].Score = 0
		}
	}
	if !reflect.DeepEqual(answers[0], answers[1]) || !reflect.DeepEqual(answers[2], answers[1]) {
		a, _ := json.Marshal(answers[0])
		b, _ := json.Marshal(answers[1])
		t.Fatal("shadow changed the returned answer\n", string(a), "\n", string(b))
	}
}
