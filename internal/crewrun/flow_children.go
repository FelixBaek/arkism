package crewrun

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/flowresult"
)

// FlowChild identifies durable work owned by one node attempt. The server
// creates the child and its parent link in the same transaction.
type FlowChild struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	CrewID   string `json:"crew_id"`
	NodeID   string `json:"node_id"`
	Attempt  int    `json:"attempt"`
	Required bool   `json:"required"`
}

type FlowPolicy struct {
	MaxDepth      int     `json:"max_depth"`
	MaxChildren   int     `json:"max_children"`
	MaxConcurrent int     `json:"max_concurrent"`
	MaxTokens     int64   `json:"max_tokens,omitempty"`
	MaxCostUSD    float64 `json:"max_cost_usd,omitempty"`
}

func childKey(nodeID string, attempt int) string { return nodeID + ":" + strconv.Itoa(attempt) }

type FlowChildRequest struct {
	NodeID  string `json:"node_id"`
	Attempt int    `json:"attempt"`
	Kind    string `json:"kind"`
	CrewID  string `json:"crew_id"`
	Input   string `json:"input"`
}

type FlowChildCreated struct {
	Parent Run       `json:"parent"`
	Child  FlowChild `json:"child"`
}

// FlowChildStore is required only for FLOW nodes that delegate work.
type FlowChildStore interface {
	CreateFlowChild(context.Context, Run, FlowChildRequest) (FlowChildCreated, error)
	CancelFlowChildren(context.Context, Run) error
}

func (c *Client) CreateFlowChild(ctx context.Context, parent Run, in FlowChildRequest) (FlowChildCreated, error) {
	var out FlowChildCreated
	err := c.do(ctx, "POST", "/v1/crew/runs/"+url.PathEscape(parent.ID)+"/children", map[string]any{
		"scope": c.Scope, "lease_token": parent.LeaseToken, "node_id": in.NodeID,
		"attempt": in.Attempt, "kind": in.Kind, "crew_id": in.CrewID, "input": in.Input,
	}, &out)
	return out, err
}

func (c *Client) CancelFlowChildren(ctx context.Context, parent Run) error {
	var out any
	return c.do(ctx, "POST", "/v1/crew/runs/"+url.PathEscape(parent.ID)+"/children/cancel",
		map[string]any{"scope": c.Scope, "lease_token": parent.LeaseToken}, &out)
}

// A transport failure after child creation is ambiguous. Keep the parent node
// recoverable so its same attempt asks for the same child, rather than failing
// the parent while the durable child continues unobserved.
var errFlowChildUnavailable = errors.New("durable child state unavailable")

func flowChildUnavailable(err error) error { return errors.Join(errFlowChildUnavailable, err) }

func flowChildNode(n Node) bool { return n.Type == "subflow" || n.Type == "team" }

func (x *runExec) childDefinition(n Node) (CrewItem, error) {
	key, kind := n.Config["workflow"], "workflow"
	if n.Type == "team" {
		key, kind = strings.TrimSpace(n.Config["team"]), "team"
	}
	if strings.TrimSpace(key) == "" {
		return CrewItem{}, failf("child_definition_missing", "the delegated definition is not specified")
	}
	if it, ok := x.run.State.Definitions[key]; ok && it.Crew.Type == kind {
		it.ID = key
		return it, nil
	}
	for id, it := range x.run.State.Definitions {
		if it.Crew.Type == kind && (id == key || kind == "workflow" && crewKey(it.Crew.Handle) == crewKey(key)) {
			it.ID = id
			return it, nil
		}
	}
	return CrewItem{}, failf("child_definition_missing", "the delegated definition is not pinned")
}

func (x *runExec) childRequest(n Node, input any) (FlowChildRequest, error) {
	it, err := x.childDefinition(n)
	if err != nil {
		return FlowChildRequest{}, err
	}
	c := x.shared(n.Config)
	in := FlowChildRequest{NodeID: n.ID, Attempt: x.state(n.ID).Runs, CrewID: it.ID, Kind: "workflow"}
	if n.Type == "subflow" {
		in.Input = encodeStored(map[string]any{"request": render(cmp.Or(c["task"], "{{input}}"), input)})
		return in, nil
	}
	in.Kind = "meeting"
	topic := strings.TrimSpace(render(cmp.Or(c["topic"], "{{input}}"), input))
	if topic == "" {
		return in, failf("bad_config", "회의 주제가 비었어요")
	}
	done := ""
	if c["done"] != "" {
		done = "완료 기준: " + c["done"]
	}
	in.Input = encodeStored(map[string]any{"topic": joinText(topic, fenceOf(c), done)})
	return in, nil
}

