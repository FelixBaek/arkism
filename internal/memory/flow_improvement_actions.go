package memory

import (
	"context"
)

func recordRef(col string, r Record) Ref { return Ref{Collection: col, ID: r.ID, Revision: r.Revision} }

func (s *Service) executeImprovementAction(ctx context.Context, token string, r Record, f *FlowImprovement, in FlowImprovementAdvance) error {
	switch in.Action {
	case "generate":
		input := f.Generation
		input.EventID = improvementEvent(r.ID, in.ActionID)
		result, e := s.GenerateSkill(ctx, token, input)
		if e != nil {
			return e
		}
		f.Candidate = recordRef(colSkillVersions, result.Record)
		f.Stage = "comparison_required"
	case "compare":
		return s.compareImprovement(ctx, token, r, f, in)
	case "approve":
		result, e := s.Transition(ctx, token, colSkillVersions, f.Candidate.ID, ChangeInput{Scope: r.Scope, ExpectedRevision: f.Candidate.Revision, Status: "approved", ValidationID: f.Comparison.ID})
		if e != nil {
			return e
		}
		f.Candidate = recordRef(colSkillVersions, result)
		f.Stage = "trial_ready"
	case "trial":
		result, e := s.StartSkillTrial(ctx, token, SkillTrialInput{Scope: r.Scope, EventID: improvementEvent(r.ID, in.ActionID), SkillID: r.SkillID, Candidate: f.Candidate, Runtime: f.Runtime, EndsAt: f.TrialEndsAt})
		if e != nil {
			return e
		}
		f.Trial = recordRef(colSkillTrials, result)
		f.Stage = "trial_running"
	case "observe":
		return s.observeImprovement(ctx, token, r, f)
	case "apply", "rollback":
		return s.applyImprovement(ctx, token, r, f, in)
	case "monitor":
		result, e := s.MonitorSkillTrial(ctx, token, f.Trial.ID, SkillTrialReadInput{Scope: r.Scope, SkillID: r.SkillID})
		if e != nil {
			return e
		}
		f.ObservationState = result.State
		f.Observations = result.Evidence
		if result.State == "rolled_back" || result.State == "recovery_unavailable" {
			f.Stage = result.State
		}
	default:
		return ErrInvalid
	}
	return nil
}

func (s *Service) compareImprovement(ctx context.Context, token string, r Record, f *FlowImprovement, in FlowImprovementAdvance) error {
	if in.Comparison == nil || in.Comparison.Candidate != f.Candidate {
		return ErrConflict
	}
	input := RecordSkillComparisonInput{Scope: r.Scope, EventID: improvementEvent(r.ID, in.ActionID), Result: *in.Comparison}
	if f.Stage == "trial_comparison_required" {
		input.TrialID = f.Trial.ID
	}
	result, e := s.RecordSkillComparison(ctx, token, input)
	if e != nil {
		return e
	}
	if input.TrialID != "" {
		f.TrialComparison = result.Evaluation
		f.Stage = "apply_ready"
	} else {
		f.Comparison = result.Evaluation
		f.Stage = "approval_required"
	}
	if !in.Comparison.Passed {
		f.Stage = "comparison_failed"
	}
	return nil
}

func (s *Service) observeImprovement(ctx context.Context, token string, r Record, f *FlowImprovement) error {
	result, e := s.ObserveSkillTrial(ctx, token, f.Trial.ID, SkillTrialReadInput{Scope: r.Scope, SkillID: r.SkillID})
	if e != nil {
		return e
	}
	f.ObservationState = result.State
	f.Observations = result.Items
	f.Trial = result.Trial
	switch result.State {
	case "promoted":
		f.Stage = "monitoring"
	case "rolled_back", "recovery_unavailable":
		f.Stage = result.State
	case "ready_for_comparison":
		if f.Stage != "apply_ready" {
			f.Stage = "trial_comparison_required"
		}
	case "stopped_regression", "stopped", "expired", "invalidated":
		f.Stage = "trial_failed"
	default:
		f.Stage = "trial_running"
	}
	return nil
}

