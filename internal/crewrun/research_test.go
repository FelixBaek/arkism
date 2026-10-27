package crewrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func reportJSON(gaps []string, needs *bool, sources bool) string {
	refs := []map[string]string{}
	if sources {
		refs = append(refs, map[string]string{"title": "Read source", "url": "https://example.org/document"})
	}
	r := map[string]any{"title": "Report", "summary": []string{"Evidence-based answer"}, "gaps": gaps, "sources": refs}
	if needs != nil {
		r["needs_more_evidence"] = *needs
	}
	b, _ := json.Marshal(r)
	return string(b)
}

func TestResearchEvidenceVerdictUsesGapsAndSources(t *testing.T) {
	yes, no := true, false
	for _, c := range []struct {
		gaps    []string
		needs   *bool
		sources bool
		port    string
	}{
		{[]string{}, nil, true, "done"},
		{[]string{"public evidence missing"}, nil, true, "deep"},
		{[]string{"originally nonpublic; reason documented"}, &no, true, "done"},
		{[]string{}, &yes, true, "deep"},
		{[]string{}, &no, false, "deep"},
	} {
		port, verdict, err := researchRoute(reportJSON(c.gaps, c.needs, c.sources), map[string]string{"research_need_port": "deep"}, []string{"done", "deep"})
		if err != nil || port != c.port || verdict.Assessment != "structured_report_only_not_fact_verification" {
			t.Fatal(c, port, verdict, err)
		}
	}
	for _, raw := range []string{reportJSON([]string{}, &no, false), reportJSON([]string{"public evidence missing"}, &yes, true), `{"title":"no body","summary":[],"sources":[],"gaps":[]}`, `{"title":"bad source","summary":["answer"],"sources":[{"url":"file:///tmp/source","title":"local"}],"gaps":[]}`, "not JSON"} {
		if _, _, err := researchRoute(raw, map[string]string{}, nil); err == nil {
			t.Fatal("accepted invalid or incomplete final report", raw)
		}
	}
}

func TestResearchVerdictAcceptsCompleteFencesAndRunnerWrappers(t *testing.T) {
	report := reportJSON([]string{"missing public evidence"}, nil, true)
	wrapped, _ := json.Marshal(map[string]any{"output": "```json\n" + report + "\n```"})
	for _, raw := range []string{report, "```json\n" + report + "\n```", string(wrapped)} {
		port, _, err := researchRoute(raw, map[string]string{"research_need_port": "deep"}, []string{"done", "deep"})
		if err != nil || port != "deep" {
			t.Fatal(port, err)
		}
	}
	if _, _, err := researchRoute("Some prose\n"+report, map[string]string{"research_need_port": "deep"}, []string{"done", "deep"}); err == nil {
		t.Fatal("accepted prose containing a JSON fragment")
	}
}

// A real local subprocess speaks the runner protocol; no model is contacted.
func TestResearchRunnerProcess(t *testing.T) {
	if reply := os.Getenv("CREWRUN_RESEARCH_REPLY"); reply != "" {
		var spec aiSpec
		_ = json.NewDecoder(os.Stdin).Decode(&spec)
		if expected := os.Getenv("CREWRUN_EXPECT_RESEARCH"); expected != "" && fmt.Sprint(spec.Research) != expected {
			fmt.Print(`{"ok":false,"error":"unexpected research runner mode"}`)
			os.Exit(0)
		}
		fmt.Print(reply)
		os.Exit(0)
	}
}

func TestResearchOnlySimpleFallbackRequestsHostResearch(t *testing.T) {
	for _, validator := range []string{"research-v1", "research-report-v1"} {
		output := reportJSON([]string{}, nil, true)
		if validator == "research-v1" {
			output = `{"answer":"Research answer","sources":[{"title":"Source","url":"https://example.org/document"}]}`
		}
		reply, _ := json.Marshal(map[string]any{"ok": true, "output": output})
		t.Setenv("CREWRUN_RESEARCH_REPLY", string(reply))
		t.Setenv("CREWRUN_EXPECT_RESEARCH", fmt.Sprint(validator == "research-v1"))
		e := testEngine(t, newFakeStore())
		e.Runner = []string{os.Args[0], "-test.run=^TestResearchRunnerProcess$", "--"}
		n := node("research", "ai", "access", "read", "validator", validator)
		x := &runExec{e: e, run: Run{State: RunState{Nodes: map[string]*NodeState{}}}}
		x.index(&Graph{Nodes: []Node{n}})
		if _, err := e.runAI(context.Background(), x, n, "research"); err != nil {
			t.Fatal(validator, err)
		}
	}
}

