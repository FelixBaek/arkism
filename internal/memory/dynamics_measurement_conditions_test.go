package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// SYS-05 B5 condition cells: each mode runs the same synthetic plan with a cold
// search, a concurrent burst, a worker restart with an expired lease, and a
// human delete racing a queued update. Results are recorded as observed; no
// condition is invented for a mode that does not reach it.

// dynamicsMeasureBurst sends n distinct updates at once through the real handler.
func dynamicsMeasureBurst(h *dynamicsMeasureHarness, base MemoryOutcomeInput, n int) []*dynamicsMeasureRow {
	h.t.Helper()
	if len(h.rows)+n > dynamicsMeasureAttemptBudget {
		h.t.Fatal("measurement attempt budget exceeded")
	}
	rows := make([]*dynamicsMeasureRow, n)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range rows {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := base
			in.EventID = "burst-" + strconv.Itoa(i+1)
			// Each request gets its own harness so only the ingress append is shared.
			burst := &dynamicsMeasureHarness{t: h.t, p: h.p, mode: h.mode, runID: h.runID, server: h.server}
			row := burst.outcome("b"+strconv.Itoa(i+1), "update", in, h.p.checkerToken)
			mu.Lock()
			rows[i] = row
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	for i, row := range rows {
		if row == nil {
			h.t.Fatalf("burst request %d did not complete", i+1)
		}
	}
	// Keep the request ledger in the order the server received the requests.
	byID := map[string]*dynamicsMeasureRow{}
	for _, row := range rows {
		byID[row.RequestID] = row
	}
	for _, in := range h.ingress[len(h.ingress)-n:] {
		h.rows = append(h.rows, byID[in.RequestID])
	}
	return rows
}

// dynamicsMeasureRestart expires the current lease and swaps in a fresh runtime,
// as a restarted worker process would see it; the old claim must be fenced.
func dynamicsMeasureRestart(t *testing.T, p *nativePGFixture, mode string) map[string]any {
	t.Helper()
	out := map[string]any{"pending_before": dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Count}
	old, err := p.worker.claimDynamics(testCtx, scopeA)
	if err != nil {
		out["old_claim"] = err.Error()
	}
	dynamicsRebindExpect(t, p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		c, e := dynamicsLockCoordinator(ctx, tx, scopeA)
		if e != nil {
			return e
		}
		c.DynamicsLoad.LeaseUntilNS = dynamicsNowNS() - 1
		return dynamicsReplace(ctx, tx, c)
	}), nil, "expire lease")
	config := dynamicsFixtureConfig()
	config.Mode = mode
	restart := dynamicsFixtureRuntime(t, config)
	p.checker.DynamicsLoad, p.worker.DynamicsLoad = restart, restart
	if _, err = restart.DynamicsSnapshot(scopeA, dynamicsNowNS()); err == nil {
		t.Fatal("restart inherited the snapshot cache")
	}
	if old.ID != "" {
		events := len(p.rows(t, "event"))
		current, err := p.worker.claimDynamics(testCtx, scopeA)
		dynamicsRebindExpect(t, err, nil, "restarted worker claim")
		if err = p.worker.applyDynamicsClaim(testCtx, p.workerToken, scopeA, old); !errors.Is(err, ErrConflict) {
			t.Fatal("stale lease applied after restart:", err)
		}
		if len(p.rows(t, "event")) != events {
			t.Fatal("stale writer mutated native state")
		}
		dynamicsRebindExpect(t, p.worker.applyDynamicsClaim(testCtx, p.workerToken, scopeA, current), nil, "restarted worker apply")
		out["stale_claim_fenced"] = true
	}
	return out
}

