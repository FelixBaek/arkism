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
		f.meetings.recs[ref.ID] = MeetingRecord{ID: ref.ID, Revision: 1, Meeting: Meeting{Status: f.meetingStatus, TeamID: in.CrewID, TeamRevision: parent.State.Definitions[in.CrewID].Revision,
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

// These are local typed/fake-store observations, never provider or child processes.
func childObserverContext() (context.Context, *AttemptObserver) {
	observer := NewAttemptObserver(nil)
	scope := AttemptContext{Run: ObservationRun{Scope: ObservationScope{WorkspaceID: "w", ProjectID: "p"}}}
	return WithAttemptContext(WithAttemptObserver(context.Background(), observer), scope), observer
}
func TestFlowObservationCallContextUsesActualLegacyRun(t *testing.T) {
	ctx, _ := childObserverContext()
	definition := Crew{Type: "workflow", Name: "actual definition"}
	x := &runExec{e: &Engine{}, item: CrewItem{ID: "crew-definition", Revision: 11, Crew: definition}, run: Run{ID: "real-run", Revision: 27, State: RunState{CrewID: "crew-definition", CrewRevision: 11, Nodes: map[string]*NodeState{"n": {Runs: 3}}}}}
	got, ok := AttemptContextFrom(x.callContext(ctx, "n"))
	if !ok || got.Run.RunRef != (ObservationRef{Collection: "crew_runs", ID: "real-run", Revision: 27}) || got.NodeAttempt != 3 || got.Run.Definition.Ref.Revision != 11 || got.Run.InvocationMode != "legacy" || *got.Run.RootRunID != "real-run" || *got.Run.RootRunCollection != "crew_runs" || got.Run.Definition.DefinitionSHA256 == nil {
		t.Fatalf("actual identity unavailable or fabricated: %+v", got)
	}
	if _, flow := x.callContext(ctx, "n").Value(flowCallScopeKey{}).(flowCallScope); flow {
		t.Fatal("legacy observer invented FLOW scope")
	}
	if got := x.callContext(context.Background(), "n"); got != context.Background() {
		t.Fatal("observer off changed legacy context")
	}
}
func TestFlowObservationCallContextRefusesMissingActualVersions(t *testing.T) {
	for _, part := range []string{"run", "definition", "node", "root"} {
		t.Run(part, func(t *testing.T) {
			ctx, observer := childObserverContext()
			x := &runExec{e: &Engine{}, item: CrewItem{ID: "definition", Revision: 5, Crew: Crew{Type: "workflow"}}, run: Run{ID: "run", Revision: 8, State: RunState{CrewID: "definition", CrewRevision: 5, Nodes: map[string]*NodeState{"n": {Runs: 2}}}}}
			switch part {
			case "run":
				x.run.Revision = 0
			case "definition":
				x.run.State.CrewRevision = 0
			case "node":
				x.run.State.Nodes["n"].Runs = 0
			case "root":
				x.run.State.RootID = "unproved-root"
			}
			actual, _ := AttemptContextFrom(x.callContext(ctx, "n"))
			if actual.Run.RunRef.ID != "" || !strings.Contains(strings.Join(observer.Snapshot().Issues, " "), "identity_unavailable") {
				t.Fatal("missing real version/root silently accepted", actual)
			}
		})
	}
}
func TestFlowDelegationObservationWaitRemainsReservedAndReusesIdentity(t *testing.T) {
	e, store, root := childFixture(t, node("call", "subflow", "workflow", "child"), waitingChild())
	ctx, observer := childObserverContext()
	if err := e.Drive(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	var first AttemptObservation
	for _, r := range observer.Snapshot().Latest {
		if r.Identity.Phase == "tool" {
			first = r
		}
	}
	if first.Identity.AttemptID == "" || first.Status != "reserved" || first.UsageRole != "transport_aggregate" || first.Identity.CanonicalAttemptID != nil || first.Identity.AttemptRef != nil {
		t.Fatalf("delegation missing or masquerades as provider FLOW call: %+v", observer.Snapshot())
	}
	if err := e.Drive(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	before := observer.Snapshot()
	if len(before.Records) != 1 || before.Latest[0].Identity.AttemptID != first.Identity.AttemptID || before.Latest[0].Status != "reserved" {
		t.Fatal("inflight delegation revised to terminal", before)
	}
	_, ref := childRun(t, store, root.ID)
	store.decide(t, ref.ID, "ask", true)
	if err := e.Drive(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	after := observer.Snapshot()
	if len(after.Records) != 2 || len(after.Latest) != 1 || after.Latest[0].Identity.AttemptID != first.Identity.AttemptID || after.Latest[0].Status != "succeeded" || after.Latest[0].ObservationRevision != 2 {
		t.Fatalf("terminal child did not settle original transport: %+v", after)
	}
	if after.Latest[0].Usage.DurationNS == nil || after.Latest[0].Usage.CostUSD != nil || after.Latest[0].LeafCoverage != "partial" {
		t.Fatal("delegation invented monetary leaf coverage", after.Latest[0])
	}
}

func TestFlowDelegationObservationNestedChildUsesRealExecutionAndParent(t *testing.T) {
	child := wf("child", &Graph{Nodes: []Node{node("nested", "subflow", "workflow", "grand"), node("end", "end")}, Edges: []Edge{edge("nested", "end")}})
	grand := wf("grand", &Graph{Nodes: []Node{node("end", "end")}})
	e, store, root := childFixture(t, node("call", "subflow", "workflow", "child"), child)
	root.State.Definitions[grand.ID] = grand
	store.runs[root.ID] = clone(root)
	ctx, observer := childObserverContext()
	if err := e.Drive(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	latest := observer.Snapshot().Latest
	if len(latest) != 2 {
		t.Fatal("nested delegation not captured", observer.Snapshot())
	}
	var outer, inner AttemptObservation
	for _, record := range latest {
		if record.Identity.Run.RunRef.ID == root.ID {
			outer = record
		} else {
			inner = record
		}
	}
	_, childRef := childRun(t, store, root.ID)
	if outer.Status != "succeeded" || inner.Status != "succeeded" || inner.Identity.Run.RunRef.ID != childRef.ID || inner.Identity.Run.RunRef.Revision < 1 || inner.Identity.Run.ParentRunID == nil || *inner.Identity.Run.ParentRunID != root.ID || inner.Identity.ParentAttemptID == nil || *inner.Identity.ParentAttemptID != outer.Identity.AttemptID || inner.Task != outer.Task || inner.Identity.Run.Definition.Ref.ID != "child" {
		t.Fatalf("child identity borrowed parent or invented root: outer=%+v inner=%+v", outer, inner)
	}
	evidence := ObservationEvidenceFromContext(ctx)
	if len(evidence.ParentLinks) != 1 || qualifyObservationParents(map[string]AttemptObservation{outer.Identity.AttemptID: outer, inner.Identity.AttemptID: inner}, evidence) != nil {
		t.Fatal("actual subflow provenance missing", evidence)
	}
	if observer.Snapshot().ProviderLeafTotals.DispatchedAttempts != 0 {
		t.Fatal("delegation counted as provider leaf")
	}
}
func TestFlowDelegationObservationLinkedMeetingRetainsActualMeeting(t *testing.T) {
	team := teamItem("round_robin", &TeamEnding{MaxRounds: 1}, threeMembers()...)
	items := baseItems(team)
	e, store, root := childFixture(t, node("call", "team", "team", "team"), team)
	for _, item := range items {
		root.State.Definitions[item.ID] = item
	}
	store.runs[root.ID] = clone(root)
	store.meetingStatus = "pending"
	calls := 0
	e.MeetingAI = func(ctx context.Context, s aiSpec) (aiAnswer, error) {
		calls++
		observed, ok := AttemptContextFrom(ctx)
		if !ok || observed.Run.RunRef.Collection != "crew_meetings" || observed.Run.RunRef.Revision < 1 || observed.Run.ParentRunID == nil || *observed.Run.ParentRunID != root.ID || observed.ParentAttemptID == nil || observed.Run.Definition.Ref.Revision != team.Revision {
			t.Errorf("linked meeting missing actual execution/parent: %+v", observed)
		}
		if ok && observed.ParentAttemptID != nil {
			parent, found := observationLatest(ctx, *observed.ParentAttemptID)
			if !found || parent.Identity.Phase != "tool" || parent.UsageRole != "transport_aggregate" {
				t.Error("linked meeting lacks real delegation parent", parent)
			}
		}
		out, err := defaultAnswer(s)
		return aiAnswer{OK: err == nil, Output: out, Model: s.Model}, err
	}
	ctx, observer := childObserverContext()
	if err := e.Drive(ctx, root.ID); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("fake meeting did not run")
	}
	parent, _ := store.GetRun(context.Background(), root.ID)
	if parent.State.Status != "succeeded" {
		t.Fatal("observation changed meeting outcome", parent.State)
	}
	for _, record := range observer.Snapshot().Latest {
		if record.Identity.Phase == "tool" && (record.Identity.CanonicalAttemptID != nil || record.Status != "succeeded") {
			t.Fatal("delegation is fake model reservation or never completed", record)
		}
	}
}
func TestFlowObservationActualClientScopeAndForeignParent(t *testing.T) {
	ctx := WithAttemptObserver(context.Background(), NewAttemptObserver(nil))
	x := &runExec{e: &Engine{Store: &Client{Scope: Scope{WorkspaceID: "actual-w", ProjectID: "actual-p"}}}, item: CrewItem{ID: "definition", Revision: 7, Crew: Crew{Type: "workflow"}}, run: Run{ID: "actual", Revision: 9, State: RunState{CrewID: "definition", CrewRevision: 7, Nodes: map[string]*NodeState{"n": {Runs: 4}}}}}
	current, _ := AttemptContextFrom(x.callContext(ctx, "n"))
	if current.Run.Scope.WorkspaceID != "actual-w" || current.Run.Scope.ProjectID != "actual-p" {
		t.Fatal("did not use actual bound Client scope", current)
	}
	x.run.State.ParentID = "unproved-parent"
	x.run.State.RootID = "unproved-root"
	invalid, _ := AttemptContextFrom(x.callContext(ctx, "n"))
	if invalid.Run.RunRef.ID != "" {
		t.Fatal("unproved parent accepted", invalid)
	}
}

func TestFlowDelegationObservationClockIndependentFromBusinessNow(t *testing.T) {
	ctx, observer := childObserverContext()
	x := &runExec{e: &Engine{Now: func() time.Time { t.Error("observer called business clock"); return time.Unix(0, 0) }}}
	parent := AttemptContext{Run: ObservationRun{Scope: ObservationScope{WorkspaceID: "w", ProjectID: "p"}, RunRef: ObservationRef{Collection: "crew_runs", ID: "actual", Revision: 9}, RootRunCollection: observationString("crew_runs"), RootRunID: observationString("actual"), Definition: ObservationDefinition{Ref: ObservationRef{Collection: "crew", ID: "definition", Revision: 4}, SourceType: "workflow", HashState: "not_recorded"}, ExecutionPath: "workflow", InvocationMode: "flow"}, Task: ObservationTask{Namespace: "crew_experiment", TaskID: "task", TaskVersion: "v1", InputSHA256: strings.Repeat("a", 64), SpecSHA256: strings.Repeat("b", 64)}, NodeID: "n", NodeAttempt: 2}
	ctx = WithAttemptContext(ctx, parent)
	before := time.Now().UnixNano()
	_, record := x.childObservationContext(ctx, FlowChild{Kind: "workflow", ID: "child", NodeID: "n", Attempt: 2})
	if record == nil {
		t.Fatal("missing transport record")
	}
	x.finishChildObservation(ctx, record, result{}, nil)
	after := time.Now().UnixNano()
	latest := observer.Snapshot().Latest
	if len(latest) != 1 || latest[0].Usage.StartedAtNS == nil || *latest[0].Usage.StartedAtNS < before || latest[0].Usage.FinishedAtNS == nil || *latest[0].Usage.FinishedAtNS > after || latest[0].Usage.DurationNS == nil || latest[0].Usage.DurationBasis != "observed_monotonic" {
		t.Fatalf("business clock corrupted observational time: %+v", latest)
	}
}
