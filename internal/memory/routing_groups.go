package memory

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// groups belong to the scope's routing revision.
type RoutingGroup struct {
	ID         string   `json:"id" bson:"id"`
	Name       string   `json:"name" bson:"name"`
	Enabled    bool     `json:"enabled" bson:"enabled"`
	Strategy   string   `json:"strategy" bson:"strategy"`
	Members    []string `json:"members" bson:"members"`
	Discovered bool     `json:"discovered,omitempty" bson:"-"`
}

type RoutingGroupsPage struct {
	Items        []RoutingGroup `json:"items"`
	Revision     int64          `json:"revision"`
	AutoDiscover bool           `json:"auto_discover"`
	Authority    string         `json:"authority"`
}

type RoutingGroupsInput struct {
	Scope            Scope          `json:"scope"`
	ExpectedRevision int64          `json:"expected_revision"`
	Groups           []RoutingGroup `json:"groups"`
	AutoDiscover     bool           `json:"auto_discover"`
}

var routingGroupIDRe = regexp.MustCompile(`^group/[a-z0-9-]{1,64}$`)

// AutoGroupID is the built-in group of every model turned on in 모델 설정. Agents connected to the
// gateway use it, so routing works before anyone makes a group; a saved group with this id replaces it.
// It is not listed with the groups: DecideRoute and the gateway know it.
const AutoGroupID = "group/auto"

func cloneRoutingGroups(groups []RoutingGroup) []RoutingGroup {
	out := make([]RoutingGroup, len(groups))
	for i, g := range groups {
		g.Members = slices.Clone(g.Members)
		out[i] = g
	}
	return out
}

func routingGroupsView(p *Providers, doc Routing, revision int64) RoutingGroupsPage {
	out := RoutingGroupsPage{Items: cloneRoutingGroups(doc.Groups), Revision: revision,
		AutoDiscover: doc.AutoDiscoverGroups, Authority: AuthorityDataOnly}
	ids := make(map[string]bool, len(doc.Groups))
	for i := range out.Items {
		out.Items[i].Discovered = false
		ids[out.Items[i].ID] = true
	}
	if !doc.AutoDiscoverGroups {
		return out
	}
	byModel := map[string][]string{}
	for _, m := range routingModels(p, doc, revision) {
		if m.Enabled && !slices.Contains(byModel[m.Model], m.ID) {
			byModel[m.Model] = append(byModel[m.Model], m.ID)
		}
	}
	names := make([]string, 0, len(byModel))
	for name := range byModel {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		members := byModel[name]
		if len(members) < 2 {
			continue
		}
		id := "group/auto-" + ContentHash(name)[:32]
		if ids[id] {
			continue
		}
		slices.Sort(members)
		out.Items = append(out.Items, RoutingGroup{ID: id, Name: name, Enabled: true,
			Strategy: "smart", Members: members, Discovered: true})
	}
	return out
}

func (s *Service) RoutingGroups(ctx context.Context, token string, scope Scope) (RoutingGroupsPage, error) {
	if !validScope(scope) {
		return RoutingGroupsPage{}, ErrInvalid
	}
	if _, err := s.authorize(ctx, token, "read:"+colRouting, scope, routingID(scope), ""); err != nil {
		return RoutingGroupsPage{}, err
	}
	doc, rev, err := s.routingDoc(ctx, scope)
	if err != nil {
		return RoutingGroupsPage{}, err
	}
	p, err := s.providersDoc(ctx, scope)
	if err != nil {
		return RoutingGroupsPage{}, err
	}
	return routingGroupsView(p, doc, rev), nil
}

func (s *Service) SetRoutingGroups(ctx context.Context, token string, in RoutingGroupsInput) (RoutingGroupsPage, error) {
	if !validScope(in.Scope) || in.ExpectedRevision < 0 || in.Groups == nil || len(in.Groups) > 64 {
		return RoutingGroupsPage{}, ErrInvalid
	}
	ids := map[string]bool{}
	for _, g := range in.Groups {
		if !routingGroupIDRe.MatchString(g.ID) || ids[g.ID] || !utf8.ValidString(g.Name) ||
			strings.TrimSpace(g.Name) == "" || utf8.RuneCountInString(g.Name) > 100 || containsSecret(g.Name) ||
			g.Discovered || (g.Strategy != "smart" && g.Strategy != "ordered") || len(g.Members) < 1 || len(g.Members) > 40 {
			return RoutingGroupsPage{}, ErrInvalid
		}
		ids[g.ID] = true
		members := map[string]bool{}
		for _, id := range g.Members {
			if strings.HasPrefix(id, "group/") || !validRoutingModelID(id) || containsSecret(id) || members[id] {
				return RoutingGroupsPage{}, ErrInvalid
			}
			members[id] = true
		}
	}
	var out RoutingGroupsPage
	r, err := s.updateRouting(ctx, token, in.Scope, in.ExpectedRevision, func(doc *Routing, now time.Time, tx Tx) error {
		providers, err := tx.Get(ctx, colProviders, in.Scope, providersID(in.Scope))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		active := map[string]bool{}
		for _, m := range routingModels(providers.Providers, *doc, 0) {
			active[m.ID] = m.Enabled
		}
		for _, g := range in.Groups {
			for _, id := range g.Members {
				if !active[id] {
					return ErrNotFound
				}
			}
		}
		doc.Groups, doc.AutoDiscoverGroups = cloneRoutingGroups(in.Groups), in.AutoDiscover
		doc.Events = append(doc.Events, RoutingEvent{At: now, Kind: "policy", Title: "라우팅 그룹 변경"})
		out = routingGroupsView(providers.Providers, *doc, 0)
		return nil
	})
	if err != nil {
		return RoutingGroupsPage{}, err
	}
	out.Revision = r.Revision
	return out, nil
}

// Preserve member order before the existing provider/exclude/quota filters.
func routingGroupModels(models []routingModel, group RoutingGroup) []routingModel {
	out := make([]routingModel, 0, len(group.Members))
	for _, id := range group.Members {
		i := slices.IndexFunc(models, func(m routingModel) bool { return m.ID == id })
		if i >= 0 {
			out = append(out, models[i])
		}
	}
	return out
}

func routingGroupRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/routing/groups", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.RoutingGroups(r.Context(), bearer(r), scopeQuery(r))
		respond(w, out, err)
	})
	mux.HandleFunc("POST /v1/routing/groups", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Scope            Scope  `json:"scope"`
			ExpectedRevision *int64 `json:"expected_revision"`
			Groups           []struct {
				RoutingGroup
				Enabled *bool `json:"enabled"`
			} `json:"groups"`
			AutoDiscover *bool `json:"auto_discover"`
		}
		if !decode(w, r, &in) {
			return
		}
		if in.ExpectedRevision == nil || in.AutoDiscover == nil || in.Groups == nil {
			respond(w, nil, ErrInvalid)
			return
		}
		groups := make([]RoutingGroup, len(in.Groups))
		for i, g := range in.Groups {
			if g.Enabled == nil {
				respond(w, nil, ErrInvalid)
				return
			}
			groups[i] = g.RoutingGroup
			groups[i].Enabled = *g.Enabled
		}
		out, err := s.SetRoutingGroups(r.Context(), bearer(r), RoutingGroupsInput{
			Scope: in.Scope, ExpectedRevision: *in.ExpectedRevision, Groups: groups, AutoDiscover: *in.AutoDiscover})
		respond(w, out, err)
	})
}
