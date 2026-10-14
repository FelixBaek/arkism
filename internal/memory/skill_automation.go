package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
)

// Deployment-owned bounds. A generated skill can never widen this policy.
type ScopedTrialEvidence struct {
	Scope      Scope `json:"scope"`
	Source     Ref   `json:"source"`
	Evaluation Ref   `json:"evaluation"`
}

type SkillAutomationPolicy struct {
	EvidenceAgents  []string  `json:"evidence_agents,omitempty"`
	PrincipalID     string    `json:"principal_id"`
	Scope           Scope     `json:"scope"`
	SkillIDs        []string  `json:"skill_ids"`
	Runtimes        []string  `json:"runtimes"`
	ExpiresAt       time.Time `json:"expires_at"`
	MaxTrialSeconds int64     `json:"max_trial_seconds"`
	MinTrialRuns    int       `json:"min_trial_runs"`
}

func ParseSkillAutomationPolicies(raw string) ([]SkillAutomationPolicy, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []SkillAutomationPolicy
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&out) != nil || !errors.Is(d.Decode(new(any)), io.EOF) || len(out) == 0 || len(out) > 32 {
		return nil, ErrInvalid
	}
	for _, p := range out {
		if !validRunLabel(p.PrincipalID, 256) || !validScope(p.Scope) || p.ExpiresAt.IsZero() || len(p.SkillIDs) == 0 || len(p.SkillIDs) > 64 || len(p.Runtimes) == 0 || len(p.Runtimes) > 5 || p.MaxTrialSeconds < 60 || p.MaxTrialSeconds > 604800 || p.MinTrialRuns < 2 || p.MinTrialRuns > 32 || containsSecret(structuredText(p)) {
			return nil, ErrInvalid
		}
		for _, id := range p.SkillIDs {
			if !validRunLabel(id, 256) || id == "*" {
				return nil, ErrInvalid
			}
		}
		if len(p.EvidenceAgents) > 8 {
			return nil, ErrInvalid
		}
		for _, agent := range p.EvidenceAgents {
			if !validRunLabel(agent, 256) {
				return nil, ErrInvalid
			}
		}
		for _, r := range p.Runtimes {
			if !trialRuntime(r) {
				return nil, ErrInvalid
			}
		}
	}
	return out, nil
}

func (s *Service) skillAutomationPolicy(d Decision, scope Scope, skillID, runtime string) (SkillAutomationPolicy, error) {
	if d.Role != roleService {
		return SkillAutomationPolicy{}, ErrDenied
	}
	for _, p := range s.SkillAutomationPolicies {
		if p.PrincipalID == d.PrincipalID && p.Scope == scope && p.ExpiresAt.After(time.Now()) && slices.Contains(p.SkillIDs, skillID) && (runtime == "" || slices.Contains(p.Runtimes, runtime)) {
			return p, nil
		}
	}
	return SkillAutomationPolicy{}, ErrDenied
}

func (s *Service) authorizeSkillAutomation(ctx context.Context, token string, d Decision, scope Scope, skillID, runtime string) (SkillAutomationPolicy, error) {
	p, e := s.skillAutomationPolicy(d, scope, skillID, runtime)
	if e != nil {
		return p, e
	}
	other, e := s.authorize(ctx, token, "auto_learn:skills", scope, skillID, "")
	if e != nil {
		return p, e
	}
	if other.PrincipalID != d.PrincipalID || other.Role != d.Role || !other.ContentSafe {
		return p, ErrDenied
	}
	return p, nil
}

