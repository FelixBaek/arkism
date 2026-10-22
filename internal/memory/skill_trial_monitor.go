package memory

import (
	"context"
	"encoding/json"
	"slices"
	"time"
)

type SkillTrialRecovery struct {
	State           string                  `json:"state"`
	Candidate       Ref                     `json:"candidate"`
	BaselineVersion string                  `json:"baseline_version"`
	Evidence        []SkillTrialObservation `json:"evidence"`
}

// Only potential regressions are selected; every returned canonical event and
// independent evaluation is revalidated before disabling a selected version.
func (p *Postgres) SkillRegressionSourceIDs(ctx context.Context, scope Scope, version string, since time.Time) ([]string, error) {
	rows, e := p.q().Query(ctx, `SELECT s.id FROM source_events s WHERE s.organization_id=$1 AND s.project_id=$2 AND s.user_id=$3 AND s.agent_id=$4 AND s.kind=$5 AND s.payload->>'version_id'=$6 AND (s.payload->>'started_at')::timestamptz >= $7 AND (s.payload->>'status' IN ('failure','validation_failed') OR (s.payload->>'tool_failures')::integer>0 OR (s.payload->>'runtime_errors')::integer>0 OR EXISTS (SELECT 1 FROM memory_records e WHERE e.collection='evaluations' AND e.workspace_id=$1 AND e.project_id=$2 AND e.user_id=$3 AND e.agent_id=$4 AND e.status='active' AND e.doc->'target'->>'collection'='sources' AND e.doc->'target'->>'id'=s.id AND e.doc->'evaluation'->>'eval_type'<>'model_self' AND e.doc->'evaluation'->>'passed'='false')) ORDER BY s.id LIMIT 32`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, skillUsageEventKind, version, since)
	if e != nil {
		return nil, pgErr(e)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, pgErr(e)
		}
		out = append(out, id)
	}
	return out, pgErr(rows.Err())
}

// MonitorSkillTrial rejects an actually regressed promoted version first. A
// subsequent rollback uses the ordinary lifecycle engine and its independent
// baseline validation. Failure to restore leaves the bad version non-live.
func (s *Service) MonitorSkillTrial(ctx context.Context, token, id string, in SkillTrialReadInput) (SkillTrialRecovery, error) {
	out := SkillTrialRecovery{Evidence: []SkillTrialObservation{}}
	actor, policy, trial, e := s.authorizeSkillTrialMonitor(ctx, token, id, in)
	if e != nil {
		return out, e
	}
	ctx = bindRequestActor(ctx, actor)
	var terminal string
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		terminal, err = skillTrialTerminal(ctx, tx, trial)
		return err
	}); e != nil {
		return out, e
	}
	candidate, e := s.Repo.Get(ctx, colSkillVersions, in.Scope, trial.SkillTrial.Candidate.ID)
	if e != nil {
		return out, e
	}
	marker := ContentHash("trial-regression/" + id)
	pending := (candidate.Status == "rejected" || candidate.Status == "superseded") && candidate.RequestHash == marker
	out.Candidate = Ref{Collection: colSkillVersions, ID: candidate.ID, Revision: candidate.Revision}
	out.BaselineVersion = trial.SkillTrial.BaselineVersion
	if terminal == "rolled_back" {
		out.State = terminal
		return out, nil
	}
	if !pending && !trial.SkillTrial.EndsAt.After(time.Now()) {
		out.State = "expired"
		return out, nil
	}
	var records []observedSkillTrialSource
	if pending {
		records, e = s.pendingSkillTrialSources(ctx, token, actor, in.Scope, policy, trial.Text, &out.Evidence)
	} else {
		records, e = s.regressionSkillTrialSources(ctx, token, actor, in.Scope, policy, candidate)
	}
	if e != nil {
		return out, e
	}
	var parentRevision int64
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		current, err := tx.Get(ctx, colSkillVersions, in.Scope, candidate.ID)
		if err != nil {
			return err
		}
		parent, err := tx.Get(ctx, colSkills, in.Scope, in.SkillID)
		if err != nil {
			return err
		}
		if current.Revision != candidate.Revision || parent.ActiveVersion != candidate.ID || !live(parent) {
			return ErrConflict
		}
		parentRevision = parent.Revision
		if pending {
			if err = recheckPendingSkillTrialEvidence(ctx, tx, records); err != nil {
				return err
			}
			out.State = "recovery_unavailable"
			return nil
		}
		if current.Revision != trial.SkillTrial.Candidate.Revision+1 {
			return ErrConflict
		}
		if err = checkActiveSkill(ctx, tx, current); err != nil {
			return err
		}
		for _, old := range records {
			item, failed, err := regressionObservation(ctx, tx, old, current, trial, in.SkillID)
			if err != nil {
				return err
			}
			if failed {
				out.Evidence = append(out.Evidence, item)
			}
		}
		if len(out.Evidence) == 0 {
			out.State = "monitoring"
			return nil
		}
		if current, err = rejectRegressedSkillVersion(ctx, tx, current, trial, in.Scope, id, marker, out.Evidence); err != nil {
			return err
		}
		out.Candidate.Revision = current.Revision
		out.State = "recovery_unavailable"
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "reject:skill_versions:runtime_regression", current.ID)
	})
	if e != nil {
		return SkillTrialRecovery{}, e
	}
	if out.State != "recovery_unavailable" || out.BaselineVersion == "" {
		return out, nil
	}
	// No synthetic validation is manufactured when the baseline is imported or
	// its old evidence has expired. ApplySkill rechecks sources and authorization.
	_, e = s.ApplySkill(ctx, token, in.SkillID, ChangeInput{Scope: in.Scope, ExpectedRevision: parentRevision, VersionID: out.BaselineVersion}, true)
	if e == nil {
		out.State = "rolled_back"
	}
	return out, nil
}

