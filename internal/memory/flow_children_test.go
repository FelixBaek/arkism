package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/FelixBaek/arkism/internal/crewrun"
)

func flowChildGraph(handle, child string) Crew {
	node := CrewNode{ID: "gate", Type: "approval", Config: map[string]string{"question": "Allow fixture work?"}}
	edges := []CrewEdge{{From: "start", To: "gate"}, {From: "gate", To: "end", Port: "approve"}, {From: "gate", To: "end", Port: "reject"}}
	if child != "" {
		node = CrewNode{ID: "delegate", Type: "subflow", Config: map[string]string{"workflow": child, "task": "{{input}}"}}
		edges = []CrewEdge{{From: "start", To: "delegate"}, {From: "delegate", To: "end"}}
	}
	return Crew{Type: "workflow", Name: handle, Handle: handle, Graph: &CrewGraph{Nodes: []CrewNode{{ID: "start", Type: "start"}, node, {ID: "end", Type: "end"}}, Edges: edges}}
}

func nestedFlowFixture(t *testing.T) (*fixture, flowHTTPHarness, Record, Record, *crewrun.Engine) {
	t.Helper()
	f, h, _ := flowHTTPFixture(t)
	leaf := f.crew(flowChildGraph("fixture-leaf", ""))
	middle := f.crew(flowChildGraph("fixture-middle", leaf.Crew.Handle))
	root := flowChildGraph("fixture-root", middle.Crew.Handle)
	root.FlowPolicy = &FlowPolicy{MaxDepth: 3, MaxChildren: 4, MaxConcurrent: 1}
	item := f.crew(root)
	run := commonFlowStart(t, h, item, "nested-flow")
	engine, _ := commonFlowEngine(t, h)
	return f, h, run, leaf, engine
}

func TestFlowChildrenNestedSingleCapacityPinsAndResumesApproval(t *testing.T) {
	f, h, run, leaf, engine := nestedFlowFixture(t)
	// The child has not started yet. A later edit must not replace the graph
	// pinned at the parent's invocation, including its approval requirement.
	edited := cloneFlowCrew(*leaf.Crew)
	edited.Graph = &CrewGraph{Nodes: []CrewNode{{ID: "start", Type: "start"}, {ID: "end", Type: "end"}}, Edges: []CrewEdge{{From: "start", To: "end"}}}
	if _, e := f.svc.UpdateCrew(testCtx, f.human, leaf.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: leaf.Revision, Crew: &edited}); e != nil {
		t.Fatal(e)
	}
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	var result FlowResultView
	h.call("GET", "/v1/crew/runs/"+run.ID+"/result"+flowHTTPQuery, f.human, nil, 200, &result)
	if result.Status != "waiting" || result.Waiting == nil || result.Waiting.Kind != "child" || result.Waiting.DecisionKind != "approval" || len(f.repo.rows(colCrewRuns)) != 3 {
		t.Fatal("nested child was lost, bypassed or capacity deadlocked", result, len(f.repo.rows(colCrewRuns)))
	}
	decision := CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: result.Revision, NodeID: result.Waiting.NodeID, Approve: true}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/decision", tokEngine, decision, 403, nil)
	h.call("POST", "/v1/crew/runs/"+run.ID+"/decision", f.agent, decision, 403, nil)
	h.call("POST", "/v1/crew/runs/"+run.ID+"/decision", f.human, decision, 200, nil)
	h.call("POST", "/v1/crew/runs/"+run.ID+"/decision", f.human, decision, 409, nil)
	// A new engine/process reads saved child IDs and decisions.
	engine, _ = commonFlowEngine(t, h)
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	h.call("GET", "/v1/crew/runs/"+run.ID+"/result"+flowHTTPQuery, f.human, nil, 200, &result)
	if result.Status != "succeeded" || len(f.repo.rows(colCrewRuns)) != 3 {
		t.Fatal("resume duplicated or abandoned a required child", result)
	}
	for _, row := range f.repo.rows(colCrewRuns) {
		if row.CrewRun.Status != "succeeded" {
			t.Fatal("parent succeeded before child", row.ID, row.CrewRun)
		}
		if row.CrewRun.CrewID == leaf.ID && row.CrewRun.CrewRevision != leaf.Revision {
			t.Fatal("child used the later definition revision")
		}
	}
}

