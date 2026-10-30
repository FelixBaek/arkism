package memory

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SYS-05 B5 A-v2 projection: the unchanged A generator plans the requests, this
// test executes exactly that plan through the real handler, keeps the raw run,
// and the A adapter and summarizer then reconcile it. Nothing here edits the A
// contract, schema or expected values.

const (
	dynamicsProjectionRecipe = "sys05-b5-projection-recipe-v1: update is POST /v1/memory-outcomes with the fixture outcome on its one target and EventID av2-<request id prefix>; read is POST /v1/search with the fixture query by the worker reader; repair_block is a human delete of the target, a drain, the purge and an explicit repair"
	dynamicsProjectionOracle = "sys05-b5-quality-oracle-v1: a read passes iff the native snapshot was selected and the response names the target exactly when the target is not deleted"
	dynamicsProjectionSumDir = "../../evaluation/memory-harness-v1/system/sys_05"
)

type dynamicsProjectionSample struct {
	AtUnixNS int64 `json:"at_unix_ns"`
	Depth    int64 `json:"depth"`
}

type dynamicsProjectionRepair struct {
	RepairID         string `json:"repair_id"`
	StartNS          int64  `json:"start_ns"`
	EndNS            int64  `json:"end_ns"`
	PublishedNS      int64  `json:"published_ns"`
	ValidationSHA256 string `json:"validation_sha256"`
	// HoldWitnessedNS is when this harness itself read the hold with the same origin, before the repair.
	HoldWitnessedNS int64 `json:"hold_witnessed_ns"`
}

// dynamicsProjectionConfig binds the A config to the runtime limits actually used.
func dynamicsProjectionConfig(mode string, limits DynamicsLoadLimits) map[string]any {
	limitsRaw, _ := json.Marshal(limits)
	ms := int64(time.Millisecond)
	return map[string]any{
		"protocol": "sys05-load-config-v1", "run_id": "av2-" + mode, "seed": 7, "mode": mode,
		"scenario": "normal", "cache": "cold", "start_ns": 0, "duration_ns": 2000 * ms,
		"arrival_schedule": []map[string]any{
			{"offset_ns": 0, "count": 2, "spacing_ns": 100 * ms, "operation": "update"},
			{"offset_ns": 400 * ms, "count": 1, "spacing_ns": 0, "operation": "read"},
			{"offset_ns": 600 * ms, "count": 1, "spacing_ns": 0, "operation": "update"},
			{"offset_ns": 1300 * ms, "count": 1, "spacing_ns": 0, "operation": "read"},
		},
		"fault_schedule":        []map[string]any{{"kind": "repair_block", "offset_ns": 700 * ms, "duration_ns": 500 * ms}},
		"queue_admission_limit": limits.QueueCountLimit, "batch_limit": limits.BatchLimit,
		"concurrency_limit": limits.ConcurrencyLimit, "retry_limit": limits.RetryLimit,
		"deadline_ns": limits.DeadlineNS, "allowed_freshness_ns": limits.AllowedFreshnessNS,
		"quality_criterion_sha256": dynamicsSHA([]byte(dynamicsProjectionOracle)),
		"model_sha256":             dynamicsSHA(limitsRaw),
		"corpus_sha256":            dynamicsSHA([]byte(dynamicsProjectionRecipe)),
	}
}

type dynamicsProjectionRequest struct {
	RequestID   string `json:"request_id"`
	ScheduledNS int64  `json:"scheduled_ns"`
	Operation   string `json:"operation"`
}

