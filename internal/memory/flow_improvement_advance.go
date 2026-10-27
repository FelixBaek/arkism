package memory

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

func improvementActionHash(in FlowImprovementAdvance) string {
	in.ExpectedRevision, in.Recover = 0, false
	return ContentHash(structuredText(in))
}

func improvementEvent(id, actionID string) string { return "flow-" + ContentHash(id+"\n"+actionID) }

func cloneImprovement(f *FlowImprovement) FlowImprovement {
	var out FlowImprovement
	b, _ := json.Marshal(f)
	_ = json.Unmarshal(b, &out)
	return out
}

func improvementAllows(stage, action string) bool {
	allowed := map[string][]string{
		"evidence_ready": {"generate"}, "comparison_required": {"compare"}, "approval_required": {"approve"},
		"trial_ready": {"trial"}, "trial_running": {"observe"}, "trial_comparison_required": {"compare", "observe"},
		"apply_ready": {"apply", "observe"}, "monitoring": {"monitor", "rollback"}, "recovery_unavailable": {"monitor", "rollback"},
	}
	return slices.Contains(allowed[stage], action)
}

func (s *Service) improvementActionActor(ctx context.Context, token string, r Record, action string) (Decision, error) {
	verb := map[string]string{"generate": "analyze:skills", "compare": "read:skill_versions", "approve": "transition:skill_versions", "trial": "apply:skills", "observe": "apply:skills", "apply": "apply:skills", "monitor": "rollback:skills", "rollback": "rollback:skills"}[action]
	if verb == "" {
		return Decision{}, ErrInvalid
	}
	id := r.SkillID
	if action == "compare" || action == "approve" {
		id = r.FlowImprovement.Candidate.ID
	}
	d, e := s.authorize(ctx, token, verb, r.Scope, id, "")
	if e != nil {
		return d, e
	}
	if action == "generate" {
		return d, nil
	}
	if action == "compare" {
		if d.Role != roleService && d.Role != "pipeline" {
			return d, ErrDenied
		}
		return d, nil
	}
	if d.Role == roleHuman && action != "observe" && action != "monitor" {
		return d, nil
	}
	_, e = s.authorizeSkillAutomation(ctx, token, d, r.Scope, r.SkillID, r.FlowImprovement.Runtime)
	return d, e
}

func validImprovementAdvance(in FlowImprovementAdvance) bool {
	if !validScope(in.Scope) || in.ExpectedRevision < 1 || !workCheckpointIDPattern.MatchString(in.ActionID) {
		return false
	}
	if (in.Action == "compare") != (in.Comparison != nil) {
		return false
	}
	return in.Application == nil || (in.Action == "apply" || in.Action == "rollback") && in.Application.Scope == in.Scope
}

func (s *Service) AdvanceFlowImprovement(ctx context.Context, token, id string, in FlowImprovementAdvance) (FlowImprovementView, error) {
	if !validImprovementAdvance(in) {
		return FlowImprovementView{}, ErrInvalid
	}
	r, e := s.readFlowImprovement(ctx, token, id, in.Scope)
	if e != nil {
		return FlowImprovementView{}, e
	}
	actor, e := s.improvementActionActor(ctx, token, r, in.Action)
	if e != nil {
		return FlowImprovementView{}, e
	}
	ctx = bindRequestActor(ctx, actor)
	claimed, reused, e := s.claimImprovement(ctx, token, r, in, actor)
	if e != nil {
		return FlowImprovementView{}, e
	}
	if reused {
		return improvementView(claimed), nil
	}
	state := cloneImprovement(claimed.FlowImprovement)
	if in.Recover {
		e = s.recoverImprovementAction(ctx, token, claimed, &state, in)
	} else {
		e = s.executeImprovementAction(ctx, token, claimed, &state, in)
	}
	if e == nil {
		if _, authErr := s.improvementActionActor(ctx, token, claimed, in.Action); authErr != nil {
			e = errors.Join(ErrUnavailable, authErr)
		}
		if authErr := s.authorizeImprovementMaterial(ctx, token, in.Scope, &state); authErr != nil {
			e = errors.Join(ErrUnavailable, authErr)
		}
	}
	saved, saveErr := s.finishImprovement(ctx, claimed, state, e)
	if saveErr != nil {
		return FlowImprovementView{}, saveErr
	}
	if e != nil {
		return improvementView(saved), e
	}
	return improvementView(saved), nil
}