func TestFlowResultChildrenCarrySummary(t *testing.T) {
	f, h, run, _, engine := nestedFlowFixture(t)
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	var result FlowResultView
	h.call("GET", "/v1/crew/runs/"+run.ID+"/result"+flowHTTPQuery, f.human, nil, 200, &result)
	child, ok := result.Children["delegate:1"]
	if !ok || child.ID == "" || child.Name != "fixture-middle" || child.Status != "waiting" || child.StartedAt == nil || child.FinishedAt != nil {
		t.Fatal("waiting child summary is wrong", result.Children)
	}
	decision := CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: result.Revision, NodeID: result.Waiting.NodeID, Approve: true}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/decision", f.human, decision, 200, nil)
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	h.call("GET", "/v1/crew/runs/"+run.ID+"/result"+flowHTTPQuery, f.human, nil, 200, &result)
	child = result.Children["delegate:1"]
	if child.Status != "succeeded" || child.StartedAt == nil || child.FinishedAt == nil || child.FinishedAt.Before(*child.StartedAt) {
		t.Fatal("finished child summary is wrong", child)
	}
}

func TestFlowChildrenCancelWaitsForDescendantsAndCannotApproveAfterStop(t *testing.T) {
	f, h, run, _, engine := nestedFlowFixture(t)
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	var result FlowResultView
	h.call("GET", "/v1/crew/runs/"+run.ID+"/result"+flowHTTPQuery, f.human, nil, 200, &result)
	if result.Waiting == nil || result.Waiting.DecisionKind != "approval" {
		t.Fatal(result)
	}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/stop", f.human, FlowStopInput{Scope: scopeA, ExpectedRevision: result.Revision}, 200, &run)
	decision := CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: run.Revision, NodeID: result.Waiting.NodeID, Approve: true}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/decision", f.human, decision, 409, nil)
	requireDecision(t, f.repo, "flow_cancelled", "") // #466: a person's stop; the refused approval is not counted
	if len(decisionsOf(f.repo, "flow_approved")) != 0 {
		t.Fatal("a refused approval was counted")
	}
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	h.call("GET", "/v1/crew/runs/"+run.ID+"/result"+flowHTTPQuery, f.human, nil, 200, &result)
	if result.Status != "stopped" {
		t.Fatal("cancelled tree did not stop", result)
	}
	for _, row := range f.repo.rows(colCrewRuns) {
		if !terminalCrewRun(row.CrewRun.Status) || row.CrewRun.Status == "succeeded" {
			t.Fatal("cancelled child still active or successful", row.ID, row.CrewRun)
		}
	}
}

type flowChildReadDeny struct {
	Authorizer
	id string
}

func (a flowChildReadDeny) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if in.Action == "read:crew" && in.ResourceID == a.id {
		return Decision{}, ErrDenied
	}
	return a.Authorizer.Authorize(ctx, token, in)
}

func TestFlowChildrenResultRechecksPinnedChildReadAuthority(t *testing.T) {
	f, h, run, leaf, engine := nestedFlowFixture(t)
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	f.svc.Auth = flowChildReadDeny{Authorizer: f.auth, id: leaf.ID}
	if _, e := f.svc.FlowResult(testCtx, f.human, scopeA, run.ID); !errors.Is(e, ErrDenied) {
		t.Fatal("result ignored revoked child definition read", e)
	}
	h.call("GET", "/v1/crew/runs/"+run.ID+"/result"+flowHTTPQuery, f.human, nil, 403, nil)
}

func TestFlowChildrenHTTPAttemptIdentityAndRequiredCompletion(t *testing.T) {
	_, h, run, _, _ := nestedFlowFixture(t)
	lease := "fixture-child-executor-0123456789"
	h.call("POST", "/v1/crew/runs/"+run.ID+"/lease", tokEngine, FlowLeaseInput{Scope: scopeA, Action: "acquire", LeaseToken: lease}, 200, nil)
	state := *run.CrewRun
	state.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "delegate": {Status: "running", Runs: 1}, "end": {Status: "pending"}}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/progress", tokEngine, CrewRunUpdate{Scope: scopeA, ExpectedRevision: run.Revision, Run: state, LeaseToken: lease}, 200, &run)
	target := flowDefinitionByHandle(run.CrewRun.Definitions, "fixture-middle")
	in := FlowChildInput{Scope: scopeA, LeaseToken: lease, NodeID: "delegate", Attempt: 1, Kind: "workflow", CrewID: target.ID, Input: "same task"}
	var first, again FlowChildCreated
	path := "/v1/crew/runs/" + run.ID + "/children"
	h.call("POST", path, tokEngine, in, 200, &first)
	h.call("POST", path, tokEngine, in, 200, &again)
	if first.Child.ID == "" || first.Child.ID != again.Child.ID || first.Parent.Revision != again.Parent.Revision {
		t.Fatal("same child attempt duplicated", first, again)
	}
	in.Input = "different task"
	h.call("POST", path, tokEngine, in, 409, nil)
	in.Input, in.LeaseToken = "same task", "wrong-lease-0123456789"
	h.call("POST", path, tokEngine, in, 409, nil)
	state = *first.Parent.CrewRun
	state.Status = "succeeded"
	h.call("POST", "/v1/crew/runs/"+run.ID+"/progress", tokEngine, CrewRunUpdate{Scope: scopeA, ExpectedRevision: first.Parent.Revision, Run: state, LeaseToken: lease}, 409, nil)
}