// dynamicsProjectionRun executes the generated plan for one mode and returns the raw run.
func dynamicsProjectionRun(t *testing.T, dir, mode string) map[string]any {
	t.Helper()
	p, shadow, search := dynamicsFunctionalSetup(t)
	runtimeConfig := dynamicsFixtureConfig()
	runtimeConfig.Mode = mode
	configRaw, err := json.Marshal(dynamicsProjectionConfig(mode, runtimeConfig.Limits))
	dynamicsRebindExpect(t, err, nil, "config")
	configPath, workloadPath := filepath.Join(dir, "config.json"), filepath.Join(dir, "workload.jsonl")
	dynamicsRebindExpect(t, os.WriteFile(configPath, configRaw, 0o600), nil, "write config")
	dynamicsProjectionPython(t, "generate_workload.py", "--config", configPath, "--out", workloadPath)
	workloadRaw, err := os.ReadFile(workloadPath)
	dynamicsRebindExpect(t, err, nil, "read workload")
	runtimeConfig.LoadConfigSHA256 = dynamicsSHA(configRaw)
	runtime := dynamicsFixtureRuntime(t, runtimeConfig)
	p.checker.DynamicsLoad, p.worker.DynamicsLoad = runtime, runtime
	if mode == "sync" {
		actions := []string{"db:read", "db:write", "read:evaluations", "read:agent_runs", "read:execution_traces", "read:sources", "read:memories", "write:evaluations", "read:jobs", "write:jobs"}
		dynamicsRebindExpect(t, p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "native-pg-checker", Role: roleService, Token: p.checkerToken, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: actions}), nil, "sync caller authority")
	}
	h := &dynamicsMeasureHarness{t: t, p: p, mode: mode, runID: "av2-" + mode}
	h.serve(p.checker)
	reader := &dynamicsMeasureHarness{t: t, p: p, mode: mode, runID: h.runID}
	reader.serve(p.worker)
	samples := []dynamicsProjectionSample{}
	sample := func() {
		samples = append(samples, dynamicsProjectionSample{time.Now().UnixNano(), dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Count})
	}
	deleted, faulted := false, false
	repairs := []dynamicsProjectionRepair{}
	anchor := time.Now()
	for _, line := range strings.Split(strings.TrimSpace(string(workloadRaw)), "\n") {
		var req dynamicsProjectionRequest
		dynamicsRebindExpect(t, json.Unmarshal([]byte(line), &req), nil, "workload line")
		if fault := 700 * int64(time.Millisecond); !faulted && req.ScheduledNS >= fault {
			faulted = true
			time.Sleep(time.Until(anchor.Add(time.Duration(fault))))
			repairs = dynamicsProjectionFault(h, &deleted)
			sample()
		}
		time.Sleep(time.Until(anchor.Add(time.Duration(req.ScheduledNS))))
		switch req.Operation {
		case "update":
			in := p.in
			in.EventID = "av2-" + req.RequestID[:24]
			h.outcome(req.RequestID, "update", in, p.checkerToken)
			h.witness()
		case "read":
			// The worker cadence before a read: drain, witness and publish, as a deployed worker round would.
			if !deleted {
				h.drain()
				h.publish()
			}
			sample()
			dynamicsProjectionRead(h, reader, shadow, search, req.RequestID, deleted)
		default:
			t.Fatalf("operation %q is outside this recipe", req.Operation)
		}
	}
	h.drain()
	h.witness()
	sample()
	time.Sleep(time.Until(anchor.Add(2 * time.Second)))
	ledger, err := json.Marshal(map[string]any{"ingress": h.ingress, "requests": h.rows})
	dynamicsRebindExpect(t, err, nil, "ledger")
	costsAt := time.Now().UnixNano()
	costs := dynamicsCosts(t, p, int64(len(ledger)))
	return map[string]any{
		"protocol": "sys05-b5-raw-run-v1", "mode": mode, "config_sha256": dynamicsSHA(configRaw), "workload_sha256": dynamicsSHA(workloadRaw),
		"runtime_config_load_sha256": runtimeConfig.LoadConfigSHA256, "anchor_unix_ns": anchor.UnixNano(),
		"observed_until_unix_ns": time.Now().UnixNano(), "ingress": h.ingress, "requests": h.rows,
		"queue_samples": samples, "costs": costs, "costs_at_unix_ns": costsAt, "repairs": repairs,
	}
}

