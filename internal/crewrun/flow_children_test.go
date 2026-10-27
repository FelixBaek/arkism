package crewrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type childStoreFake struct {
	*fakeStore
	tokens        map[string]string
	meetings      *fakeMeetings
	created       int
	loseResponse  bool
	meetingStatus string
	attempts      map[string]FlowAttemptReservation
	settlements   map[string]FlowAttemptSettlement
	requests      []FlowAttemptRequest
	reserveErr    error
	settleErr     error
	createErr     error
}

func (f *childStoreFake) LeaseRun(_ context.Context, id, action, token string) (FlowLease, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tokens == nil {
		f.tokens = map[string]string{}
	}
	if f.tokens[id] != "" && f.tokens[id] != token {
		return FlowLease{}, &APIError{409, "revision_conflict"}
	}
	if action == "release" {
		delete(f.tokens, id)
	} else {
		f.tokens[id] = token
	}
	return FlowLease{LeaseUntil: time.Now().Add(time.Minute), CancelRequested: f.runs[id].State.CancelRequested}, nil
}

func (f *childStoreFake) UpdateRun(ctx context.Context, r Run) (Run, error) {
	f.mu.Lock()
	valid := f.tokens[r.ID] == r.LeaseToken && r.LeaseToken != ""
	cancelled := f.runs[r.ID].State.CancelRequested
	f.mu.Unlock()
	if !valid || cancelled && r.State.Status != "stopped" {
		return Run{}, &APIError{409, "revision_conflict"}
	}
	r.State.CancelRequested = cancelled
	return f.fakeStore.UpdateRun(ctx, r)
}

func (f *childStoreFake) CreateFlowChild(_ context.Context, parent Run, in FlowChildRequest) (FlowChildCreated, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return FlowChildCreated{}, f.createErr
	}
	parent = f.runs[parent.ID]
	key := childKey(in.NodeID, in.Attempt)
	if ref, ok := parent.State.Children[key]; ok {
		return FlowChildCreated{Parent: clone(parent), Child: ref}, nil
	}
	ref := FlowChild{Kind: in.Kind, ID: fmt.Sprintf("%s-%s", parent.ID, key), CrewID: in.CrewID, NodeID: in.NodeID, Attempt: in.Attempt, Required: true}
	if parent.State.Children == nil {
		parent.State.Children = map[string]FlowChild{}
	}
	parent.State.Children[key] = ref
	parent.Revision++
	f.runs[parent.ID] = clone(parent)
	f.created++
	if in.Kind == "workflow" {
		it := parent.State.Definitions[in.CrewID]
		st := RunState{InvocationMode: "flow", CrewID: it.ID, CrewRevision: it.Revision, Definition: &it.Crew,
			Definitions: parent.State.Definitions, RootID: parent.State.RootID, ParentID: parent.ID, ParentNodeID: in.NodeID,
			Depth: parent.State.Depth + 1, Deadline: parent.State.Deadline, Status: "running", Trigger: "call", Input: in.Input, Nodes: map[string]*NodeState{}}
		for _, n := range it.Crew.Graph.Nodes {
			st.Nodes[n.ID] = &NodeState{Status: "pending"}
		}
		f.runs[ref.ID] = clone(Run{ID: ref.ID, Revision: 1, State: st})
	} else {
		f.meetings.mu.Lock()
		snapshot := &MeetingSnapshot{Team: parent.State.Definitions[in.CrewID].Crew, Agents: map[string]Crew{}}
		for id, item := range parent.State.Definitions {
			snapshot.Agents[id] = item.Crew
		}
		f.meetings.recs[ref.ID] = MeetingRecord{ID: ref.ID, Revision: 1, Meeting: Meeting{Status: f.meetingStatus, TeamID: in.CrewID,
			Topic: in.Input, MaxRounds: 1, TeamSnapshot: snapshot, RootID: parent.State.RootID, ParentID: parent.ID, ParentNodeID: in.NodeID}}
		f.meetings.mu.Unlock()
	}
	if f.loseResponse {
		f.loseResponse = false
		return FlowChildCreated{}, errors.New("response lost after commit")
	}
	return FlowChildCreated{Parent: clone(parent), Child: ref}, nil
}

