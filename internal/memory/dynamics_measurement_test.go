package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// SYS-05 B5 development pilot: the same small synthetic request plan runs once
// per mode through the real Handler on a loopback server. It checks that the
// three ledgers and the cost channel can be collected; it is not a locked
// comparison, a performance claim or a tail estimate.
const dynamicsMeasureAttemptBudget = 64

type dynamicsIngress struct {
	RequestID  string `json:"request_id"`
	Path       string `json:"path"`
	BodySHA256 string `json:"body_sha256"`
	ReceivedNS int64  `json:"received_ns"`
}

type dynamicsMeasureRow struct {
	RunID            string `json:"run_id"`
	Mode             string `json:"mode"`
	RequestID        string `json:"request_id"`
	Op               string `json:"op"`
	EventID          string `json:"event_id,omitempty"`
	SentNS           int64  `json:"client_sent_ns"`
	CompletedNS      int64  `json:"completed_ns"`
	HTTPStatus       int    `json:"http_status"`
	Code             string `json:"code,omitempty"`
	Evaluation       *Ref   `json:"evaluation,omitempty"`
	Ordinal          int64  `json:"queue_ordinal,omitempty"`
	RawAcceptedNS    int64  `json:"raw_entry_accepted_ns,omitempty"`
	RawStartedNS     int64  `json:"raw_entry_started_ns,omitempty"`
	RawAppliedNS     int64  `json:"raw_entry_applied_ns,omitempty"`
	DurableWitnessNS *int64 `json:"durable_witness_ns"`
	AppliedWitnessNS *int64 `json:"applied_witness_ns"`
	VisibleWitnessNS *int64 `json:"visible_witness_ns"`
	Disposition      string `json:"receipt_disposition,omitempty"`
	AppliedNow       *bool  `json:"receipt_applied_now"`
	LearningConsumed *bool  `json:"receipt_learning_consumed"`
	SearchStatus     string `json:"search_observation_status,omitempty"`
}

type dynamicsMeasureHarness struct {
	t       *testing.T
	p       *nativePGFixture
	mode    string
	runID   string
	server  *httptest.Server
	mu      sync.Mutex
	ingress []dynamicsIngress
	rows    []*dynamicsMeasureRow
}

// The ingress record is appended before the Service sees the request, from the
// exact bytes received; it is never rebuilt from responses.
func (h *dynamicsMeasureHarness) serve(svc *Service) {
	inner := Handler(svc)
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received := time.Now().UnixNano()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "ingress read", http.StatusBadRequest)
			return
		}
		sum := sha256.Sum256(body)
		h.mu.Lock()
		h.ingress = append(h.ingress, dynamicsIngress{r.Header.Get("X-Measure-Request"), r.URL.Path, hex.EncodeToString(sum[:]), received})
		h.mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(body))
		inner.ServeHTTP(w, r)
	}))
	h.t.Cleanup(h.server.Close)
}

