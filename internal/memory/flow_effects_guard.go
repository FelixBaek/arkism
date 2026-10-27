package memory

import "context"

// Called by both direct and delegated decisions inside their transaction. A
// generic approval decision can never manufacture an approved effect receipt.
func (s *Service) decideFlowEffect(ctx context.Context, tx Tx, token string, run Record, approve bool, actor Decision) error {
	w := run.CrewRun.Waiting
	if w == nil || w.EffectID == "" {
		return nil
	}
	if actor.Role != roleHuman || run.CrewRun.CancelRequested {
		return ErrDenied
	}
	if e := s.authorizeFlowSourcesFrom(ctx, token, run, tx); e != nil {
		return e
	}
	r, e := linkedEffect(ctx, tx, run, w.EffectID, w.EffectHash)
	if e != nil {
		return e
	}
	a := *r.FlowEffect
	if a.Status != "awaiting_approval" || a.NodeID != w.NodeID || ContentHash(w.Question) != a.PreviewHash || run.CrewRun.Nodes[w.NodeID].Runs != a.Attempt {
		return ErrConflict
	}
	if _, e = s.effectActor(bindRequestActor(ctx, actor), token, run.ID, run.Scope, a.Adapter, w.Question); e != nil {
		return e
	}
	a.Status = "rejected"
	if approve {
		a.Status = "approved"
	}
	a.ApprovedBy = actor.PrincipalID
	r.FlowEffect = &a
	if e = replaceFlowAccounting(ctx, tx, colJobs, &r); e != nil {
		return e
	}
	return audit(ctx, tx, run.Scope, actor.PrincipalID, "decide:flow_effect", r.ID)
}

// The runner cannot change the preview, forge a receipt-backed wait or claim
// success while a required external effect has an uncertain outcome.
func checkFlowEffectProgress(ctx context.Context, tx Tx, old Record, next CrewRun) error {
	if old.CrewRun.InvocationMode != "flow" {
		return nil
	}
	if e := checkEffectWait(ctx, tx, old, next.Waiting); e != nil {
		return e
	}
	for _, id := range old.CrewRun.Effects {
		r, e := effectRecord(ctx, tx, old.Scope, id)
		if e != nil {
			return e
		}
		a := r.FlowEffect
		if next.Status == "succeeded" && a.Status != "succeeded" {
			return ErrConflict
		}
		if st := next.Nodes[a.NodeID]; st.Runs == a.Attempt && st.Status == "done" && a.Status != "succeeded" {
			return ErrConflict
		}
	}
	return checkEffectNodeCompletion(ctx, tx, old, next)
}

func checkEffectWait(ctx context.Context, tx Tx, old Record, w *CrewRunWaiting) error {
	if w == nil || w.EffectID == "" {
		if w != nil && w.EffectHash != "" {
			return ErrInvalid
		}
		return nil
	}
	r, e := linkedEffect(ctx, tx, old, w.EffectID, w.EffectHash)
	if e != nil {
		return e
	}
	a := r.FlowEffect
	if w.Kind != "approval" || w.NodeID != a.NodeID || ContentHash(w.Question) != a.PreviewHash {
		return ErrConflict
	}
	return nil
}

func checkEffectNodeCompletion(ctx context.Context, tx Tx, old Record, next CrewRun) error {
	if old.CrewRun.Definition == nil || old.CrewRun.Definition.Graph == nil {
		return nil
	}
	for _, n := range old.CrewRun.Definition.Graph.Nodes {
		if n.Type != "notify" {
			continue
		}
		st := next.Nodes[n.ID]
		if st.Status != "done" {
			continue
		}
		id := old.CrewRun.Effects[flowChildKey(n.ID, st.Runs)]
		if id == "" {
			return ErrConflict
		}
		r, e := effectRecord(ctx, tx, old.Scope, id)
		if e != nil {
			return e
		}
		if r.FlowEffect.Status != "succeeded" {
			return ErrConflict
		}
	}
	return nil
}
