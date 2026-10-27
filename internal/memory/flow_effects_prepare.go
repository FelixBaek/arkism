package memory

import (
	"context"
	"errors"
	"maps"
	"time"
)

func validFlowEffectInput(id string, in FlowEffectInput) bool {
	return validScope(in.Scope) && validRunLabel(id, 256) && validRunLabel(in.NodeID, 40) &&
		in.Attempt > 0 && in.Attempt <= 10000 && (in.Adapter == effectAdapter("telegram") || in.Adapter == effectAdapter("slack")) &&
		effectHashValid(in.TargetHash) && effectHashValid(in.ArgsHash) && runes(in.Preview) > 0 && runes(in.Preview) <= 1900
}

func flowEffectIdentity(run string, in FlowEffectInput) (string, string) {
	id := "flow-effect-" + ContentHash(structuredText([]any{in.Scope, run, in.NodeID, in.Attempt}))
	in.LeaseToken = ""
	return id, ContentHash(structuredText(in))
}

func (s *Service) PrepareFlowEffect(ctx context.Context, token, id string, in FlowEffectInput) (FlowEffectView, error) {
	if !validFlowEffectInput(id, in) {
		return FlowEffectView{}, ErrInvalid
	}
	if e := noSecrets(in.Preview); e != nil {
		return FlowEffectView{}, e
	}
	d, e := s.effectActor(ctx, token, id, in.Scope, in.Adapter, in.Preview)
	if e != nil {
		return FlowEffectView{}, e
	}
	ctx = bindRequestActor(ctx, d)
	var out FlowEffectView
	e = flowAttemptTransaction(ctx, s.Repo, func(ctx context.Context, tx Tx) error {
		run, root, e := s.flowAccountingRuns(ctx, tx, token, id, in.Scope, in.LeaseToken, false)
		if e != nil {
			return e
		}
		if e = s.authorizeFlowSourcesFrom(ctx, token, run, tx); e != nil {
			return e
		}
		if e = flowEffectNode(run, in); e != nil {
			return e
		}
		out, e = prepareFlowEffect(ctx, tx, run, root.ID, in, d)
		return e
	})
	return out, e
}

func flowEffectNode(run Record, in FlowEffectInput) error {
	n, ok := flowNode(run.CrewRun.Definition, in.NodeID)
	if !ok || !FlowNotifyNode(n) || in.Adapter != effectAdapter(n.Config["channel"]) {
		return ErrInvalid
	}
	st, ok := run.CrewRun.Nodes[in.NodeID]
	if !ok || st.Runs != in.Attempt {
		return ErrConflict
	}
	if st.Status == "running" && run.CrewRun.Status == "running" {
		return nil
	}
	w := run.CrewRun.Waiting
	if st.Status == "waiting" && run.CrewRun.Status == "waiting" && w != nil && w.NodeID == in.NodeID && w.EffectID != "" {
		return nil
	}
	return ErrConflict
}

func effectRecord(ctx context.Context, tx Tx, scope Scope, id string) (Record, error) {
	r, e := tx.Get(ctx, colJobs, scope, id)
	if e != nil {
		return r, e
	}
	if r.Kind != flowEffectKind || r.FlowEffect == nil || r.ContentErased || r.PrivacyQuarantined {
		return Record{}, ErrNotFound
	}
	return r, nil
}

func prepareFlowEffect(ctx context.Context, tx Tx, run Record, root string, in FlowEffectInput, d Decision) (FlowEffectView, error) {
	id, hash := flowEffectIdentity(run.ID, in)
	prior, e := effectRecord(ctx, tx, in.Scope, id)
	if e == nil {
		if prior.RequestHash != hash || run.CrewRun.Effects[flowChildKey(in.NodeID, in.Attempt)] != id {
			return FlowEffectView{}, ErrConflict
		}
		return flowEffectView(prior, false), nil
	}
	if !errors.Is(e, ErrNotFound) {
		return FlowEffectView{}, e
	}
	if len(run.CrewRun.Effects) >= 128 {
		return FlowEffectView{}, ErrInvalid
	}
	if e = checkEarlierEffects(ctx, tx, run, in.NodeID); e != nil {
		return FlowEffectView{}, e
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	effect := &FlowEffect{RootID: root, RunID: run.ID, NodeID: in.NodeID, Attempt: in.Attempt, Adapter: in.Adapter, TargetHash: in.TargetHash, ArgsHash: in.ArgsHash, PreviewHash: ContentHash(in.Preview), Status: "awaiting_approval"}
	r := Record{ID: id, Scope: in.Scope, Kind: flowEffectKind, Status: "managed", Revision: 1, FlowEffect: effect, RequestHash: hash, CreatedBy: d.PrincipalID, CreatedByType: d.Role, CreatedAt: now, UpdatedAt: now}
	st := *run.CrewRun
	st.Nodes = maps.Clone(st.Nodes)
	n := st.Nodes[in.NodeID]
	n.Status = "waiting"
	st.Nodes[in.NodeID] = n
	st.Effects = maps.Clone(st.Effects)
	if st.Effects == nil {
		st.Effects = map[string]string{}
	}
	st.Effects[flowChildKey(in.NodeID, in.Attempt)] = id
	st.Status = "waiting"
	st.Waiting = &CrewRunWaiting{NodeID: in.NodeID, Kind: "approval", Question: in.Preview, EffectID: id, EffectHash: hash}
	run.CrewRun = &st
	if e = tx.Insert(ctx, colJobs, r); e != nil {
		return FlowEffectView{}, e
	}
	if e = replaceFlowAccounting(ctx, tx, colCrewRuns, &run); e != nil {
		return FlowEffectView{}, e
	}
	return flowEffectView(r, false), audit(ctx, tx, in.Scope, d.PrincipalID, "prepare:flow_effect", id)
}

func checkEarlierEffects(ctx context.Context, tx Tx, run Record, node string) error {
	for _, id := range run.CrewRun.Effects {
		r, e := effectRecord(ctx, tx, run.Scope, id)
		if e != nil {
			return e
		}
		if r.FlowEffect.NodeID == node && r.FlowEffect.Status != "succeeded" {
			return errFlowEffectUnknown
		}
	}
	return nil
}
