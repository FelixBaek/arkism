package memory

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type SkillTrialOutcomeInput struct {
	Scope         Scope  `json:"scope"`
	TrialScope    Scope  `json:"trial_scope"`
	TrialID       string `json:"trial_id"`
	EventID       string `json:"event_id"`
	Source        Ref    `json:"source"`
	Evidence      []Ref  `json:"evidence"`
	CheckerSHA256 string `json:"checker_sha256"`
	Passed        bool   `json:"passed"`
}

func (v *SkillTrialOutcomeInput) UnmarshalJSON(data []byte) error {
	type plain SkillTrialOutcomeInput
	return decodeComparison(data, (*plain)(v), []string{"scope", "trial_scope", "trial_id", "event_id", "source", "evidence", "checker_sha256", "passed"})
}

// The independently configured checker publishes measured results, never
// approval. Same operation retries reuse the exact source-bound evaluation.
func (s *Service) RecordSkillTrialOutcome(ctx context.Context, token string, in SkillTrialOutcomeInput) (Ref, error) {
	if !validScope(in.Scope) || !validScope(in.TrialScope) || in.Scope.WorkspaceID != in.TrialScope.WorkspaceID || in.Scope.ProjectID != in.TrialScope.ProjectID || in.Scope.UserID != in.TrialScope.UserID || !workCheckpointIDPattern.MatchString(in.EventID) || !validRunLabel(in.TrialID, 128) || in.Source.Collection != "sources" || in.Source.Revision < 1 || !validRunLabel(in.Source.ID, 256) || !sourceHashPattern.MatchString(in.CheckerSHA256) || len(in.Evidence) == 0 || len(in.Evidence) > 64 {
		return Ref{}, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "write:evaluations", in.Scope, "", "")
	if e != nil {
		return Ref{}, e
	}
	if actor.Role != "service" && actor.Role != "pipeline" {
		return Ref{}, ErrDenied
	}
	ctx = bindRequestActor(ctx, actor)
	for _, ref := range append(append([]Ref{}, in.Evidence...), in.Source) {
		if ref.Collection != "sources" || ref.Revision < 1 {
			return Ref{}, ErrInvalid
		}
		if _, e = s.authorize(ctx, token, "read:sources", in.Scope, ref.ID, ""); e != nil {
			return Ref{}, e
		}
	}
	bound := actor
	bound.Scope = in.TrialScope
	if _, e = s.authorize(bindRequestActor(ctx, bound), token, "read:skills", in.TrialScope, "", ""); e != nil {
		return Ref{}, e
	}
	trial, e := s.Repo.Get(ctx, colSkillTrials, in.TrialScope, in.TrialID)
	if e != nil {
		return Ref{}, e
	}
	if _, e = s.authorize(bindRequestActor(ctx, bound), token, "read:skills", in.TrialScope, trial.SkillID, ""); e != nil {
		return Ref{}, e
	}
	if trial.SkillTrial == nil {
		return Ref{}, ErrNotFound
	}
	if _, e = s.authorize(bindRequestActor(ctx, bound), token, "read:skill_versions", in.TrialScope, trial.SkillTrial.Candidate.ID, ""); e != nil {
		return Ref{}, e
	}
	snapshot, e := s.Repo.Get(ctx, "sources", in.Scope, in.Source.ID)
	if e != nil {
		return Ref{}, e
	}
	if _, e = s.authorize(ctx, token, "read:execution_traces", in.Scope, snapshot.TraceID, ""); e != nil {
		return Ref{}, e
	}
	id := "trial-outcome-" + ContentHash(structuredText(in.Scope)+"\n"+actor.PrincipalID+"\n"+in.EventID)
	hash := ContentHash(structuredText(in))
	result := Ref{Collection: "evaluations", ID: id, Revision: 1}
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		old, e := tx.Get(ctx, "evaluations", in.Scope, id)
		if e == nil {
			if old.RequestHash != hash || !live(old) || old.ContentErased || old.PrivacyQuarantined {
				return ErrConflict
			}
			source, e := tx.Get(ctx, "sources", in.Scope, in.Source.ID)
			if e != nil {
				return e
			}
			if source.Revision != in.Source.Revision || !live(source) || source.ContentErased || source.PrivacyQuarantined {
				return ErrConflict
			}
			return checkEvaluationEvidence(ctx, tx, old)
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		current, e := tx.Get(ctx, colSkillTrials, in.TrialScope, in.TrialID)
		if e != nil {
			return e
		}
		if current.SkillTrial == nil || current.Revision != trial.Revision || current.Status != "active" || current.PrivacyQuarantined || current.ContentErased || current.CreatedBy == actor.PrincipalID {
			return ErrDenied
		}
		candidate, e := skillTrialCandidate(ctx, tx, in.TrialScope, current.SkillID, current.SkillTrial)
		if e != nil {
			return e
		}
		if candidate.CreatedBy == actor.PrincipalID {
			return ErrDenied
		}
		source, e := tx.Get(ctx, "sources", in.Scope, in.Source.ID)
		if e != nil {
			return e
		}
		if source.Revision != in.Source.Revision || !live(source) || source.ContentErased || source.PrivacyQuarantined || source.CreatedBy == actor.PrincipalID || source.TraceID == "" {
			return ErrConflict
		}
		var usage SkillUsage
		if json.Unmarshal([]byte(source.Text), &usage) != nil || usage.TrialID != current.ID || usage.SkillID != candidate.SkillID || usage.VersionID != candidate.ID || usage.Runtime != current.SkillTrial.Runtime || usage.ContentHash != candidate.Analysis.ContentHash || usage.OfflineCache || usage.StartedAt.Before(current.CreatedAt) || !usage.StartedAt.Before(current.SkillTrial.EndsAt) || usage.CompletedAt.Before(usage.StartedAt) || usage.CompletedAt.After(time.Now()) {
			return ErrConflict
		}
		if in.Passed && (usage.Status != "success" || usage.ToolFailures != 0 || usage.RuntimeErrors != 0) {
			return ErrDenied
		}
		expectedTraceID := source.TraceID
		trace, e := tx.Get(ctx, "execution_traces", in.Scope, expectedTraceID)
		if e != nil {
			return e
		}
		if !live(trace) || trace.ContentErased || trace.PrivacyQuarantined {
			return ErrConflict
		}
		sources := []SourceRef{}
		seen := map[string]bool{}
		for _, ref := range append(append([]Ref{}, in.Evidence...), in.Source) {
			if seen[ref.ID] {
				continue
			}
			seen[ref.ID] = true
			source, e := tx.Get(ctx, "sources", in.Scope, ref.ID)
			if e != nil {
				return e
			}
			if source.Revision != ref.Revision || !live(source) || source.ContentErased || source.PrivacyQuarantined || source.TraceID != expectedTraceID {
				return ErrConflict
			}
			sources = append(sources, sourceReference(source, "trial-check"))
		}
		now := time.Now().UTC()
		passed := in.Passed
		record := Record{ID: id, Scope: in.Scope, Kind: "trial_outcome", Status: "active", Revision: 1, Text: "Independent pinned trial checker " + in.CheckerSHA256, Sources: sources, Target: &in.Source, Evaluation: &Evaluation{EvalType: "automated_check", Passed: &passed}, Passed: passed, RequestHash: hash, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, CreatedAt: now, UpdatedAt: now}
		if e = checkEvaluationEvidence(ctx, tx, record); e != nil {
			return e
		}
		if e = insertSourceRecord(ctx, tx, "evaluations", record); e != nil {
			return e
		}
		return audit(ctx, tx, in.Scope, actor.PrincipalID, "evaluate:skill_trial", id)
	})
	if e != nil {
		return Ref{}, e
	}
	return result, nil
}
