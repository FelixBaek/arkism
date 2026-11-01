package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/crewrun"
	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/capture"
	_ "github.com/FelixBaek/arkism/internal/host/crew"
	"github.com/FelixBaek/arkism/internal/memory"
	"github.com/jackc/pgx/v5"
)

func TestDynamicsIntegrationHostOutcomeChainPG(t *testing.T) {
	r := newDaemonRankingHTTPRig(t, false)
	first := r.search(t, 2)
	kept, refs := daemonChainBudget(t, first)
	if len(refs) != 1 || refs[0].ID != r.target {
		t.Fatal("actual host budget did not retain the native-ranked memory")
	}
	producer := daemonChainProducer(t, r)
	trace := daemonChainTrace(t, r, producer, kept, refs)
	daemonChainCheckReceipt(t, r, producer, trace)
	daemonChainAdvance(t, r)
	next := r.search(t, 2)
	if next.Items[0].ID != r.target {
		obs := r.apis[2].NativeSearchShadow.Observations()
		last := obs[len(obs)-1]
		t.Fatal("next query lost learned memory", last.Status, last.Reason, last.Served)
	}
	obs := r.apis[2].NativeSearchShadow.Observations()
	if len(obs) != 2 || !obs[0].Served || !obs[1].Served || obs[0].PublishedSHA == obs[1].PublishedSHA {
		t.Fatal("next query did not observe newly published native state")
	}
}

func daemonChainBudget(t *testing.T, bundle memory.ContextBundle) ([]any, []memory.Ref) {
	t.Helper()
	raw, err := json.Marshal(bundle.Items)
	if err != nil {
		t.Fatal(err)
	}
	var items []any
	if err = json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_RECALL_BUDGET", "user=1:4000")
	kept, scoreDropped, budgetDropped := host.LoadRecallBudget().Apply(items, host.VectorRan([]any{"fts"}, map[string]any{}))
	if len(kept) != 1 || scoreDropped != 0 || budgetDropped != 1 {
		t.Fatal("host did not apply actual kind budget", len(kept), scoreDropped, budgetDropped)
	}
	var refs []memory.Ref
	for _, item := range kept {
		m := item.(map[string]any)
		refs = append(refs, memory.Ref{Collection: "memories", ID: m["id"].(string), Revision: int64(m["revision"].(float64))})
	}
	return kept, refs
}

