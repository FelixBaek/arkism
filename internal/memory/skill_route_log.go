package memory

import (
	"context"
	"net/http"
	"slices"
	"time"
)

const (
	colSkillRoutes = "skill_routes"
	// shortcut: the list reads the newest 200 decisions; page by time if owners need older ones.
	skillRoutesRead = 200
)

// SkillRoute is one route_skills decision as the router made it: skill ids,
// names and the judge, never the query.
type SkillRoute struct {
	Selected     string   `json:"selected,omitempty" bson:"selected,omitempty"`
	SelectedName string   `json:"selected_name,omitempty" bson:"selected_name,omitempty"`
	Confirmed    bool     `json:"confirmed" bson:"confirmed"` // a judge picked it; false for rules and jev_uncertain
	Provider     string   `json:"provider" bson:"provider"`   // rules, or the judge model (clef-flash, jev-…)
	Degraded     []string `json:"degraded,omitempty" bson:"degraded,omitempty"`
	Candidates   int      `json:"candidates" bson:"candidates"`
	Runtime      string   `json:"runtime,omitempty" bson:"runtime,omitempty"`
	// Loaded is the skill version the agent then loaded (the pick or another candidate); Outcome is how
	// its run ended (a skill.usage status). Both stay empty until the router reports them.
	Loaded  string `json:"loaded,omitempty" bson:"loaded,omitempty"`
	Outcome string `json:"outcome,omitempty" bson:"outcome,omitempty"`
}

func newSkillRoute(out RouteResult, candidates []SkillCandidate, runtime string) SkillRoute {
	sr := SkillRoute{Selected: out.SelectedID, Confirmed: out.SelectedID != "" && out.Provider != "rules", Provider: out.Provider,
		Degraded: out.Degraded, Candidates: len(candidates), Runtime: runtime}
	if sr.Selected == "" { // jev_uncertain withholds the pick: keep the judge's best guess, unconfirmed
		best := -1.0
		for _, j := range out.Judgments {
			if v := j.Suitability * j.Confidence; v > best {
				best, sr.Selected = v, j.ID
			}
		}
	}
	for _, c := range candidates {
		if c.ID == sr.Selected {
			sr.SelectedName = c.Name
		}
	}
	return sr
}

// recordSkillRoute keeps the decision for ROUTER and returns its id; a failed write never fails the
// route, it only returns no id.
func (s *Service) recordSkillRoute(ctx context.Context, scope Scope, actor Decision, sr SkillRoute) string {
	now := time.Now().UTC().Truncate(time.Millisecond)
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jevTimeout)
	defer cancel()
	id := ID()
	if s.Repo.Insert(recordCtx, colSkillRoutes, Record{ID: id, Scope: scope, Kind: colSkillRoutes, Status: statusActive, Revision: 1,
		Text: sr.Selected, SkillRoute: &sr, CreatedAt: now, UpdatedAt: now, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role}) != nil {
		return ""
	}
	return id
}

// SkillRouteUseInput reports what the agent did after a route: event "loaded" with the loaded
// version, then once the run's skill.usage status.
type SkillRouteUseInput struct {
	Scope     Scope  `json:"scope"`
	Event     string `json:"event"`
	VersionID string `json:"version_id"`
}

// MarkSkillRoute records load and outcome on a decision. Only the principal that asked for the route
// may report on it; an outcome is final, and it must follow a load of the same version.
func (s *Service) MarkSkillRoute(ctx context.Context, token, id string, in SkillRouteUseInput) (SkillRouteItem, error) {
	if !validScope(in.Scope) || id == "" || in.VersionID == "" || (in.Event != "loaded" && !slices.Contains(skillUsageStatuses, in.Event)) {
		return SkillRouteItem{}, ErrInvalid
	}
	d, err := s.authorize(ctx, token, "search:skill_versions", in.Scope, id, "")
	if err != nil {
		return SkillRouteItem{}, err
	}
	r, err := s.Repo.Get(ctx, colSkillRoutes, in.Scope, id)
	if err != nil || r.Kind != colSkillRoutes || r.SkillRoute == nil {
		return SkillRouteItem{}, ErrNotFound
	}
	if r.CreatedBy != d.PrincipalID {
		return SkillRouteItem{}, ErrDenied
	}
	if _, err := s.Repo.Get(ctx, colSkillVersions, in.Scope, in.VersionID); err != nil {
		return SkillRouteItem{}, ErrInvalid
	}
	sr := *r.SkillRoute
	switch {
	case sr.Outcome != "":
		return SkillRouteItem{}, ErrConflict
	case in.Event == "loaded":
		sr.Loaded = in.VersionID
	case sr.Loaded != in.VersionID:
		return SkillRouteItem{}, ErrConflict
	default:
		sr.Outcome = in.Event
	}
	r.SkillRoute, r.UpdatedAt = &sr, time.Now().UTC().Truncate(time.Millisecond)
	if err := s.Repo.Replace(ctx, colSkillRoutes, r, r.Revision); err != nil {
		return SkillRouteItem{}, err
	}
	return SkillRouteItem{ID: r.ID, At: r.CreatedAt, SkillRoute: sr}, nil
}

type SkillRouteItem struct {
	ID string    `json:"id"`
	At time.Time `json:"at"`
	SkillRoute
}

// SkillRoutes lists the newest skill decisions for ROUTER, under the same read as model routing.
func (s *Service) SkillRoutes(ctx context.Context, token string, scope Scope) ([]SkillRouteItem, error) {
	if !validScope(scope) {
		return nil, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colRouting, scope, routingID(scope), ""); err != nil {
		return nil, err
	}
	rows, err := s.Repo.ListRecords(ctx, scope, colSkillRoutes, skillRoutesRead)
	if err != nil {
		return nil, err
	}
	out := make([]SkillRouteItem, 0, len(rows))
	for _, r := range rows {
		if r.SkillRoute != nil {
			out = append(out, SkillRouteItem{ID: r.ID, At: r.CreatedAt, SkillRoute: *r.SkillRoute})
		}
	}
	return out, nil
}

func skillRouteLogRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/routing/skills", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.SkillRoutes(r.Context(), bearer(r), scopeQuery(r))
		respond(w, map[string]any{"items": out}, e)
	})
	mux.HandleFunc("POST /v1/routing/skills/{id}/use", func(w http.ResponseWriter, r *http.Request) {
		var in SkillRouteUseInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.MarkSkillRoute(r.Context(), bearer(r), r.PathValue("id"), in)
		respond(w, out, e)
	})
}