// dynamicsProjectionFault deletes the target while the last update may still be
// queued, then purges and repairs; only a closed server interval is reported.
func dynamicsProjectionFault(h *dynamicsMeasureHarness, deleted *bool) []dynamicsProjectionRepair {
	h.t.Helper()
	_, at := dynamicsMeasureDeleteOnly(h)
	*deleted = true
	h.drain()
	held, err := h.p.worker.ObserveDynamicsLoad(testCtx, h.p.workerToken, scopeA)
	dynamicsRebindExpect(h.t, err, nil, "observe hold")
	heldAt := time.Now().UnixNano()
	repaired := dynamicsMeasureRepair(h, at)
	if repaired["publish_after_repair"] != "ok" {
		h.t.Fatalf("repair: %v", repaired)
	}
	o, err := h.p.worker.ObserveDynamicsLoad(testCtx, h.p.workerToken, scopeA)
	dynamicsRebindExpect(h.t, err, nil, "observe repair")
	// Only an interval this harness saw held, with the same origin, is reported.
	if !held.Withheld || held.RepairStartNS == nil || o.LastRepairStartNS == nil || o.LastRepairEndNS == nil || *held.RepairStartNS != *o.LastRepairStartNS {
		return []dynamicsProjectionRepair{}
	}
	// The validation hash is the server's commitment to the republished state; recompute it from the served copy.
	c := dynamicsRebindCoordinator(h.t, h.p.admin)
	snapshot, err := h.p.worker.DynamicsLoad.DynamicsSnapshot(scopeA, dynamicsNowNS())
	dynamicsRebindExpect(h.t, err, nil, "republished snapshot")
	witness, err := dynamicsSnapshotWitness(snapshot)
	dynamicsRebindExpect(h.t, err, nil, "snapshot witness")
	if witness != c.RequestHash || snapshot.PublishedNS != o.PublishedNS {
		h.t.Fatalf("republished snapshot does not match its validation hash")
	}
	return []dynamicsProjectionRepair{{"delete-race", *o.LastRepairStartNS, *o.LastRepairEndNS, o.PublishedNS, c.RequestHash, heldAt}}
}

// dynamicsMeasureDeleteOnly is the human delete step of the delete race.
func dynamicsMeasureDeleteOnly(h *dynamicsMeasureHarness) (Record, time.Time) {
	h.t.Helper()
	fixture := newFixture(h.t)
	fixture.svc.Repo = h.p.admin
	target, err := h.p.admin.Get(testCtx, colMemories, scopeA, h.p.in.Target.ID)
	dynamicsRebindExpect(h.t, err, nil, "target before delete")
	at := time.Now()
	dynamicsRebindExpect(h.t, fixture.svc.Delete(testCtx, fixture.human, colMemories, target.ID, ChangeInput{Scope: scopeA, ExpectedRevision: target.Revision}), nil, "human delete")
	return target, at
}

// dynamicsProjectionRead records the serving snapshot only when its generation
// is the same before and after this serial request and the read selected it.
func dynamicsProjectionRead(h, reader *dynamicsMeasureHarness, shadow *NativeSearchShadowRuntime, search SearchInput, id string, deleted bool) {
	h.t.Helper()
	before, beforeErr := h.p.worker.DynamicsLoad.DynamicsSnapshot(scopeA, dynamicsNowNS())
	reader.rows = h.rows
	count := len(shadow.Observations())
	row, out := reader.post(id, "read", "/v1/search", h.p.workerToken, search)
	if o := shadow.Observations(); row.HTTPStatus == http.StatusOK && len(o) == count+1 {
		row.SearchStatus = o[len(o)-1].Status
	}
	after, afterErr := h.p.worker.DynamicsLoad.DynamicsSnapshot(scopeA, dynamicsNowNS())
	if row.SearchStatus == "selected" && beforeErr == nil && afterErr == nil && before.Generation == after.Generation {
		asOf := before.AsOfNS
		row.SnapshotAsOfNS = &asOf
	}
	if row.HTTPStatus == http.StatusOK {
		passed := row.SearchStatus == "selected" && strings.Contains(string(out), h.p.in.Target.ID) == !deleted
		row.QualityPassed = &passed
	}
	h.rows = reader.rows
	h.ingress, reader.ingress = append(h.ingress, reader.ingress...), nil
	h.markVisible(row)
}

func dynamicsProjectionPython(t *testing.T, script string, args ...string) {
	t.Helper()
	if err := dynamicsProjectionTry(script, args...); err != nil {
		t.Fatal(err)
	}
}