func improvementApplication(r Record, f *FlowImprovement, in FlowImprovementAdvance) (ChangeInput, error) {
	input := ChangeInput{Scope: r.Scope, ExpectedRevision: f.Pending.SkillRevision, VersionID: f.Candidate.ID, TrialID: f.Trial.ID, ValidationID: f.TrialComparison.ID}
	if in.Action == "rollback" {
		input.VersionID = f.Generation.ComparisonPlan.BaselineVersion
		input.ValidationID = ""
	}
	if in.Application != nil {
		requested := in.Application
		if requested.VersionID != "" && requested.VersionID != input.VersionID || requested.TrialID != "" && requested.TrialID != input.TrialID || requested.ExpectedRevision != 0 && requested.ExpectedRevision != input.ExpectedRevision {
			return input, ErrConflict
		}
		if in.Action == "rollback" {
			input.ValidationID = requested.ValidationID
		} else if requested.ValidationID != "" && requested.ValidationID != input.ValidationID {
			return input, ErrConflict
		}
	}
	for _, item := range f.Observations {
		if item.Evaluation != nil {
			input.ScopedTrialEvidence = append(input.ScopedTrialEvidence, ScopedTrialEvidence{Scope: item.Scope, Source: item.Source, Evaluation: *item.Evaluation})
		}
	}
	return input, nil
}

func (s *Service) applyImprovement(ctx context.Context, token string, r Record, f *FlowImprovement, in FlowImprovementAdvance) error {
	input, e := improvementApplication(r, f, in)
	if e != nil {
		return e
	}
	rollback := in.Action == "rollback"
	if input.VersionID == "" {
		return ErrDenied
	}
	if !rollback {
		candidate, e := s.Get(ctx, token, colSkillVersions, r.Scope, f.Candidate.ID)
		if e != nil {
			return e
		}
		if candidate.Revision != f.Candidate.Revision {
			return ErrConflict
		}
		if e = validation(ctx, s.Repo, candidate, f.TrialComparison.ID, true); e != nil {
			return e
		}
		if e = s.checkImprovementTrialEvidence(ctx, candidate, f, input); e != nil {
			return e
		}
	}
	if _, e = s.ApplySkill(ctx, token, r.SkillID, input, rollback); e != nil {
		return e
	}
	if rollback {
		f.Stage = "rolled_back"
	} else {
		f.Stage = "monitoring"
	}
	return nil
}

// Recovery reads only deterministic child records. It does not call a model,
// rerun a comparison, approve, trial, apply, or roll back a second time.
func (s *Service) recoverImprovementAction(ctx context.Context, token string, r Record, f *FlowImprovement, in FlowImprovementAdvance) error {
	p := f.Pending
	event := improvementEvent(r.ID, in.ActionID)
	switch in.Action {
	case "generate":
		id := "skill-generation-" + ContentHash(structuredText(r.Scope)+"\n"+p.PrincipalID+"\n"+p.Role+"\n"+event)
		receipt, e := s.Repo.Get(ctx, colJobs, r.Scope, id)
		if e != nil {
			return ErrUnavailable
		}
		input := f.Generation
		input.EventID = event
		actor := Decision{PrincipalID: p.PrincipalID, Role: p.Role}
		if skillGenerationReceiptConflicts(receipt, ContentHash(structuredText(input)), actor) {
			return ErrConflict
		}
		result, e := s.reuseSkillGeneration(ctx, token, input, receipt)
		if e != nil {
			return e
		}
		f.Candidate = recordRef(colSkillVersions, result.Record)
		f.Stage = "comparison_required"
	case "compare":
		return s.recoverImprovementComparison(ctx, token, r, f, in, event)
	case "approve":
		candidate, e := s.Get(ctx, token, colSkillVersions, r.Scope, f.Candidate.ID)
		if e != nil {
			return e
		}
		if candidate.Revision != f.Candidate.Revision+1 || candidate.Status != "approved" || candidate.ValidationID != f.Comparison.ID || candidate.ApprovedBy != p.PrincipalID {
			return ErrUnavailable
		}
		f.Candidate = recordRef(colSkillVersions, candidate)
		f.Stage = "trial_ready"
	case "trial":
		return s.recoverImprovementTrial(ctx, r, f, event)
	case "apply", "rollback":
		return s.recoverImprovementApplication(ctx, token, r, f, in)
	case "observe", "monitor":
		return s.recoverImprovementObservation(ctx, token, r, f, in.Action)
	default:
		return ErrUnavailable // observations may change; inspect the existing trial before retrying
	}
	return nil
}