func (f *childStoreFake) CancelFlowChildren(_ context.Context, parent Run) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ref := range f.runs[parent.ID].State.Children {
		if ref.Kind == "workflow" {
			child := f.runs[ref.ID]
			if !terminal(child.State.Status) {
				child.State.CancelRequested = true
				child.Revision++
				f.runs[ref.ID] = child
			}
		} else {
			f.meetings.mu.Lock()
			child := f.meetings.recs[ref.ID]
			if !terminalMeeting(child.Meeting.Status) {
				child.Meeting.Status = "stopping"
				child.Revision++
				f.meetings.recs[ref.ID] = child
			}
			f.meetings.mu.Unlock()
		}
	}
	return nil
}

func (f *childStoreFake) cancelRoot(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.runs[id]
	r.State.CancelRequested = true
	r.Revision++
	f.runs[id] = r
}

func childFixture(t *testing.T, call Node, child CrewItem) (*Engine, *childStoreFake, Run) {
	t.Helper()
	root := wf("root", &Graph{Nodes: []Node{node("start", "start"), call, node("end", "end")}, Edges: []Edge{edge("start", call.ID), edge(call.ID, "end")}})
	fs := &childStoreFake{fakeStore: newFakeStore(root, child), meetingStatus: "waiting"}
	fs.meetings = &fakeMeetings{recs: map[string]MeetingRecord{}, lease: map[string]string{}, spend: map[string]float64{}}
	e := testEngine(t, fs.fakeStore)
	e.Store, e.Meetings = fs, fs.meetings
	if err := e.LoadCrew(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err := e.StartRun(context.Background(), root.ID, "call", map[string]any{"request": "same request"})
	if err != nil {
		t.Fatal(err)
	}
	run.State.InvocationMode, run.State.Definition, run.State.RootID = "flow", &root.Crew, run.ID
	run.State.Definitions = map[string]CrewItem{root.ID: root, child.ID: child}
	fs.runs[run.ID] = clone(run)
	return e, fs, run
}

func waitingChild() CrewItem {
	return wf("child", &Graph{Nodes: []Node{node("ask", "approval"), node("end", "end", "summary", "child finished")}, Edges: []Edge{{From: "ask", To: "end", Port: "approve"}}})
}

func childRun(t *testing.T, fs *childStoreFake, parentID string) (Run, FlowChild) {
	t.Helper()
	run, err := fs.GetRun(context.Background(), parentID)
	if err != nil {
		t.Fatal(err)
	}
	ref := run.State.Children[childKey("call", 1)]
	if ref.ID == "" {
		t.Fatal("missing persisted child", run.State)
	}
	return run, ref
}

func TestFlowChildApprovalSurvivesRestartAndReusesExecution(t *testing.T) {
	e, fs, root := childFixture(t, node("call", "subflow", "workflow", "child"), waitingChild())
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	parent, ref := childRun(t, fs, root.ID)
	child, _ := fs.GetRun(context.Background(), ref.ID)
	if parent.State.Status != "waiting" || parent.State.Waiting.Kind != "child" || child.State.Status != "waiting" {
		t.Fatal("approval must park both executions", parent.State, child.State)
	}
	if w := parent.State.Waiting; w.ChildRunID != ref.ID || w.ChildKind != "workflow" || w.DecisionKind != "approval" || w.Question == "" {
		t.Fatal("caller cannot discover the child's decision", w)
	}
	fs.decide(t, ref.ID, "ask", true)
	fresh := testEngine(t, fs.fakeStore)
	fresh.Store, fresh.Meetings = fs, fs.meetings
	if err := fresh.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	parent, same := childRun(t, fs, root.ID)
	if parent.State.Status != "succeeded" || same.ID != ref.ID || fs.created != 1 || parent.State.Nodes["call"].Runs != 1 {
		t.Fatal("resume duplicated or lost child", parent.State, fs.created)
	}
}

func TestFlowChildLostCreateResponseReusesSameAttempt(t *testing.T) {
	e, fs, root := childFixture(t, node("call", "subflow", "workflow", "child"), waitingChild())
	fs.loseResponse = true
	if err := e.Drive(context.Background(), root.ID); !errors.Is(err, errFlowChildUnavailable) {
		t.Fatal("ambiguous create must stay recoverable", err)
	}
	before, ref := childRun(t, fs, root.ID)
	if before.State.Status != "running" || before.State.Nodes["call"].Status != "running" {
		t.Fatal("ambiguous create incorrectly finalized", before.State)
	}
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	after, same := childRun(t, fs, root.ID)
	if after.State.Status != "waiting" || same.ID != ref.ID || fs.created != 1 || after.State.Nodes["call"].Runs != 1 {
		t.Fatal("recovery created another attempt", after.State, fs.created)
	}
}

func TestFlowChildCapacityRetriesTheSameAttemptAndDepthRefusalFails(t *testing.T) {
	e, fs, root := childFixture(t, node("call", "subflow", "workflow", "child"), waitingChild())
	now := time.Now()
	e.Now = func() time.Time { return now }
	fs.createErr = &APIError{409, "flow_concurrency_limit"}
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	parked, _ := fs.GetRun(context.Background(), root.ID)
	if parked.State.Status != "waiting" || parked.State.Waiting.Kind != "capacity" || fs.created != 0 {
		t.Fatal("capacity must park before creating work", parked.State)
	}
	now = now.Add(4 * time.Second)
	fs.createErr = nil
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	parent, ref := childRun(t, fs, root.ID)
	if parent.State.Status != "waiting" || ref.Attempt != 1 || parent.State.Nodes["call"].Runs != 1 {
		t.Fatal("capacity retry changed attempt", parent.State, ref)
	}
	other, otherStore, otherRoot := childFixture(t, node("call", "subflow", "workflow", "child"), waitingChild())
	otherStore.createErr = &APIError{409, "flow_depth_limit"}
	if err := other.Drive(context.Background(), otherRoot.ID); err != nil {
		t.Fatal(err)
	}
	failed, _ := otherStore.GetRun(context.Background(), otherRoot.ID)
	if failed.State.Status != "failed" || !strings.Contains(failed.State.Summary, "flow_depth_limit") {
		t.Fatal("definite refusal kept retrying", failed.State)
	}
}

func TestFlowChildLoopKeepsPreviousAttemptLinks(t *testing.T) {
	child := wf("child", &Graph{Nodes: []Node{node("end", "end")}})
	e, fs, root := childFixture(t, node("call", "subflow", "workflow", "child"), child)
	root.State.Definition.Graph.Edges = append(root.State.Definition.Graph.Edges, back("call", "call", "", 1))
	fs.runs[root.ID] = clone(root)
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	parent, first := childRun(t, fs, root.ID)
	second := parent.State.Children[childKey("call", 2)]
	if fs.created != 2 || len(parent.State.Children) != 2 || first.ID == second.ID || second.Attempt != 2 {
		t.Fatal("a new loop overwrote or reused earlier child work", parent.State, fs.created)
	}
}

func TestFlowChildCancellationWaitsForChildAcknowledgement(t *testing.T) {
	e, fs, root := childFixture(t, node("call", "subflow", "workflow", "child"), waitingChild())
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	_, ref := childRun(t, fs, root.ID)
	// Another engine has the child: accepting cancellation cannot finalize the parent.
	fs.tokens[ref.ID] = "remote-owner"
	fs.cancelRoot(root.ID)
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	parent, _ := childRun(t, fs, root.ID)
	child, _ := fs.GetRun(context.Background(), ref.ID)
	if terminal(parent.State.Status) || !child.State.CancelRequested {
		t.Fatal("parent acknowledged before child stopped", parent.State, child.State)
	}
	delete(fs.tokens, ref.ID)
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	parent, _ = childRun(t, fs, root.ID)
	child, _ = fs.GetRun(context.Background(), ref.ID)
	if parent.State.Status != "stopped" || child.State.Status != "stopped" {
		t.Fatal("cancel did not reach durable child", parent.State, child.State)
	}
}

func TestFlowMeetingWaitAndNoConclusionAreNotSuccess(t *testing.T) {
	for _, status := range []string{"concluded", "no_conclusion", "failed"} {
		t.Run(status, func(t *testing.T) {
			e, fs, root := childFixture(t, node("call", "team", "team", "team"), CrewItem{ID: "team", Revision: 1, Crew: Crew{Type: "team"}})
			if err := e.Drive(context.Background(), root.ID); err != nil {
				t.Fatal(err)
			}
			parent, ref := childRun(t, fs, root.ID)
			if parent.State.Status != "waiting" || parent.State.Waiting.Kind != "child" {
				t.Fatal("meeting wait became failure", parent.State)
			}
			fs.meetings.person(ref.ID, func(m *Meeting) {
				m.Status = status
				if status == "concluded" {
					m.Conclusion = &MeetingConclusion{Summary: "accepted result"}
				}
			})
			if err := e.Drive(context.Background(), root.ID); err != nil {
				t.Fatal(err)
			}
			parent, _ = childRun(t, fs, root.ID)
			want := "failed"
			if status == "concluded" {
				want = "succeeded"
			}
			if parent.State.Status != want || fs.created != 1 {
				t.Fatal("meeting outcome or reuse", parent.State, fs.created)
			}
		})
	}
}

func TestFlowEndCannotHideAnUnfinishedRequiredChild(t *testing.T) {
	e, fs, root := childFixture(t, node("call", "subflow", "workflow", "child"), waitingChild())
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	parent, _ := childRun(t, fs, root.ID)
	// Simulate restored progress whose end is ready while the required child is still waiting.
	parent.State.Status, parent.State.Waiting = "running", nil
	parent.State.Nodes["call"].Status, parent.State.Nodes["call"].Output = "done", `"premature"`
	fs.runs[root.ID] = parent
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	parent, _ = childRun(t, fs, root.ID)
	if parent.State.Status != "waiting" || parent.State.Waiting.Kind != "child" || strings.Contains(parent.State.Summary, "premature") {
		t.Fatal("end hid unfinished child", parent.State)
	}
}

func TestFlowChildOutputUsesTerminalTopologyRatherThanNodeArrayOrder(t *testing.T) {
	graph := &Graph{Nodes: []Node{node("start", "start"), node("end", "end"), node("work", "ai")},
		Edges: []Edge{edge("start", "work"), edge("work", "end")}}
	run := Run{State: RunState{Status: "succeeded", Summary: "short summary", Definition: &Crew{Graph: graph}, Nodes: map[string]*NodeState{
		"start": {Status: "done", Output: `"input"`}, "work": {Status: "done", Output: `"intermediate"`}, "end": {Status: "done", Output: `"final output"`},
	}}}
	if got := childWorkflowOutput(run); got != "final output" {
		t.Fatal("selected intermediate output", got)
	}
	graph.Nodes[1].Config["summary"] = "explicit summary"
	if got := childWorkflowOutput(run); got != "short summary" {
		t.Fatal("discarded explicit end summary", got)
	}
}

func TestFlowChildActiveCallEndsBeforeParentStop(t *testing.T) {
	child := wf("child", &Graph{Nodes: []Node{node("read", "http", "access", "read", "url", "https://local.test")}})
	e, fs, root := childFixture(t, node("call", "subflow", "workflow", "child"), child)
	e.flowPollInterval, e.flowRenewInterval = 5*time.Millisecond, 10*time.Millisecond
	started, exited := blockingFlow(e)
	done := driveFlow(e, context.Background(), root.ID)
	awaitFlow(t, started)
	fs.cancelRoot(root.ID)
	if err := awaitDrive(t, done); err != nil {
		t.Fatal(err)
	}
	awaitFlow(t, exited)
	parent, ref := childRun(t, fs, root.ID)
	got, _ := fs.GetRun(context.Background(), ref.ID)
	if parent.State.Status != "stopped" || got.State.Status != "stopped" {
		t.Fatal("child process or parent still active", parent.State, got.State)
	}
}

func TestFlowMeetingStopInterruptsTheTurnBeforeAcknowledgement(t *testing.T) {
	rig := newMeetingRig(t, baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...))...)
	rig.add("linked", Meeting{ParentID: "parent", RootID: "root", ParentNodeID: "call"})
	rig.e.flowPollInterval = 5 * time.Millisecond
	rig.e.Store = &childStoreFake{fakeStore: rig.e.Store.(*fakeStore)}
	started, exited := make(chan struct{}), make(chan struct{})
	rig.e.MeetingAI = func(ctx context.Context, _ aiSpec) (aiAnswer, error) {
		close(started)
		<-ctx.Done()
		close(exited)
		return aiAnswer{}, ctx.Err()
	}
	done := make(chan error, 1)
	ctx := context.WithValue(context.Background(), flowCallScopeKey{}, flowCallScope{run: Run{ID: "parent", LeaseToken: "owned"}, nodeID: "call", attempt: 1})
	go func() { done <- rig.e.DriveMeeting(ctx, "linked") }()
	awaitFlow(t, started)
	rig.store.person("linked", func(m *Meeting) { m.Status = "stopping" })
	if err := awaitDrive(t, done); err != nil {
		t.Fatal(err)
	}
	awaitFlow(t, exited)
	rec, _ := rig.store.GetMeeting(context.Background(), "linked")
	if rec.Meeting.Status != "stopped" || rec.Meeting.Speaking != "" {
		t.Fatal("linked meeting did not acknowledge after stopping the turn", rec.Meeting)
	}
}

