package memory

import (
	"context"
	"slices"
)

// Reuse the existing trial evidence checker for human and service advances.
// These bounds only narrow the check to already observed scopes; they do not
// authorize automation or replace the deployment policy checked by ApplySkill.
func (s *Service) checkImprovementTrialEvidence(ctx context.Context, candidate Record, f *FlowImprovement, in ChangeInput) error {
	bounds := SkillAutomationPolicy{Runtimes: []string{f.Runtime}, MinTrialRuns: 2}
	for _, item := range f.Observations {
		if item.Scope != candidate.Scope && !slices.Contains(bounds.EvidenceAgents, item.Scope.AgentID) {
			bounds.EvidenceAgents = append(bounds.EvidenceAgents, item.Scope.AgentID)
		}
	}
	return s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return checkAutomaticSkillPromotion(ctx, tx, candidate, in, bounds)
	})
}