func (s *Service) claimImprovement(ctx context.Context, token string, r Record, in FlowImprovementAdvance, actor Decision) (Record, bool, error) {
	var out Record
	reused := false
	hash := improvementActionHash(in)
	parentRevision := int64(0)
	if parent, e := s.Repo.Get(ctx, colSkills, r.Scope, r.SkillID); e == nil {
		parentRevision = parent.Revision
	} else if !errors.Is(e, ErrNotFound) {
		return out, false, e
	}
	err := s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		current, e := tx.Get(ctx, colJobs, r.Scope, r.ID)
		if e != nil {
			return e
		}
		if !readableImprovement(current) {
			return ErrNotFound
		}
		if e = checkImprovementSource(ctx, tx, r.Scope, current.FlowImprovement.SourceRun); e != nil {
			return e
		}
		state := cloneImprovement(current.FlowImprovement)
		for _, receipt := range state.Receipts {
			if receipt.ActionID != in.ActionID {
				continue
			}
			if receipt.RequestHash != hash || receipt.PrincipalID != actor.PrincipalID || receipt.Role != actor.Role {
				return ErrConflict
			}
			out, reused = current, true
			return nil
		}
		if current.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		if state.Pending != nil {
			return recoverImprovementClaim(current, in, actor, hash, &out)
		}
		if in.Recover || len(state.Receipts) >= 128 || !improvementAllows(state.Stage, in.Action) {
			return ErrConflict
		}
		state.Pending = &FlowImprovementAttempt{ActionID: in.ActionID, Action: in.Action, RequestHash: hash, PrincipalID: actor.PrincipalID, Role: actor.Role, SkillRevision: parentRevision, StartedAt: time.Now().UTC(), State: "running"}
		current.FlowImprovement = &state
		if e = replaceImprovement(ctx, tx, &current); e != nil {
			return e
		}
		out = current
		return audit(ctx, tx, r.Scope, actor.PrincipalID, "advance:flow_improvement:"+in.Action, r.ID)
	})
	return out, reused, err
}

func recoverImprovementClaim(r Record, in FlowImprovementAdvance, actor Decision, hash string, out *Record) error {
	p := r.FlowImprovement.Pending
	if !in.Recover || p.ActionID != in.ActionID || p.RequestHash != hash || p.PrincipalID != actor.PrincipalID || p.Role != actor.Role {
		return ErrConflict
	}
	*out = r
	return nil
}

func replaceImprovement(ctx context.Context, tx Tx, r *Record) error {
	prev := r.Revision
	r.Revision++
	r.UpdatedAt = time.Now().UTC()
	return tx.Replace(ctx, colJobs, *r, prev)
}

func improvementErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrUnavailable):
		return "outcome_unknown"
	case errors.Is(err, ErrDenied):
		return "permission_denied"
	case errors.Is(err, ErrInvalid):
		return "invalid_evidence"
	case errors.Is(err, ErrConflict):
		return "state_conflict"
	case errors.Is(err, ErrNotFound):
		return "evidence_unavailable"
	default:
		return "outcome_unknown"
	}
}

func (s *Service) finishImprovement(ctx context.Context, r Record, state FlowImprovement, operationErr error) (Record, error) {
	var out Record
	bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err := s.Repo.Transaction(bounded, func(ctx context.Context, tx Tx) error {
		current, e := tx.Get(ctx, colJobs, r.Scope, r.ID)
		if e != nil {
			return e
		}
		if !readableImprovement(current) || current.Revision != r.Revision {
			return ErrConflict
		}
		code := improvementErrorCode(operationErr)
		if code != "" {
			state = cloneImprovement(current.FlowImprovement)
			if code == "outcome_unknown" {
				state.Pending.State = "unknown"
			} else {
				receipt := FlowImprovementReceipt{FlowImprovementAttempt: *state.Pending, ErrorCode: code}
				receipt.State = "failed"
				state.Receipts = append(state.Receipts, receipt)
				state.Pending = nil
			}
		} else {
			if e = checkImprovementSource(ctx, tx, r.Scope, state.SourceRun); e != nil {
				return e
			}
			receipt := FlowImprovementReceipt{FlowImprovementAttempt: *state.Pending}
			receipt.State = "done"
			state.Receipts = append(state.Receipts, receipt)
			state.Pending = nil
		}
		current.FlowImprovement = &state
		if e = replaceImprovement(ctx, tx, &current); e != nil {
			return e
		}
		out = current
		return nil
	})
	return out, err
}
