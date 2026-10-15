package crewrun

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A finished run is stored once as an execution trace under the producer token, and its ref-only
// receipt lands in the outcome spool (a replay writes no second file).
func TestFinishedRunWritesTraceAndOutcomeReceipt(t *testing.T) {
	flow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"ok":true}`)) }))
	defer flow.Close()
	var traces []map[string]any
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/execution_traces/completed" || r.Header.Get("Authorization") != "Bearer producer" {
			t.Errorf("%s %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		traces = append(traces, body)
		w.Write([]byte(`{"trace":{"collection":"execution_traces","id":"` + body["trace_id"].(string) + `","revision":1},
			"run":{"collection":"agent_runs","id":"r1","revision":1},"source_events":[{"collection":"sources","id":"e1","revision":1}]}`))
	}))
	defer api.Close()
	dir := t.TempDir()
	engineToken, producerToken := filepath.Join(dir, "crew.token"), filepath.Join(dir, "producer.token")
	os.WriteFile(engineToken, []byte("engine\n"), 0o600)
	os.WriteFile(producerToken, []byte("producer\n"), 0o600)
	t.Setenv("ARKISM_CREW_TRACE_TOKEN_FILE", engineToken)
	base := &Client{BaseURL: api.URL, TokenFile: engineToken, Scope: Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "crew"}}
	if tracesFromEnv(base) != nil {
		t.Fatal("the engine's own token must not produce traces")
	}
	t.Setenv("ARKISM_CREW_TRACE_TOKEN_FILE", producerToken)
	spool := filepath.Join(dir, "receipts")
	var receipts []TraceReceipt
	g := &Graph{Nodes: []Node{node("start", "start"), node("flow", "n8n", "url", flow.URL, "mode", "sync")}, Edges: []Edge{edge("start", "flow")}}
	e := testEngine(t, newFakeStore(wf("a", g)))
	e.Traces = tracesFromEnv(base)
	e.Sinks = []ReceiptSink{func(r TraceReceipt) error { receipts = append(receipts, r); return EnqueueReceipt(spool, r) }}
	r := runOnce(t, e, "a", map[string]any{})
	if r.State.Status != "succeeded" || len(traces) != 1 || len(receipts) != 1 {
		t.Fatal(r.State.Status, traces, receipts)
	}
	events := traces[0]["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["kind"] != "tool_result" || traces[0]["agent_run"].(map[string]any)["outcome"] != "success" {
		t.Fatal(traces[0])
	}
	if err := EnqueueReceipt(spool, receipts[0]); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(spool, "*.json"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	raw, _ := os.ReadFile(files[0])
	if strings.Contains(string(raw), "ok") || !strings.Contains(string(raw), `"trace":{"collection":"execution_traces"`) {
		t.Fatal(string(raw))
	}
	if EnqueueReceipt(spool, TraceReceipt{Trace: Ref{Collection: "sources", ID: "x", Revision: 1}}) == nil {
		t.Fatal("non-trace receipt accepted")
	}
}