func (s *Service) recoverImprovementTrial(ctx context.Context, r Record, f *FlowImprovement, event string) error {
	id := "trial-" + ContentHash(structuredText([]any{r.Scope, f.Pending.PrincipalID, event}))
	trial, e := s.Repo.Get(ctx, colSkillTrials, r.Scope, id)
	if e != nil {
		return ErrUnavailable
	}
	input := SkillTrialInput{Scope: r.Scope, EventID: event, SkillID: r.SkillID, Candidate: f.Candidate, Runtime: f.Runtime, EndsAt: f.TrialEndsAt}
	if trial.SkillTrial == nil || trial.SkillID != r.SkillID || trial.SkillTrial.Candidate != f.Candidate || trial.ContentErased || trial.PrivacyQuarantined || trial.RequestHash != ContentHash(structuredText(input)) {
		return ErrConflict
	}
	f.Trial = recordRef(colSkillTrials, trial)
	f.Stage = "trial_running"
	return nil
}

func (s *Service) recoverImprovementObservation(ctx context.Context, token string, r Record, f *FlowImprovement, action string) error {
	status, e := s.ReadSkillTrialStatus(ctx, token, f.Trial.ID, SkillTrialReadInput{Scope: r.Scope, SkillID: r.SkillID})
	if e != nil {
		return e
	}
	f.Trial = status.Trial
	f.ObservationState = "awaiting_observation"
	f.Observations = nil
	if action == "observe" {
		f.Stage = "trial_running"
	} else {
		f.Stage = "monitoring"
	}
	switch status.State {
	case "promoted", "recovery_pending":
		f.Stage = "monitoring"
		f.ObservationState = status.State
	case "rolled_back", "recovery_unavailable":
		f.Stage = status.State
		f.ObservationState = status.State
	case "stopped_regression", "stopped", "expired", "invalidated":
		f.Stage = "trial_failed"
		f.ObservationState = status.State
	}
	return nil
}

func (s *Service) recoverImprovementComparison(ctx context.Context, token string, r Record, f *FlowImprovement, in FlowImprovementAdvance, event string) error {
	p := f.Pending
	prefix := "comparison-" + ContentHash(structuredText(r.Scope)+"\n"+p.PrincipalID+"\n"+p.Role+"\n"+event)
	out := RecordSkillComparisonResult{SourceID: prefix + "-source", Runs: []Ref{{Collection: "agent_runs", ID: prefix + "-baseline", Revision: 1}, {Collection: "agent_runs", ID: prefix + "-candidate", Revision: 1}}, Evaluation: Ref{Collection: "evaluations", ID: prefix + "-evaluation", Revision: 1}}
	for _, ref := range append(out.Runs, out.Evaluation) {
		if _, e := s.Get(ctx, token, ref.Collection, r.Scope, ref.ID); e != nil {
			return ErrUnavailable
		}
	}
	source, e := s.Get(ctx, token, "sources", r.Scope, out.SourceID)
	if e != nil {
		return ErrUnavailable
	}
	if in.Comparison == nil || in.Comparison.Candidate != f.Candidate {
		return ErrConflict
	}
	body := structuredText(*in.Comparison)
	if f.Stage == "trial_comparison_required" {
		body = structuredText(struct {
			TrialID string                `json:"trial_id"`
			Result  SkillComparisonResult `json:"result"`
		}{f.Trial.ID, *in.Comparison})
	}
	if e = checkRecordedSkillComparison(ctx, s.Repo, r.Scope, out, source, body, p.PrincipalID, f.Candidate.ID); e != nil {
		return e
	}
	if f.Stage == "trial_comparison_required" {
		f.TrialComparison = out.Evaluation
		f.Stage = "apply_ready"
	} else {
		f.Comparison = out.Evaluation
		f.Stage = "approval_required"
	}
	if !in.Comparison.Passed {
		f.Stage = "comparison_failed"
	}
	return nil
}

func (s *Service) recoverImprovementApplication(ctx context.Context, token string, r Record, f *FlowImprovement, in FlowImprovementAdvance) error {
	input, e := improvementApplication(r, f, in)
	if e != nil {
		return e
	}
	parent, e := s.Get(ctx, token, colSkills, r.Scope, r.SkillID)
	if e != nil {
		return e
	}
	if parent.ActiveVersion != input.VersionID || parent.Revision != input.ExpectedRevision+1 {
		return ErrUnavailable
	}
	if in.Action == "rollback" {
		f.Stage = "rolled_back"
	} else {
		f.Stage = "monitoring"
	}
	return nil
}
