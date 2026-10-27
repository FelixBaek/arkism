package memory

import (
	"errors"
	"testing"
)

// A restored descendant snapshot exercises accounting linkage independently
// from the child creation tests. The budget always belongs to the root record.
func attemptDescendant(t *testing.T, f *fixture, root Record, in FlowAttemptInput) Record {
	t.Helper()
	parent, e := f.svc.Repo.Get(testCtx, colCrewRuns, scopeA, root.ID)
	if e != nil {
		t.Fatal(e)
	}
	child := parent
	child.ID = "accounted-descendant"
	child.Revision = 1
	child.FlowBudget = nil
	state := *parent.CrewRun
	state.ParentID, state.ParentNodeID, state.Depth = parent.ID, "delegate", 1
	state.Children = nil
	child.CrewRun = &state
	child.LeaseToken = in.LeaseToken
	if e = f.svc.Repo.Insert(testCtx, colCrewRuns, child); e != nil {
		t.Fatal(e)
	}
	ps := *parent.CrewRun
	ps.Children = map[string]FlowChildRef{"delegate:1": {Kind: "workflow", ID: child.ID, NodeID: "delegate", Attempt: 1, CrewID: child.CrewRun.CrewID, Required: true}}
	parent.CrewRun = &ps
	if e = f.svc.Repo.Replace(testCtx, colCrewRuns, parent, parent.Revision); e != nil {
		t.Fatal(e)
	}
	return child
}

func TestFlowAttemptDescendantsShareRootBudgetAndRequireActualLink(t *testing.T) {
	f, root, in := flowAttemptFixture(t)
	setAttemptPolicy(t, f, root, FlowPolicy{MaxDepth: 3, MaxChildren: 32, MaxConcurrent: 4, MaxTokens: 100})
	in.MaxInputTokens, in.MaxOutputTokens = 30, 30
	first := attemptReserve(t, f, root.ID, in)
	child := attemptDescendant(t, f, root, in)
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, child.ID, in); !errors.Is(e, errFlowBudgetLimit) {
		t.Fatal("descendant manufactured a new budget", e)
	}
	settle := FlowAttemptSettleInput{Scope: scopeA, LeaseToken: in.LeaseToken, FlowAttemptUsage: FlowAttemptUsage{Status: "succeeded", UsageKnown: true, InputTokens: 5, OutputTokens: 5}}
	if _, e := f.svc.SettleFlowAttempt(testCtx, tokEngine, root.ID, first.ID, settle); e != nil {
		t.Fatal(e)
	}
	second := attemptReserve(t, f, child.ID, in)
	ledger, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, root.ID)
	if ledger.FlowBudget.Attempts != 2 || ledger.FlowBudget.ReservedTokens != 60 || ledger.FlowBudget.CommittedTokens != 10 {
		t.Fatal(ledger.FlowBudget)
	}
	if _, e := f.svc.SettleFlowAttempt(testCtx, tokEngine, root.ID, second.ID, settle); !errors.Is(e, ErrConflict) {
		t.Fatal("wrong run settled descendant", e)
	}
	current, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, root.ID)
	st := *current.CrewRun
	st.Children = nil
	current.CrewRun = &st
	if e := f.repo.Replace(testCtx, colCrewRuns, current, current.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, child.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal("unlinked descendant escaped root", e)
	}
}

func TestFlowAttemptLinkedMeetingMayResumeOnlyItsActualWaitingNode(t *testing.T) {
	f, run, in := flowAttemptFixture(t)
	current, _ := f.repo.Get(testCtx, colCrewRuns, scopeA, run.ID)
	st := *current.CrewRun
	definition := cloneFlowCrew(*st.Definition)
	definition.Graph.Nodes[1] = CrewNode{ID: "research", Type: "team", Config: map[string]string{"team": "saved-team"}}
	st.Definition = &definition
	st.Status = "waiting"
	st.Nodes = map[string]CrewRunNode{"research": {Status: "waiting", Runs: 1}}
	st.Children = map[string]FlowChildRef{"research:1": {Kind: "meeting", ID: "linked-meeting", NodeID: "research", Attempt: 1, CrewID: "saved-team", Required: true}}
	st.Waiting = &CrewRunWaiting{Kind: "child", NodeID: "research", ChildKind: "meeting", ChildRunID: "linked-meeting"}
	current.CrewRun = &st
	meeting := Record{ID: "linked-meeting", Scope: scopeA, Kind: colCrewMeetings, Status: statusActive, Revision: 1, CrewMeeting: &CrewMeeting{Status: "running", RootID: run.ID, ParentID: run.ID, ParentNodeID: "research", TeamID: "saved-team"}}
	if e := f.repo.Insert(testCtx, colCrewMeetings, meeting); e != nil {
		t.Fatal(e)
	}
	if e := f.repo.Replace(testCtx, colCrewRuns, current, current.Revision); e != nil {
		t.Fatal(e)
	}
	in.CallKey = "meeting/turn2-1/retry0"
	if r := attemptReserve(t, f, run.ID, in); !r.DispatchAllowed {
		t.Fatal(r)
	}
	meeting.CrewMeeting.Status = "waiting"
	meeting.CrewMeeting.Waiting = &MeetingWaiting{Kind: "budget"}
	if e := f.repo.Replace(testCtx, colCrewMeetings, meeting, meeting.Revision); e != nil {
		t.Fatal(e)
	}
	in.CallKey = "meeting/turn2-2/retry0"
	if _, e := f.svc.ReserveFlowAttempt(testCtx, tokEngine, run.ID, in); !errors.Is(e, ErrConflict) {
		t.Fatal("budget approval bypassed", e)
	}
}
