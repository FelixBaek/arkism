package memory

import (
	"context"
	"strings"
	"testing"
)

func TestFlowSourceRevocationBlocksProgressAndNewChildButAllowsRedactedStop(t *testing.T) {
	f, h, run, leaf, _ := nestedFlowFixture(t)
	lease := leaseFlow(t, f, run.ID)
	state := *run.CrewRun
	state.Nodes["delegate"] = CrewRunNode{Status: "running", Runs: 1}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/progress", tokEngine, CrewRunUpdate{Scope: scopeA, ExpectedRevision: run.Revision, Run: state, LeaseToken: lease}, 200, &run)
	f.svc.Auth = flowChildReadDeny{Authorizer: f.auth, id: leaf.ID}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/progress", tokEngine, CrewRunUpdate{Scope: scopeA, ExpectedRevision: run.Revision, Run: *run.CrewRun, LeaseToken: lease}, 403, nil)
	target := flowDefinitionByHandle(run.CrewRun.Definitions, "fixture-middle")
	h.call("POST", "/v1/crew/runs/"+run.ID+"/children", tokEngine, FlowChildInput{Scope: scopeA, LeaseToken: lease, NodeID: "delegate", Attempt: 1, Kind: "workflow", CrewID: target.ID, Input: "Fixture task"}, 403, nil)
	if len(f.repo.rows(colCrewRuns)) != 1 {
		t.Fatal("revoked source still created a child")
	}
	var stopped Record
	raw := h.call("POST", "/v1/crew/runs/"+run.ID+"/stop", tokEngine, FlowStopInput{Scope: scopeA, ExpectedRevision: run.Revision}, 200, &stopped)
	assertRedactedFlowStop(t, raw, stopped, run.ID)
	if stopped.CrewRun.Status != "running" || !stopped.CrewRun.CancelRequested || stopped.Revision != run.Revision+1 {
		t.Fatal("stop fabricated completion or failed to request cancellation", stopped)
	}
	raw = h.call("POST", "/v1/crew/runs/"+run.ID+"/stop", tokEngine, FlowStopInput{Scope: scopeA, ExpectedRevision: run.Revision}, 200, &stopped)
	assertRedactedFlowStop(t, raw, stopped, run.ID)
	requireDecision(t, f.repo, "flow_cancelled", "automatic") // #466: the engine's stop, counted once though asked twice
	f.svc.Auth = f.auth
	h.call("GET", "/v1/crew/runs/"+run.ID+flowHTTPQuery, f.human, nil, 200, &stopped)
	if stopped.CrewRun.Definition == nil || !stopped.CrewRun.CancelRequested {
		t.Fatal("redacted response destroyed the stored run")
	}
}

func assertRedactedFlowStop(t *testing.T, raw []byte, r Record, id string) {
	t.Helper()
	if r.ID != id || r.CrewRun == nil || r.CrewRun.Definition != nil || len(r.CrewRun.Definitions) != 0 || len(r.CrewRun.Nodes) != 0 || len(r.CrewRun.Children) != 0 || r.CrewRun.Input != "" || r.CrewRun.Waiting != nil || r.CreatedBy != "" || strings.Contains(string(raw), "Fixture task") {
		t.Fatal("cancellation response exposed stored content", string(raw))
	}
}

func TestFlowSourceRevocationStopsParentApprovalBeforeAnyDescendantChanges(t *testing.T) {
	f, h, run, leaf, engine := nestedFlowFixture(t)
	if err := engine.Drive(testCtx, run.ID); err != nil {
		t.Fatal(err)
	}
	h.call("GET", "/v1/crew/runs/"+run.ID+flowHTTPQuery, f.human, nil, 200, &run)
	before := map[string]int64{}
	for _, row := range f.repo.rows(colCrewRuns) {
		before[row.ID] = row.Revision
	}
	f.svc.Auth = flowChildReadDeny{Authorizer: f.auth, id: leaf.ID}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/decision", f.human, CrewRunDecisionInput{Scope: scopeA, ExpectedRevision: run.Revision, NodeID: run.CrewRun.Waiting.NodeID, Approve: true}, 403, nil)
	for _, row := range f.repo.rows(colCrewRuns) {
		if row.Revision != before[row.ID] || len(row.CrewRun.Decisions) != 0 {
			t.Fatal("revoked source changed a decision", row.ID)
		}
	}
}

type flowSourceConflictRepo struct{ Repository }

func (r flowSourceConflictRepo) Transaction(context.Context, func(context.Context, Tx) error) error {
	return ErrConflict
}