func (x *runExec) runFlowChild(ctx context.Context, n Node, input any) (result, error) {
	store, ok := x.e.Store.(FlowChildStore)
	if !ok {
		return result{}, failf("child_store_required", "durable delegation is unavailable")
	}
	in, err := x.childRequest(n, input)
	if err != nil {
		return result{}, err
	}
	x.mu.Lock()
	run := x.run
	ref, ok := run.State.Children[childKey(n.ID, in.Attempt)]
	x.mu.Unlock()
	if !ok || ref.Attempt != in.Attempt {
		created, err := store.CreateFlowChild(ctx, run, in)
		if err != nil {
			return x.childCreateError(err)
		}
		x.mu.Lock()
		x.run.Revision = created.Parent.Revision
		x.run.State.Children = created.Parent.State.Children
		x.mu.Unlock()
		ref = created.Child
	}
	if ref.Kind != in.Kind || ref.CrewID != in.CrewID || ref.NodeID != n.ID || ref.ID == "" {
		return result{}, failf("child_reference_invalid", "stored child does not match this node attempt")
	}
	if AttemptObserverEnabled(ctx) {
		ctx = x.callContext(ctx, n.ID)
	}
	return x.driveChild(ctx, ref)
}

func (x *runExec) childCreateError(err error) (result, error) {
	switch apiCode(err) {
	case "flow_concurrency_limit":
		at := x.e.now().UTC().Add(3 * time.Second).Truncate(time.Millisecond)
		return result{wait: &Waiting{Kind: "capacity", ResumeAt: &at}}, nil
	case "flow_depth_limit", "flow_child_limit", "flow_budget_limit", "invalid_request":
		return result{}, failf(apiCode(err), "the execution policy refused this delegated work")
	}
	return result{}, flowChildUnavailable(err)
}

// observationCallContextLocked uses the run snapshot already held by the caller.
// It adds no store reads, and never substitutes revision1 for missing evidence.
func (x *runExec) observationCallContextLocked(ctx context.Context, nodeID string) context.Context {
	if !AttemptObserverEnabled(ctx) {
		return ctx
	}
	st := x.state(nodeID)
	if x.e == nil || st == nil || st.Runs < 1 || x.run.ID == "" || x.run.Revision < 1 || x.run.State.CrewID == "" || x.run.State.CrewRevision < 1 {
		return unavailableObservationContext(ctx)
	}
	definition := x.item
	if x.run.State.Definition != nil {
		definition = CrewItem{ID: x.run.State.CrewID, Revision: x.run.State.CrewRevision, Crew: *x.run.State.Definition}
	}
	if definition.ID != x.run.State.CrewID || definition.Revision != x.run.State.CrewRevision || (x.run.State.SourceType != "" && x.run.State.SourceType != definition.Crew.Type) {
		return unavailableObservationContext(ctx)
	}
	client, _ := x.e.Store.(*Client)
	scope, ok := observationScopeFrom(ctx, client)
	digest, err := observationDigest(definition.Crew)
	if !ok || err != nil {
		return unavailableObservationContext(ctx)
	}
	mode := x.run.State.InvocationMode
	if mode == "" {
		mode = "legacy"
	}
	run := ObservationRun{Scope: scope, RunRef: ObservationRef{Collection: "crew_runs", ID: x.run.ID, Revision: x.run.Revision},
		Definition:    ObservationDefinition{Ref: ObservationRef{Collection: "crew", ID: definition.ID, Revision: definition.Revision}, SourceType: definition.Crew.Type, DefinitionSHA256: &digest, HashState: "recorded"},
		ExecutionPath: map[string]string{"workflow": "workflow", "agent": "single", "team": "crew"}[definition.Crew.Type], InvocationMode: mode}
	if !x.observationRunParent(ctx, &run) {
		return unavailableObservationContext(ctx)
	}
	return observationExecutionContext(ctx, run, x.run.State.Input, definition.Crew, nodeID, int64(st.Runs))
}

