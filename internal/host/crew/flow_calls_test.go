package crew

import (
	"strings"
	"testing"
	"time"
)

func TestFlowExpertAndTeamUseCommonRunAndStableRequest(t *testing.T) {
	for _, typ := range []string{"agent", "team"} {
		t.Run(typ, func(t *testing.T) {
			var posted map[string]any
			patch(t, &apiGet, func(path string, _ time.Duration) (map[string]any, error) {
				if !strings.Contains(path, "/v1/flow/capabilities/") {
					t.Fatal(path)
				}
				return map[string]any{"supported": true, "modes": []any{"once"}}, nil
			})
			patch(t, &apiPost, func(path string, body any, _ time.Duration) (map[string]any, error) {
				if path != "/v1/crew/runs" {
					t.Fatalf("new @ call used a legacy start: %s", path)
				}
				posted = obj(plain(t, body))
				rec := runRec("running", nil, nil, "", 1)
				obj(rec["crew_run"])["source_type"] = typ
				return rec, nil
			})
			view, err := startWorkflowRequest("@검토", "검토 요청", 0, "/work", "event-1", func(string) (map[string]any, error) {
				// An owned workflow is resolved and pinned by the server, not started
				// through a second assist with a separate 120-second lifetime.
				return atFixture(typ, "검토", map[string]any{"workflow": "private-flow"}), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			run := obj(posted["run"])
			if posted["request_id"] != "event-1" || run["crew_id"] != "id-검토" || run["invocation_mode"] != "flow" || len(obj(run["nodes"])) != 0 {
				t.Fatal(posted)
			}
			eq(t, loads(t, pyStr(run["input"])), map[string]any{"request": "검토 요청", "cwd": "/work"})
			if view["run_kind"] != "workflow" || view["source_type"] != typ || view["terminal"] != false || view["status"] != "running" || view["idempotency_supported"] != true {
				t.Fatal(view)
			}
		})
	}
}

func TestFlowExpertWaitKeepsSameRunPastLegacyAssistDeadline(t *testing.T) {
	now, starts, reads := 0.0, 0, 0
	patch(t, &runClock, func() float64 { return now })
	patch(t, &runSleep, func(seconds float64) { now += seconds })
	patch(t, &apiPost, func(path string, _ any, _ time.Duration) (map[string]any, error) {
		starts++
		if path != "/v1/crew/runs" {
			t.Fatal(path)
		}
		return runRec("running", nil, nil, "", 1), nil
	})
	patch(t, &apiGet, func(path string, _ time.Duration) (map[string]any, error) {
		if strings.Contains(path, "/v1/flow/capabilities/") {
			return map[string]any{"supported": true}, nil
		}
		reads++
		if !strings.HasPrefix(path, "/v1/crew/runs/run-1?") {
			t.Fatal(path)
		}
		if now < 130 {
			return runRec("running", nil, nil, "", 2), nil
		}
		return runRec("succeeded", nil, nil, "최종 검토", 3), nil
	})
	started, err := startWorkflowRequest("@검토", "오래 검토", 120, "", "event-2", func(string) (map[string]any, error) {
		return atFixture("agent", "검토", nil), nil
	})
	if err != nil || started["terminal"] != false || now != 120 {
		t.Fatal(started, err, now)
	}
	final, err := waitWorkflow(pyStr(started["run_id"]), pyStr(started["run_kind"]), 30)
	if err != nil || final["terminal"] != true || final["result"] != "최종 검토" || starts != 1 || reads < 2 {
		t.Fatal(final, err, starts, reads)
	}
}

func TestFlowTargetUsesCommonCancelAndWaitsForActualStop(t *testing.T) {
	rec := runRec("running", nil, nil, "", 4)
	obj(rec["crew_run"])["invocation_mode"] = "flow"
	patch(t, &apiGet, func(string, time.Duration) (map[string]any, error) { return rec, nil })
	patch(t, &apiPost, func(path string, body any, _ time.Duration) (map[string]any, error) {
		if path != "/v1/crew/runs/run-1/stop" || pyStr(obj(plain(t, body))["expected_revision"]) != "4" {
			t.Fatal(path, body)
		}
		obj(rec["crew_run"])["cancel_requested"] = true
		return rec, nil
	})
	requested, err := cancelWorkflow("run-1", "workflow")
	if err != nil || requested["terminal"] != false || requested["cancel_requested"] != true {
		t.Fatal(requested, err)
	}
	rec = runRec("stopped", nil, nil, "중단 확인", 5)
	stopped, err := waitWorkflow("run-1", "workflow", 0)
	if err != nil || stopped["terminal"] != true || stopped["status"] != "stopped" {
		t.Fatal(stopped, err)
	}
}

func TestLegacyMeetingWithoutConclusionIsPartial(t *testing.T) {
	view := meetingView("meeting-1", map[string]any{"status": "no_conclusion", "conclusion": map[string]any{"open_questions": []any{"미해결 사항"}}})
	if view["status"] != "partial" || view["terminal"] != true || view["result"] != "남은 질문: 미해결 사항" {
		t.Fatal(view)
	}
}

func TestFlowUnsupportedRoleSettingsRefuseBeforeStarting(t *testing.T) {
	patch(t, &apiPost, func(string, any, time.Duration) (map[string]any, error) {
		t.Fatal("unsupported role must not start or fall back to an assist")
		return nil, nil
	})
	patch(t, &apiGet, func(path string, _ time.Duration) (map[string]any, error) {
		if !strings.Contains(path, "/v1/flow/capabilities/") {
			t.Fatal(path)
		}
		return map[string]any{"supported": false, "unsupported": []any{"tools", "temperature"}}, nil
	})
	_, err := startWorkflowRequest("@검토", "검토 요청", 0, "", "event-3", func(string) (map[string]any, error) {
		return atFixture("agent", "검토", nil), nil
	})
	if err == nil || !strings.Contains(err.Error(), "tools, temperature") {
		t.Fatal(err)
	}
}

func TestFlowChildApprovalRetainsParentDecisionIdentity(t *testing.T) {
	waiting := map[string]any{"node_id": "delegate", "kind": "child", "child_run_id": "child-1", "child_kind": "workflow", "child_revision": 3, "decision_kind": "approval", "question": "진행할까요?"}
	rec := runRec("waiting", waiting, nil, "", 7)
	view, err := waitRun("run-1", 600, rec)
	if err != nil {
		t.Fatal(err)
	}
	eq(t, plain(t, view["waiting"]), waiting)
	patch(t, &apiGet, func(path string, _ time.Duration) (map[string]any, error) {
		if !strings.HasPrefix(path, "/v1/crew/runs/run-1?") {
			t.Fatal(path)
		}
		return rec, nil
	})
	patch(t, &apiPost, func(path string, body any, _ time.Duration) (map[string]any, error) {
		data := obj(plain(t, body))
		if path != "/v1/crew/runs/run-1/decision" || data["node_id"] != "delegate" || pyStr(data["expected_revision"]) != "7" || data["approve"] != true {
			t.Fatal(path, data)
		}
		return runRec("running", nil, nil, "", 8), nil
	})
	view, err = decideRun("run-1", true, "사람이 진행 승인", 0)
	if err != nil || view["terminal"] != false || view["run_id"] != "run-1" {
		t.Fatal(view, err)
	}
}