func (h *dynamicsMeasureHarness) post(id, op, path, token string, body any) (*dynamicsMeasureRow, []byte) {
	h.t.Helper()
	if len(h.rows) >= dynamicsMeasureAttemptBudget {
		h.t.Fatal("measurement attempt budget exceeded")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(testCtx, http.MethodPost, h.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Measure-Request", id)
	row := &dynamicsMeasureRow{RunID: h.runID, Mode: h.mode, RequestID: id, Op: op, SentNS: time.Now().UnixNano()}
	resp, err := h.server.Client().Do(req)
	if err != nil {
		h.t.Fatal("transport", err) // the pilot treats a lost response as a failed run, not a rejection
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	row.CompletedNS = time.Now().UnixNano()
	if err != nil {
		h.t.Fatal(err)
	}
	row.HTTPStatus = resp.StatusCode
	if resp.StatusCode != http.StatusOK {
		var failure map[string]string
		_ = json.Unmarshal(out, &failure)
		row.Code = failure["code"]
	}
	h.rows = append(h.rows, row)
	return row, out
}

func (h *dynamicsMeasureHarness) outcome(id, op string, in MemoryOutcomeInput, token string) *dynamicsMeasureRow {
	h.t.Helper()
	row, out := h.post(id, op, "/v1/memory-outcomes", token, in)
	row.EventID = in.EventID
	if row.HTTPStatus == http.StatusOK {
		var ref Ref
		if err := json.Unmarshal(out, &ref); err != nil {
			h.t.Fatal(err)
		}
		row.Evaluation = &ref
	}
	return row
}

func (h *dynamicsMeasureHarness) search(id string, in SearchInput, shadow *NativeSearchShadowRuntime) *dynamicsMeasureRow {
	h.t.Helper()
	before := len(shadow.Observations())
	row, _ := h.post(id, "search", "/v1/search", h.p.workerToken, in)
	// The client is serial; the observation is bound only if this request added exactly one.
	if o := shadow.Observations(); row.HTTPStatus == http.StatusOK && len(o) == before+1 {
		row.SearchStatus = o[len(o)-1].Status
	}
	return row
}

// captures returns each accepted update's capture whose evaluation also exists.
func (h *dynamicsMeasureHarness) captures() map[string]Record {
	out := map[string]Record{}
	for _, row := range h.rows {
		if row.Evaluation == nil || row.Op != "update" {
			continue
		}
		if _, err := h.p.admin.Get(testCtx, "evaluations", scopeA, row.Evaluation.ID); err != nil {
			continue
		}
		if capture, err := h.p.admin.Get(testCtx, colJobs, scopeA, nativeCaptureID(scopeA, *row.Evaluation)); err == nil {
			out[capture.ID] = capture
		}
	}
	return out
}

// witness reads the canonical rows serially; a later stage is recorded only
// when the earlier one is seen in the same or an earlier readback.
func (h *dynamicsMeasureHarness) witness() {
	h.t.Helper()
	c := dynamicsRebindCoordinator(h.t, h.p.admin).DynamicsLoad
	byCapture := map[string]Record{}
	for ordinal := int64(1); ordinal <= c.Tail; ordinal++ {
		var entry Record
		dynamicsRebindExpect(h.t, h.p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
			var e error
			entry, e = tx.(*Postgres).DynamicsLoadLock(ctx, scopeA, dynamicsEntryID(scopeA, ordinal))
			return e
		}), nil, "entry readback")
		byCapture[entry.DynamicsLoad.Capture.ID] = entry
	}
	captures := h.captures()
	// Evaluation, capture and entry were all read before this witness time.
	now := time.Now().UnixNano()
	for _, row := range h.rows {
		if row.Evaluation == nil || row.Op != "update" {
			continue
		}
		entry, ok := byCapture[nativeCaptureID(scopeA, *row.Evaluation)]
		capture, seen := captures[entry.DynamicsLoad.Capture.ID]
		if !ok || !seen {
			continue
		}
		if row.DurableWitnessNS == nil {
			row.DurableWitnessNS, row.Ordinal, row.RawAcceptedNS = &now, entry.DynamicsLoad.Ordinal, entry.DynamicsLoad.AcceptedNS
		}
		row.RawStartedNS, row.RawAppliedNS = entry.DynamicsLoad.StartedNS, entry.DynamicsLoad.AppliedNS
		if r := capture.NativeDynamics.Receipt; row.AppliedWitnessNS == nil && entry.Status == JobStatusDone && r != nil && r.ProcessingSeq != nil {
			applied, consumed := r.AppliedNow, r.LearningConsumed
			row.AppliedWitnessNS, row.Disposition, row.AppliedNow, row.LearningConsumed = &now, r.Disposition, &applied, &consumed
		}
	}
}

// markVisible binds a selected serial search to every update already applied.
func (h *dynamicsMeasureHarness) markVisible(search *dynamicsMeasureRow) {
	if search.SearchStatus != "selected" {
		return
	}
	for _, row := range h.rows {
		if row.AppliedWitnessNS != nil && row.VisibleWitnessNS == nil && *row.AppliedWitnessNS < search.SentNS {
			completed := search.CompletedNS
			row.VisibleWitnessNS = &completed
		}
	}
}

func (h *dynamicsMeasureHarness) publish() {
	h.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		dynamicsRebindExpect(h.t, h.p.worker.PublishDynamicsSnapshot(testCtx, h.p.workerToken, scopeA), nil, "publish")
		if _, err := h.p.worker.DynamicsLoad.DynamicsSnapshot(scopeA, dynamicsNowNS()); err == nil {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatal("snapshot not published")
		}
		time.Sleep(time.Millisecond)
	}
}

