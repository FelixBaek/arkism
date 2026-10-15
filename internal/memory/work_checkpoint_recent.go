package memory

import (
	"context"
	"errors"
	"time"
)

const recentWorkCandidateLimit = 30

// RecentWorkInput selects historical sessions, never a work item to execute.
type RecentWorkInput struct {
	Scope      Scope  `json:"scope"`
	Repository string `json:"repository"`
	SessionID  string `json:"session_id"`
	Limit      int    `json:"limit"`
	MaxAgeDays int    `json:"max_age_days"`
}
type RecentWorkOutput struct {
	Items     []WorkCheckpointView `json:"items"`
	Authority string               `json:"authority"`
}
type recentWorkRowsReader interface {
	RecentWorkRows(context.Context, Scope, string, string, time.Time, int) ([]Record, error)
}

// RecentWorkCheckpoints reuses the full read gate: current policy, source revision,
// erasure, expiry and transaction fences apply to every returned summary.
func (s *Service) RecentWorkCheckpoints(ctx context.Context, token string, in RecentWorkInput) (RecentWorkOutput, error) {
	out := RecentWorkOutput{Items: []WorkCheckpointView{}, Authority: AuthorityDataOnly}
	if in.Limit == 0 {
		in.Limit = 3
	}
	if in.MaxAgeDays == 0 {
		in.MaxAgeDays = 7
	}
	if !validScope(in.Scope) || !validWorkCheckpointString(in.Repository, 2048) || !validWorkCheckpointString(in.SessionID, 256) || in.Limit < 1 || in.Limit > 10 || in.MaxAgeDays < 1 || in.MaxAgeDays > 30 {
		return out, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "read:"+colWorkCheckpoints, in.Scope, "", "")
	if err != nil {
		return out, err
	}
	store, ok := s.Repo.(recentWorkRowsReader)
	if !ok {
		return out, ErrUnavailable
	}
	ctx = bindRequestActor(ctx, d)
	since := time.Now().UTC().Add(-time.Duration(in.MaxAgeDays) * 24 * time.Hour)
	rows, err := store.RecentWorkRows(ctx, in.Scope, in.Repository, in.SessionID, since, recentWorkCandidateLimit)
	if err != nil {
		return out, err
	}
	if len(rows) > recentWorkCandidateLimit {
		return RecentWorkOutput{}, ErrConflict
	}
	for _, row := range rows {
		if !sameScope(row.Scope, in.Scope) || !isWorkCheckpoint(row) {
			return RecentWorkOutput{}, ErrConflict
		}
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if len(out.Items) == in.Limit {
			break
		}
		view, err := s.GetWorkCheckpoint(ctx, token, WorkCheckpointGetInput{Scope: in.Scope, ID: row.ID})
		if err != nil {
			if errors.Is(err, ErrConflict) {
				// A stale evidence reference is ineligible; a changed checkpoint itself
				// is an integrity race and must not be mixed into this response.
				current, readErr := s.Repo.Get(ctx, colWorkCheckpoints, in.Scope, row.ID)
				if readErr != nil && !errors.Is(readErr, ErrNotFound) && !errors.Is(readErr, ErrDenied) {
					return RecentWorkOutput{}, readErr
				}
				if readErr == nil && (!sameScope(current.Scope, in.Scope) || current.ID != row.ID || current.Revision != row.Revision) {
					return RecentWorkOutput{}, ErrConflict
				}
			}
			if errors.Is(err, ErrDenied) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
				continue
			}
			return RecentWorkOutput{}, err
		}
		pkg := view.Package
		// Fail closed if a row changed after selection; do not emit mixed revisions.
		if view.Revision != row.Revision || pkg.Repository != in.Repository || pkg.SessionID == "" || pkg.SessionID == in.SessionID || view.UpdatedAt.Before(since) || seen[pkg.SessionID] {
			return RecentWorkOutput{}, ErrConflict
		}
		seen[pkg.SessionID] = true
		// Facts and source IDs only: no saved commands, file bodies or archive text.
		view.Package = WorkCheckpointPackage{Title: pkg.Title, Goal: pkg.Goal, Repository: pkg.Repository, Status: pkg.Status, Origin: pkg.Origin, SessionID: pkg.SessionID, Decisions: pkg.Decisions, Completed: pkg.Completed, NextSteps: pkg.NextSteps, SourceRefs: pkg.SourceRefs}
		view.ArchiveMessages = nil
		out.Items = append(out.Items, view)
	}
	return out, nil
}

func (p *Postgres) RecentWorkRows(ctx context.Context, scope Scope, repository, session string, since time.Time, limit int) ([]Record, error) {
	if checkScope(scope) != nil || limit < 1 || limit > recentWorkCandidateLimit {
		return nil, ErrInvalid
	}
	// The anti-join chooses one most recent checkpoint per session. All
	// filtering, including the tenant fence, stays inside selectRecords.
	return p.selectRecords(ctx, colWorkCheckpoints, scope,
		`status='active' AND doc->'work_checkpoint'->'package'->>'repository'=$6 AND COALESCE(doc->'work_checkpoint'->'package'->>'session_id','') NOT IN ('',$7) AND updated_at >= $8 AND NOT EXISTS (SELECT 1 FROM memory_records newer WHERE newer.collection=memory_records.collection AND newer.workspace_id=memory_records.workspace_id AND newer.project_id=memory_records.project_id AND newer.user_id=memory_records.user_id AND newer.agent_id=memory_records.agent_id AND newer.status='active' AND newer.doc->'work_checkpoint'->'package'->>'repository'=$6 AND newer.doc->'work_checkpoint'->'package'->>'session_id'=memory_records.doc->'work_checkpoint'->'package'->>'session_id' AND (newer.updated_at,newer.id)>(memory_records.updated_at,memory_records.id))`,
		[]any{repository, session, since}, "updated_at DESC, id DESC", limit)
}
