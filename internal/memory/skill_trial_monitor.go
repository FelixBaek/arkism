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
	if !validScope(in.Scope) || !validRunLabel(id, 128) || !validRunLabel(in.SkillID, 256) {
		return out, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "rollback:skills", in.Scope, in.SkillID, "")
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
	trial, e := s.Repo.Get(ctx, colSkillTrials, in.Scope, id)
	if e != nil {
		return out, e
	}
	if trial.SkillTrial == nil || trial.SkillID != in.SkillID || trial.ContentErased || trial.PrivacyQuarantined {
		return out, ErrNotFound
	}
	policy, e = s.authorizeSkillAutomation(ctx, token, actor, in.Scope, in.SkillID, trial.SkillTrial.Runtime)
	if e != nil {
		return out, e
	}
	candidate, e := s.Repo.Get(ctx, colSkillVersions, in.Scope, trial.SkillTrial.Candidate.ID)
	if e != nil {
		return out, e
	}
	marker := ContentHash("trial-regression/" + id)
	pending := candidate.Status == "rejected" && candidate.RequestHash == marker
	out.Candidate = Ref{Collection: colSkillVersions, ID: candidate.ID, Revision: candidate.Revision}
	out.BaselineVersion = trial.SkillTrial.BaselineVersion
	if !pending && !trial.SkillTrial.EndsAt.After(time.Now()) {
		out.State = "expired"
		return out, nil
	}
	type observed struct {
		scope  Scope
		source Record
		evals  []Record
	}
	records := []observed{}
	if pending {
		if json.Unmarshal([]byte(trial.Text), &out.Evidence) != nil || len(out.Evidence) == 0 {
			return out, ErrConflict
		}
		for _, item := range out.Evidence {
			if !slices.Contains(skillTrialEvidenceScopes(in.Scope, policy), item.Scope) {
				return out, ErrDenied
			}
			bound := actor
			bound.Scope = item.Scope
			scoped := bindRequestActor(ctx, bound)
			if _, e = s.authorize(scoped, token, "read:sources", item.Scope, item.Source.ID, ""); e != nil {
				return out, e
			}
			source, err := s.Repo.Get(scoped, "sources", item.Scope, item.Source.ID)
			if err != nil {
				return out, err
			}
			if source.Revision != item.Source.Revision {
				return out, ErrConflict
			}
			if _, e = s.authorize(scoped, token, "read:execution_traces", item.Scope, source.TraceID, ""); e != nil {
				return out, e
			}
			record := observed{scope: item.Scope, source: source}
			if item.Evaluation != nil {
				if _, e = s.authorize(scoped, token, "read:evaluations", item.Scope, item.Evaluation.ID, ""); e != nil {
					return out, e
				}
				v, err := s.Repo.Get(scoped, "evaluations", item.Scope, item.Evaluation.ID)
				if err != nil {
					return out, err
				}
				if v.Revision != item.Evaluation.Revision {
					return out, ErrConflict
				}
				record.evals = []Record{v}
			}
			records = append(records, record)
		}
	}
	if !pending {
		reader, ok := s.Repo.(interface {
			SkillRegressionSourceIDs(context.Context, Scope, string, time.Time) ([]string, error)
		})
		if !ok {
			return out, ErrUnavailable
		}
		for _, scope := range skillTrialEvidenceScopes(in.Scope, policy) {
			bound := actor
			bound.Scope = scope
			scoped := bindRequestActor(ctx, bound)
			ids, err := reader.SkillRegressionSourceIDs(scoped, scope, candidate.ID, candidate.UpdatedAt)
			if err != nil {
				return out, err
			}
			for _, sourceID := range ids {
				if _, err = s.authorize(scoped, token, "read:sources", scope, sourceID, ""); err != nil {
					return out, err
				}
				src, err := s.Repo.Get(scoped, "sources", scope, sourceID)
				if err != nil {
					return out, err
				}
				if _, err = s.authorize(scoped, token, "read:execution_traces", scope, src.TraceID, ""); err != nil {
					return out, err
				}
				evals, err := s.Repo.EvaluationsFor(scoped, scope, Ref{Collection: "sources", ID: src.ID, Revision: src.Revision})
				if err != nil {
					return out, err
				}
				if len(evals) > evaluationSummaryLimit {
					return out, ErrUnavailable
				}
				for _, v := range evals {
					if _, err = s.authorize(scoped, token, "read:evaluations", scope, v.ID, ""); err != nil {
						return out, err
					}
				}
				records = append(records, observed{scope, src, evals})
			}
		}
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
				for _, oldEval := range old.evals {
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
			source, err := tx.Get(ctx, "sources", old.scope, old.source.ID)
			if err != nil {
				return err
			}
			if source.Revision != old.source.Revision || !live(source) || source.ContentErased {
				return ErrConflict
			}
			trace, err := tx.Get(ctx, "execution_traces", old.scope, source.TraceID)
			if err != nil {
				return err
			}
			if !live(trace) || trace.ContentErased {
				return ErrConflict
			}
			var usage SkillUsage
			if json.Unmarshal([]byte(source.Text), &usage) != nil || usage.VersionID != current.ID || usage.SkillID != in.SkillID || current.Analysis == nil || usage.ContentHash != current.Analysis.ContentHash || usage.OfflineCache || usage.StartedAt.Before(current.UpdatedAt) || usage.CompletedAt.Before(usage.StartedAt) || usage.CompletedAt.After(time.Now()) {
				return ErrConflict
			}
			failed := usage.Status == "failure" || usage.Status == "validation_failed" || usage.ToolFailures > 0 || usage.RuntimeErrors > 0
			item := SkillTrialObservation{Scope: old.scope, Source: Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, Outcome: "failure"}
			for _, oldEval := range old.evals {
				v, err := tx.Get(ctx, "evaluations", old.scope, oldEval.ID)
				if err != nil {
					return err
				}
				if v.Revision != oldEval.Revision {
					return ErrConflict
				}
				if !live(v) || v.Evaluation == nil || !validEvaluation(v.Evaluation) || !evaluationRole(v.Evaluation.EvalType, v.CreatedByType) || v.Evaluation.EvalType == "model_self" || v.Evaluation.Passed == nil || *v.Evaluation.Passed || v.Target == nil || *v.Target != item.Source || v.CreatedBy == source.CreatedBy || v.CreatedBy == current.CreatedBy || v.CreatedBy == trial.CreatedBy {
					continue
				}
				if err = checkEvaluationEvidence(ctx, tx, v); err != nil {
					return err
				}
				failed = true
				ref := Ref{Collection: "evaluations", ID: v.ID, Revision: v.Revision}
				item.Evaluation = &ref
				item.IndependentlyVerified = true
			}
			if failed {
				out.Evidence = append(out.Evidence, item)
			}
		}
		if len(out.Evidence) == 0 {
			out.State = "monitoring"
			return nil
		}
		prev := current.Revision
		current.Revision++
		current.Status = "rejected"
		current.RequestHash = marker
		current.UpdatedAt = time.Now().UTC()
		if err = tx.Replace(ctx, colSkillVersions, current, prev); err != nil {
			return err
		}
		tr, err := tx.Get(ctx, colSkillTrials, in.Scope, id)
		if err != nil {
			return err
		}
		if tr.Revision != trial.Revision {
			return ErrConflict
		}
		previous := tr.Revision
		tr.Revision++
		tr.Text = structuredText(out.Evidence)
		tr.UpdatedAt = time.Now().UTC()
		if err = tx.Replace(ctx, colSkillTrials, tr, previous); err != nil {
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