// dynamicsMeasureDelete queues one update, deletes its target as a human, then
// drains; the deleted memory must not come back and publication is withheld.
func dynamicsMeasureDelete(h *dynamicsMeasureHarness, base MemoryOutcomeInput) (map[string]any, time.Time) {
	h.t.Helper()
	in := base
	in.EventID = "delete-race"
	row := h.outcome("d1", "update", in, h.p.checkerToken)
	pending := dynamicsRebindCoordinator(h.t, h.p.admin).DynamicsLoad.Count
	fixture := newFixture(h.t)
	fixture.svc.Repo = h.p.admin
	target, err := h.p.admin.Get(testCtx, colMemories, scopeA, base.Target.ID)
	dynamicsRebindExpect(h.t, err, nil, "target before delete")
	start := time.Now()
	dynamicsRebindExpect(h.t, fixture.svc.Delete(testCtx, fixture.human, colMemories, target.ID, ChangeInput{Scope: scopeA, ExpectedRevision: target.Revision}), nil, "human delete")
	h.drain()
	_, err = h.p.admin.Get(testCtx, colMemories, scopeA, base.Target.ID)
	return map[string]any{
		"update_status": row.HTTPStatus, "update_code": row.Code, "pending_at_delete": pending,
		"target_after_drain":   dynamicsErrName(err),
		"publish_after_delete": dynamicsErrName(h.p.worker.PublishDynamicsSnapshot(testCtx, h.p.workerToken, scopeA)),
	}, start
}

// dynamicsMeasureRepair runs the existing purge step, then the explicit
// verify-then-republish step, and records the serving outage since the delete.
func dynamicsMeasureRepair(h *dynamicsMeasureHarness, deleted time.Time) map[string]any {
	h.t.Helper()
	purges := 0
	for _, purge := range h.p.rows(h.t, "purge") {
		for done := false; !done && purges < 64; purges++ {
			var err error
			done, err = h.p.worker.ProcessNativeDynamicsPurge(testCtx, h.p.workerToken, scopeA, Ref{Collection: colJobs, ID: purge.ID, Revision: purge.Revision}, 64)
			dynamicsRebindExpect(h.t, err, nil, "purge step")
		}
	}
	repair := dynamicsErrName(h.p.worker.RepairDynamicsPublication(testCtx, h.p.workerToken, scopeA))
	publish := repair
	if repair == "conflict" {
		// Nothing was withheld (sync applied before the delete); only the purge held publication.
		publish = dynamicsErrName(h.p.worker.PublishDynamicsSnapshot(testCtx, h.p.workerToken, scopeA))
	}
	outage := time.Since(deleted).Nanoseconds()
	observation, err := h.p.worker.ObserveDynamicsLoad(testCtx, h.p.workerToken, scopeA)
	dynamicsRebindExpect(h.t, err, nil, "observe after repair")
	var span *int64
	if observation.LastRepairStartNS != nil && observation.LastRepairEndNS != nil {
		v := *observation.LastRepairEndNS - *observation.LastRepairStartNS
		span = &v
	}
	return map[string]any{
		"purge_steps": purges, "repair_result": repair, "publish_after_repair": publish,
		"withheld_after": observation.Withheld, "repair_start_after": observation.RepairStartNS,
		"repair_ns": span, "outage_ns": outage,
	}
}

func dynamicsErrName(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrDynamicsSnapshotUnavailable):
		return "snapshot_unavailable"
	case errors.Is(err, ErrConflict):
		return "conflict"
	}
	return "error"
}

func (h *dynamicsMeasureHarness) drain() {
	h.t.Helper()
	for step := 0; step < 16 && dynamicsRebindCoordinator(h.t, h.p.admin).DynamicsLoad.Count > 0; step++ {
		dynamicsRebindExpect(h.t, h.p.worker.WorkDynamicsOnce(testCtx, h.p.workerToken), nil, "worker round")
		h.witness()
	}
	if c := dynamicsRebindCoordinator(h.t, h.p.admin).DynamicsLoad; c.Count != 0 {
		h.t.Fatalf("queue did not drain: %+v", c)
	}
}