func (x *runExec) observationRunParent(ctx context.Context, run *ObservationRun) bool {
	previous, hasPrevious := AttemptContextFrom(ctx)
	if x.run.State.ParentID == "" {
		return x.observationSelfRoot(ctx, run, previous, hasPrevious)
	}
	if !hasPrevious || previous.ParentAttemptID == nil {
		return false
	}
	parent, ok := observationLatest(ctx, *previous.ParentAttemptID)
	if !ok || parent.Identity.Run.Scope != run.Scope || parent.Task != previous.Task || parent.Identity.Run.RunRef.Collection != "crew_runs" || parent.Identity.Run.RunRef.ID != x.run.State.ParentID ||
		parent.Identity.Run.RootRunID == nil || parent.Identity.Run.RootRunCollection == nil || !observationParentContextMatches(previous.Run, parent.Identity.Run, *run) {
		return false
	}
	if x.run.State.RootID != "" && x.run.State.RootID != *parent.Identity.Run.RootRunID || x.run.State.ParentNodeID != "" && x.run.State.ParentNodeID != parent.Identity.NodeID {
		return false
	}
	collection, id := "crew_runs", x.run.State.ParentID
	run.ParentRunCollection, run.ParentRunID = &collection, &id
	run.RootRunCollection, run.RootRunID = parent.Identity.Run.RootRunCollection, parent.Identity.Run.RootRunID
	if x.run.State.ParentNodeID != "" {
		node := x.run.State.ParentNodeID
		run.ParentNodeID = &node
	}
	return true
}

// A second binding in the same child node may already carry the child's own
// identity. Both forms require the independently observed delegation parent.
func observationParentContextMatches(previous, parent, child ObservationRun) bool {
	if previous.RunRef.Collection != "crew_runs" || previous.RunRef.Revision < 1 {
		return false
	}
	if previous.RunRef.ID == parent.RunRef.ID {
		return true
	}
	return previous.RunRef.ID == child.RunRef.ID && previous.ParentRunID != nil && *previous.ParentRunID == parent.RunRef.ID && previous.ParentRunCollection != nil && *previous.ParentRunCollection == parent.RunRef.Collection && observationRoot(previous) == observationRoot(parent)
}

func (x *runExec) observationSelfRoot(ctx context.Context, run *ObservationRun, previous AttemptContext, hasPrevious bool) bool {
	if x.run.State.RootID != "" && x.run.State.RootID != x.run.ID {
		return false
	}
	collection, id := "crew_runs", x.run.ID
	run.RootRunCollection, run.RootRunID = &collection, &id
	if !hasPrevious || previous.ParentAttemptID == nil {
		return true
	}
	parent, ok := observationLatest(ctx, *previous.ParentAttemptID)
	return ok && parent.Identity.Run.Scope == run.Scope && parent.Identity.Run.RunRef.Collection == collection && parent.Identity.Run.RunRef.ID == id && parent.Task == previous.Task
}

func (x *runExec) childCallContext(ctx context.Context, ref FlowChild) context.Context {
	// Normal calls already carry the snapshot acquired by callContext. Resume
	// calls without that scope hold x.mu while reading these same fields.
	scope, ok := ctx.Value(flowCallScopeKey{}).(flowCallScope)
	if !AttemptObserverEnabled(ctx) || !ok {
		scope = flowCallScope{run: Run{ID: x.run.ID, LeaseToken: x.run.LeaseToken}, definitions: x.run.State.Definitions}
	}
	scope.nodeID, scope.attempt = ref.NodeID, ref.Attempt
	return context.WithValue(ctx, flowCallScopeKey{}, scope)
}

// A delegation is a real non-model transport parent. It is not a provider leaf,
// financial reservation, or synthetic FLOW jobs receipt. Partial coverage is
// intentional until a separate accepted end-to-end child ledger proves more.
func (x *runExec) childObservationContext(ctx context.Context, ref FlowChild) (context.Context, *AttemptObservation) {
	if !AttemptObserverEnabled(ctx) {
		return ctx, nil
	}
	identity, err := AttemptIdentityFromContext(ctx, "tool", "delegate/"+ref.Kind+"/"+ref.ID, 0, nil)
	if err != nil || identity.NodeID != ref.NodeID || identity.NodeAttempt != int64(ref.Attempt) {
		return unavailableObservationContext(ctx), nil
	}
	current, _ := AttemptContextFrom(ctx)
	record, exists := observationLatest(ctx, identity.AttemptID)
	if !exists {
		record = AttemptObservation{ContractID: CrewObservationContractID, Identity: identity, Task: current.Task, Status: "reserved", DispatchState: "unknown", Usage: UnknownUsageObservation(), ObservationRevision: 1, UsageRole: "transport_aggregate", LeafCoverage: "partial"}
		started := childObservationStart(ctx, identity.AttemptID, time.Now())
		ns := started.UnixNano()
		record.Usage.StartedAtNS = &ns
		var accepted bool
		record, accepted = observationRecord(ctx, record)
		if !accepted {
			return unavailableObservationContext(ctx), nil
		}
	}
	id := record.Identity.AttemptID
	current.ParentAttemptID, current.FallbackParentCallKey = &id, nil
	return WithAttemptContext(ctx, current), &record
}