func (s *Service) authorizeSkillTrialMonitor(ctx context.Context, token, id string, in SkillTrialReadInput) (Decision, SkillAutomationPolicy, Record, error) {
	if !validScope(in.Scope) || !validRunLabel(id, 128) || !validRunLabel(in.SkillID, 256) {
		return Decision{}, SkillAutomationPolicy{}, Record{}, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "rollback:skills", in.Scope, in.SkillID, "")
	if e != nil {
		return actor, SkillAutomationPolicy{}, Record{}, e
	}
	policy, e := s.authorizeSkillAutomation(ctx, token, actor, in.Scope, in.SkillID, "")
	if e != nil {
		return actor, policy, Record{}, e
	}
	ctx = bindRequestActor(ctx, actor)
	if e = s.authorizeTrialEvidence(ctx, token, actor, ChangeInput{Scope: in.Scope}, policy); e != nil {
		return actor, policy, Record{}, e
	}
	trial, e := s.Repo.Get(ctx, colSkillTrials, in.Scope, id)
	if e != nil {
		return actor, policy, trial, e
	}
	if trial.SkillTrial == nil || trial.SkillID != in.SkillID || trial.ContentErased || trial.PrivacyQuarantined {
		return actor, policy, trial, ErrNotFound
	}
	policy, e = s.authorizeSkillAutomation(ctx, token, actor, in.Scope, in.SkillID, trial.SkillTrial.Runtime)
	return actor, policy, trial, e
}

