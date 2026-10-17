package crew

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func captureReports(t *testing.T) *[]map[string]any {
	got := &[]map[string]any{}
	patch(t, &host.ReportCall, func(c map[string]any) { *got = append(*got, c) })
	return got
}

func TestRunNodeReportsUsageWithItsOrigin(t *testing.T) {
	t.Cleanup(func() { stopOnTerm.Store(false) })
	reports := captureReports(t)
	fakeSpawn(t, "검토 끝: 좋아요", "", 0)
	var buf bytes.Buffer
	stdin := `{"model": "opus 5.5", "task": "이 PR 봐 줘", "once": true, "access": "read", "origin": {"kind": "meeting", "meeting_id": "m1", "agent_id": "a1", "team_id": "t1"}}`
	if code := crewMain([]string{"run-node", "--json"}, strings.NewReader(stdin), &buf, &buf); code != 0 {
		t.Fatal(code, buf.String())
	}
	out := loads(t, buf.String())
	u := obj(out["usage"])
	if u["estimated"] != true || tnum(t, u["input_tokens"]) <= 0 || tnum(t, u["output_tokens"]) <= 0 || tnum(t, u["cost_usd"]) <= 0 {
		t.Fatal(out)
	}
	if len(*reports) != 1 {
		t.Fatal(*reports)
	}
	r := (*reports)[0]
	if r["model"] != "anthropic/opus 5.5" || r["decision"] != "fixed" || r["ok"] != true || obj(r["origin"])["meeting_id"] != "m1" || r["estimated"] != true {
		t.Fatal(r)
	}
	// without an origin a workflow node is not counted twice (the engine's own calls stay as they were)
	*reports = nil
	buf.Reset()
	_ = crewMain([]string{"run-node", "--json"}, strings.NewReader(`{"model": "opus", "task": "x", "cwd": "/tmp"}`), &buf, &buf)
	if len(*reports) != 0 {
		t.Fatal(*reports)
	}
	// a failure is counted too
	fakeSpawn(t, "", "boom", 3)
	buf.Reset()
	_ = crewMain([]string{"run-node", "--json"}, strings.NewReader(stdin), &buf, &buf)
	if len(*reports) != 1 || (*reports)[0]["ok"] != false || (*reports)[0]["error"] != "exit_3" {
		t.Fatal(*reports)
	}
}

func tnum(t *testing.T, v any) float64 {
	t.Helper()
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	f, err := strconv.ParseFloat(fmt.Sprint(v), 64)
	if err != nil {
		t.Fatalf("not a number: %#v", v)
	}
	return f
}

func TestRoutedAttemptsCarryOrigin(t *testing.T) {
	var got []map[string]any
	decide := func(string, string) (map[string]any, error) {
		return map[string]any{"id": "anthropic/opus", "decision": "clef", "fallbacks": []any{"openai/gpt-6.1-sol"}}, nil
	}
	run := func(s map[string]any) (map[string]any, error) {
		if s["model"] == "opus" {
			return result(false, "", "opus", "", "claude", time.Time{}, "quota_exhausted"), nil
		}
		return result(true, "답", "gpt-6.1-sol", "medium", "codex", time.Now(), ""), nil
	}
	res, err := runRouted(map[string]any{"task": "x", "origin": map[string]any{"kind": "agent", "agent_id": "a1"}}, decide, run,
		func(c map[string]any) { got = append(got, c) })
	if err != nil || res["ok"] != true || len(got) != 2 {
		t.Fatal(res, err, got)
	}
	if obj(got[0]["origin"])["agent_id"] != "a1" || got[0]["input_tokens"] != nil || got[1]["decision"] != "failover" || got[1]["input_tokens"] == nil {
		t.Fatal(got)
	}
}

func TestTryRunsTheAgentReadOnly(t *testing.T) {
	reports := captureReports(t)
	patch(t, &runnerCall, func(path string, _ any, _ time.Duration) (map[string]any, error) {
		if !strings.Contains(path, "handle=%40%EB%A6%AC%EB%B7%B0%EC%96%B4") && !strings.Contains(path, "handle=@") {
			t.Fatal(path)
		}
		return map[string]any{"item": map[string]any{"id": "a1", "crew": map[string]any{"type": "agent", "role": "리뷰", "instructions": "꼼꼼히",
			"model": "opus 5.5", "fallback_model": "sonnet 5", "output_format": "markdown"}}}, nil
	})
	var specs []map[string]any
	runner := func(s map[string]any) (map[string]any, error) {
		specs = append(specs, s)
		if s["model"] == "opus 5.5" {
			return result(false, "", "opus 5.5", "", "claude", time.Time{}, "timeout"), nil
		}
		return result(true, "좋아요", pyStr(s["model"]), "medium", "claude", time.Now(), ""), nil
	}
	res, err := answerAssist(map[string]any{"id": "x", "created_by_type": "service", "crew_assist": map[string]any{"kind": "try", "expert": "리뷰어", "prompt": "봐 줘"}}, runner)
	if err != nil || res["status"] != "done" || res["output"] != "좋아요" || len(specs) != 2 {
		t.Fatal(res, err, specs)
	}
	s := specs[0]
	if s["access"] != "read" || s["once"] != true || !strings.Contains(pyStr(s["instructions"]), "markdown") || obj(s["origin"])["trigger"] != "discord" {
		t.Fatal(s)
	}
	if len(*reports) != 2 || (*reports)[1]["ok"] != true || obj((*reports)[1]["origin"])["agent_id"] != "a1" {
		t.Fatal(*reports)
	}
}

func TestAgentWithAWorkflowRunsItInsteadOfAnswering(t *testing.T) {
	patch(t, &runnerCall, func(string, any, time.Duration) (map[string]any, error) {
		return map[string]any{"item": map[string]any{"id": "a1", "crew": map[string]any{"type": "agent", "workflow": "$리서치"}}}, nil
	})
	patch(t, &apiGet, func(path string, _ time.Duration) (map[string]any, error) {
		if strings.Contains(path, "/v1/crew/resolve") {
			return workflowFixture(t, "리서치", ""), nil
		}
		return runRec("succeeded", nil, nil, "보고서", 2), nil
	})
	patch(t, &apiPost, func(string, any, time.Duration) (map[string]any, error) {
		return runRec("running", nil, nil, "", 1), nil
	})
	patch(t, &runSleep, func(float64) {})
	runner := func(map[string]any) (map[string]any, error) { t.Fatal("the agent answered itself"); return nil, nil }
	res, err := answerAssist(map[string]any{"id": "x", "crew_assist": map[string]any{"kind": "try", "expert": "리서처", "prompt": "AI 뉴스"}}, runner)
	if err != nil || res["status"] != "done" || res["output"] != "보고서" {
		t.Fatal(res, err)
	}
}