func childObservationStart(ctx context.Context, key string, now time.Time) time.Time {
	runtime := observationRuntimeFor(ctx)
	if runtime == nil {
		return now
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if started, ok := runtime.starts[key]; ok {
		return started
	}
	runtime.starts[key] = now
	return now
}

func (x *runExec) finishChildObservation(ctx context.Context, record *AttemptObservation, res result, err error) {
	if record == nil || record.Status != "reserved" || res.wait != nil || errors.Is(err, errFlowChildUnavailable) || ctx.Err() != nil {
		return
	}
	finished := time.Now()
	started := childObservationStart(ctx, record.Identity.AttemptID, finished)
	ns, duration := finished.UnixNano(), finished.Sub(started).Nanoseconds()
	if ns < started.UnixNano() {
		ns = started.UnixNano()
	}
	if duration < 0 {
		MarkAttemptObservationIncomplete(ctx, "host_attempts_incomplete")
		return
	}
	record.Usage.FinishedAtNS, record.Usage.DurationNS, record.Usage.DurationBasis = &ns, &duration, "observed_monotonic"
	record.Status, record.DispatchState, record.ObservationRevision = "succeeded", "dispatched", record.ObservationRevision+1
	if err != nil {
		record.Status, record.ErrorCode = "failed", "failed"
	}
	observationRecord(ctx, *record)
}

func (x *runExec) driveChild(ctx context.Context, ref FlowChild) (out result, resultErr error) {
	ctx, observed := x.childObservationContext(ctx, ref)
	defer func() { x.finishChildObservation(ctx, observed, out, resultErr) }()
	var err error
	switch ref.Kind {
	case "workflow":
		err = x.e.Drive(ctx, ref.ID)
	case "meeting":
		if x.e.Meetings == nil {
			return result{}, flowChildUnavailable(errors.New("meeting store unavailable"))
		}
		err = x.e.DriveMeeting(x.childCallContext(ctx, ref), ref.ID)
	default:
		return result{}, failf("child_reference_invalid", "unsupported child kind")
	}
	if err != nil {
		return result{}, flowChildUnavailable(err)
	}
	return x.childOutcome(ctx, ref)
}

func (x *runExec) childOutcome(ctx context.Context, ref FlowChild) (result, error) {
	if ref.Kind == "workflow" {
		run, err := x.e.Store.GetRun(ctx, ref.ID)
		if err != nil {
			return result{}, flowChildUnavailable(err)
		}
		if run.State.Status == "succeeded" {
			return result{out: childWorkflowOutput(run)}, nil
		}
		if terminal(run.State.Status) {
			return result{}, failf("child_failed", "workflow %s ended %s", ref.ID, run.State.Status)
		}
		res := childWaiting(ref)
		res.wait.ChildRevision = run.Revision
		if w := run.State.Waiting; w != nil {
			res.wait.Question, res.wait.DecisionKind = w.Question, w.DecisionKind
			if w.Kind == "approval" || w.Kind == "budget" {
				res.wait.DecisionKind = w.Kind
			}
		}
		return res, nil
	}
	if ref.Kind != "meeting" || x.e.Meetings == nil {
		return result{}, flowChildUnavailable(errors.New("meeting store or child kind unavailable"))
	}
	rec, err := x.e.Meetings.GetMeeting(ctx, ref.ID)
	if err != nil {
		return result{}, flowChildUnavailable(err)
	}
	m := rec.Meeting
	if m.Status == "concluded" && m.Conclusion != nil {
		return result{out: map[string]any{"summary": m.Conclusion.Summary, "decisions": m.Conclusion.Decisions,
			"todos": m.Conclusion.Todos, "open_questions": m.Conclusion.OpenQuestions, "meeting_id": rec.ID}}, nil
	}
	if terminalMeeting(m.Status) {
		return result{}, failf("child_failed", "meeting %s ended %s", ref.ID, m.Status)
	}
	res := childWaiting(ref)
	res.wait.ChildRevision = rec.Revision
	if w := m.Waiting; w != nil {
		res.wait.Question = w.Reason
		if w.Kind == "budget" {
			res.wait.DecisionKind = "budget"
		}
	}
	return res, nil
}

func childWaiting(ref FlowChild) result {
	return result{wait: &Waiting{Kind: "child", NodeID: ref.NodeID, ChildRunID: ref.ID, ChildKind: ref.Kind}}
}

func childWorkflowOutput(run Run) string {
	if c := run.State.Definition; c != nil && c.Graph != nil {
		for _, n := range c.Graph.Nodes {
			st := run.State.Nodes[n.ID]
			if n.Type == "end" && st != nil && st.Status == "done" && (n.Config["summary"] != "" || n.Config["message"] != "") {
				return run.State.Summary
			}
		}
	}
	raw, _ := json.Marshal(run.State)
	return flowresult.Text(raw)
}

func (x *runExec) resumeChildLocked(ctx context.Context, nodeID string) (bool, error) {
	ref, ok := x.waitingChild(nodeID)
	if !ok {
		return false, failf("child_reference_missing", "waiting child is not linked")
	}
	if AttemptObserverEnabled(ctx) {
		ctx = x.observationCallContextLocked(ctx, nodeID)
		ctx = context.WithValue(ctx, flowCallScopeKey{}, flowCallScope{run: Run{ID: x.run.ID, LeaseToken: x.run.LeaseToken}, nodeID: ref.NodeID, attempt: ref.Attempt, definitions: x.run.State.Definitions})
	}
	res, err := x.driveChild(ctx, ref)
	if x.expireFlowLocked() {
		return false, errFlowDeadline
	}
	if errors.Is(err, errFlowChildUnavailable) || ctx.Err() != nil {
		return false, err
	}
	if err != nil {
		x.state(nodeID).Status, x.state(nodeID).Error = "failed", clip(errText(err), 500)
		x.stamp(x.state(nodeID))
		x.closeRun("failed", errText(err))
		return false, x.saveLocked(ctx, nodeID)
	}
	if res.wait != nil {
		if !reflect.DeepEqual(x.run.State.Waiting, res.wait) {
			x.run.State.Waiting = res.wait
			return false, x.saveLocked(ctx, nodeID)
		}
		return false, nil
	}
	st := x.state(nodeID)
	st.Status, st.Output = "done", encodeStored(res.out)
	x.stamp(st)
	x.run.State.Status, x.run.State.Waiting = "running", nil
	x.followBack(nodeID)
	return !terminal(x.run.State.Status), x.saveLocked(ctx, nodeID)
}

func (x *runExec) waitingChild(nodeID string) (FlowChild, bool) {
	if w := x.run.State.Waiting; w != nil && w.ChildRunID != "" {
		for _, ref := range x.run.State.Children {
			if ref.ID == w.ChildRunID && ref.NodeID == nodeID {
				return ref, true
			}
		}
		return FlowChild{}, false
	}
	ref, ok := x.run.State.Children[childKey(nodeID, x.state(nodeID).Runs)]
	return ref, ok
}

// A graph's end node cannot override unfinished required work, including a
// child link persisted just before an engine crash.
func (x *runExec) completeState(ctx context.Context, summary string) error {
	if x.flow != nil {
		if x.e.flowExpired(x.run.State) {
			x.flow.cancel(errFlowDeadline)
			return errFlowDeadline
		}
		for _, ref := range x.run.State.Children {
			if !ref.Required {
				continue
			}
			res, err := x.childOutcome(ctx, ref)
			if errors.Is(err, errFlowChildUnavailable) {
				return err
			}
			if err != nil {
				x.closeRun("failed", errText(err))
				return nil
			}
			if res.wait != nil {
				if x.state(ref.NodeID) == nil {
					return failf("child_reference_invalid", "child node is missing")
				}
				x.state(ref.NodeID).Status = "waiting"
				x.run.State.Status, x.run.State.Waiting = "waiting", res.wait
				return nil
			}
		}
	}
	x.closeRun("succeeded", summary)
	return nil
}
