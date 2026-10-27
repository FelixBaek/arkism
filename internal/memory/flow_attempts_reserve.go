package memory

import (
	"context"
	"errors"
	"maps"
	"slices"
	"time"
)

const errFlowAttemptReplay flowLimitError = "attempt_replay_blocked"
const errFlowTimeLimit flowLimitError = "flow_time_limit"

// errFlowLegacyRun refuses runs saved before root accounting and execution
// policies existed: they cannot be reserved against, so they fail instead of retrying forever.
const errFlowLegacyRun flowLimitError = "flow_legacy_run"

func (s *Service) ReserveFlowAttempt(ctx context.Context, token, id string, in FlowAttemptInput) (FlowAttemptReservation, error) {
	if !validFlowAttemptInput(id, in) {
		return FlowAttemptReservation{}, ErrInvalid
	}
	if e := noSecrets(in.CallKey + "\n" + in.Model); e != nil {
		return FlowAttemptReservation{}, e
	}
	d, e := s.flowAttemptActor(ctx, token, id, in.Scope)
	if e != nil {
		return FlowAttemptReservation{}, e
	}
	ctx = bindRequestActor(ctx, d)
	var out FlowAttemptReservation
	e = flowAttemptTransaction(ctx, s.Repo, func(ctx context.Context, tx Tx) error {
		run, root, e := s.flowAccountingRuns(ctx, tx, token, id, in.Scope, in.LeaseToken, false)
		if e != nil {
			return e
		}
		if e = s.flowAttemptNode(ctx, tx, token, run, in); e != nil {
			return e
		}
		out, e = reserveFlowAttempt(ctx, tx, run, root, in, d)
		return e
	})
	return out, e
}

// Every ancestor is still live and authorized. A forged root_id cannot move
// accounting into another run: each parent must contain the child's exact link.
func (s *Service) flowAccountingRuns(ctx context.Context, tx Tx, token, id string, scope Scope, lease string, settle bool) (Record, Record, error) {
	run, e := tx.Get(ctx, colCrewRuns, scope, id)
	if e != nil {
		return run, Record{}, e
	}
	if !readableCrewRun(run) || run.CrewRun.InvocationMode != "flow" {
		return run, Record{}, ErrNotFound
	}
	if !flowLeaseMatches(run, lease) {
		return run, Record{}, ErrConflict
	}
	root, e := s.flowAccountingRoot(ctx, tx, token, run, settle)
	return run, root, e
}

func (s *Service) flowAccountingRoot(ctx context.Context, tx Tx, token string, run Record, settle bool) (Record, error) {
	current := run
	for depth := 0; depth <= 8; depth++ {
		if !readableCrewRun(current) || current.CrewRun.InvocationMode != "flow" {
			return Record{}, ErrNotFound
		}
		if !settle && (current.CrewRun.CancelRequested || terminalCrewRun(current.CrewRun.Status)) {
			return Record{}, ErrConflict
		}
		if !settle && current.CrewRun.Deadline != nil && !current.CrewRun.Deadline.After(time.Now()) {
			return Record{}, errFlowTimeLimit
		}
		if _, e := s.flowAttemptActor(ctx, token, current.ID, current.Scope); e != nil {
			return Record{}, e
		}
		if current.CrewRun.ParentID == "" {
			if run.CrewRun.RootID == "" || current.CrewRun.Policy == nil {
				return Record{}, errFlowLegacyRun
			}
			if current.ID != run.CrewRun.RootID || current.CrewRun.RootID != current.ID {
				return Record{}, ErrConflict
			}
			return current, nil
		}
		parent, e := tx.Get(ctx, colCrewRuns, run.Scope, current.CrewRun.ParentID)
		if e != nil {
			return Record{}, e
		}
		if !flowAccountingChildLinked(parent, current) {
			return Record{}, ErrConflict
		}
		current = parent
	}
	return Record{}, ErrInvalid
}

func flowAccountingChildLinked(parent, child Record) bool {
	if parent.CrewRun == nil || child.CrewRun == nil || parent.CrewRun.RootID != child.CrewRun.RootID {
		return false
	}
	for _, ref := range parent.CrewRun.Children {
		if ref.Kind == "workflow" && ref.ID == child.ID && ref.NodeID == child.CrewRun.ParentNodeID && ref.CrewID == child.CrewRun.CrewID {
			return true
		}
	}
	return false
}

func (s *Service) flowAttemptNode(ctx context.Context, tx Tx, token string, run Record, in FlowAttemptInput) error {
	n, ok := flowNode(run.CrewRun.Definition, in.NodeID)
	if !ok || !slices.Contains([]string{"ai", "ai_once", "expert", "team", "loop"}, n.Type) {
		return ErrInvalid
	}
	st, ok := run.CrewRun.Nodes[in.NodeID]
	if !ok || st.Runs != in.Attempt {
		return ErrConflict
	}
	if n.Type == "team" {
		ref := run.CrewRun.Children[flowChildKey(in.NodeID, in.Attempt)]
		if _, e := s.authorize(ctx, token, "write:"+colCrewMeetings, run.Scope, ref.ID, ""); e != nil {
			return e
		}
		return flowAttemptMeetingNode(ctx, tx, run, n, in, st.Status)
	}
	if st.Status != "running" || run.CrewRun.Status != "running" {
		return ErrConflict
	}
	return nil
}

