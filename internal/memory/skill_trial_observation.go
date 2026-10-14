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
	reader, ok := s.Repo.(interface {
		SkillTrialSourceIDs(context.Context, Scope, string) ([]string, error)
	})
	if !ok {
		return out, ErrUnavailable
	}
	type snapshot struct {
		scope Scope
		ids   []string
	}
	snapshots := []snapshot{}
	type observedSource struct {
		scope       Scope
		source      Record
		evaluations []Record
	}
	records := []observedSource{}
	for _, scope := range skillTrialEvidenceScopes(in.Scope, policy) {
		ids, e := reader.SkillTrialSourceIDs(ctx, scope, id)
		if e != nil {
			return out, e
		}
		if len(ids) > 32 || len(records)+len(ids) > 32 {
			return out, ErrUnavailable
		}
		snapshots = append(snapshots, snapshot{scope, ids})
		bound := actor
		bound.Scope = scope
		scoped := bindRequestActor(ctx, bound)
		for _, sourceID := range ids {
			if _, e = s.authorize(scoped, token, "read:sources", scope, sourceID, ""); e != nil {
				return out, e
			}
			source, e := s.Repo.Get(ctx, "sources", scope, sourceID)
			if e != nil {
				return out, e
			}
			if _, e = s.authorize(scoped, token, "read:execution_traces", scope, source.TraceID, ""); e != nil {
				return out, e
			}
			evaluations, e := s.Repo.EvaluationsFor(ctx, scope, Ref{Collection: "sources", ID: source.ID, Revision: source.Revision})
			if e != nil {
				return out, e
			}
			if len(evaluations) > evaluationSummaryLimit {
				return out, ErrUnavailable
			}
			for _, v := range evaluations {
				if _, e = s.authorize(scoped, token, "read:evaluations", scope, v.ID, ""); e != nil {
					return out, e
				}
			}
			records = append(records, observedSource{scope, source, evaluations})
		}
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
		failed, verified := false, 0
		seen := map[string]bool{}
		for _, saved := range records {
			source, e := tx.Get(ctx, "sources", saved.scope, saved.source.ID)
			if e != nil {
				return e
			}
			if source.Revision != saved.source.Revision || !live(source) || source.ContentErased || source.PrivacyQuarantined {
				return ErrConflict
			}
			trace, e := tx.Get(ctx, "execution_traces", saved.scope, source.TraceID)
			if e != nil {
				return e
			}
			if !live(trace) || trace.ContentErased || trace.PrivacyQuarantined {
				return ErrConflict
			}
			key := structuredText(saved.scope) + "/" + source.TraceID
			if seen[key] {
				return ErrConflict
			}
			seen[key] = true
			var usage SkillUsage
			if json.Unmarshal([]byte(source.Text), &usage) != nil || usage.TrialID != id || usage.VersionID != candidate.ID || usage.SkillID != candidate.SkillID || usage.Runtime != trial.SkillTrial.Runtime || candidate.Analysis == nil || usage.ContentHash != candidate.Analysis.ContentHash || usage.OfflineCache || usage.StartedAt.Before(trial.CreatedAt) || !usage.StartedAt.Before(trial.SkillTrial.EndsAt) || usage.CompletedAt.Before(usage.StartedAt) || usage.CompletedAt.After(time.Now()) {
				return ErrConflict
			}
			item := SkillTrialObservation{Scope: saved.scope, Source: Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, Outcome: "unobserved"}
			if usage.Status == "failure" || usage.Status == "validation_failed" || usage.ToolFailures > 0 || usage.RuntimeErrors > 0 {
				item.Outcome = "failure"
				failed = true
			} else if usage.Status == "success" {
				item.Outcome = "success"
			}
			for _, old := range saved.evaluations {
				v, e := tx.Get(ctx, "evaluations", saved.scope, old.ID)
				if e != nil {
					return e
				}
				if v.Revision != old.Revision {
					return ErrConflict
				}
				if !live(v) || v.Evaluation == nil || v.Evaluation.EvalType == "model_self" || v.Evaluation.Passed == nil || v.Target == nil || *v.Target != item.Source || v.CreatedBy == source.CreatedBy || v.CreatedBy == candidate.CreatedBy || v.CreatedBy == trial.CreatedBy {
					continue
				}
				if e = checkEvaluationEvidence(ctx, tx, v); e != nil {
					return e
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
			if item.Outcome == "success" && item.IndependentlyVerified {
				verified++
			}
			out.Items = append(out.Items, item)
		}
		out.State = "awaiting_observation"
		if len(records) >= policy.MinTrialRuns {
			out.State = "awaiting_independent_evaluation"
		}
		if verified >= policy.MinTrialRuns && verified == len(records) {
			out.State = "ready_for_comparison"
		}
		if failed {
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
			if e = tx.Replace(ctx, colSkillTrials, trial, prev); e != nil {
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
