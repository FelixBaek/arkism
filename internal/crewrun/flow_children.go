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

func (x *runExec) childCallContext(ctx context.Context, ref FlowChild) context.Context {
	return context.WithValue(ctx, flowCallScopeKey{}, flowCallScope{
		run: Run{ID: x.run.ID, LeaseToken: x.run.LeaseToken}, nodeID: ref.NodeID, attempt: ref.Attempt,
		definitions: x.run.State.Definitions,
	})
}

func (x *runExec) driveChild(ctx context.Context, ref FlowChild) (result, error) {
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