func dynamicsMeasureConditions(t *testing.T, mode, runID string) map[string]any {
	t.Helper()
	p, shadow, search := dynamicsFunctionalSetup(t)
	config := dynamicsFixtureConfig()
	config.Mode = mode
	runtime := dynamicsFixtureRuntime(t, config)
	p.checker.DynamicsLoad, p.worker.DynamicsLoad = runtime, runtime
	if mode == "sync" {
		actions := []string{"db:read", "db:write", "read:evaluations", "read:agent_runs", "read:execution_traces", "read:sources", "read:memories", "write:evaluations", "read:jobs", "write:jobs"}
		dynamicsRebindExpect(t, p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "native-pg-checker", Role: roleService, Token: p.checkerToken, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: actions}), nil, "sync caller authority")
	}
	h := &dynamicsMeasureHarness{t: t, p: p, mode: mode, runID: runID}
	h.serve(p.checker)
	reader := &dynamicsMeasureHarness{t: t, p: p, mode: mode, runID: runID}
	reader.serve(p.worker)
	// Each search records whether its own response body names the target memory.
	served := map[string]bool{}
	searchAt := func(id string) *dynamicsMeasureRow {
		reader.rows = h.rows
		before := len(shadow.Observations())
		row, out := reader.post(id, "search", "/v1/search", p.workerToken, search)
		if o := shadow.Observations(); row.HTTPStatus == http.StatusOK && len(o) == before+1 {
			row.SearchStatus = o[len(o)-1].Status
		}
		served[id] = row.HTTPStatus == http.StatusOK && strings.Contains(string(out), p.in.Target.ID)
		h.rows = reader.rows
		h.ingress, reader.ingress = append(h.ingress, reader.ingress...), nil
		h.markVisible(row)
		return row
	}
	cold := searchAt("cold")
	burst := dynamicsMeasureBurst(h, p.in, 6)
	h.witness()
	restart := dynamicsMeasureRestart(t, p, mode)
	h.drain()
	h.publish()
	warm := searchAt("warm")
	deleted, deletedAt := dynamicsMeasureDelete(h, p.in)
	after := searchAt("after_delete")
	// Costs are read before the purge, which scrubs the stored receipts.
	ledger, err := json.Marshal(map[string]any{"ingress": h.ingress, "requests": h.rows})
	dynamicsRebindExpect(t, err, nil, "ledger")
	costs := dynamicsSliceCosts(t, p, dynamicsCosts(t, p, int64(len(ledger))))
	repaired := dynamicsMeasureRepair(h, deletedAt)
	afterRepair := searchAt("after_repair")
	final := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad
	return map[string]any{
		"run_id": runID, "mode": mode, "ingress": h.ingress, "requests": h.rows,
		"cold_search": cold.SearchStatus, "warm_search": warm.SearchStatus, "after_delete_search": after.SearchStatus,
		"warm_serves_target": served["warm"], "after_delete_serves_target": served["after_delete"],
		"after_repair_search": afterRepair.SearchStatus, "after_repair_serves_target": served["after_repair"], "repair": repaired,
		"burst_statuses": dynamicsMeasureStatuses(burst),
		"restart":        restart, "delete_race": deleted,
		"queue_final": map[string]int64{"head": final.Head, "tail": final.Tail, "count": final.Count},
		"costs":       costs,
	}
}

// dynamicsSliceCosts fills receipt_bytes and replay_bytes from the exact stored
// payload bytes of each native_dynamics record: the raw JSON slice of its
// receipt and of its retained input, never a re-encoding.
func dynamicsSliceCosts(t *testing.T, p *nativePGFixture, costs []dynamicsCost) []dynamicsCost {
	t.Helper()
	rows, err := p.admin.q().Query(testCtx, `SELECT payload FROM memory_records WHERE collection='jobs' AND kind LIKE 'native\_dynamics\_%'`)
	dynamicsRebindExpect(t, err, nil, "payload query")
	defer rows.Close()
	var receiptB, receiptN, inputB, inputN int64
	for rows.Next() {
		var raw []byte
		dynamicsRebindExpect(t, rows.Scan(&raw), nil, "payload")
		var record struct {
			Native map[string]json.RawMessage `json:"native_dynamics"`
		}
		dynamicsRebindExpect(t, json.Unmarshal(raw, &record), nil, "payload decode")
		if r := record.Native["receipt"]; len(r) > 0 && string(r) != "null" {
			receiptB, receiptN = receiptB+int64(len(r)), receiptN+1
		}
		if in := record.Native["input"]; len(in) > 0 && string(in) != "null" {
			inputB, inputN = inputB+int64(len(in)), inputN+1
		}
	}
	dynamicsRebindExpect(t, rows.Err(), nil, "payload rows")
	for i := range costs {
		switch costs[i].Name {
		case "receipt_bytes":
			costs[i] = dynamicsCost{"receipt_bytes", &receiptB, &receiptN, "sum of raw stored JSON slices of native_dynamics.receipt over every stored copy (capture, event, seen/consumption, state); rows count copies, not receipts", "logical component; overlaps ledger_bytes/state_bytes, not additive; no receipt spool file in this run"}
		case "replay_bytes":
			costs[i] = dynamicsCost{"replay_bytes", &inputB, &inputN, "raw stored JSON slice of native_dynamics.input retained for replay in jobs payloads", "logical component; overlaps ledger_bytes, not additive; replay CPU not expressed; no separate replay file exists in this run"}
		}
	}
	return costs
}

