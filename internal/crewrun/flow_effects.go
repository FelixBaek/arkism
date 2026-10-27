package crewrun

import (
	"context"
	"errors"
	"net/url"
	"time"
)

type FlowEffectRequest struct {
	NodeID     string `json:"node_id"`
	Attempt    int    `json:"attempt"`
	Adapter    string `json:"adapter"`
	TargetHash string `json:"target_hash"`
	ArgsHash   string `json:"args_hash"`
	Preview    string `json:"preview"`
}
type FlowEffectView struct {
	ID              string `json:"id"`
	EffectHash      string `json:"effect_hash"`
	Status          string `json:"status"`
	DispatchAllowed bool   `json:"dispatch_allowed"`
}
type FlowEffectReceipt struct {
	Status       string `json:"status"`
	HTTPStatus   int    `json:"http_status"`
	ResponseHash string `json:"response_hash,omitempty"`
}
type FlowEffectStore interface {
	PrepareFlowEffect(context.Context, Run, FlowEffectRequest) (FlowEffectView, error)
	ClaimFlowEffect(context.Context, Run, FlowEffectView, string) (FlowEffectView, error)
	SettleFlowEffect(context.Context, Run, FlowEffectView, FlowEffectReceipt) error
}

func effectURL(run string) string { return "/v1/crew/runs/" + url.PathEscape(run) + "/effects" }
func (c *Client) PrepareFlowEffect(ctx context.Context, run Run, in FlowEffectRequest) (FlowEffectView, error) {
	var out FlowEffectView
	err := c.do(ctx, "POST", effectURL(run.ID), map[string]any{"scope": c.Scope, "lease_token": run.LeaseToken, "node_id": in.NodeID, "attempt": in.Attempt, "adapter": in.Adapter, "target_hash": in.TargetHash, "args_hash": in.ArgsHash, "preview": in.Preview}, &out)
	return out, err
}
func (c *Client) ClaimFlowEffect(ctx context.Context, run Run, ref FlowEffectView, preview string) (FlowEffectView, error) {
	var out FlowEffectView
	err := c.do(ctx, "POST", effectURL(run.ID)+"/"+url.PathEscape(ref.ID)+"/claim", map[string]any{"scope": c.Scope, "lease_token": run.LeaseToken, "effect_hash": ref.EffectHash, "preview": preview}, &out)
	return out, err
}
func (c *Client) SettleFlowEffect(ctx context.Context, run Run, ref FlowEffectView, receipt FlowEffectReceipt) error {
	var out FlowEffectView
	return c.do(ctx, "POST", effectURL(run.ID)+"/"+url.PathEscape(ref.ID)+"/settle", map[string]any{"scope": c.Scope, "lease_token": run.LeaseToken, "effect_hash": ref.EffectHash, "status": receipt.Status, "http_status": receipt.HTTPStatus, "response_hash": receipt.ResponseHash}, &out)
}

var errFlowEffectUnavailable = errors.New("durable effect state unavailable")

func flowEffectError(err error) error {
	var api *APIError
	if errors.As(err, &api) && api.Status < 500 {
		return failf(api.Code, "the notification effect was refused; no automatic retry")
	}
	return errors.Join(errFlowEffectUnavailable, err)
}

func (x *runExec) runFlowNotify(ctx context.Context, n Node, input any) (result, error) {
	store, ok := x.e.Store.(FlowEffectStore)
	if !ok {
		return result{}, failf("effect_store_required", "FLOW notifications require durable approval and receipts")
	}
	p, err := x.e.prepareFlowNotify(x.shared(n.Config), input)
	if err != nil {
		return result{}, err
	}
	in := p.request
	in.NodeID, in.Attempt = n.ID, x.state(n.ID).Runs
	ref, err := store.PrepareFlowEffect(ctx, x.run, in)
	if err != nil {
		return result{}, flowEffectError(err)
	}
	switch ref.Status {
	case "awaiting_approval":
		return result{wait: &Waiting{Kind: "approval", Question: in.Preview, EffectID: ref.ID, EffectHash: ref.EffectHash}}, nil
	case "succeeded":
		return p.result(ref.ID), nil
	case "approved":
		return x.dispatchFlowNotify(ctx, store, p, ref)
	case "rejected":
		return result{}, failf("effect_rejected", "사람이 알림 발송을 거절했어요")
	default:
		return result{}, failf("effect_outcome_unknown", "발송 결과를 확인할 수 없어 자동으로 다시 보내지 않아요")
	}
}

func (x *runExec) dispatchFlowNotify(ctx context.Context, store FlowEffectStore, p preparedFlowNotify, ref FlowEffectView) (result, error) {
	claimed, err := store.ClaimFlowEffect(ctx, x.run, ref, p.request.Preview)
	if err != nil {
		return result{}, flowEffectError(err)
	}
	if !claimed.DispatchAllowed {
		return result{}, failf("effect_outcome_unknown", "이미 발송 권한을 사용했거나 승인이 유효하지 않아요")
	}
	receipt := x.e.sendFlowNotify(ctx, p)
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err = store.SettleFlowEffect(settle, x.run, ref, receipt); err != nil {
		return result{}, flowEffectError(err)
	}
	if receipt.Status != "succeeded" {
		return result{}, failf("effect_outcome_unknown", "발송 결과를 확인할 수 없어 자동으로 다시 보내지 않아요")
	}
	return p.result(ref.ID), nil
}

func (x *runExec) resumeEffectLocked(ctx context.Context, w *Waiting) (bool, error) {
	d, ok := x.run.State.Decisions[w.NodeID]
	if !ok {
		return false, nil
	}
	st := x.state(w.NodeID)
	if !d.Approve {
		st.Status, st.Error = "skipped", "effect_rejected: 사람이 알림 발송을 거절했어요"
		x.closeRun("stopped", st.Error)
		return false, x.saveLocked(ctx, w.NodeID)
	}
	st.Status = "pending"
	if st.Runs > 0 {
		st.Runs--
	}
	x.run.State.Status, x.run.State.Waiting = "running", nil
	return true, x.saveLocked(ctx, w.NodeID)
}
