package memory

import (
	"context"

	"github.com/FelixBaek/arkism/internal/prices"
)

func (s *Service) SettleFlowAttempt(ctx context.Context, token, runID, id string, in FlowAttemptSettleInput) (FlowAttemptReservation, error) {
	if !validScope(in.Scope) || !validRunLabel(runID, 256) || !validRunLabel(id, 128) || !validFlowAttemptUsage(in.FlowAttemptUsage) {
		return FlowAttemptReservation{}, ErrInvalid
	}
	d, e := s.flowAttemptActor(ctx, token, runID, in.Scope)
	if e != nil {
		return FlowAttemptReservation{}, e
	}
	ctx = bindRequestActor(ctx, d)
	var out FlowAttemptReservation
	e = flowAttemptTransaction(ctx, s.Repo, func(ctx context.Context, tx Tx) error {
		_, root, e := s.flowAccountingRuns(ctx, tx, token, runID, in.Scope, in.LeaseToken, true)
		if e != nil {
			return e
		}
		out, e = settleFlowAttempt(ctx, tx, root, runID, id, in.FlowAttemptUsage, d)
		return e
	})
	return out, e
}

func settleFlowAttempt(ctx context.Context, tx Tx, root Record, runID, id string, u FlowAttemptUsage, actor Decision) (FlowAttemptReservation, error) {
	r, e := tx.Get(ctx, colJobs, root.Scope, id)
	if e != nil {
		return FlowAttemptReservation{}, e
	}
	if r.Kind != flowAttemptKind || r.FlowAttempt == nil || r.ContentErased || r.PrivacyQuarantined {
		return FlowAttemptReservation{}, ErrNotFound
	}
	a := *r.FlowAttempt
	if a.RootID != root.ID || a.RunID != runID {
		return FlowAttemptReservation{}, ErrConflict
	}
	hash := ContentHash(structuredText(u))
	if a.SettlementHash != "" {
		if a.SettlementHash != hash {
			return FlowAttemptReservation{}, ErrConflict
		}
		return flowAttemptView(r, false), nil
	}
	b := cloneFlowBudget(root.FlowBudget)
	pending, ok := b.Pending[id]
	if !ok || pending.RunID != runID || pending.NodeID != a.NodeID || pending.Attempt != a.Attempt {
		return FlowAttemptReservation{}, ErrConflict
	}
	accountFlowSettlement(b, a, u)
	if u.Status == "unknown" {
		pending.Unknown = true
		b.Pending[id] = pending
	} else {
		delete(b.Pending, id)
	}
	a.Status, a.Settlement, a.SettlementHash = u.Status, &u, hash
	r.FlowAttempt, root.FlowBudget = &a, b
	if e = replaceFlowAccounting(ctx, tx, colJobs, &r); e != nil {
		return FlowAttemptReservation{}, e
	}
	if e = replaceFlowAccounting(ctx, tx, colCrewRuns, &root); e != nil {
		return FlowAttemptReservation{}, e
	}
	return flowAttemptView(r, false), audit(ctx, tx, root.Scope, actor.PrincipalID, "settle:flow_attempt", id)
}

func accountFlowSettlement(b *FlowBudget, a FlowAttempt, u FlowAttemptUsage) {
	tokens := u.InputTokens + u.OutputTokens
	switch {
	case !u.UsageKnown:
		b.UnknownUsage++
	case u.Estimated:
		b.EstimatedTokens += tokens
	default:
		b.CommittedTokens += tokens
		if u.Status != "unknown" {
			b.ReservedTokens -= a.ReservedTokens
		}
	}
	if u.CostUSD == nil {
		b.UnknownCost++
		if u.UsageKnown {
			if cost, ok := prices.Cost(a.Model, u.InputTokens, u.OutputTokens); ok {
				b.EstimatedCostUSD += cost
			}
		}
		return
	}
	if u.Estimated {
		b.EstimatedCostUSD += *u.CostUSD
		return
	}
	b.CommittedCostUSD += *u.CostUSD
	if u.Status != "unknown" {
		b.ReservedCostUSD = max(0, b.ReservedCostUSD-a.ReservedCostUSD)
	}
}
