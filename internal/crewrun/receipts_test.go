package crewrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	base := &Client{BaseURL: api.URL, TokenFile: engineToken, Scope: Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u"}}
	if tracesFromEnv(base) != nil {
		t.Fatal("the engine's own token must not produce traces")
	}
	t.Setenv("ARKISM_CREW_TRACE_TOKEN_FILE", producerToken)
	spool := filepath.Join(dir, "receipts")
	var receipts []TraceReceipt
	g := &Graph{Nodes: []Node{node("start", "start"), node("flow", "http", "url", flow.URL, "method", "POST")}, Edges: []Edge{edge("start", "flow")}}
	e := testEngine(t, newFakeStore(wf("a", g)))
	e.Traces = tracesFromEnv(base)
	e.Sinks = []ReceiptSink{func(r TraceReceipt) error { receipts = append(receipts, r); return EnqueueReceipt(spool, r) }}
	r := runOnce(t, e, "a", map[string]any{})
	e.Wait()
	if r.State.Status != "succeeded" || len(traces) != 1 || len(receipts) != 1 {
		t.Fatal(r.State.Status, traces, receipts)
	}
	if traces[0]["scope"].(map[string]any)["agent_id"] != "crew" {
		t.Fatal("unset ARKISM_AGENT must still send a non-empty agent_id", traces[0]["scope"])
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

func receiptObservationRun(count int) (Run, map[string]Node) {
	started := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	finished := started.Add(time.Second)
	r := Run{ID: "synthetic-trace-run", State: RunState{Status: "succeeded", CrewID: "team", Nodes: map[string]*NodeState{}}}
	nodes := map[string]Node{}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("node-%03d", i)
		nodes[id] = Node{ID: id, Type: "ai"}
		r.State.Nodes[id] = &NodeState{Status: "done", StartedAt: &started, FinishedAt: &finished, Runs: 3, Output: "synthetic final projection"}
	}
	return r, nodes
}
func receiptObservationClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	token := filepath.Join(t.TempDir(), "producer.token")
	if err := os.WriteFile(token, []byte("synthetic-token"), 0600); err != nil {
		t.Fatal(err)
	}
	return &Client{BaseURL: server.URL, HTTP: server.Client(), TokenFile: token, Scope: Scope{WorkspaceID: "w", ProjectID: "p", UserID: "u", AgentID: "crew"}}
}
func TestReceiptObservationMissingVersusNotApplicable(t *testing.T) {
	for _, count := range []int{0, 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			r, nodes := receiptObservationRun(count)
			o := NewAttemptObserver(nil)
			e := &Engine{}
			e.recordFinished(WithAttemptObserver(context.Background(), o), r, nodes)
			if slicesContains := strings.Contains(strings.Join(o.Snapshot().Issues, ","), "trace_missing"); slicesContains != (count > 0) {
				t.Fatal("trace scope misclassified", o.Snapshot().Issues)
			}
		})
	}
}
func TestReceiptObservationAsyncPendingSurvivesCancelAndFailure(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	client := receiptObservationClient(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusInternalServerError)
	})
	e := &Engine{Traces: client, Sinks: []ReceiptSink{func(TraceReceipt) error { t.Error("sink after POST failure"); return nil }}}
	o := NewAttemptObserver(nil)
	ctx, cancel := context.WithCancel(WithAttemptObserver(context.Background(), o))
	r, nodes := receiptObservationRun(1)
	cancel()
	e.recordFinished(ctx, r, nodes)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		e.Wait()
		t.Fatal("trace context cancelled or delivery not started")
	}
	s := o.Snapshot()
	if !strings.Contains(strings.Join(s.Issues, ","), "sink_pending") || s.Complete {
		t.Error("async delivery prematurely complete", s)
	}
	close(release)
	e.Wait()
	issues := strings.Join(o.Snapshot().Issues, ",")
	if !strings.Contains(issues, "trace_store_failed") || strings.Contains(issues, "sink_pending") {
		t.Fatal("completion lost sticky failure or pending leaked", issues)
	}
}
func TestReceiptObservationInvalidReceiptAndEachSinkPreserveBusiness(t *testing.T) {
	for _, trace := range []string{`{"collection":"wrong","id":"t","revision":1}`, `{"collection":"execution_traces","id":"","revision":1}`, `{"collection":"execution_traces","id":"matched","revision":1}`, `{"collection":"execution_traces","id":"unrelated","revision":1}`} {
		t.Run(trace, func(t *testing.T) {
			client := receiptObservationClient(t, func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				actualTrace := trace
				if strings.Contains(trace, `"id":"matched"`) {
					actualTrace = strings.ReplaceAll(trace, `"id":"matched"`, `"id":"`+body["trace_id"].(string)+`"`)
				}
				fmt.Fprint(w, `{"trace":`+actualTrace+`,"run":{"collection":"agent_runs","id":"run","revision":1}}`)
			})
			count := 0
			e := &Engine{Traces: client, Sinks: []ReceiptSink{func(TraceReceipt) error { count++; return errors.New("synthetic sink1") }, func(TraceReceipt) error { count++; return errors.New("synthetic sink2") }}}
			o := NewAttemptObserver(nil)
			r, nodes := receiptObservationRun(1)
			e.recordFinished(WithAttemptObserver(context.Background(), o), r, nodes)
			e.Wait()
			issues := strings.Join(o.Snapshot().Issues, ",")
			invalid := strings.Contains(trace, `"wrong"`) || strings.Contains(trace, `"id":""`) || strings.Contains(trace, `"id":"unrelated"`)
			if strings.Contains(issues, "trace_receipt_invalid") != invalid {
				t.Fatal(issues)
			}
			want := 2
			if strings.Contains(trace, `"wrong"`) {
				want = 0
			}
			if count != want || r.State.Status != "succeeded" {
				t.Fatal("observation changed old sink/run behavior", count, r.State.Status)
			}
			if want > 0 && !strings.Contains(issues, "sink_failed") {
				t.Fatal("sink failure not preserved", issues)
			}
		})
	}
}
func TestReceiptObservationProjectionRetains64Cap(t *testing.T) {
	r, nodes := receiptObservationRun(70)
	body, ok := traceInput(Scope{AgentID: "crew"}, r, nodes)
	if !ok || len(body["events"].([]any)) != 64 {
		t.Fatal("final projection cap changed")
	}
	if body["agent_run"].(map[string]any)["task_type"] != "crew_run" {
		t.Fatal("crew trace reattributed to personal outcome")
	}
	for _, event := range body["events"].([]any) {
		payload := event.(map[string]any)["payload"].(json.RawMessage)
		var p map[string]any
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Fatal(err)
		}
		if _, exists := p["attempt_id"]; exists {
			t.Fatal("final node projection presented as attempt ledger")
		}
	}
}