type dynamicsCost struct {
	Name   string `json:"name"`
	Bytes  *int64 `json:"bytes"`
	Rows   *int64 `json:"rows,omitempty"`
	Source string `json:"source"`
	Limit  string `json:"limit"`
}

func dynamicsKnown(v int64) *int64 { return &v }

func dynamicsProcRSS(t *testing.T, pid int) *int64 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[0] == "VmRSS:" {
			kb, err := strconv.ParseInt(f[1], 10, 64)
			if err == nil {
				return dynamicsKnown(kb * 1024)
			}
		}
	}
	return nil
}

// dynamicsCosts reads only the own disposable database and this test process.
func dynamicsCosts(t *testing.T, p *nativePGFixture, ledgerBytes int64) []dynamicsCost {
	t.Helper()
	sum := func(where string) (*int64, *int64) {
		var bytes, rows int64
		dynamicsRebindExpect(t, p.admin.q().QueryRow(testCtx, `SELECT COALESCE(SUM(octet_length(payload)),0),COUNT(*) FROM memory_records WHERE `+where).Scan(&bytes, &rows), nil, "cost query")
		return &bytes, &rows
	}
	stateB, stateN := sum(`collection='jobs' AND kind='native_dynamics_state'`)
	queueB, queueN := sum(`collection='jobs' AND (id LIKE 'sys05\_coordinator\_%' OR id LIKE 'sys05\_entry\_%')`)
	eventB, eventN := sum(`collection='jobs' AND kind IN ('native_dynamics_event','native_dynamics_seen','native_dynamics_consumption','native_dynamics_capture')`)
	checkB, checkN := sum(`collection='jobs' AND kind='native_dynamics_checkpoint'`)
	sourceB, sourceN := sum(`collection IN ('memories','sources','execution_traces','agent_runs','evaluations')`)
	ledger := *queueB + *eventB
	ledgerN := *queueN + *eventN
	var dbBytes int64
	dynamicsRebindExpect(t, p.admin.q().QueryRow(testCtx, `SELECT pg_database_size(current_database())`).Scan(&dbBytes), nil, "db size")
	var backendRSS int64
	known := true
	pids, err := p.admin.q().Query(testCtx, `SELECT pid FROM pg_stat_activity WHERE datname=current_database()`)
	dynamicsRebindExpect(t, err, nil, "backend pids")
	for pids.Next() {
		var pid int
		dynamicsRebindExpect(t, pids.Scan(&pid), nil, "pid")
		comm, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
		if rss := dynamicsProcRSS(t, pid); rss != nil && strings.TrimSpace(string(comm)) == "postgres" {
			backendRSS += *rss
		} else {
			known = false
		}
	}
	pids.Close()
	backend := dynamicsKnown(backendRSS)
	if !known {
		backend = nil
	}
	return []dynamicsCost{
		{"state_bytes", stateB, stateN, "stored payload octets, kind native_dynamics_state", "receipts embedded in state are included; not whole-system memory"},
		{"receipt_bytes", nil, nil, "not extracted", "receipt is an embedded BSON component of capture/state payloads; raw slice not measured in this pilot"},
		{"ledger_bytes", &ledger, &ledgerN, "stored payload octets, dynamics_load coordinator/entries incl. terminal + native event/seen/consumption/capture", "external ingress/client ledgers reported separately as log_bytes"},
		{"checkpoint_bytes", checkB, checkN, "stored payload octets, kind native_dynamics_checkpoint", "PostgreSQL checkpoint/WAL excluded"},
		{"replay_bytes", nil, nil, "not measured", "no replay artifact path was enumerated in this pilot"},
		{"source_bytes", sourceB, sourceN, "stored payload octets of memories/sources/execution_traces/agent_runs/evaluations", "blob roots not opened"},
		{"log_bytes", dynamicsKnown(ledgerBytes), nil, "length of this run's ingress+request ledgers as in-memory compact JSON (no file written)", "server/worker/DB logs not captured"},
		{"db_bytes", dynamicsKnown(dbBytes), nil, "pg_database_size of the own disposable test database", "overlaps logical payload; shared WAL excluded"},
		{"rss_bytes", dynamicsProcRSS(t, os.Getpid()), nil, "VmRSS of this test process (API, worker and client share it)", "no API/worker attribution; async runs before sync in the same process, so the sync sample carries order bias; DB backends reported separately"},
		{"rss_db_backends", backend, nil, "sum of VmRSS of this database's backends, host /proc, comm=postgres", "shared pages double counted; null if any backend was unreadable or not a host postgres process"},
	}
}