func dynamicsMeasureStatuses(rows []*dynamicsMeasureRow) map[string]int {
	out := map[string]int{}
	for _, row := range rows {
		out[strconv.Itoa(row.HTTPStatus)+":"+row.Code]++
	}
	return out
}

func TestDynamicsMeasurementConditionsPG(t *testing.T) {
	runs := []map[string]any{}
	for _, mode := range []string{"async", "sync"} {
		run := dynamicsMeasureConditions(t, mode, "conditions-"+mode)
		if err := dynamicsConditionCheck(run); err != nil {
			t.Fatal(mode, err)
		}
		dynamicsConditionNegatives(t, run)
		runs = append(runs, run)
	}
	report := map[string]any{
		"protocol": "sys05-b5-conditions-v1", "classification": "development_condition_cells_not_locked_comparison", "comparison_ready": false,
		"limits": "N=1 per cell, same in-process synthetic plan, burst of 6 concurrent updates on one trace (duplicates, not independent learning); sync burst on one target is refused by revision conflicts, recorded as observed; restart is an in-process runtime swap with an expired lease, not a separate process, and in sync nothing is pending so no stale claim is fenced; in sync the update applies before the delete, so there is no queued race; only async withholds, so only async closes a repair interval, measured server-side, while the outage is measured by this harness; no sustained overload; receipt_bytes and replay_bytes are logical stored slices, not additive with other costs",
		"runs":   runs,
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	dynamicsRebindExpect(t, err, nil, "report")
	root := os.Getenv("SYS05_MEASUREMENT_DIR")
	if root == "" {
		root = t.TempDir()
	}
	dynamicsRebindExpect(t, os.WriteFile(filepath.Join(root, "sys05-b5-conditions.json"), raw, 0o600), nil, "write report")
}

var dynamicsConditionPlan = []string{"cold", "b1", "b2", "b3", "b4", "b5", "b6", "d1", "warm", "after_delete", "after_repair"}

// dynamicsConditionCheck reconciles the full denominator and the condition outcomes.
func dynamicsConditionCheck(run map[string]any) error {
	rows, ingress := run["requests"].([]*dynamicsMeasureRow), run["ingress"].([]dynamicsIngress)
	byID, err := dynamicsMeasureIndex(rows, ingress)
	if err != nil {
		return err
	}
	if len(byID) != len(dynamicsConditionPlan) {
		return fmt.Errorf("planned %d requests, got %d", len(dynamicsConditionPlan), len(byID))
	}
	for _, id := range dynamicsConditionPlan {
		if byID[id] == nil {
			return fmt.Errorf("plan request %s missing", id)
		}
	}
	mode := run["mode"].(string)
	if err = dynamicsBurstCheck(rows, mode); err != nil {
		return err
	}
	if run["cold_search"] != "fallback" || run["warm_search"] != "selected" || run["warm_serves_target"] != true {
		return fmt.Errorf("cold/warm: %v/%v served=%v", run["cold_search"], run["warm_search"], run["warm_serves_target"])
	}
	if run["after_delete_search"] != "fallback" || run["after_delete_serves_target"] != false {
		return fmt.Errorf("deleted memory served after the race: %v", run["after_delete_search"])
	}
	// A restart with pending work must fence the stale claim; async must have pending work.
	restart := run["restart"].(map[string]any)
	pending := restart["pending_before"].(int64)
	if pending > 0 && restart["stale_claim_fenced"] != true || mode == "async" && pending == 0 {
		return fmt.Errorf("restart did not fence a stale claim: %v", restart)
	}
	if err = dynamicsSliceCheck(run["costs"].([]dynamicsCost)); err != nil {
		return err
	}
	if err = dynamicsDeleteCheck(run["delete_race"].(map[string]any), mode); err != nil {
		return err
	}
	return dynamicsRepairCheck(run, mode)
}

// After the purge and the explicit repair, publication resumes without the
// deleted memory. Only async withheld, so only async closes a repair interval.
func dynamicsRepairCheck(run map[string]any, mode string) error {
	r := run["repair"].(map[string]any)
	want := map[string]string{"async": "ok", "sync": "conflict"}[mode]
	span, _ := r["repair_ns"].(*int64)
	if r["repair_result"] != want || r["publish_after_repair"] != "ok" || r["withheld_after"] != false || r["repair_start_after"] != (*int64)(nil) {
		return fmt.Errorf("repair: %v", r)
	}
	if mode == "async" && (span == nil || *span <= 0) || mode == "sync" && span != nil {
		return fmt.Errorf("repair interval: %v", r)
	}
	if outage, _ := r["outage_ns"].(int64); outage <= 0 || span != nil && *span > outage {
		return fmt.Errorf("serving outage: %v", r)
	}
	if run["after_repair_search"] != "selected" || run["after_repair_serves_target"] != false {
		return fmt.Errorf("after repair: %v served=%v", run["after_repair_search"], run["after_repair_serves_target"])
	}
	return nil
}

func dynamicsSliceCheck(costs []dynamicsCost) error {
	for _, c := range costs {
		if (c.Name == "receipt_bytes" || c.Name == "replay_bytes") && (c.Bytes == nil || *c.Bytes <= 0) {
			return fmt.Errorf("%s not measured", c.Name)
		}
	}
	return nil
}

// In async the update must still be queued when the human deletes its target.
func dynamicsDeleteCheck(d map[string]any, mode string) error {
	if d["update_status"] != http.StatusOK || d["target_after_drain"] != "not_found" || d["publish_after_delete"] != "snapshot_unavailable" {
		return fmt.Errorf("delete race: %v", d)
	}
	if mode == "async" && d["pending_at_delete"] != int64(1) {
		return fmt.Errorf("async delete did not race a queued update: %v", d)
	}
	return nil
}

// Every burst request is applied or refused with its mode's recorded reason.
func dynamicsBurstCheck(rows []*dynamicsMeasureRow, mode string) error {
	status, code := http.StatusTooManyRequests, "dynamics_queue_full"
	if mode == "sync" {
		status, code = http.StatusConflict, "revision_conflict"
	}
	accepted := 0
	for _, row := range rows {
		if !strings.HasPrefix(row.RequestID, "b") {
			continue
		}
		switch {
		case row.HTTPStatus == http.StatusOK && row.AppliedWitnessNS != nil:
			accepted++
		case row.HTTPStatus == http.StatusOK:
			return fmt.Errorf("accepted burst update not applied: %s", row.RequestID)
		case row.HTTPStatus != status || row.Code != code:
			return fmt.Errorf("burst %s refused for an unrecorded reason: %d %q", row.RequestID, row.HTTPStatus, row.Code)
		}
	}
	if accepted == 0 {
		return fmt.Errorf("burst accepted nothing")
	}
	return nil
}

func dynamicsConditionEdit(r map[string]any, edit func(*dynamicsMeasureRow)) {
	rows := r["requests"].([]*dynamicsMeasureRow)
	out := make([]*dynamicsMeasureRow, len(rows))
	for i, row := range rows {
		c := *row
		edit(&c)
		out[i] = &c
	}
	r["requests"] = out
}

func dynamicsConditionSet(key string, value any) func(map[string]any) {
	return func(r map[string]any) { r[key] = value }
}

func dynamicsConditionDelete(key string, value any) func(map[string]any) {
	return dynamicsConditionNested("delete_race", key, value)
}

func dynamicsConditionNested(section, key string, value any) func(map[string]any) {
	return func(r map[string]any) {
		d := map[string]any{}
		for k, v := range r[section].(map[string]any) {
			d[k] = v
		}
		d[key] = value
		r[section] = d
	}
}

// Altered copies of a real condition run must be refused, one change at a time.
func dynamicsConditionNegatives(t *testing.T, run map[string]any) {
	t.Helper()
	alterations := dynamicsBurstAlterations()
	for name, alter := range map[string]func(map[string]any){
		"cold served as selected":   dynamicsConditionSet("cold_search", "selected"),
		"cold request lost":         dynamicsConditionSet("cold_search", ""),
		"warm did not serve target": dynamicsConditionSet("warm_serves_target", false),
		"deleted memory served":     dynamicsConditionSet("after_delete_serves_target", true),
		"restart fence missing":     dynamicsConditionSet("restart", map[string]any{"pending_before": int64(4)}),
		"deleted target revived":    dynamicsConditionDelete("target_after_drain", "ok"),
		"published through repair":  dynamicsConditionDelete("publish_after_delete", "ok"),
		"publish failed otherwise":  dynamicsConditionDelete("publish_after_delete", "error"),
		"raced update refused":      dynamicsConditionDelete("update_status", http.StatusTooManyRequests),
		"repair left the hold":      dynamicsConditionNested("repair", "withheld_after", true),
		"repair not republished":    dynamicsConditionNested("repair", "publish_after_repair", "snapshot_unavailable"),
		"repair interval swapped": func(r map[string]any) {
			dynamicsConditionNested("repair", "repair_ns", map[string]*int64{"async": nil, "sync": new(int64(1))}[r["mode"].(string)])(r)
		},
		"no serving outage": dynamicsConditionNested("repair", "outage_ns", int64(0)),
		"repair longer than outage": func(r map[string]any) { // only async has an interval to compare
			if r["mode"] == "sync" {
				dynamicsConditionNested("repair", "repair_ns", new(int64(2)))(r)
			}
			dynamicsConditionNested("repair", "outage_ns", int64(1))(r)
		},
		"repair result altered":       dynamicsConditionNested("repair", "repair_result", "snapshot_unavailable"),
		"repair origin left":          dynamicsConditionNested("repair", "repair_start_after", new(int64(1))),
		"deleted served after repair": dynamicsConditionSet("after_repair_serves_target", true),
		"no selection after repair":   dynamicsConditionSet("after_repair_search", "fallback"),
		"receipt cost null": func(r map[string]any) {
			costs := append([]dynamicsCost{}, r["costs"].([]dynamicsCost)...)
			for i := range costs {
				if costs[i].Name == "receipt_bytes" {
					costs[i].Bytes = nil
				}
			}
			r["costs"] = costs
		},
	} {
		alterations[name] = alter
	}
	for name, alter := range alterations {
		copied := map[string]any{}
		for k, v := range run {
			copied[k] = v
		}
		alter(copied)
		if dynamicsConditionCheck(copied) == nil {
			t.Fatal("negative control accepted:", name)
		}
	}
}

func dynamicsIsBurst(c *dynamicsMeasureRow) bool { return strings.HasPrefix(c.RequestID, "b") }

// Burst alterations: each hides, swaps or drops what the burst cell recorded.
func dynamicsBurstAlterations() map[string]func(map[string]any) {
	isBurst := dynamicsIsBurst
	return map[string]func(map[string]any){
		"burst refusal hidden": func(r map[string]any) {
			dynamicsConditionEdit(r, func(c *dynamicsMeasureRow) {
				if isBurst(c) && c.HTTPStatus != http.StatusOK {
					c.HTTPStatus, c.Code = http.StatusInternalServerError, "internal"
				}
			})
		},
		"burst refusal from the other mode": func(r map[string]any) {
			dynamicsConditionEdit(r, func(c *dynamicsMeasureRow) {
				if isBurst(c) && c.HTTPStatus != http.StatusOK {
					c.Code = map[string]string{"dynamics_queue_full": "revision_conflict", "revision_conflict": "dynamics_queue_full"}[c.Code]
				}
			})
		},
		"accepted burst not applied": func(r map[string]any) {
			dynamicsConditionEdit(r, func(c *dynamicsMeasureRow) {
				if isBurst(c) && c.HTTPStatus == http.StatusOK {
					c.AppliedWitnessNS = nil
				}
			})
		},
		"burst request dropped": func(r map[string]any) {
			rows, ingress := r["requests"].([]*dynamicsMeasureRow), r["ingress"].([]dynamicsIngress)
			for i, row := range rows {
				if isBurst(row) {
					r["requests"] = append(append([]*dynamicsMeasureRow{}, rows[:i]...), rows[i+1:]...)
					r["ingress"] = append(append([]dynamicsIngress{}, ingress[:i]...), ingress[i+1:]...)
					return
				}
			}
		},
	}
}