func flowAttemptMeetingNode(ctx context.Context, tx Tx, run Record, n CrewNode, in FlowAttemptInput, status string) error {
	ref, ok := run.CrewRun.Children[flowChildKey(in.NodeID, in.Attempt)]
	if !ok || ref.Kind != "meeting" || ref.CrewID != n.Config["team"] || ref.NodeID != in.NodeID {
		return ErrConflict
	}
	if status != "running" {
		w := run.CrewRun.Waiting
		if status != "waiting" || w == nil || w.Kind != "child" || w.ChildRunID != ref.ID || w.NodeID != in.NodeID {
			return ErrConflict
		}
	}
	child, e := flowChildRecord(ctx, tx, run.Scope, ref)
	if e != nil {
		return e
	}
	m := child.CrewMeeting
	if m == nil || m.ParentID != run.ID || m.ParentNodeID != in.NodeID || m.RootID != run.CrewRun.RootID || terminalMeeting(m.Status) || m.Status == "stopping" || m.Status == "waiting" {
		return ErrConflict
	}
	return nil
}

func cloneFlowBudget(in *FlowBudget) *FlowBudget {
	if in == nil {
		return &FlowBudget{Pending: map[string]FlowAttemptPending{}}
	}
	out := *in
	out.Pending = maps.Clone(in.Pending)
	if out.Pending == nil {
		out.Pending = map[string]FlowAttemptPending{}
	}
	return &out
}

func checkFlowAttemptBudget(b *FlowBudget, p *FlowPolicy, a FlowAttempt) error {
	if p == nil {
		return ErrInvalid
	}
	for _, pending := range b.Pending {
		if pending.RunID == a.RunID && pending.NodeID == a.NodeID && (pending.Unknown || pending.Attempt != a.Attempt) {
			return errFlowAttemptReplay
		}
	}
	if b.Attempts >= 10000 || flowAttemptsInFlight(b) >= p.MaxConcurrent {
		return errFlowConcurrencyLimit
	}
	if p.MaxTokens > 0 && (a.MaxInputTokens == 0 || a.MaxOutputTokens == 0) {
		return errFlowBudgetUnsupported
	}
	if p.MaxCostUSD > 0 && (!a.CostKnown || a.MaxInputTokens == 0 || a.MaxOutputTokens == 0) {
		return errFlowBudgetUnsupported
	}
	if p.MaxTokens > 0 && b.ReservedTokens+b.CommittedTokens+a.ReservedTokens > p.MaxTokens {
		return errFlowBudgetLimit
	}
	if p.MaxCostUSD > 0 && b.ReservedCostUSD+b.CommittedCostUSD+a.ReservedCostUSD > p.MaxCostUSD {
		return errFlowBudgetLimit
	}
	return nil
}

// flowAttemptsInFlight counts reserved calls still running. An unknown
// outcome stays pending for replay blocking and accounting, but no longer runs.
func flowAttemptsInFlight(b *FlowBudget) int {
	n := 0
	for _, pending := range b.Pending {
		if !pending.Unknown {
			n++
		}
	}
	return n
}

func reserveFlowAttempt(ctx context.Context, tx Tx, run, root Record, in FlowAttemptInput, actor Decision) (FlowAttemptReservation, error) {
	id, hash := flowAttemptIdentity(run.ID, in)
	prior, e := tx.Get(ctx, colJobs, in.Scope, id)
	if e == nil {
		if prior.FlowAttempt == nil || prior.Kind != flowAttemptKind || prior.RequestHash != hash || prior.ContentErased || prior.PrivacyQuarantined {
			return FlowAttemptReservation{}, ErrConflict
		}
		return flowAttemptView(prior, false), nil
	}
	if !errors.Is(e, ErrNotFound) {
		return FlowAttemptReservation{}, e
	}
	a := newFlowAttempt(root.ID, run.ID, in)
	b := cloneFlowBudget(root.FlowBudget)
	if e = checkFlowAttemptBudget(b, root.CrewRun.Policy, a); e != nil {
		return FlowAttemptReservation{}, e
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	r := Record{ID: id, Scope: in.Scope, Kind: flowAttemptKind, Status: "managed", Revision: 1, FlowAttempt: &a, RequestHash: hash, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, CreatedAt: now, UpdatedAt: now}
	b.Attempts++
	b.ReservedTokens += a.ReservedTokens
	b.ReservedCostUSD += a.ReservedCostUSD
	b.Pending[id] = FlowAttemptPending{RunID: run.ID, NodeID: in.NodeID, Attempt: in.Attempt}
	root.FlowBudget = b
	if e = tx.Insert(ctx, colJobs, r); e != nil {
		return FlowAttemptReservation{}, e
	}
	if e = replaceFlowAccounting(ctx, tx, colCrewRuns, &root); e != nil {
		return FlowAttemptReservation{}, e
	}
	return flowAttemptView(r, true), audit(ctx, tx, in.Scope, actor.PrincipalID, "reserve:flow_attempt", id)
}