func TestFlowSourceIdempotentReplayChecksOldPinsAfterLiveGraphChanges(t *testing.T) {
	f, h, _ := flowHTTPFixture(t)
	leaf := f.crew(flowChildGraph("source-replay-leaf", ""))
	item := f.crew(flowChildGraph("source-replay-parent", leaf.Crew.Handle))
	in := CrewRunInput{Scope: scopeA, RequestID: "source-replay", Run: CrewRun{CrewID: item.ID, CrewRevision: item.Revision, Trigger: "manual", Status: "running", InvocationMode: "flow", Input: "Fixture task", Nodes: map[string]CrewRunNode{}}}
	h.call("POST", "/v1/crew/runs", tokEngine, in, 200, nil)
	edited := cloneFlowCrew(*item.Crew)
	edited.Graph = &CrewGraph{Nodes: []CrewNode{{ID: "start", Type: "start"}, {ID: "end", Type: "end"}}, Edges: []CrewEdge{{From: "start", To: "end"}}}
	if _, err := f.svc.UpdateCrew(testCtx, f.human, item.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: item.Revision, Crew: &edited}); err != nil {
		t.Fatal(err)
	}
	f.svc.Auth = flowChildReadDeny{Authorizer: f.auth, id: leaf.ID}
	h.call("POST", "/v1/crew/runs", tokEngine, in, 403, nil)
	// Simulate the concurrent-create conflict branch: it also returns the old
	// pinned execution and must not fall back to an unchecked full record.
	f.svc.Repo = flowSourceConflictRepo{f.repo}
	h.call("POST", "/v1/crew/runs", tokEngine, in, 403, nil)
}

func linkedMeetingSourceFixture(t *testing.T) (*fixture, flowHTTPHarness, Record, Record, Record) {
	t.Helper()
	f, h, _ := flowHTTPFixture(t)
	a := f.crew(crewAgent("Private fixture role", "private-fixture-role"))
	team := f.crew(crewTeam("private-fixture-team", CrewMember{AgentID: a.ID, Role: "worker"}))
	run := commonFlowStart(t, h, team, "source-meeting")
	lease := leaseFlow(t, f, run.ID)
	state := *run.CrewRun
	state.Nodes["work"] = CrewRunNode{Status: "running", Runs: 1}
	h.call("POST", "/v1/crew/runs/"+run.ID+"/progress", tokEngine, CrewRunUpdate{Scope: scopeA, ExpectedRevision: run.Revision, Run: state, LeaseToken: lease}, 200, &run)
	var child FlowChildCreated
	h.call("POST", "/v1/crew/runs/"+run.ID+"/children", tokEngine, FlowChildInput{Scope: scopeA, LeaseToken: lease, NodeID: "work", Attempt: 1, Kind: "meeting", CrewID: team.ID, Input: "Private fixture topic"}, 200, &child)
	var meeting Record
	h.call("GET", "/v1/crew/meetings/"+child.Child.ID+flowHTTPQuery, f.human, nil, 200, &meeting)
	return f, h, child.Parent, meeting, a
}

func TestFlowLinkedMeetingSourceRevocationProtectsSnapshotAndWrites(t *testing.T) {
	f, h, parent, meeting, source := linkedMeetingSourceFixture(t)
	f.svc.Auth = flowChildReadDeny{Authorizer: f.auth, id: source.ID}
	h.call("GET", "/v1/crew/meetings/"+meeting.ID+flowHTTPQuery, f.human, nil, 403, nil)
	var page CrewMeetingPage
	h.call("GET", "/v1/crew/meetings"+flowHTTPQuery, f.human, nil, 200, &page)
	if len(page.Items) != 0 || page.Counts["all"] != 0 {
		t.Fatal("meeting list exposed revoked linked snapshot", page)
	}
	next := *meeting.CrewMeeting
	next.Status = "running"
	h.call("POST", "/v1/crew/meetings/"+meeting.ID+"/progress", tokEngine, CrewMeetingUpdate{Scope: scopeA, ExpectedRevision: meeting.Revision, LeaseToken: "meeting-source-lease-0123456789", Meeting: next}, 403, nil)
	h.call("POST", "/v1/crew/meetings/"+meeting.ID+"/notes", f.human, CrewMeetingNoteInput{Scope: scopeA, ExpectedRevision: meeting.Revision, Text: "new note"}, 403, nil)
	stored, err := f.repo.Get(testCtx, colCrewMeetings, scopeA, meeting.ID)
	if err != nil || stored.Revision != meeting.Revision {
		t.Fatal("source denial mutated linked meeting", err, stored.Revision)
	}
	f.svc.Auth = f.auth
	h.call("GET", "/v1/crew/runs/"+parent.ID+flowHTTPQuery, f.human, nil, 200, nil)
	h.call("GET", "/v1/crew/meetings/"+meeting.ID+flowHTTPQuery, f.human, nil, 200, nil)
	h.call("POST", "/v1/crew/meetings/"+meeting.ID+"/notes", f.human, CrewMeetingNoteInput{Scope: scopeA, ExpectedRevision: meeting.Revision, Text: "authorized note"}, 200, nil)
}
