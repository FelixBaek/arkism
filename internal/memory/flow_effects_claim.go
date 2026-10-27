package memory

import "context"

const errFlowEffectUnknown flowLimitError = "effect_outcome_unknown"

func validEffectClaim(run, id string, in FlowEffectClaimInput) bool {
	return validScope(in.Scope) && validRunLabel(run, 256) && validRunLabel(id, 128) && effectHashValid(in.EffectHash)
}

func linkedEffect(ctx context.Context, tx Tx, run Record, id, hash string) (Record, error) {
	r, e := effectRecord(ctx, tx, run.Scope, id)
	if e != nil {
		return r, e
	}
	a := r.FlowEffect
	if a.RunID != run.ID || a.RootID != run.CrewRun.RootID || r.RequestHash != hash || run.CrewRun.Effects[flowChildKey(a.NodeID, a.Attempt)] != id {
		return Record{}, ErrConflict
	}
	return r, nil
}

func (s *Service) ClaimFlowEffect(ctx context.Context, token, runID, id string, in FlowEffectClaimInput) (FlowEffectView, error) {
	if !validEffectClaim(runID, id, in) || runes(in.Preview) > 1900 {
		return FlowEffectView{}, ErrInvalid
	}
	d, e := s.flowAttemptActor(ctx, token, runID, in.Scope)
	if e != nil {
		return FlowEffectView{}, e
	}
	ctx = bindRequestActor(ctx, d)
	var out FlowEffectView
	e = flowAttemptTransaction(ctx, s.Repo, func(ctx context.Context, tx Tx) error {
		run, _, e := s.flowAccountingRuns(ctx, tx, token, runID, in.Scope, in.LeaseToken, false)
		if e != nil {
			return e
		}
		if e = s.authorizeFlowSourcesFrom(ctx, token, run, tx); e != nil {
			return e
		}
		r, e := linkedEffect(ctx, tx, run, id, in.EffectHash)
		if e != nil {
			return e
		}
		a := r.FlowEffect
		if ContentHash(in.Preview) != a.PreviewHash {
			return ErrConflict
		}
		if _, e = s.effectActor(ctx, token, runID, in.Scope, a.Adapter, in.Preview); e != nil {
			return e
		}
		if e = effectClaimNode(run, *a); e != nil {
			return e
		}
		out, e = claimFlowEffect(ctx, tx, r, in.LeaseToken, d)
		return e
	})
	return out, e
}

func effectClaimNode(run Record, a FlowEffect) error {
	n, ok := flowNode(run.CrewRun.Definition, a.NodeID)
	if !ok || !FlowNotifyNode(n) || effectAdapter(n.Config["channel"]) != a.Adapter {
		return ErrInvalid
	}
	st := run.CrewRun.Nodes[a.NodeID]
	if run.CrewRun.Status != "running" || st.Status != "running" || st.Runs != a.Attempt {
		return ErrConflict
	}
	return nil
}

func claimFlowEffect(ctx context.Context, tx Tx, r Record, lease string, actor Decision) (FlowEffectView, error) {
	if r.FlowEffect.Status != "approved" {
		return flowEffectView(r, false), nil
	}
	a := *r.FlowEffect
	a.Status, a.ClaimLeaseHash = "claimed", ContentHash(lease)
	r.FlowEffect = &a
	if e := replaceFlowAccounting(ctx, tx, colJobs, &r); e != nil {
		return FlowEffectView{}, e
	}
	return flowEffectView(r, true), audit(ctx, tx, r.Scope, actor.PrincipalID, "claim:flow_effect", r.ID)
}

func (s *Service) SettleFlowEffect(ctx context.Context, token, runID, id string, in FlowEffectSettleInput) (FlowEffectView, error) {
	if !validEffectClaim(runID, id, in.FlowEffectClaimInput) || !validEffectReceipt(in.FlowEffectReceipt) || in.Preview != "" {
		return FlowEffectView{}, ErrInvalid
	}
	d, e := s.flowAttemptActor(ctx, token, runID, in.Scope)
	if e != nil {
		return FlowEffectView{}, e
	}
	ctx = bindRequestActor(ctx, d)
	var out FlowEffectView
	e = flowAttemptTransaction(ctx, s.Repo, func(ctx context.Context, tx Tx) error {
		run, _, e := s.flowAccountingRuns(ctx, tx, token, runID, in.Scope, in.LeaseToken, true)
		if e != nil {
			return e
		}
		if e = s.authorizeFlowSourcesFrom(ctx, token, run, tx); e != nil {
			return e
		}
		r, e := linkedEffect(ctx, tx, run, id, in.EffectHash)
		if e != nil {
			return e
		}
		out, e = settleFlowEffect(ctx, tx, r, in, d)
		return e
	})
	return out, e
}

func validEffectReceipt(r FlowEffectReceipt) bool {
	if r.Status != "succeeded" && r.Status != "unknown" {
		return false
	}
	if r.HTTPStatus < 0 || r.HTTPStatus > 599 || r.ResponseHash != "" && !effectHashValid(r.ResponseHash) {
		return false
	}
	return r.Status != "succeeded" || r.HTTPStatus >= 200 && r.HTTPStatus < 300 && r.ResponseHash != ""
}

func settleFlowEffect(ctx context.Context, tx Tx, r Record, in FlowEffectSettleInput, d Decision) (FlowEffectView, error) {
	a := *r.FlowEffect
	if a.ClaimLeaseHash != ContentHash(in.LeaseToken) {
		return FlowEffectView{}, ErrConflict
	}
	if a.Receipt != nil {
		if *a.Receipt != in.FlowEffectReceipt {
			return FlowEffectView{}, ErrConflict
		}
		return flowEffectView(r, false), nil
	}
	if a.Status != "claimed" {
		return FlowEffectView{}, ErrConflict
	}
	a.Status, a.Receipt = in.Status, &in.FlowEffectReceipt
	r.FlowEffect = &a
	if e := replaceFlowAccounting(ctx, tx, colJobs, &r); e != nil {
		return FlowEffectView{}, e
	}
	return flowEffectView(r, false), audit(ctx, tx, r.Scope, d.PrincipalID, "settle:flow_effect", r.ID)
}
