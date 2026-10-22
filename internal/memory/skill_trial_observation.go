package memory

import (
	"context"
	"encoding/json"
	"slices"
	"time"
)

type SkillTrialObservation struct {
	Scope                 Scope  `json:"scope"`
	Source                Ref    `json:"source"`
	Evaluation            *Ref   `json:"evaluation,omitempty"`
	Outcome               string `json:"outcome"`
	IndependentlyVerified bool   `json:"independently_verified"`
}
type SkillTrialObservations struct {
	Trial           Ref                     `json:"trial"`
	Candidate       Ref                     `json:"candidate"`
	BaselineVersion string                  `json:"baseline_version"`
	State           string                  `json:"state"`
	Items           []SkillTrialObservation `json:"items"`
	Authority       string                  `json:"authority"`
}

// ObserveSkillTrial reconciles actual recorded executions, never a usage-count
// success heuristic. Failure cancels the trial in the same observation tx;
// baseline selection stays unchanged. No model call or external execution here.
func (s *Service) ObserveSkillTrial(ctx context.Context, token, id string, in SkillTrialReadInput) (SkillTrialObservations, error) {
	out := SkillTrialObservations{Authority: AuthorityDataOnly, Items: []SkillTrialObservation{}}
	if !validScope(in.Scope) || !validRunLabel(in.SkillID, 256) || !validRunLabel(id, 128) {
		return out, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "apply:skills", in.Scope, in.SkillID, "")
	if e != nil {
		return out, e
	}
	policy, e := s.authorizeSkillAutomation(ctx, token, actor, in.Scope, in.SkillID, "")
	if e != nil {
		return out, e
	}
	ctx = bindRequestActor(ctx, actor)
	if e = s.authorizeTrialEvidence(ctx, token, actor, ChangeInput{Scope: in.Scope}, policy); e != nil {
		return out, e
	}
	// Promoted/recovery jobs no longer depend on old trial observations; the
	// monitor separately revalidates post-promotion evidence under current grants.
	terminalFound := false
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		trial, err := tx.Get(ctx, colSkillTrials, in.Scope, id)
		if err != nil {
			return err
		}
		if trial.SkillID != in.SkillID {
			return ErrNotFound
		}
		state, err := skillTrialTerminal(ctx, tx, trial)
		if err != nil {
			return err
		}
		if state != "" {
			terminalFound = true
			out.State = state
			out.Trial = Ref{Collection: colSkillTrials, ID: id, Revision: trial.Revision}
			out.Candidate = trial.SkillTrial.Candidate
			out.BaselineVersion = trial.SkillTrial.BaselineVersion
		}
		return nil
	})
	if e != nil || terminalFound {
		return out, e
	}
	snapshots, records, e := s.collectSkillTrialSources(ctx, token, actor, id, in.Scope, policy)
	if e != nil {
		return out, e
	}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Items = []SkillTrialObservation{}
		trial, e := tx.Get(ctx, colSkillTrials, in.Scope, id)
		if e != nil {
			return e
		}
		if trial.SkillID != in.SkillID || trial.SkillTrial == nil || trial.PrivacyQuarantined || trial.ContentErased {
			return ErrNotFound
		}
		out.Trial = Ref{Collection: colSkillTrials, ID: id, Revision: trial.Revision}
		out.Candidate = trial.SkillTrial.Candidate
		out.BaselineVersion = trial.SkillTrial.BaselineVersion
		if trial.Status != "active" {
			out.State = trial.Status
			return nil
		}
		terminal, e := skillTrialTerminal(ctx, tx, trial)
		if e != nil {
			return e
		}
		if terminal != "" {
			out.State = terminal
			return nil
		}
		candidate, e := skillTrialCandidate(ctx, tx, in.Scope, in.SkillID, trial.SkillTrial)
		if e != nil {
			return e
		}
		if e = checkSkillTrialSnapshots(ctx, tx, id, snapshots); e != nil {
			return e
		}
		failed, verified := false, 0
		seen := map[string]bool{}
		for _, saved := range records {
			item, itemFailed, e := observeSkillTrialSource(ctx, tx, id, saved, candidate, trial, seen)
			if e != nil {
				return e
			}
			if itemFailed {
				failed = true
			}
			if item.Outcome == "success" && item.IndependentlyVerified {
				verified++
			}
			out.Items = append(out.Items, item)
		}
		out.State = skillTrialObservationState(len(records), verified, policy.MinTrialRuns)
		if failed {
			if trial, e = stopSkillTrialForRegression(ctx, tx, trial, actor); e != nil {
				return e
			}
			out.Trial.Revision = trial.Revision
			out.State = "stopped_regression"
			return audit(ctx, tx, in.Scope, actor.PrincipalID, "stop:skill_trials:regression", id)
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "observe:skill_trials", id)
	})
	if e != nil {
		return SkillTrialObservations{}, e
	}
	return out, nil
}