func dynamicsMeasureMode(t *testing.T, mode, runID string) map[string]any {
	t.Helper()
	p, shadow, search := nativeSearchPGFixture(t)
	dynamicsUseLogin(t, p)
	config := dynamicsFixtureConfig()
	config.Mode = mode
	runtime := dynamicsFixtureRuntime(t, config)
	configRaw, err := json.Marshal(config)
	dynamicsRebindExpect(t, err, nil, "config")
	p.checker.DynamicsLoad, p.worker.DynamicsLoad, p.worker.NativeSearchShadow = runtime, runtime, shadow
	callerAuthority := []string{"db:read", "db:write", "read:evaluations", "read:agent_runs", "read:execution_traces", "read:sources", "read:memories", "write:evaluations"}
	if mode == "sync" {
		// Synchronous apply needs explicit job authority on the caller; recorded, not hidden.
		callerAuthority = append(callerAuthority, "read:jobs", "write:jobs")
		dynamicsRebindExpect(t, p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "native-pg-checker", Role: roleService, Token: p.checkerToken, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: callerAuthority}), nil, "sync caller authority")
	}
	h := &dynamicsMeasureHarness{t: t, p: p, mode: mode, runID: runID}
	h.serve(p.checker)
	searchServer := &dynamicsMeasureHarness{t: t, p: p, mode: mode, runID: runID}
	searchServer.serve(p.worker)
	base := p.in
	for i := 1; i <= 5; i++ {
		in := base
		in.EventID = "measure-" + strconv.Itoa(i)
		h.outcome("u"+strconv.Itoa(i), "update", in, p.checkerToken)
		h.witness()
	}
	replay := base
	replay.EventID = "measure-1"
	h.outcome("r1", "replay", replay, p.checkerToken)
	replay.Passed = !replay.Passed
	h.outcome("c1", "conflict", replay, p.checkerToken)
	for step := 0; step < 8 && dynamicsRebindCoordinator(t, p.admin).DynamicsLoad.Count > 0; step++ {
		dynamicsRebindExpect(t, p.worker.WorkDynamicsOnce(testCtx, p.workerToken), nil, "worker round")
		h.witness()
	}
	h.publish()
	searchServer.rows = h.rows
	s := searchServer.search("s1", search, shadow)
	h.rows = searchServer.rows
	h.markVisible(s)
	h.ingress = append(h.ingress, searchServer.ingress...)
	ledger, err := json.Marshal(map[string]any{"ingress": h.ingress, "requests": h.rows})
	dynamicsRebindExpect(t, err, nil, "ledger")
	final := dynamicsRebindCoordinator(t, p.admin).DynamicsLoad
	return map[string]any{
		"run_id": runID, "mode": mode, "caller_authority": callerAuthority, "runtime_config_sha256": dynamicsSHA(configRaw),
		"ingress": h.ingress, "requests": h.rows,
		"queue_final": map[string]int64{"head": final.Head, "tail": final.Tail, "count": final.Count},
		"costs":       dynamicsCosts(t, p, int64(len(ledger))),
	}
}

