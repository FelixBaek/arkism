package memory

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

type FlowImprovementPage struct {
	Items      []Record `json:"items"`
	NextCursor string   `json:"next_cursor,omitempty"`
}

type flowImprovementLister interface {
	ListFlowImprovements(context.Context, Scope, string, int) ([]Record, error)
}

func (p *Postgres) ListFlowImprovements(ctx context.Context, scope Scope, after string, limit int) ([]Record, error) {
	if limit < 1 || limit > 101 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, colJobs, scope, `kind=$6 AND id>$7`, []any{flowImprovementKind, after}, "id ASC", limit)
}

func improvementListRecord(r Record) Record {
	f := r.FlowImprovement
	state := &FlowImprovement{SourceRun: f.SourceRun, Stage: f.Stage, Runtime: f.Runtime, TrialEndsAt: f.TrialEndsAt, Candidate: f.Candidate, Comparison: f.Comparison, Trial: f.Trial, TrialComparison: f.TrialComparison, ObservationState: f.ObservationState}
	if f.Pending != nil {
		state.Pending = &FlowImprovementAttempt{ActionID: f.Pending.ActionID, Action: f.Pending.Action, State: f.Pending.State, StartedAt: f.Pending.StartedAt}
	}
	return Record{ID: r.ID, Scope: r.Scope, Kind: r.Kind, Status: r.Status, Revision: r.Revision, SkillID: r.SkillID, FlowImprovement: state, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

func (s *Service) ListFlowImprovements(ctx context.Context, token string, scope Scope, after string, limit int) (FlowImprovementPage, error) {
	out := FlowImprovementPage{Items: []Record{}}
	if limit == 0 {
		limit = 50
	}
	if !validScope(scope) || limit < 1 || limit > 100 || after != "" && !validRunLabel(after, 128) {
		return out, ErrInvalid
	}
	actor, e := s.authorize(ctx, token, "read:"+colCrew, scope, "", "")
	if e != nil {
		return out, e
	}
	ctx = bindRequestActor(ctx, actor)
	reader, ok := s.Repo.(flowImprovementLister)
	if !ok {
		return out, ErrUnavailable
	}
	rows, e := reader.ListFlowImprovements(ctx, scope, after, limit+1)
	if e != nil {
		return out, e
	}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextCursor = rows[len(rows)-1].ID
	}
	for _, r := range rows {
		if !readableImprovement(r) {
			continue
		}
		if e = s.authorizeImprovementMaterial(ctx, token, scope, r.FlowImprovement); e != nil {
			if errors.Is(e, ErrDenied) || errors.Is(e, ErrNotFound) || errors.Is(e, ErrConflict) {
				continue
			}
			return FlowImprovementPage{}, e
		}
		out.Items = append(out.Items, improvementListRecord(r))
	}
	return out, nil
}

func flowImprovementListRoute(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/flow/improvements", func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil {
				respond(w, nil, ErrInvalid)
				return
			}
			limit = n
		}
		out, e := s.ListFlowImprovements(r.Context(), bearer(r), scopeQuery(r), r.URL.Query().Get("after"), limit)
		respond(w, out, e)
	})
}
