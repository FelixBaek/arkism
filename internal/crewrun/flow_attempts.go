package crewrun

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"
)

type FlowAttemptRequest struct {
	NodeID          string `json:"node_id"`
	Attempt         int    `json:"attempt"`
	CallKey         string `json:"call_key"`
	Model           string `json:"model"`
	MaxInputTokens  int64  `json:"max_input_tokens"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
}

type FlowAttemptReservation struct {
	ID              string  `json:"id"`
	Status          string  `json:"status"`
	DispatchAllowed bool    `json:"dispatch_allowed"`
	ReservedTokens  int64   `json:"reserved_tokens"`
	ReservedCostUSD float64 `json:"reserved_cost_usd"`
	CostKnown       bool    `json:"cost_known"`
}

type FlowAttemptSettlement struct {
	Status       string   `json:"status"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	UsageKnown   bool     `json:"usage_known"`
	Estimated    bool     `json:"estimated"`
	CostUSD      *float64 `json:"cost_usd,omitempty"`
}

type FlowAttemptStore interface {
	ReserveFlowAttempt(context.Context, Run, FlowAttemptRequest) (FlowAttemptReservation, error)
	SettleFlowAttempt(context.Context, Run, string, FlowAttemptSettlement) error
}

func (c *Client) ReserveFlowAttempt(ctx context.Context, run Run, in FlowAttemptRequest) (FlowAttemptReservation, error) {
	var out FlowAttemptReservation
	err := c.do(ctx, "POST", "/v1/crew/runs/"+url.PathEscape(run.ID)+"/attempts", map[string]any{
		"scope": c.Scope, "lease_token": run.LeaseToken, "node_id": in.NodeID, "attempt": in.Attempt,
		"call_key": in.CallKey, "model": in.Model, "max_input_tokens": in.MaxInputTokens, "max_output_tokens": in.MaxOutputTokens,
	}, &out)
	return out, err
}

func (c *Client) SettleFlowAttempt(ctx context.Context, run Run, id string, in FlowAttemptSettlement) error {
	body := map[string]any{"scope": c.Scope, "lease_token": run.LeaseToken, "status": in.Status,
		"input_tokens": in.InputTokens, "output_tokens": in.OutputTokens, "usage_known": in.UsageKnown, "estimated": in.Estimated}
	if in.CostUSD != nil {
		body["cost_usd"] = *in.CostUSD
	}
	var out any
	return c.do(ctx, "POST", "/v1/crew/runs/"+url.PathEscape(run.ID)+"/attempts/"+url.PathEscape(id)+"/settle", body, &out)
}

type flowCallScope struct {
	run         Run
	nodeID      string
	attempt     int
	definitions map[string]CrewItem
}

type flowCallScopeKey struct{}
type flowCallLabelKey struct{}

func (x *runExec) callContext(ctx context.Context, nodeID string) context.Context {
	if x.flow == nil {
		return ctx
	}
	x.mu.Lock()
	scope := flowCallScope{run: Run{ID: x.run.ID, LeaseToken: x.run.LeaseToken}, nodeID: nodeID, attempt: x.state(nodeID).Runs, definitions: x.run.State.Definitions}
	x.mu.Unlock()
	return context.WithValue(ctx, flowCallScopeKey{}, scope)
}

func (e *Engine) roleItem(ctx context.Context, id string) (CrewItem, bool) {
	if scope, flow := ctx.Value(flowCallScopeKey{}).(flowCallScope); flow {
		it, ok := scope.definitions[id]
		return it, ok
	}
	return e.item(id)
}

func withCallLabel(ctx context.Context, label string) context.Context {
	if prefix, _ := ctx.Value(flowCallLabelKey{}).(string); prefix != "" {
		label = prefix + "/" + label
	}
	return context.WithValue(ctx, flowCallLabelKey{}, label)
}

var errFlowAttemptUnavailable = errors.New("flow attempt accounting unavailable")