func dynamicsProjectionTry(script string, args ...string) error {
	cmd := exec.Command("python3", append([]string{"-I", filepath.Join(dynamicsProjectionSumDir, script)}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %v %s", script, err, out)
	}
	return nil
}

func dynamicsProjectionSummary(t *testing.T, dir string, raw map[string]any) map[string]any {
	t.Helper()
	summary, err := dynamicsProjectionTrySummary(dir, "", raw)
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

// dynamicsProjectionTrySummary writes outputs under a tag so altered copies never replace the real run.
func dynamicsProjectionTrySummary(dir, tag string, raw map[string]any) (map[string]any, error) {
	rawBytes, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	j := func(name string) string { return filepath.Join(dir, tag+name) }
	if err = os.WriteFile(j("raw-run.json"), rawBytes, 0o600); err != nil {
		return nil, err
	}
	for _, name := range []string{"ledger.jsonl", "observations.json", "summary.json"} {
		_ = os.Remove(j(name))
	}
	in := func(name string) string { return filepath.Join(dir, name) }
	if err = dynamicsProjectionTry("measurement_adapter.py", "--config", in("config.json"), "--workload", in("workload.jsonl"), "--raw", j("raw-run.json"), "--ledger-out", j("ledger.jsonl"), "--observations-out", j("observations.json")); err != nil {
		return nil, err
	}
	if err = dynamicsProjectionTry("summarize_load.py", "--config", in("config.json"), "--workload", in("workload.jsonl"), "--ledger", j("ledger.jsonl"), "--observations", j("observations.json"), "--out", j("summary.json")); err != nil {
		return nil, err
	}
	summaryRaw, err := os.ReadFile(j("summary.json"))
	if err != nil {
		return nil, err
	}
	var summary map[string]any
	return summary, json.Unmarshal(summaryRaw, &summary)
}

func dynamicsProjectionCopy(raw map[string]any, change map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range raw {
		out[k] = v
	}
	for k, v := range change {
		out[k] = v
	}
	return out
}

// dynamicsProjectionEdit copies the request rows and changes the first one edit accepts.
func dynamicsProjectionEdit(t *testing.T, rows []*dynamicsMeasureRow, edit func(*dynamicsMeasureRow) bool) []*dynamicsMeasureRow {
	t.Helper()
	out, done := []*dynamicsMeasureRow{}, false
	for _, r := range rows {
		c := *r
		if !done && edit(&c) {
			done = true
		}
		out = append(out, &c)
	}
	if !done {
		t.Fatal("alteration found no row")
	}
	return out
}

type dynamicsProjectionChange struct {
	code string
	edit map[string]any
}

// dynamicsProjectionRefusals lists altered copies the adapter or summarizer must refuse, with the expected code.
func dynamicsProjectionRefusals(t *testing.T, raw map[string]any) map[string]dynamicsProjectionChange {
	t.Helper()
	ingress := raw["ingress"].([]dynamicsIngress)
	rows := raw["requests"].([]*dynamicsMeasureRow)
	received := map[string]int64{}
	for _, in := range ingress {
		received[in.RequestID] = in.ReceivedNS
	}
	refused := map[string]dynamicsProjectionChange{
		"ingress dropped":    {"missing_ledger", map[string]any{"ingress": ingress[1:]}},
		"ingress duplicated": {"duplicate_record", map[string]any{"ingress": append(append([]dynamicsIngress{}, ingress...), ingress[0])}},
		"accepted before received": {"invalid_time", map[string]any{"requests": dynamicsProjectionEdit(t, rows, func(r *dynamicsMeasureRow) bool {
			if r.Op == "update" && r.RawAcceptedNS != 0 && r.DurableWitnessNS != nil {
				r.RawAcceptedNS = received[r.RequestID] - 1
				return true
			}
			return false
		})}},
		"read relabeled as update": {"request_mismatch", map[string]any{"requests": dynamicsProjectionEdit(t, rows, func(r *dynamicsMeasureRow) bool {
			if r.Op == "read" {
				r.Op = "update"
				return true
			}
			return false
		})}},
		"config swapped": {"config_mismatch", map[string]any{"config_sha256": strings.Repeat("0", 64)}},
	}
	if repairs := raw["repairs"].([]dynamicsProjectionRepair); len(repairs) == 1 {
		for name, at := range map[string]int64{"hold seen before its origin": repairs[0].StartNS - 1, "hold seen after the republish": repairs[0].PublishedNS + 1} {
			moved := append([]dynamicsProjectionRepair{}, repairs...)
			moved[0].HoldWitnessedNS = at
			refused[name] = dynamicsProjectionChange{"invalid_time", map[string]any{"repairs": moved}}
		}
	}
	return refused
}

// dynamicsProjectionNegatives: altered copies of the real raw run must be refused
// with the intended code, or lose readiness; each alteration changes one thing.
func dynamicsProjectionNegatives(t *testing.T, dir string, raw map[string]any) {
	t.Helper()
	rows := raw["requests"].([]*dynamicsMeasureRow)
	for name, c := range dynamicsProjectionRefusals(t, raw) {
		_, err := dynamicsProjectionTrySummary(dir, "neg-", dynamicsProjectionCopy(raw, c.edit))
		if err == nil || !strings.Contains(err.Error(), `"error":"`+c.code+`"`) {
			t.Fatalf("altered run %s: want %s, got %v", name, c.code, err)
		}
	}
	failed := dynamicsProjectionCopy(raw, map[string]any{"requests": dynamicsProjectionEdit(t, rows, func(r *dynamicsMeasureRow) bool {
		if r.QualityPassed != nil {
			no := false
			r.QualityPassed = &no
			return true
		}
		return false
	})})
	if summary, err := dynamicsProjectionTrySummary(dir, "neg-", failed); err != nil || summary["comparison_input_ready"] != false {
		t.Fatalf("failed quality kept readiness: %v %v", err, summary["comparison_input_ready"])
	}
	if raw["mode"] == "async" {
		dropped := dynamicsProjectionCopy(raw, map[string]any{"repairs": []dynamicsProjectionRepair{}})
		if summary, err := dynamicsProjectionTrySummary(dir, "neg-", dropped); err != nil || summary["v2_repair_coverage_ready"] != false {
			t.Fatalf("dropped repair kept coverage: %v %v", err, summary["v2_repair_coverage_ready"])
		}
	}
	for _, name := range []string{"raw-run.json", "ledger.jsonl", "observations.json", "summary.json"} {
		_ = os.Remove(filepath.Join(dir, "neg-"+name))
	}
}

func TestDynamicsMeasurementProjectionPG(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required to run the unchanged A tools")
	}
	root := os.Getenv("SYS05_MEASUREMENT_DIR")
	if root == "" {
		root = t.TempDir()
	}
	report := map[string]any{}
	for _, mode := range []string{"async", "sync"} {
		dir := filepath.Join(root, "av2-"+mode)
		// The A tools never overwrite, so a repeated run (plain then race) starts from an empty folder.
		dynamicsRebindExpect(t, os.RemoveAll(dir), nil, "clear dir")
		dynamicsRebindExpect(t, os.MkdirAll(dir, 0o700), nil, "dir")
		raw := dynamicsProjectionRun(t, dir, mode)
		summary := dynamicsProjectionSummary(t, dir, raw)
		if rec := summary["reconciliation"].(map[string]any); rec["complete"] != true {
			t.Fatalf("%s reconciliation: %v", mode, rec)
		}
		repair := summary["repair"].(map[string]any)
		if ready := summary["comparison_input_ready"]; ready != (mode == "async") {
			t.Fatalf("%s comparison_input_ready: %v", mode, ready)
		}
		if mode == "async" && (summary["v2_repair_coverage_ready"] != true || repair["reported_events"] != float64(1)) {
			t.Fatalf("async repair coverage: %v", repair)
		}
		if mode == "sync" && (summary["v2_repair_coverage_ready"] != false || repair["reported_events"] != float64(0)) {
			t.Fatalf("sync repair coverage: %v", repair)
		}
		dynamicsProjectionNegatives(t, dir, raw)
		report[mode] = map[string]any{"summary": summary, "raw_run_sha256": dynamicsSHA(mustJSON(t, raw))}
	}
	out, err := json.MarshalIndent(report, "", "  ")
	dynamicsRebindExpect(t, err, nil, "report")
	dynamicsRebindExpect(t, os.WriteFile(filepath.Join(root, "sys05-b5-av2-projection.json"), out, 0o600), nil, "write report")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	dynamicsRebindExpect(t, err, nil, "json")
	return raw
}