// dynamicsMeasureIndex binds each request to its own ingress record, once.
func dynamicsMeasureIndex(rows []*dynamicsMeasureRow, ingress []dynamicsIngress) (map[string]*dynamicsMeasureRow, error) {
	if len(ingress) != len(rows) {
		return nil, fmt.Errorf("ingress %d != requests %d", len(ingress), len(rows))
	}
	byID := map[string]*dynamicsMeasureRow{}
	for i, row := range rows {
		if ingress[i].RequestID != row.RequestID || ingress[i].ReceivedNS < row.SentNS || ingress[i].ReceivedNS > row.CompletedNS {
			return nil, fmt.Errorf("ingress does not bind request %s", row.RequestID)
		}
		if byID[row.RequestID] != nil {
			return nil, fmt.Errorf("request %s counted twice", row.RequestID)
		}
		if row.DurableWitnessNS != nil && row.AppliedWitnessNS != nil && *row.AppliedWitnessNS < *row.DurableWitnessNS {
			return nil, fmt.Errorf("applied witnessed before durable: %s", row.RequestID)
		}
		byID[row.RequestID] = row
	}
	return byID, nil
}

func dynamicsMeasureCheck(rows []*dynamicsMeasureRow, ingress []dynamicsIngress) error {
	byID, err := dynamicsMeasureIndex(rows, ingress)
	if err != nil {
		return err
	}
	for _, id := range []string{"u1", "u2", "u3", "u4", "u5", "r1", "c1", "s1"} {
		if byID[id] == nil {
			return fmt.Errorf("plan request %s missing", id)
		}
	}
	if len(byID) != 8 {
		return fmt.Errorf("unplanned requests: %d", len(byID))
	}
	if u5 := byID["u5"]; u5.HTTPStatus != http.StatusOK && u5.Code != "dynamics_queue_full" {
		return fmt.Errorf("overload request neither applied nor refused as full: %d %q", u5.HTTPStatus, u5.Code)
	}
	if byID["c1"].HTTPStatus != http.StatusConflict || byID["r1"].HTTPStatus != http.StatusOK || byID["r1"].Evaluation == nil || byID["u1"].Evaluation == nil || *byID["r1"].Evaluation != *byID["u1"].Evaluation {
		return errors.New("replay/conflict semantics")
	}
	if byID["s1"].SearchStatus != "selected" {
		return fmt.Errorf("search did not consume the published snapshot: %q", byID["s1"].SearchStatus)
	}
	unique := 0
	for _, row := range rows {
		if row.Op == "update" && row.HTTPStatus == http.StatusOK && (row.AppliedWitnessNS == nil || row.VisibleWitnessNS == nil) {
			return fmt.Errorf("accepted update not applied and visible: %s", row.RequestID)
		}
		if row.LearningConsumed != nil && *row.LearningConsumed {
			unique++
		}
	}
	// Same trace and evidence under new EventIDs: one learning, the rest duplicates.
	if unique != 1 {
		return fmt.Errorf("independent learning count %d, want 1", unique)
	}
	return nil
}