// Auto-promotion requires current trial execution sources AND a separate
// evaluator of that exact revision. Merely loading, mentioning or self-rating
// a skill is not a successful trial. Read the evidence inside the apply tx.
func checkAutomaticSkillPromotion(ctx context.Context, tx Tx, r Record, in ChangeInput, policy SkillAutomationPolicy) error {
	evidence := append([]ScopedTrialEvidence{}, in.ScopedTrialEvidence...)
	for _, ref := range in.TrialEvidence {
		evidence = append(evidence, ScopedTrialEvidence{Scope: r.Scope, Source: ref})
	}
	if in.TrialID == "" || in.ValidationID == "" || len(evidence) < policy.MinTrialRuns || len(evidence) > 32 {
		return ErrDenied
	}
	trial, e := tx.Get(ctx, colSkillTrials, r.Scope, in.TrialID)
	if e != nil {
		return e
	}
	if trial.SkillTrial == nil || trial.Status != "active" || trial.ContentErased || trial.PrivacyQuarantined || trial.SkillID != r.SkillID || trial.SkillTrial.Candidate.ID != r.ID || trial.SkillTrial.Candidate.Revision != r.Revision || !slices.Contains(policy.Runtimes, trial.SkillTrial.Runtime) {
		return ErrConflict
	}
	evaluation, e := tx.Get(ctx, "evaluations", r.Scope, in.ValidationID)
	if e != nil {
		return e
	}
	if evaluation.CreatedBy == trial.CreatedBy || evaluation.CreatedBy == r.CreatedBy || evaluation.Target == nil || evaluation.Target.Revision != r.Revision {
		return ErrDenied
	}
	if e = validation(ctx, tx, r, in.ValidationID, true); e != nil {
		return e
	}
	reader, ok := tx.(interface {
		SkillTrialSourceIDs(context.Context, Scope, string) ([]string, error)
	})
	if !ok {
		return ErrUnavailable
	}
	requested := map[string]bool{}
	for _, item := range evidence {
		key := structuredText(item.Scope) + "/" + item.Source.ID
		if requested[key] {
			return ErrInvalid
		}
		requested[key] = true
	}
	count := 0
	for _, scope := range skillTrialEvidenceScopes(r.Scope, policy) {
		ids, e := reader.SkillTrialSourceIDs(ctx, scope, in.TrialID)
		if e != nil {
			return e
		}
		count += len(ids)
		for _, id := range ids {
			if !requested[structuredText(scope)+"/"+id] {
				return ErrDenied
			}
		}
	}
	if count != len(evidence) {
		return ErrDenied
	}
	seen := map[string]bool{}
	for _, item := range evidence {
		ref := item.Source
		if ref.Collection != "sources" || ref.Revision < 1 {
			return ErrInvalid
		}
		source, e := tx.Get(ctx, "sources", item.Scope, ref.ID)
		if e != nil {
			return e
		}
		traceKey := structuredText(item.Scope) + "/" + source.TraceID
		if source.Revision != ref.Revision || !live(source) || source.ContentErased || source.PrivacyQuarantined || source.TraceID == "" || seen[traceKey] {
			return ErrConflict
		}
		seen[traceKey] = true
		if item.Scope == r.Scope && item.Evaluation.ID == "" {
			bound := false
			for _, basis := range evaluation.Sources {
				if basis.SourceID == ref.ID {
					bound = true
				}
			}
			if !bound {
				return ErrDenied
			}
		} else {
			if item.Evaluation.Collection != "evaluations" || item.Evaluation.Revision < 1 {
				return ErrInvalid
			}
			observed, e := tx.Get(ctx, "evaluations", item.Scope, item.Evaluation.ID)
			if e != nil {
				return e
			}
			if observed.Revision != item.Evaluation.Revision || !live(observed) || !independentEvaluation(observed) || observed.Target == nil || *observed.Target != ref || observed.CreatedBy == source.CreatedBy || observed.CreatedBy == trial.CreatedBy || observed.CreatedBy == r.CreatedBy {
				return ErrDenied
			}
			if e = checkEvaluationEvidence(ctx, tx, observed); e != nil {
				return e
			}
		}
		evaluations, e := tx.EvaluationsFor(ctx, item.Scope, ref)
		if e != nil {
			return e
		}
		if len(evaluations) > evaluationSummaryLimit {
			return ErrUnavailable
		}
		for _, observed := range evaluations {
			if !live(observed) || observed.Evaluation == nil || observed.Evaluation.EvalType == "model_self" || observed.Evaluation.Passed == nil || *observed.Evaluation.Passed || observed.CreatedBy == source.CreatedBy || observed.CreatedBy == trial.CreatedBy || observed.CreatedBy == r.CreatedBy {
				continue
			}
			if e = checkEvaluationEvidence(ctx, tx, observed); e != nil {
				return e
			}
			return ErrDenied
		}
		var usage SkillUsage
		if json.Unmarshal([]byte(source.Text), &usage) != nil || usage.TrialID != in.TrialID || usage.VersionID != r.ID || usage.SkillID != r.SkillID || r.Analysis == nil || usage.ContentHash != r.Analysis.ContentHash || usage.Runtime != trial.SkillTrial.Runtime || usage.Status != "success" || usage.ToolFailures != 0 || usage.RuntimeErrors != 0 || usage.OfflineCache || usage.StartedAt.Before(trial.CreatedAt) || !usage.StartedAt.Before(trial.SkillTrial.EndsAt) || usage.CompletedAt.Before(usage.StartedAt) || usage.CompletedAt.After(time.Now()) {
			return ErrDenied
		}
		trace, e := tx.Get(ctx, "execution_traces", item.Scope, source.TraceID)
		if e != nil {
			return e
		}
		if !live(trace) || trace.ContentErased || trace.PrivacyQuarantined {
			return ErrConflict
		}
	}
	return nil
}

