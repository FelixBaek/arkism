package memory

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Read the identity recorded during trace ingest, including historical rows.
// Scope predicates remain necessary even though the local ids are unique.
func (p *Postgres) skillTrialRunIdentity(ctx context.Context, scope Scope, trace string) (string, string, error) {
	var run, upstream string
	err := p.q().QueryRow(ctx, `SELECT agent_run_id,COALESCE(langsmith_run_id,'') FROM execution_traces WHERE organization_id=$1 AND project_id=$2 AND user_id=$3 AND agent_id=$4 AND id=$5`, scope.WorkspaceID, scope.ProjectID, scope.UserID, scope.AgentID, trace).Scan(&run, &upstream)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return run, upstream, err
}

type trialExecutionInterval struct {
	runtime    string
	start, end time.Time
}
type trialExecutions struct {
	ids map[[2]string]bool
	// Distinct upstream ids can disambiguate concurrent calls. Older records
	// without one cannot establish independence for identical execution windows.
	intervals map[trialExecutionInterval]bool
	repeated  bool
}

func newTrialExecutions() *trialExecutions {
	return &trialExecutions{ids: make(map[[2]string]bool), intervals: make(map[trialExecutionInterval]bool)}
}
func (s *trialExecutions) observe(ctx context.Context, tx Tx, scope Scope, trace Record, u SkillUsage) (bool, error) {
	var local, upstream string
	if repo, ok := tx.(interface {
		skillTrialRunIdentity(context.Context, Scope, string) (string, string, error)
	}); ok {
		var err error
		local, upstream, err = repo.skillTrialRunIdentity(ctx, scope, trace.ID)
		if err != nil {
			return false, err
		}
	}
	if local == "" {
		for _, ref := range trace.Relations {
			if ref.Collection == "agent_runs" {
				local = ref.ID
				break
			}
		}
	}
	return s.add(trace.ID, local, upstream, u), nil
}
func (s *trialExecutions) add(trace, run, upstream string, u SkillUsage) bool {
	duplicate := false
	for _, key := range [][2]string{{"trace", trace}, {"run", run}, {"upstream", upstream}} {
		if key[1] != "" {
			duplicate = duplicate || s.ids[key]
			s.ids[key] = true
		}
	}
	window := trialExecutionInterval{u.Runtime, u.StartedAt.UTC(), u.CompletedAt.UTC()}
	hasUnknown, exists := s.intervals[window]
	if exists && (hasUnknown || upstream == "") {
		duplicate = true
	}
	s.intervals[window] = hasUnknown || upstream == ""
	s.repeated = s.repeated || duplicate
	return !duplicate
}