// Each altered copy of a real run must be refused by the same check.
func dynamicsMeasureNegatives(t *testing.T, rows []*dynamicsMeasureRow, ingress []dynamicsIngress) {
	t.Helper()
	clone := func() ([]*dynamicsMeasureRow, []dynamicsIngress) {
		out := make([]*dynamicsMeasureRow, len(rows))
		for i, r := range rows {
			c := *r
			out[i] = &c
		}
		return out, append([]dynamicsIngress{}, ingress...)
	}
	consumed := true
	for name, alter := range map[string]func([]*dynamicsMeasureRow, []dynamicsIngress) ([]*dynamicsMeasureRow, []dynamicsIngress){
		"ingress missing": func(r []*dynamicsMeasureRow, i []dynamicsIngress) ([]*dynamicsMeasureRow, []dynamicsIngress) {
			return r, i[1:]
		},
		"ingress extra": func(r []*dynamicsMeasureRow, i []dynamicsIngress) ([]*dynamicsMeasureRow, []dynamicsIngress) {
			return r, append(i, i[0])
		},
		"retry double count": func(r []*dynamicsMeasureRow, i []dynamicsIngress) ([]*dynamicsMeasureRow, []dynamicsIngress) {
			return append(r, r[0]), append(i, i[0])
		},
		"conflict as success": func(r []*dynamicsMeasureRow, i []dynamicsIngress) ([]*dynamicsMeasureRow, []dynamicsIngress) {
			r[6].HTTPStatus = http.StatusOK
			return r, i
		},
		"duplicate as learning": func(r []*dynamicsMeasureRow, i []dynamicsIngress) ([]*dynamicsMeasureRow, []dynamicsIngress) {
			r[1].LearningConsumed = &consumed
			return r, i
		},
		"baseline as native": func(r []*dynamicsMeasureRow, i []dynamicsIngress) ([]*dynamicsMeasureRow, []dynamicsIngress) {
			r[7].SearchStatus = "baseline"
			return r, i
		},
		"updates dropped together": func(r []*dynamicsMeasureRow, i []dynamicsIngress) ([]*dynamicsMeasureRow, []dynamicsIngress) {
			return append(r[:1:1], r[5:]...), append(i[:1:1], i[5:]...)
		},
		"ingress outside request window": func(r []*dynamicsMeasureRow, i []dynamicsIngress) ([]*dynamicsMeasureRow, []dynamicsIngress) {
			i[2].ReceivedNS = r[2].CompletedNS + 1
			return r, i
		},
		"overload hidden as other failure": func(r []*dynamicsMeasureRow, i []dynamicsIngress) ([]*dynamicsMeasureRow, []dynamicsIngress) {
			r[4].HTTPStatus, r[4].Code = http.StatusInternalServerError, "internal"
			return r, i
		},
		"applied before durable": func(r []*dynamicsMeasureRow, i []dynamicsIngress) ([]*dynamicsMeasureRow, []dynamicsIngress) {
			early := *r[0].DurableWitnessNS - 1
			r[0].AppliedWitnessNS = &early
			return r, i
		},
	} {
		r, i := clone()
		if err := dynamicsMeasureCheck(alter(r, i)); err == nil {
			t.Fatal("negative control accepted:", name)
		}
	}
}

func TestDynamicsMeasurementPilotSyncAsyncPG(t *testing.T) {
	runs := []map[string]any{}
	for _, mode := range []string{"async", "sync"} {
		run := dynamicsMeasureMode(t, mode, "pilot-"+mode)
		rows, ingress := run["requests"].([]*dynamicsMeasureRow), run["ingress"].([]dynamicsIngress)
		if err := dynamicsMeasureCheck(rows, ingress); err != nil {
			t.Fatal(mode, err)
		}
		dynamicsMeasureNegatives(t, rows, ingress)
		runs = append(runs, run)
	}
	async, syncRows := runs[0]["requests"].([]*dynamicsMeasureRow), runs[1]["requests"].([]*dynamicsMeasureRow)
	if async[4].Code != "dynamics_queue_full" || syncRows[4].HTTPStatus != http.StatusOK {
		t.Fatalf("overload outcome: async %d %q, sync %d", async[4].HTTPStatus, async[4].Code, syncRows[4].HTTPStatus)
	}
	report := map[string]any{
		"protocol": "sys05-b5-dev-pilot-v1", "classification": "development_pilot_not_locked_comparison", "comparison_ready": false,
		"limits": "same in-process synthetic plan, N=1 per cell, same trace with distinct EventIDs (duplicates, not independent learning); no restart, burst, sustained overload, cold/warm, delete race or repair window; A-v2 projection not produced; comparison_ready stays false",
		"runs":   runs,
	}
	raw, err := json.MarshalIndent(report, "", "  ")
	dynamicsRebindExpect(t, err, nil, "report")
	root := os.Getenv("SYS05_MEASUREMENT_DIR")
	if root == "" {
		root = t.TempDir()
	}
	dynamicsRebindExpect(t, os.WriteFile(filepath.Join(root, "sys05-b5-dev-pilot.json"), raw, 0o600), nil, "write report")
}