// pendingSkillTrialSources reloads the evidence a previous rejection saved on the trial.
func (s *Service) pendingSkillTrialSources(ctx context.Context, token string, actor Decision, trialScope Scope, policy SkillAutomationPolicy, text string, evidence *[]SkillTrialObservation) ([]observedSkillTrialSource, error) {
	records := []observedSkillTrialSource{}
	if json.Unmarshal([]byte(text), evidence) != nil || len(*evidence) == 0 {
		return records, ErrConflict
	}
	for _, item := range *evidence {
		if !slices.Contains(skillTrialEvidenceScopes(trialScope, policy), item.Scope) {
			return records, ErrDenied
		}
		bound := actor
		bound.Scope = item.Scope
		scoped := bindRequestActor(ctx, bound)
		if _, e := s.authorize(scoped, token, "read:sources", item.Scope, item.Source.ID, ""); e != nil {
			return records, e
		}
		source, err := s.Repo.Get(scoped, "sources", item.Scope, item.Source.ID)
		if err != nil {
			return records, err
		}
		if source.Revision != item.Source.Revision {
			return records, ErrConflict
		}
		if _, e := s.authorize(scoped, token, "read:execution_traces", item.Scope, source.TraceID, ""); e != nil {
			return records, e
		}
		record := observedSkillTrialSource{scope: item.Scope, source: source}
		if item.Evaluation != nil {
			if _, e := s.authorize(scoped, token, "read:evaluations", item.Scope, item.Evaluation.ID, ""); e != nil {
				return records, e
			}
			v, err := s.Repo.Get(scoped, "evaluations", item.Scope, item.Evaluation.ID)
			if err != nil {
				return records, err
			}
			if v.Revision != item.Evaluation.Revision {
				return records, ErrConflict
			}
			record.evaluations = []Record{v}
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Service) regressionSkillTrialSources(ctx context.Context, token string, actor Decision, trialScope Scope, policy SkillAutomationPolicy, candidate Record) ([]observedSkillTrialSource, error) {
	records := []observedSkillTrialSource{}
	reader, ok := s.Repo.(interface {
		SkillRegressionSourceIDs(context.Context, Scope, string, time.Time) ([]string, error)
	})
	if !ok {
		return records, ErrUnavailable
	}
	for _, scope := range skillTrialEvidenceScopes(trialScope, policy) {
		bound := actor
		bound.Scope = scope
		scoped := bindRequestActor(ctx, bound)
		ids, err := reader.SkillRegressionSourceIDs(scoped, scope, candidate.ID, candidate.UpdatedAt)
		if err != nil {
			return records, err
		}
		for _, sourceID := range ids {
			if _, err = s.authorize(scoped, token, "read:sources", scope, sourceID, ""); err != nil {
				return records, err
			}
			src, err := s.Repo.Get(scoped, "sources", scope, sourceID)
			if err != nil {
				return records, err
			}
			if _, err = s.authorize(scoped, token, "read:execution_traces", scope, src.TraceID, ""); err != nil {
				return records, err
			}
			evals, err := s.Repo.EvaluationsFor(scoped, scope, Ref{Collection: "sources", ID: src.ID, Revision: src.Revision})
			if err != nil {
				return records, err
			}
			if len(evals) > evaluationSummaryLimit {
				return records, ErrUnavailable
			}
			for _, v := range evals {
				if _, err = s.authorize(scoped, token, "read:evaluations", scope, v.ID, ""); err != nil {
					return records, err
				}
			}
			records = append(records, observedSkillTrialSource{scope, src, evals})
		}
	}
	return records, nil
}

func recheckPendingSkillTrialEvidence(ctx context.Context, tx Tx, records []observedSkillTrialSource) error {
	for _, old := range records {
		src, err := tx.Get(ctx, "sources", old.scope, old.source.ID)
		if err != nil {
			return err
		}
		if src.Revision != old.source.Revision || !live(src) || src.ContentErased {
			return ErrConflict
		}
		tr, err := tx.Get(ctx, "execution_traces", old.scope, src.TraceID)
		if err != nil {
			return err
		}
		if !live(tr) || tr.ContentErased {
			return ErrConflict
		}
		for _, oldEval := range old.evaluations {
			v, err := tx.Get(ctx, "evaluations", old.scope, oldEval.ID)
			if err != nil {
				return err
			}
			if v.Revision != oldEval.Revision || !live(v) {
				return ErrConflict
			}
			if err = checkEvaluationEvidence(ctx, tx, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func regressionObservation(ctx context.Context, tx Tx, old observedSkillTrialSource, current, trial Record, skillID string) (SkillTrialObservation, bool, error) {
	var item SkillTrialObservation
	source, err := tx.Get(ctx, "sources", old.scope, old.source.ID)
	if err != nil {
		return item, false, err
	}
	if source.Revision != old.source.Revision || !live(source) || source.ContentErased {
		return item, false, ErrConflict
	}
	trace, err := tx.Get(ctx, "execution_traces", old.scope, source.TraceID)
	if err != nil {
		return item, false, err
	}
	if !live(trace) || trace.ContentErased {
		return item, false, ErrConflict
	}
	var usage SkillUsage
	if invalidRegressionUsage(source.Text, &usage, current, skillID) {
		return item, false, ErrConflict
	}
	failed := usage.Status == "failure" || usage.Status == "validation_failed" || usage.ToolFailures > 0 || usage.RuntimeErrors > 0
	item = SkillTrialObservation{Scope: old.scope, Source: Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, Outcome: "failure"}
	for _, oldEval := range old.evaluations {
		v, err := tx.Get(ctx, "evaluations", old.scope, oldEval.ID)
		if err != nil {
			return item, false, err
		}
		if v.Revision != oldEval.Revision {
			return item, false, ErrConflict
		}
		if ignoredRegressionEvaluation(v, item.Source, source, current, trial) {
			continue
		}
		if err = checkEvaluationEvidence(ctx, tx, v); err != nil {
			return item, false, err
		}
		failed = true
		ref := Ref{Collection: "evaluations", ID: v.ID, Revision: v.Revision}
		item.Evaluation = &ref
		item.IndependentlyVerified = true
	}
	return item, failed, nil
}

func invalidRegressionUsage(text string, usage *SkillUsage, current Record, skillID string) bool {
	return json.Unmarshal([]byte(text), usage) != nil || usage.VersionID != current.ID || usage.SkillID != skillID || current.Analysis == nil || usage.ContentHash != current.Analysis.ContentHash || usage.OfflineCache || usage.StartedAt.Before(current.UpdatedAt) || usage.CompletedAt.Before(usage.StartedAt) || usage.CompletedAt.After(time.Now())
}

// ignoredRegressionEvaluation keeps only independent failing evaluations of this source.
func ignoredRegressionEvaluation(v Record, target Ref, source, current, trial Record) bool {
	return !live(v) || v.Evaluation == nil || !validEvaluation(v.Evaluation) || !evaluationRole(v.Evaluation.EvalType, v.CreatedByType) || v.Evaluation.EvalType == "model_self" || v.Evaluation.Passed == nil || *v.Evaluation.Passed || v.Target == nil || *v.Target != target || v.CreatedBy == source.CreatedBy || v.CreatedBy == current.CreatedBy || v.CreatedBy == trial.CreatedBy
}

func rejectRegressedSkillVersion(ctx context.Context, tx Tx, current, trial Record, scope Scope, id, marker string, evidence []SkillTrialObservation) (Record, error) {
	prev := current.Revision
	current.Revision++
	current.Status = "rejected"
	current.RequestHash = marker
	current.UpdatedAt = time.Now().UTC()
	if err := tx.Replace(ctx, colSkillVersions, current, prev); err != nil {
		return current, err
	}
	tr, err := tx.Get(ctx, colSkillTrials, scope, id)
	if err != nil {
		return current, err
	}
	if tr.Revision != trial.Revision {
		return current, ErrConflict
	}
	previous := tr.Revision
	tr.Revision++
	tr.Text = structuredText(evidence)
	tr.UpdatedAt = time.Now().UTC()
	if err = tx.Replace(ctx, colSkillTrials, tr, previous); err != nil {
		return current, err
	}
	return current, nil
}
