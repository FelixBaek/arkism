package memory

import (
	"context"
	"net/http"
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

// recordSkillRoute keeps the decision for ROUTER; a failed write never fails the route.
func (s *Service) recordSkillRoute(ctx context.Context, scope Scope, actor Decision, sr SkillRoute) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	_ = s.Repo.Insert(context.WithoutCancel(ctx), colSkillRoutes, Record{ID: ID(), Scope: scope, Kind: colSkillRoutes, Status: statusActive, Revision: 1,
		Text: sr.Selected, SkillRoute: &sr, CreatedAt: now, UpdatedAt: now, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role})
}

type SkillRouteItem struct {
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
			out = append(out, SkillRouteItem{At: r.CreatedAt, SkillRoute: *r.SkillRoute})
		}
	}
	return out, nil
}

func skillRouteLogRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/routing/skills", func(w http.ResponseWriter, r *http.Request) {
		out, e := s.SkillRoutes(r.Context(), bearer(r), scopeQuery(r))
		respond(w, map[string]any{"items": out}, e)
	})
}