func dynamicsSHA(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// dynamicsMeasureLock is written before the locked run from the pilot evidence;
// the run refuses any lock whose conditions differ from this code.
type dynamicsMeasureLock struct {
	Protocol         string            `json:"protocol"`
	PilotReportSHA   string            `json:"pilot_report_sha256"`
	Plan             []string          `json:"plan"`
	Orders           [][]string        `json:"mode_orders"`
	AttemptBudget    int               `json:"attempt_budget"`
	RuntimeConfigSHA map[string]string `json:"runtime_config_sha256"`
	SelectionRule    string            `json:"selection_rule"`
	Analysis         string            `json:"analysis"`
}

func TestDynamicsMeasurementLockedSyncAsyncPG(t *testing.T) {
	path := os.Getenv("SYS05_MEASUREMENT_LOCK")
	if path == "" {
		t.Skip("SYS05_MEASUREMENT_LOCK is not set; locked comparison not run")
	}
	raw, err := os.ReadFile(path)
	dynamicsRebindExpect(t, err, nil, "lock")
	var lock dynamicsMeasureLock
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	dynamicsRebindExpect(t, decoder.Decode(&lock), nil, "lock decode")
	plan := []string{"u1", "u2", "u3", "u4", "u5", "r1", "c1", "s1"}
	if lock.Protocol != "sys05-b5-locked-v1" || strings.Join(lock.Plan, ",") != strings.Join(plan, ",") || len(lock.Orders) != 2 || len(lock.Orders)*2*len(plan) > lock.AttemptBudget || lock.AttemptBudget > dynamicsMeasureAttemptBudget {
		t.Fatal("lock does not match the coded plan or budget")
	}
	for _, mode := range []string{"async", "sync"} {
		config := dynamicsFixtureConfig()
		config.Mode = mode
		c, err := json.Marshal(config)
		dynamicsRebindExpect(t, err, nil, "config")
		if lock.RuntimeConfigSHA[mode] != dynamicsSHA(c) {
			t.Fatal("lock runtime config differs:", mode, dynamicsSHA(c))
		}
	}
	runs := []map[string]any{}
	for o, order := range lock.Orders {
		if len(order) != 2 || order[0] == order[1] {
			t.Fatal("unbalanced order", order)
		}
		for _, mode := range order {
			// Each mode run builds its own fresh database through the fixture.
			run := dynamicsMeasureMode(t, mode, "locked-o"+strconv.Itoa(o+1)+"-"+mode)
			rows, ingress := run["requests"].([]*dynamicsMeasureRow), run["ingress"].([]dynamicsIngress)
			if err := dynamicsMeasureCheck(rows, ingress); err != nil {
				t.Fatal(mode, err) // after the lock a failed run is reported, never replaced
			}
			run["order"] = o + 1
			runs = append(runs, run)
		}
	}
	report := map[string]any{
		"protocol": lock.Protocol, "lock_sha256": dynamicsSHA(raw), "classification": "locked_small_functional_comparison_not_performance_claim", "comparison_ready": false,
		"limits": "N=2 per mode (one per order), same in-process synthetic plan, same trace with distinct EventIDs; no restart, burst, sustained overload, cold/warm, delete race or repair window; A-v2 projection not produced",
		"runs":   runs,
	}
	out, err := json.MarshalIndent(report, "", "  ")
	dynamicsRebindExpect(t, err, nil, "report")
	root := os.Getenv("SYS05_MEASUREMENT_DIR")
	if root == "" {
		root = t.TempDir()
	}
	dynamicsRebindExpect(t, os.WriteFile(filepath.Join(root, "sys05-b5-locked.json"), out, 0o600), nil, "write report")
}