func TestFlowChildrenApprovalRejectsStaleDescendantRevisionAtomically(t *testing.T) {
	f, h, run, leaf, engine := nestedFlowFixture(t)
	if e := engine.Drive(testCtx, run.ID); e != nil {
		t.Fatal(e)
	}
	var parent Record
	h.call("GET", "/v1/crew/runs/"+run.ID+flowHTTPQuery, f.human, nil, 200, &parent)
	var child Record
	for _, row := range f.repo.rows(colCrewRuns) {
		if row.CrewRun.CrewID == leaf.ID {
			child = row
		}
	}
	if child.ID == "" || child.CrewRun.Waiting == nil {
		t.Fatal("missing approval leaf")
	}
	direct := CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: child.Revision, NodeID: child.CrewRun.Waiting.NodeID, Approve: false}
	h.call("POST", "/v1/crew/runs/"+child.ID+"/decision", f.human, direct, 200, nil)
	stale := CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: parent.Revision, NodeID: parent.CrewRun.Waiting.NodeID, Approve: true}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/decision", f.human, stale, 409, nil)
	var current Record
	h.call("GET", "/v1/crew/runs/"+run.ID+flowHTTPQuery, f.human, nil, 200, &current)
	if current.Revision != parent.Revision || current.CrewRun.Waiting.DecisionKind != "approval" {
		t.Fatal("failed descendant decision partly changed parent", current)
	}
	h.call("GET", "/v1/crew/runs/"+child.ID+flowHTTPQuery, f.human, nil, 200, &current)
	if current.CrewRun.Decisions[direct.NodeID].Approve {
		t.Fatal("stale parent overrode a newer leaf rejection")
	}
}

func TestFlowChildrenQuarantinedChildEndsWithoutSuccess(t *testing.T) {
	f, h, run, _, _ := nestedFlowFixture(t)
	lease := "fixture-child-executor-0123456789"
	h.call("POST", "/v1/crew/runs/"+run.ID+"/lease", tokEngine, FlowLeaseInput{Scope: scopeA, Action: "acquire", LeaseToken: lease}, 200, nil)
	state := *run.CrewRun
	state.Nodes = map[string]CrewRunNode{"start": {Status: "done"}, "delegate": {Status: "running", Runs: 1}, "end": {Status: "pending"}}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/progress", tokEngine, CrewRunUpdate{Scope: scopeA, ExpectedRevision: run.Revision, Run: state, LeaseToken: lease}, 200, &run)
	target := flowDefinitionByHandle(run.CrewRun.Definitions, "fixture-middle")
	var created FlowChildCreated
	h.call("POST", "/v1/crew/runs/"+run.ID+"/children", tokEngine, FlowChildInput{Scope: scopeA, LeaseToken: lease, NodeID: "delegate", Attempt: 1, Kind: "workflow", CrewID: target.ID, Input: "task"}, 200, &created)
	child, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, created.Child.ID)
	child.PrivacyQuarantined = true
	if e := f.repo.Replace(testCtx, colCrewRuns, child, child.Revision); e != nil {
		t.Fatal(e)
	}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/children/cancel", tokEngine, FlowChildrenCancel{Scope: scopeA, LeaseToken: lease}, 200, nil)
	state = *created.Parent.CrewRun
	state.Status = "succeeded"
	update := CrewRunUpdate{Scope: scopeA, ExpectedRevision: created.Parent.Revision, Run: state, LeaseToken: lease}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/progress", tokEngine, update, 409, nil)
	update.Run.Status = "failed"
	h.call("POST", "/v1/crew/runs/"+run.ID+"/progress", tokEngine, update, 200, nil)
}