func TestFlowMeetingBudgetWaitResumesTheOriginalTranscript(t *testing.T) {
	items := baseItems(teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...))
	items[1] = agentItem("w", "작성자", func(c *Crew) { c.Budget = &AgentBudget{MonthlyUSD: 5} })
	e, fs, root := childFixture(t, node("call", "team", "team", "team"), items[0])
	for _, it := range items {
		root.State.Definitions[it.ID] = it
	}
	fs.runs[root.ID] = clone(root)
	fs.meetingStatus, fs.meetings.spend = "pending", map[string]float64{"w": 5.2}
	turns := &fakeTurns{answer: defaultAnswer}
	e.MeetingAI = turns.call
	if err := e.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	parent, ref := childRun(t, fs, root.ID)
	rec, _ := fs.meetings.GetMeeting(context.Background(), ref.ID)
	if parent.State.Status != "waiting" || parent.State.Waiting.DecisionKind != "budget" || kinds(rec.Meeting) != "brief:s" {
		t.Fatal("meeting budget wait was not preserved", parent.State, rec.Meeting)
	}
	fs.meetings.person(ref.ID, func(m *Meeting) {
		m.Status, m.Waiting, m.BudgetApproved = "running", nil, json.RawMessage(`{"by":"human-1"}`)
	})
	fresh := testEngine(t, fs.fakeStore)
	fresh.Store, fresh.Meetings, fresh.MeetingAI = fs, fs.meetings, turns.call
	if err := fresh.Drive(context.Background(), root.ID); err != nil {
		t.Fatal(err)
	}
	parent, same := childRun(t, fs, root.ID)
	rec, _ = fs.meetings.GetMeeting(context.Background(), ref.ID)
	if parent.State.Status != "succeeded" || same.ID != ref.ID || fs.created != 1 || kinds(rec.Meeting) != "brief:s turn:w turn:r conclusion:s" {
		t.Fatal("meeting transcript restarted instead of resuming", parent.State, rec.Meeting)
	}
}