type skillTrialSourceSnapshot struct {
	scope Scope
	ids   []string
}

type observedSkillTrialSource struct {
	scope       Scope
	source      Record
	evaluations []Record
}

func (s *Service) collectSkillTrialSources(ctx context.Context, token string, actor Decision, id string, trialScope Scope, policy SkillAutomationPolicy) ([]skillTrialSourceSnapshot, []observedSkillTrialSource, error) {
	reader, ok := s.Repo.(interface {
		SkillTrialSourceIDs(context.Context, Scope, string) ([]string, error)
	})
	if !ok {
		return nil, nil, ErrUnavailable
	}
	snapshots := []skillTrialSourceSnapshot{}
	records := []observedSkillTrialSource{}
	for _, scope := range skillTrialEvidenceScopes(trialScope, policy) {
		ids, e := reader.SkillTrialSourceIDs(ctx, scope, id)
		if e != nil {
			return nil, nil, e
		}
		if len(ids) > 32 || len(records)+len(ids) > 32 {
			return nil, nil, ErrUnavailable
		}
		snapshots = append(snapshots, skillTrialSourceSnapshot{scope, ids})
		bound := actor
		bound.Scope = scope
		scoped := bindRequestActor(ctx, bound)
		for _, sourceID := range ids {
			if _, e = s.authorize(scoped, token, "read:sources", scope, sourceID, ""); e != nil {
				return nil, nil, e
			}
			source, e := s.Repo.Get(ctx, "sources", scope, sourceID)
			if e != nil {
				return nil, nil, e
			}
			if _, e = s.authorize(scoped, token, "read:execution_traces", scope, source.TraceID, ""); e != nil {
				return nil, nil, e
			}
			evaluations, e := s.Repo.EvaluationsFor(ctx, scope, Ref{Collection: "sources", ID: source.ID, Revision: source.Revision})
			if e != nil {
				return nil, nil, e
			}
			if len(evaluations) > evaluationSummaryLimit {
				return nil, nil, ErrUnavailable
			}
			for _, v := range evaluations {
				if _, e = s.authorize(scoped, token, "read:evaluations", scope, v.ID, ""); e != nil {
					return nil, nil, e
				}
			}
			records = append(records, observedSkillTrialSource{scope, source, evaluations})
		}
	}
	return snapshots, records, nil
}

func checkSkillTrialSnapshots(ctx context.Context, tx Tx, id string, snapshots []skillTrialSourceSnapshot) error {
	query, ok := tx.(interface {
		SkillTrialSourceIDs(context.Context, Scope, string) ([]string, error)
	})
	if !ok {
		return ErrUnavailable
	}
	for _, snap := range snapshots {
		ids, e := query.SkillTrialSourceIDs(ctx, snap.scope, id)
		if e != nil {
			return e
		}
		if !slices.Equal(ids, snap.ids) {
			return ErrConflict
		}
	}
	return nil
}