func skillTrialEvidenceScopes(scope Scope, policy SkillAutomationPolicy) []Scope {
	scopes := []Scope{scope}
	for _, agent := range policy.EvidenceAgents {
		child := scope
		child.AgentID = agent
		if !slices.Contains(scopes, child) {
			scopes = append(scopes, child)
		}
	}
	return scopes
}

func (s *Service) authorizeTrialEvidence(ctx context.Context, token string, d Decision, in ChangeInput, policy SkillAutomationPolicy) error {
	allowed := skillTrialEvidenceScopes(in.Scope, policy)
	// Read authority is checked for every configured observation scope, even
	// when a caller omits that agent's failing evidence from its request.
	for _, scope := range allowed {
		for _, col := range []string{"sources", "execution_traces", "evaluations"} {
			bound := d
			bound.Scope = scope
			who, e := s.authorize(bindRequestActor(ctx, bound), token, "read:"+col, scope, "", "")
			if e != nil {
				return e
			}
			if who.PrincipalID != d.PrincipalID || who.Role != d.Role {
				return ErrDenied
			}
		}
	}
	for _, item := range in.ScopedTrialEvidence {
		if !slices.Contains(allowed, item.Scope) {
			return ErrDenied
		}
		for _, ref := range []Ref{item.Source, item.Evaluation} {
			bound := d
			bound.Scope = item.Scope
			who, e := s.authorize(bindRequestActor(ctx, bound), token, "read:"+ref.Collection, item.Scope, ref.ID, "")
			if e != nil {
				return e
			}
			if who.PrincipalID != d.PrincipalID || who.Role != d.Role {
				return ErrDenied
			}
		}
	}
	return nil
}

// Enumerate canonical usage events, including failed/cancelled ones, so callers
// cannot cherry-pick successful trial sources. PostgreSQL Serializable apply
// sees one stable snapshot; all returned sources are revalidated in that tx.
func (p *Postgres) SkillTrialSourceIDs(ctx context.Context, scope Scope, trialID string) ([]string, error) {
	rows, e := p.q().Query(ctx, `SELECT id FROM source_events WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND kind=$5 AND payload->>'trial_id'=$6 ORDER BY id LIMIT 33`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, skillUsageEventKind, trialID)
	if e != nil {
		return nil, pgErr(e)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, pgErr(e)
		}
		ids = append(ids, id)
	}
	return ids, pgErr(rows.Err())
}