func TestFlowChildClientCarriesLeaseAndAttempt(t *testing.T) {
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("local-test"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls int
	c := &Client{BaseURL: "https://local.test", TokenFile: file, Scope: Scope{WorkspaceID: "w", ProjectID: "p"}}
	c.HTTP = &http.Client{Transport: flowTransport(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"lease_token":"owned"`) || !strings.Contains(string(body), `"scope"`) {
			t.Fatal("missing child authorization", string(body))
		}
		calls++
		out := `{"children":[]}`
		if strings.HasSuffix(r.URL.Path, "/children") {
			if !strings.Contains(string(body), `"attempt":2`) || !strings.Contains(string(body), `"node_id":"call"`) {
				t.Fatal("missing deterministic attempt", string(body))
			}
			out = `{"parent":{"id":"r","revision":4},"child":{"kind":"workflow","id":"c","node_id":"call","attempt":2,"required":true}}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(out)), Header: http.Header{}}, nil
	})}
	parent := Run{ID: "r", LeaseToken: "owned"}
	got, err := c.CreateFlowChild(context.Background(), parent, FlowChildRequest{NodeID: "call", Attempt: 2, Kind: "workflow", CrewID: "definition", Input: `"query"`})
	if err != nil || got.Child.ID != "c" || got.Parent.Revision != 4 {
		t.Fatal(got, err)
	}
	if err := c.CancelFlowChildren(context.Background(), parent); err != nil || calls != 2 {
		t.Fatal("cancel request", calls, err)
	}
}