func observeSkillTrialSource(ctx context.Context, tx Tx, id string, saved observedSkillTrialSource, candidate, trial Record, seen map[string]bool) (SkillTrialObservation, bool, error) {
	var item SkillTrialObservation
	source, e := tx.Get(ctx, "sources", saved.scope, saved.source.ID)
	if e != nil {
		return item, false, e
	}
	if source.Revision != saved.source.Revision || !live(source) || source.ContentErased || source.PrivacyQuarantined {
		return item, false, ErrConflict
	}
	trace, e := tx.Get(ctx, "execution_traces", saved.scope, source.TraceID)
	if e != nil {
		return item, false, e
	}
	if !live(trace) || trace.ContentErased || trace.PrivacyQuarantined {
		return item, false, ErrConflict
	}
	key := structuredText(saved.scope) + "/" + source.TraceID
	if seen[key] {
		return item, false, ErrConflict
	}
	seen[key] = true
	var usage SkillUsage
	if invalidSkillTrialUsage(source.Text, &usage, id, candidate, trial) {
		return item, false, ErrConflict
	}
	item = SkillTrialObservation{Scope: saved.scope, Source: Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, Outcome: "unobserved"}
	failed := false
	if usage.Status == "failure" || usage.Status == "validation_failed" || usage.ToolFailures > 0 || usage.RuntimeErrors > 0 {
		item.Outcome = "failure"
		failed = true
	} else if usage.Status == "success" {
		item.Outcome = "success"
	}
	evaluationFailed, e := applySkillTrialEvaluations(ctx, tx, saved, &item, source, candidate, trial)
	if e != nil {
		return item, false, e
	}
	return item, failed || evaluationFailed, nil
}

func invalidSkillTrialUsage(text string, usage *SkillUsage, id string, candidate, trial Record) bool {
	return json.Unmarshal([]byte(text), usage) != nil || usage.TrialID != id || usage.VersionID != candidate.ID || usage.SkillID != candidate.SkillID || usage.Runtime != trial.SkillTrial.Runtime || candidate.Analysis == nil || usage.ContentHash != candidate.Analysis.ContentHash || usage.OfflineCache || usage.StartedAt.Before(trial.CreatedAt) || !usage.StartedAt.Before(trial.SkillTrial.EndsAt) || usage.CompletedAt.Before(usage.StartedAt) || usage.CompletedAt.After(time.Now())
}

func applySkillTrialEvaluations(ctx context.Context, tx Tx, saved observedSkillTrialSource, item *SkillTrialObservation, source, candidate, trial Record) (bool, error) {
	failed := false
	for _, old := range saved.evaluations {
		v, e := tx.Get(ctx, "evaluations", saved.scope, old.ID)
		if e != nil {
			return failed, e
		}
		if v.Revision != old.Revision {
			return failed, ErrConflict
		}
		if ignoredSkillTrialEvaluation(v, item.Source, source, candidate, trial) {
			continue
		}
		if e = checkEvaluationEvidence(ctx, tx, v); e != nil {
			return failed, e
		}
		if !*v.Evaluation.Passed {
			item.Outcome = "failure"
			failed = true
			continue
		}
		if independentEvaluation(v) {
			ref := Ref{Collection: "evaluations", ID: v.ID, Revision: v.Revision}
			item.Evaluation = &ref
			item.IndependentlyVerified = true
		}
	}
	return failed, nil
}

func ignoredSkillTrialEvaluation(v Record, target Ref, source, candidate, trial Record) bool {
	return !live(v) || v.Evaluation == nil || v.Evaluation.EvalType == "model_self" || v.Evaluation.Passed == nil || v.Target == nil || *v.Target != target || v.CreatedBy == source.CreatedBy || v.CreatedBy == candidate.CreatedBy || v.CreatedBy == trial.CreatedBy
}

func skillTrialObservationState(observed, verified, minRuns int) string {
	state := "awaiting_observation"
	if observed >= minRuns {
		state = "awaiting_independent_evaluation"
	}
	if verified >= minRuns && verified == observed {
		state = "ready_for_comparison"
	}
	return state
}

func stopSkillTrialForRegression(ctx context.Context, tx Tx, trial Record, actor Decision) (Record, error) {
	prev := trial.Revision
	now := time.Now().UTC()
	details := *trial.SkillTrial
	details.StoppedAt = &now
	details.StoppedBy = actor.PrincipalID
	trial.SkillTrial = &details
	trial.Status = "stopped"
	trial.VerificationStatus = "regression_observed"
	trial.Revision++
	trial.UpdatedAt = now
	if e := tx.Replace(ctx, colSkillTrials, trial, prev); e != nil {
		return trial, e
	}
	if e := closeSkillTrialCheck(ctx, tx, trial); e != nil {
		return trial, e
	}
	return trial, nil
}