func daemonChainProducer(t *testing.T, r *daemonFastSlowHTTPRig) string {
	t.Helper()
	token, err := memory.NewPrincipalToken()
	if err != nil {
		t.Fatal(err)
	}
	admin, err := memory.NewPostgres(context.Background(), r.db.url)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	err = admin.ProvisionPrincipal(context.Background(), memory.PrincipalGrant{PrincipalID: "dynamic-chain-producer", Role: "service", Token: token, OrganizationID: "default", ProjectID: "default", Actions: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func daemonChainPOST(t *testing.T, r *daemonFastSlowHTTPRig, path, token string, value, out any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+r.cfgs[2].Listen+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatal("chain HTTP stage refused", path, resp.StatusCode)
	}
	if err = json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func daemonChainTrace(t *testing.T, r *daemonFastSlowHTTPRig, producer string, kept []any, refs []memory.Ref) memory.TraceIngestResult {
	t.Helper()
	input, err := json.Marshal(kept)
	if err != nil {
		t.Fatal(err)
	}
	task := exec.Command("/usr/bin/python3", "-c", "import json,sys; rows=json.load(sys.stdin); sys.exit(0 if len(rows)==1 and 'searchcanary' in rows[0]['text'] else 1)")
	task.Env = []string{"PATH=/usr/bin:/bin"}
	task.Stdin = bytes.NewReader(input)
	if err = task.Run(); err != nil {
		t.Fatal("local deterministic task failed", err)
	}
	contextRaw, err := json.Marshal(map[string]any{"retrieved": refs})
	if err != nil {
		t.Fatal(err)
	}
	resultRaw, err := json.Marshal(map[string]any{"actual_exit": task.ProcessState.ExitCode()})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)
	var trace memory.TraceIngestResult
	daemonChainPOST(t, r, "/v1/execution_traces/completed", producer, memory.TraceIngestInput{Scope: r.scope, TraceID: "dynamic-host-chain", ThreadID: "dynamic-host-thread", AgentRun: memory.CompletedAgentRunInput{AgentID: "synthetic-local-task", TaskType: "agent_session", Outcome: "partial", StartedAt: at, FinishedAt: at.Add(time.Millisecond), Text: "synthetic host budget chain"}, Events: []memory.TraceEventInput{{ID: "dynamic-chain-context", Kind: "context", OccurredAt: at, Payload: contextRaw}, {ID: "dynamic-chain-task", Kind: "tool_result", OccurredAt: at.Add(time.Millisecond), Payload: resultRaw}}}, &trace)
	return trace
}

func daemonChainCheckReceipt(t *testing.T, r *daemonFastSlowHTTPRig, producer string, trace memory.TraceIngestResult) {
	t.Helper()
	root := t.TempDir()
	spool := filepath.Join(root, "receipts")
	receipt := crewrun.TraceReceipt{Scope: map[string]string{"workspace_id": r.scope.WorkspaceID, "project_id": r.scope.ProjectID, "user_id": r.scope.UserID, "agent_id": r.scope.AgentID}, Trace: crewrun.Ref{Collection: trace.Trace.Collection, ID: trace.Trace.ID, Revision: trace.Trace.Revision}}
	if err := crewrun.EnqueueReceipt(spool, receipt); err != nil {
		t.Fatal(err)
	}
	python, err := filepath.EvalSymlinks("/usr/bin/python3")
	if err != nil {
		t.Fatal(err)
	}
	checker := filepath.Join(root, "checker.py")
	code := []byte("import json,sys\nx=json.load(sys.stdin)\ne=x['input']['events'][1]['payload'].get('actual_exit')\nprint(json.dumps({'cost':0,'output':{'observed':e is not None,'passed': e==0 if e is not None else None}}))\n")
	if err = os.WriteFile(checker, code, 0600); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(python)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := json.Marshal(map[string]any{"argv": []string{python, checker}, "files": map[string]string{python: memory.ContentHash(string(image)), checker: memory.ContentHash(string(code))}, "env": map[string]string{}, "timeout_ms": 5000, "max_output_bytes": 4096})
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal([]any{map[string]any{"dimension": "work_task", "environment": filepath.Join(root, "environment.json"), "checker_sha256": memory.ContentHash(string(environment))}})
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string][]byte{"producer.token": []byte(producer), "evaluator.token": []byte(r.db.token), "environment.json": environment, "config.json": config} {
		if err = os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	argv := []string{"--url", "http://" + r.cfgs[2].Listen, "--evaluator-token-file", filepath.Join(root, "evaluator.token"), "--producer-token-file", filepath.Join(root, "producer.token"), "--spool", spool, "--config", filepath.Join(root, "config.json")}
	var stdout, stderr bytes.Buffer
	if code := capture.Outcomes(argv, bytes.NewReader(nil), &stdout, &stderr); code != 0 {
		t.Fatal("actual receipt/checker/outcome chain failed", code, stderr.String())
	}
	var rows []map[string]any
	if err = json.Unmarshal(stdout.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0]["state"] != "done" {
		t.Fatal("canonical receipt was not fully observed", err, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := capture.Outcomes(argv, bytes.NewReader(nil), &stdout, &stderr); code != 0 {
		t.Fatal("receipt retry failed", code, stderr.String())
	}
	if err = json.Unmarshal(stdout.Bytes(), &rows); err != nil || len(rows) != 0 {
		t.Fatal("completed receipt was published twice", err)
	}
}

func daemonChainAdvance(t *testing.T, r *daemonFastSlowHTTPRig) {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), r.db.url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for {
		// This is the same bounded native round deferred by Service.WorkOnce.
		// A completed round provides a publication barrier before the API reads.
		if err = r.worker.WorkDynamicsOnce(context.Background(), r.db.token); err != nil {
			t.Fatal("actual native worker round failed", err)
		}
		var value float64
		err = conn.QueryRow(context.Background(), `SELECT (doc#>>'{native_dynamics,state,value}')::float8 FROM arkism.memory_records WHERE collection='jobs' AND kind='native_dynamics_state' AND doc#>>'{native_dynamics,key,target,id}'=$1`, r.target).Scan(&value)
		if err == nil && math.Abs(value-0.6171875) < 1e-14 {
			if err = r.apis[2].RefreshDynamicsSnapshot(context.Background(), r.cfgs[2].dynamicsReaderToken, r.scope); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("checked state/publication not ready", err, value)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var droppedValue float64
	err = conn.QueryRow(context.Background(), `SELECT (doc#>>'{native_dynamics,state,value}')::float8 FROM arkism.memory_records WHERE collection='jobs' AND kind='native_dynamics_state' AND doc#>>'{native_dynamics,key,target,id}'=$1`, r.other).Scan(&droppedValue)
	if err != nil || droppedValue != 0.5 {
		t.Fatal("host-dropped memory received a reinforcement", err, droppedValue)
	}
}
