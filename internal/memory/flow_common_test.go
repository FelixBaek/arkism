package memory

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/crewrun"
)

func commonFlowEngine(t *testing.T, h flowHTTPHarness) (*crewrun.Engine, string) {
	t.Helper()
	dir := t.TempDir()
	token := filepath.Join(dir, "engine.token")
	if e := os.WriteFile(token, []byte(tokEngine), 0600); e != nil {
		t.Fatal(e)
	}
	log := filepath.Join(dir, "fixture-inputs.jsonl")
	runner := filepath.Join(dir, "fixture-runner")
	script := `#!/bin/sh
in=$(cat)
printf '%s\n' "$in" >> "$1"
case "$in" in
  *"JSON 하나로만"*) out='{\"summary\":\"Fixture conclusion\",\"decisions\":[\"Fixture decision\"],\"todos\":[],\"open_questions\":[]}' ;;
  *STANCE*) out='Fixture review\nSTANCE: approve' ;;
  *) out='Fixture completed answer' ;;
esac
printf '{"ok":true,"output":"%s","model":"opus 5.5","effort":"medium","cli":"fake","duration_ms":1,"usage":{"input_tokens":100,"output_tokens":20,"cost_usd":0.001,"estimated":false}}\n' "$out"
`
	if e := os.WriteFile(runner, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	client := &crewrun.Client{BaseURL: "http://memoryd.test", TokenFile: token, Scope: crewrun.Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID}, HTTP: &http.Client{Transport: flowHandlerTransport{h.handler}}}
	return &crewrun.Engine{Store: client, Meetings: client, Runner: []string{runner, log}, DefaultModel: "opus 5.5"}, log
}

func commonFlowStart(t *testing.T, h flowHTTPHarness, item Record, request string) Record {
	t.Helper()
	in := CrewRunInput{Scope: scopeA, RequestID: request, Run: CrewRun{CrewID: item.ID, CrewRevision: item.Revision, Trigger: "manual", Status: "running", InvocationMode: "flow", Input: "Fixture task", Nodes: map[string]CrewRunNode{}}}
	var out Record
	h.call("POST", "/v1/crew/runs", tokEngine, in, 200, &out)
	var again Record
	h.call("POST", "/v1/crew/runs", tokEngine, in, 200, &again)
	if out.ID != again.ID || out.Revision != again.Revision {
		t.Fatal("same invocation duplicated", out.ID, again.ID)
	}
	return out
}

func TestFlowCommonAgentRunsPinnedDefinitionThroughActualHandler(t *testing.T) {
	f, h, _ := flowHTTPFixture(t)
	agent := crewAgent("Fixture reviewer", "fixture-reviewer")
	agent.Instructions = "original pinned reviewer instruction"
	item := f.crew(agent)
	var capabilities FlowCapabilities
	h.call("GET", "/v1/flow/capabilities/"+item.ID+flowHTTPQuery, f.human, nil, 200, &capabilities)
	if !capabilities.Supported {
		t.Fatal(capabilities)
	}
	run := commonFlowStart(t, h, item, "common-agent")
	if run.CrewRun.SourceType != "agent" || run.CrewRun.Definition.Type != "workflow" {
		t.Fatal("agent was not normalized to common flow", run.CrewRun)
	}
	edited := cloneFlowCrew(*item.Crew)
	edited.Instructions = "replacement live instruction"
	if _, e := f.svc.UpdateCrew(testCtx, f.human, item.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: item.Revision, Crew: &edited}); e != nil {
		t.Fatal(e)
	}
	engine, log := commonFlowEngine(t, h)
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	var result FlowResultView
	h.call("GET", "/v1/crew/runs/"+run.ID+"/result"+flowHTTPQuery, f.human, nil, 200, &result)
	if result.Status != "succeeded" || result.SourceType != "agent" || !strings.Contains(result.Result, "Fixture completed answer") {
		t.Fatal(result)
	}
	sent, e := os.ReadFile(log)
	if e != nil || !strings.Contains(string(sent), agent.Instructions) || strings.Contains(string(sent), edited.Instructions) {
		t.Fatal("live role replaced pinned role", string(sent), e)
	}
	revision := result.Revision
	if e = engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	h.call("GET", "/v1/crew/runs/"+run.ID+"/result"+flowHTTPQuery, f.human, nil, 200, &result)
	if result.Revision != revision {
		t.Fatal("terminal agent reran")
	}
}

func TestFlowCommonTeamWaitsForDurableMeetingConclusion(t *testing.T) {
	f, h, _ := flowHTTPFixture(t)
	a := f.crew(crewAgent("Fixture author", "fixture-author"))
	b := f.crew(crewAgent("Fixture reviewer", "fixture-reviewer"))
	team := f.crew(crewTeam("fixture-team", CrewMember{AgentID: a.ID, Order: 1, Role: "worker"}, CrewMember{AgentID: b.ID, Order: 2, Role: "reviewer"}))
	run := commonFlowStart(t, h, team, "common-team")
	engine, _ := commonFlowEngine(t, h)
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	var result FlowResultView
	h.call("GET", "/v1/crew/runs/"+run.ID+"/result"+flowHTTPQuery, f.human, nil, 200, &result)
	if result.Status != "succeeded" || result.SourceType != "team" || len(result.Children) != 1 || !strings.Contains(result.Result, "Fixture conclusion") {
		t.Fatal(result)
	}
	for _, child := range result.Children {
		var meeting Record
		h.call("GET", "/v1/crew/meetings/"+child.ID+flowHTTPQuery, f.human, nil, 200, &meeting)
		if child.Kind != "meeting" || meeting.CrewMeeting.Status != "concluded" || meeting.CrewMeeting.Conclusion == nil || meeting.CrewMeeting.ParentID != run.ID {
			t.Fatal("parent completed before required meeting", child, meeting)
		}
	}
}

func TestFlowCommonReportsUnsupportedReferencedOptions(t *testing.T) {
	f, h, _ := flowHTTPFixture(t)
	agent := crewAgent("Fixture unsupported", "fixture-unsupported")
	agent.Temperature = ptr(0.5)
	a := f.crew(agent)
	team := f.crew(crewTeam("fixture-unsupported-team", CrewMember{AgentID: a.ID, Role: "worker"}))
	for _, item := range []Record{a, team} {
		var capabilities FlowCapabilities
		h.call("GET", "/v1/flow/capabilities/"+item.ID+flowHTTPQuery, f.human, nil, 200, &capabilities)
		if capabilities.Supported || len(capabilities.Unsupported) == 0 {
			t.Fatal("unsupported member option silently ignored", item.ID, capabilities)
		}
		in := CrewRunInput{Scope: scopeA, RequestID: "unsupported-" + item.ID, Run: CrewRun{CrewID: item.ID, CrewRevision: item.Revision, Trigger: "manual", Status: "running", InvocationMode: "flow", Input: "Fixture task", Nodes: map[string]CrewRunNode{}}}
		h.call("POST", "/v1/crew/runs", tokEngine, in, 400, nil)
	}
}