func TestResearchRuntimeOverridesModelRouteWithoutUsingElapsedTime(t *testing.T) {
	for _, duration := range []int{1, 3600000} {
		reply, _ := json.Marshal(map[string]any{"ok": true, "output": reportJSON([]string{"missing public source"}, nil, true) + "\nROUTE: done", "duration_ms": duration})
		t.Setenv("CREWRUN_RESEARCH_REPLY", string(reply))
		e := testEngine(t, newFakeStore())
		e.Runner = []string{os.Args[0], "-test.run=^TestResearchRunnerProcess$", "--"}
		n := node("answer", "ai", "access", "read", "validator", "research-report-v1", "research_need_port", "deep")
		x := &runExec{e: e, run: Run{State: RunState{Nodes: map[string]*NodeState{}}}}
		x.index(&Graph{Nodes: []Node{n, node("done", "end"), node("plan", "start")}, Edges: []Edge{edge("answer", "done", "done"), edge("answer", "plan", "deep")}})
		out, err := e.runAI(context.Background(), x, n, "research")
		if err != nil || out.port != "deep" {
			t.Fatal(duration, out.port, err)
		}
	}
}

func TestResearchApprovalCannotBeBypassedByPlanEdge(t *testing.T) {
	for _, approve := range []bool{false, true} {
		g := &Graph{Nodes: []Node{node("plan", "start"), node("approve", "approval"), node("deep", "http", "research_approval", "approve", "url", "https://local.test/deep"), node("end", "end")}, Edges: []Edge{edge("plan", "approve"), edge("plan", "deep"), edge("approve", "deep", "approve"), edge("deep", "end")}}
		fs := newFakeStore(wf("r", g))
		e := testEngine(t, fs)
		calls := 0
		e.HTTP = &http.Client{Transport: flowTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("evidence"))}, nil
		})}
		run := runOnce(t, e, "r", nil)
		if run.State.Status != "waiting" || calls != 0 {
			t.Fatal(run.State, calls)
		}
		fs.decide(t, run.ID, "approve", approve)
		if err := e.Drive(context.Background(), run.ID); err != nil {
			t.Fatal(err)
		}
		run, _ = fs.GetRun(context.Background(), run.ID)
		if approve && (calls != 1 || run.State.Status != "succeeded") || !approve && (calls != 0 || run.State.Status != "failed" || !strings.Contains(run.State.Summary, "research_approval_required")) {
			t.Fatal(approve, calls, run.State)
		}
	}
}

func TestResearchOutputLimitFailsBeforeStoredTruncation(t *testing.T) {
	e := testEngine(t, newFakeStore())
	x := &runExec{e: e, item: CrewItem{Crew: Crew{Research: &CrewResearch{}}}}
	_, err := e.execute(context.Background(), x, node("start", "start"), strings.Repeat("x", maxStored))
	if err == nil || !strings.Contains(err.Error(), "research_output_too_large") {
		t.Fatal(err)
	}
	if err := researchOutputSize(map[string]any{"output": strings.Repeat("x", maxStored-5)}); err == nil {
		t.Fatal("JSON envelope exceeded storage cap without failure")
	}
}

func TestResearchFallbackRejectsEnvelopeOverflowAfterValidReport(t *testing.T) {
	output := ""
	for size := maxStored; size > 0; size-- {
		b, _ := json.Marshal(map[string]any{"answer": strings.Repeat("a", size), "sources": []map[string]string{{"title": "Source", "url": "https://example.org/document"}}})
		output = string(b)
		if researchOutputSize(map[string]any{"output": output}) == nil {
			break
		}
	}
	reply, _ := json.Marshal(map[string]any{"ok": true, "output": output, "model": "model", "effort": "high", "cli": "fake", "duration_ms": 1})
	t.Setenv("CREWRUN_RESEARCH_REPLY", string(reply))
	e := testEngine(t, newFakeStore())
	e.Runner = []string{os.Args[0], "-test.run=^TestResearchRunnerProcess$", "--"}
	n := node("research", "ai", "access", "read", "validator", "research-v1")
	x := &runExec{e: e, run: Run{State: RunState{Nodes: map[string]*NodeState{}}}}
	x.index(&Graph{Nodes: []Node{n}})
	if _, err := e.runAI(context.Background(), x, n, "research"); err == nil || !strings.Contains(err.Error(), "research_output_too_large") {
		t.Fatal("valid report overflowed the stored envelope without explicit failure", err)
	}
}
