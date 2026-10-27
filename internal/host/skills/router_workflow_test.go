package skills

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestWorkflowStdioSessionReturnsPendingThenFinalResult(t *testing.T) {
	swapRouter(t)
	swapCrew(t)
	starts, waits := 0, 0
	host.StartWorkflow = func(string, string, time.Duration, string) (map[string]any, error) {
		starts++
		return map[string]any{"run_id": "persisted-1", "run_kind": "workflow", "status": "running", "terminal": false}, nil
	}
	host.WaitWorkflow = func(id, kind string, wait time.Duration) (map[string]any, error) {
		waits++
		if id != "persisted-1" || kind != "workflow" {
			t.Fatal(id, kind)
		}
		if waits == 1 {
			return map[string]any{"run_id": id, "run_kind": kind, "status": "running", "terminal": false}, nil
		}
		return map[string]any{"run_id": id, "run_kind": kind, "status": "succeeded", "terminal": true, "result": "완성 보고서와 출처"}, nil
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"run_workflow","arguments":{"handle":"@딥리서치","task":"자료 조사","wait_seconds":0}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"wait_workflow","arguments":{"run_id":"persisted-1","wait_seconds":0}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"wait_workflow","arguments":{"run_id":"persisted-1","wait_seconds":0}}}`,
	}, "\n") + "\n"
	var output strings.Builder
	routerMain(nil, strings.NewReader(input), &output, nil)
	decoder := json.NewDecoder(strings.NewReader(output.String()))
	for i := 1; i <= 3; i++ {
		var reply map[string]any
		if err := decoder.Decode(&reply); err != nil {
			t.Fatal(err, output.String())
		}
		v := field(reply["result"], "structuredContent")
		if reply["id"] != float64(i) || reply["error"] != nil || field(v, "run_id") != "persisted-1" || field(v, "terminal") != (i == 3) {
			t.Fatal(reply)
		}
		if i == 3 && field(v, "result") != "완성 보고서와 출처" {
			t.Fatal(reply)
		}
	}
	if starts != 1 || waits != 2 {
		t.Fatal(starts, waits)
	}
}

// A delayed result must be collected from the same run, including the full
// result rather than a truncated summary. No second execution is created.
func TestWorkflowLifecycleCollectsDelayedResultWithoutRestart(t *testing.T) {
	swapRouter(t)
	swapCrew(t)
	starts, waits := 0, 0
	host.StartWorkflowRequest = func(handle, task string, wait time.Duration, cwd, requestID string) (map[string]any, error) {
		starts++
		if handle != "@딥리서치" || task != "자료 조사" || requestID != "request-1" || wait != 0 || cwd != "/work" {
			t.Fatal(handle, task, wait, cwd, requestID)
		}
		return map[string]any{"run_id": "run-1", "run_kind": "workflow", "status": "running", "terminal": false}, nil
	}
	full := strings.Repeat("근거와 출처\n", 1000)
	host.WaitWorkflow = func(id, kind string, wait time.Duration) (map[string]any, error) {
		waits++
		if id != "run-1" || kind != "workflow" || wait != 60*time.Second {
			t.Fatal(id, kind, wait)
		}
		if waits == 1 {
			return map[string]any{"run_id": id, "run_kind": kind, "status": "running", "terminal": false}, nil
		}
		return map[string]any{"run_id": id, "run_kind": kind, "status": "succeeded", "terminal": true, "summary": "요약", "result": full}, nil
	}
	started := structured(t, call(t, "run_workflow", `{"handle":"@딥리서치","task":"자료 조사","request_id":"request-1","cwd":"/work","wait_seconds":0}`))
	if field(started, "terminal") != false || field(field(started, "next_action"), "tool") != "wait_workflow" {
		t.Fatal(started)
	}
	for i := 0; i < 2; i++ {
		out := structured(t, call(t, "wait_workflow", `{"run_id":"run-1","run_kind":"workflow","wait_seconds":900}`))
		if i == 0 && field(out, "terminal") != false {
			t.Fatal(out)
		}
		if i == 1 && (field(out, "terminal") != true || field(out, "result") != full || field(out, "next_action") != nil) {
			t.Fatal(out)
		}
	}
	if starts != 1 || waits != 2 {
		t.Fatal(starts, waits)
	}
}

func TestWorkflowReadCancelAndReconnectKeepRunKind(t *testing.T) {
	swapRouter(t)
	swapCrew(t)
	cancelled := false
	host.CancelWorkflow = func(id, kind string) (map[string]any, error) {
		if id != "assist-1" || kind != "assist" {
			t.Fatal(id, kind)
		}
		cancelled = true
		return map[string]any{"run_id": id, "run_kind": kind, "status": "running", "terminal": false, "cancel_requested": true}, nil
	}
	host.GetWorkflow = func(id, kind string) (map[string]any, error) {
		if !cancelled || id != "assist-1" || kind != "assist" {
			t.Fatal(cancelled, id, kind)
		}
		return map[string]any{"run_id": id, "run_kind": kind, "status": "stopped", "terminal": true, "result": "사용자가 취소"}, nil
	}
	out := call(t, "cancel_workflow", `{"run_id":"assist-1","run_kind":"assist"}`)
	v := structured(t, out)
	if field(v, "terminal") != false || field(out, "isError") != false || field(field(field(v, "next_action"), "arguments"), "run_kind") != "assist" {
		t.Fatal(out)
	}
	out = call(t, "get_workflow", `{"run_id":"assist-1","run_kind":"assist"}`)
	if field(structured(t, out), "terminal") != true || field(out, "isError") != true {
		t.Fatal(out)
	}
}

func TestWorkflowWaitingDistinguishesTimerAndHumanDecision(t *testing.T) {
	swapRouter(t)
	swapCrew(t)
	for _, c := range []struct {
		kind     string
		waiting  any
		decision bool
	}{
		{"workflow", nil, false},
		{"workflow", map[string]any{"node_id": "approve", "question": "보낼까요?"}, true},
		{"workflow", map[string]any{"kind": "wait", "until": "later"}, false},
		{"workflow", map[string]any{"kind": "child", "child_run_id": "child-1", "decision_kind": "approval"}, true},
		{"workflow", map[string]any{"kind": "child", "child_run_id": "child-1", "decision_kind": "budget"}, true},
		{"workflow", map[string]any{"kind": "child", "child_run_id": "child-1", "decision_kind": "wait"}, false},
		{"meeting", nil, true},
	} {
		host.GetWorkflow = func(id, kind string) (map[string]any, error) {
			return map[string]any{"run_id": id, "run_kind": c.kind, "status": "waiting", "waiting": c.waiting}, nil
		}
		v := structured(t, call(t, "get_workflow", `{"run_id":"r"}`))
		if field(v, "terminal") != false || field(v, "requires_decision") != c.decision {
			t.Fatal(c, v)
		}
		if !c.decision && field(field(v, "next_action"), "tool") != "wait_workflow" {
			t.Fatal(c, v)
		}
	}
}

func TestWorkflowLifecycleRejectsInvalidIdentityAndNonFiniteWait(t *testing.T) {
	swapRouter(t)
	swapCrew(t)
	host.GetWorkflow = func(string, string) (map[string]any, error) { t.Fatal("unexpected host read"); return nil, nil }
	host.WaitWorkflow = func(string, string, time.Duration) (map[string]any, error) {
		t.Fatal("unexpected host wait")
		return nil, nil
	}
	for _, c := range []struct{ name, args string }{
		{"get_workflow", `{}`},
		{"get_workflow", `{"run_id":" "}`},
		{"get_workflow", `{"run_id":7}`},
		{"get_workflow", `{"run_id":"r","run_kind":"unknown"}`},
		{"wait_workflow", `{"run_id":"r","wait_seconds":"NaN"}`},
		{"wait_workflow", `{"run_id":"r","wait_seconds":"Inf"}`},
		{"decide_workflow", `{"run_id":"m","run_kind":"meeting","approve":true}`},
		{"decide_workflow", `{"run_id":"r","approve":"false"}`},
		{"decide_workflow", `{"run_id":"r"}`},
	} {
		if _, err := callTool(c.name, j(t, c.args)); err == nil {
			t.Fatal(c)
		}
	}
}

func TestWorkflowPartialMeetingIsNotReportedAsSuccess(t *testing.T) {
	swapRouter(t)
	swapCrew(t)
	host.GetWorkflow = func(string, string) (map[string]any, error) {
		return map[string]any{"run_id": "meeting-1", "run_kind": "meeting", "status": "partial", "terminal": true, "result": "합의에 이르지 못했어요"}, nil
	}
	out := call(t, "get_workflow", `{"run_id":"meeting-1","run_kind":"meeting"}`)
	v := structured(t, out)
	if field(out, "isError") != true || field(v, "terminal") != true || field(v, "next_action") != nil || field(v, "result") != "합의에 이르지 못했어요" {
		t.Fatal(out)
	}
}