func (e *Engine) budgetedAI(ctx context.Context, s aiSpec, retry int, dispatch func() (aiAnswer, error)) (aiAnswer, error) {
	scope, flow := ctx.Value(flowCallScopeKey{}).(flowCallScope)
	if !flow {
		return dispatch()
	}
	store, ok := e.Store.(FlowAttemptStore)
	if !ok {
		return aiAnswer{}, failf("attempt_store_required", "FLOW model calls require durable accounting")
	}
	label, _ := ctx.Value(flowCallLabelKey{}).(string)
	if label == "" {
		label = "main"
	}
	in := FlowAttemptRequest{NodeID: scope.nodeID, Attempt: scope.attempt, CallKey: fmt.Sprintf("%s/retry%d", label, retry), Model: s.Model}
	// Host CLIs can perform internal tool loops and do not enforce token
	// ceilings. Zero advertises that limitation instead of inventing a bound.
	reservation, err := store.ReserveFlowAttempt(ctx, scope.run, in)
	if err != nil {
		return aiAnswer{}, flowAttemptError(err)
	}
	if reservation.ID == "" {
		return aiAnswer{}, errors.Join(errFlowAttemptUnavailable, errors.New("reservation id missing"))
	}
	if !reservation.DispatchAllowed {
		return aiAnswer{}, failf("attempt_replay_blocked", "the earlier call has no reusable result; automatic redispatch is blocked")
	}
	answer, callErr := dispatch()
	bounded, done := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer done()
	if err := store.SettleFlowAttempt(bounded, scope.run, reservation.ID, flowSettlement(answer, callErr)); err != nil {
		return aiAnswer{}, errors.Join(errFlowAttemptUnavailable, err)
	}
	return answer, callErr
}

func flowSettlement(answer aiAnswer, err error) FlowAttemptSettlement {
	out := FlowAttemptSettlement{Status: "succeeded"}
	if err != nil {
		out.Status = "failed"
		if answer.Usage == nil {
			out.Status = "unknown"
		}
	}
	if u := answer.Usage; u != nil {
		out.InputTokens, out.OutputTokens, out.UsageKnown, out.CostUSD = u.InputTokens, u.OutputTokens, true, u.CostUSD
		out.Estimated = u.Estimated
	}
	return out
}

func flowAttemptError(err error) error {
	switch apiCode(err) {
	case "flow_budget_limit", "flow_budget_unsupported", "flow_concurrency_limit", "flow_time_limit", "flow_legacy_run", "attempt_replay_blocked", "invalid_request":
		return failf(apiCode(err), "the execution policy refused this model call")
	}
	return errors.Join(errFlowAttemptUnavailable, err)
}

func flowCallControl(err error) bool {
	if errors.Is(err, errFlowAttemptUnavailable) {
		return true
	}
	var ne *nodeError
	return errors.As(err, &ne) && (ne.code == "flow_budget_limit" || ne.code == "flow_budget_unsupported" || ne.code == "flow_concurrency_limit" || ne.code == "flow_time_limit" || ne.code == "flow_legacy_run" || ne.code == "attempt_replay_blocked" || ne.code == "attempt_store_required" || ne.code == "invalid_request")
}

func firstFlowCallError(errs []error) error {
	for _, err := range errs {
		if flowCallControl(err) {
			return err
		}
	}
	return nil
}

func (e *Engine) callWithFallback(ctx context.Context, spec aiSpec, fallback string, limit time.Duration, retries int) (aiAnswer, error) {
	answer, err := e.callAI(ctx, spec, limit, retries)
	if err == nil || fallback == "" || spec.Backend != "" || ctx.Err() != nil || flowCallControl(err) {
		return answer, err
	}
	spec.Model = fallback
	return e.callAI(withCallLabel(ctx, "fallback"), spec, limit, retries)
}

func (x *runExec) resumeCapacityLocked(ctx context.Context, w *Waiting) (bool, error) {
	if w.ResumeAt != nil && x.e.now().Before(*w.ResumeAt) {
		return false, nil
	}
	st := x.state(w.NodeID)
	st.Status, st.StartedAt = "pending", nil
	if st.Runs > 0 {
		st.Runs-- // capacity was refused before dispatch; reuse the original attempt
	}
	x.run.State.Status, x.run.State.Waiting = "running", nil
	return true, x.saveLocked(ctx, w.NodeID)
}
