package memory

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"time"
)

// RTE-06 attribution record: one routed step as the runner saw it, kept so an independent result can later be
// tied to the decision and the attempt it judges. The runner writes it only when ARKISM_ROUTING_ATTRIBUTION=1.
// Ids, model names, codes and times only: no prompt, output, memory body, token or key.
const routingAttributionKind = "routing_attribution"

type RoutingAttribution struct {
	DecisionID string                      `json:"decision_id" bson:"decision_id"` // the runner's id for this routed step
	DecidedAt  time.Time                   `json:"decided_at" bson:"decided_at"`
	Decision   string                      `json:"decision" bson:"decision"` // how the first pick was made
	Group      string                      `json:"group,omitempty" bson:"group,omitempty"`
	ViewHash   string                      `json:"view_hash,omitempty" bson:"view_hash,omitempty"`
	Chosen     string                      `json:"chosen" bson:"chosen"`       // the first pick, provider/model
	Fallbacks  []string                    `json:"fallbacks" bson:"fallbacks"` // in order, as the runner capped them
	Attempts   []RoutingAttributionAttempt `json:"attempts" bson:"attempts"`
	Stopped    string                      `json:"stopped,omitempty" bson:"stopped,omitempty"` // effect_unknown | budget_spent
	Arm        string                      `json:"arm,omitempty" bson:"arm,omitempty"`         // pilot arm label (RTE-08)
	Origin     *RoutingOrigin              `json:"origin,omitempty" bson:"origin,omitempty"`
}

type RoutingAttributionAttempt struct {
	Index     int    `json:"index" bson:"index"`
	Model     string `json:"model" bson:"model"`
	OK        bool   `json:"ok" bson:"ok"`
	Error     string `json:"error,omitempty" bson:"error,omitempty"`
	LatencyMS int64  `json:"latency_ms" bson:"latency_ms"`
}

type RoutingAttributionInput struct {
	Scope  Scope              `json:"scope"`
	Record RoutingAttribution `json:"record"`
}

type RoutingAttributionView struct {
	ID        string             `json:"id"`
	Record    RoutingAttribution `json:"record"`
	CreatedAt time.Time          `json:"created_at"`
}

type RoutingAttributionPage struct {
	Items      []RoutingAttributionView `json:"items"`
	NextCursor string                   `json:"next_cursor,omitempty"`
	Authority  string                   `json:"authority"`
}

var (
	attributionIDRe  = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`) // the RTE-01 id pattern
	attributionArmRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	viewHashRe       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

func invalidRoutingAttribution(a RoutingAttribution) bool {
	return !attributionIDRe.MatchString(a.DecisionID) || a.DecidedAt.IsZero() || !storableTime(a.DecidedAt) ||
		!slices.Contains(routingDecisions, a.Decision) || a.Decision == "failover" || len(a.Fallbacks) > 3 ||
		a.Group != "" && !routingGroupIDRe.MatchString(a.Group) || a.ViewHash != "" && !viewHashRe.MatchString(a.ViewHash) ||
		!slices.Contains([]string{"", "effect_unknown", "budget_spent"}, a.Stopped) ||
		a.Arm != "" && !attributionArmRe.MatchString(a.Arm) || !validRoutingOrigin(a.Origin) || invalidAttempts(a)
}

func invalidAttempts(a RoutingAttribution) bool {
	ids := append([]string{a.Chosen}, a.Fallbacks...)
	if len(a.Attempts) == 0 || len(a.Attempts) > len(ids) || slices.ContainsFunc(ids, func(id string) bool { return !validRoutingModelID(id) }) {
		return true
	}
	for i, t := range a.Attempts {
		// attempts run the candidates in order; only the last may succeed
		if t.Index != i || t.Model != ids[i] || !routingErrorRe.MatchString(t.Error) || t.LatencyMS < 0 || t.LatencyMS > 864e5 ||
			t.OK && i != len(a.Attempts)-1 {
			return true
		}
	}
	return false
}

func routingAttributionID(scope Scope, decisionID string) string {
	return "routing-attribution-" + ContentHash(structuredText(scope)+"\n"+decisionID)
}

// RecordRoutingAttribution stores one step record. The same record again is a no-op (the runner may resend after a
// lost reply); another record under the same decision id is a conflict.
func (s *Service) RecordRoutingAttribution(ctx context.Context, token string, in RoutingAttributionInput) (RoutingAttributionView, error) {
	if !validScope(in.Scope) || invalidRoutingAttribution(in.Record) {
		return RoutingAttributionView{}, ErrInvalid
	}
	actor, err := s.authorize(ctx, token, "write:"+colRoutingCalls, in.Scope, "", "")
	if err != nil {
		return RoutingAttributionView{}, err
	}
	in.Record.DecidedAt = in.Record.DecidedAt.UTC().Truncate(time.Millisecond)
	id, hash := routingAttributionID(in.Scope, in.Record.DecisionID), ContentHash(structuredText(in.Record))
	var saved Record
	err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		prior, e := tx.Get(ctx, colJobs, in.Scope, id)
		if e == nil {
			if prior.Kind != routingAttributionKind || prior.RoutingAttribution == nil || prior.RequestHash != hash {
				return ErrConflict
			}
			saved = prior
			return nil
		}
		if !errors.Is(e, ErrNotFound) {
			return e
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		rec := in.Record
		saved = Record{ID: id, Scope: in.Scope, Kind: routingAttributionKind, Status: JobStatusDone, Revision: 1, RoutingAttribution: &rec,
			RequestHash: hash, CreatedBy: actor.PrincipalID, CreatedByType: actor.Role, CreatedAt: now, UpdatedAt: now}
		return tx.Insert(ctx, colJobs, saved)
	})
	if err != nil {
		return RoutingAttributionView{}, err
	}
	return attributionView(saved), nil
}

func attributionView(r Record) RoutingAttributionView {
	return RoutingAttributionView{ID: r.ID, Record: *r.RoutingAttribution, CreatedAt: r.CreatedAt}
}

type routingAttributionLister interface {
	ListRoutingAttributions(context.Context, Scope, string, int) ([]Record, error)
}

func (p *Postgres) ListRoutingAttributions(ctx context.Context, scope Scope, after string, limit int) ([]Record, error) {
	if limit < 1 || limit > 501 {
		return nil, ErrInvalid
	}
	return p.selectRecords(ctx, colJobs, scope, `kind=$6 AND id>$7`, []any{routingAttributionKind, after}, "id ASC", limit)
}

// ListRoutingAttributions is the read-only export the offline analysis (RTE-07) reads, in id order.
func (s *Service) ListRoutingAttributions(ctx context.Context, token string, scope Scope, after string, limit int) (RoutingAttributionPage, error) {
	out := RoutingAttributionPage{Items: []RoutingAttributionView{}, Authority: AuthorityDataOnly}
	if limit == 0 {
		limit = 100
	}
	if !validScope(scope) || limit < 1 || limit > 500 || after != "" && !validRunLabel(after, 128) {
		return out, ErrInvalid
	}
	if _, e := s.authorize(ctx, token, "read:routing", scope, "", ""); e != nil {
		return out, e
	}
	reader, ok := s.Repo.(routingAttributionLister)
	if !ok {
		return out, ErrUnavailable
	}
	rows, e := reader.ListRoutingAttributions(ctx, scope, after, limit+1)
	if e != nil {
		return out, e
	}
	if len(rows) > limit {
		rows = rows[:limit]
		out.NextCursor = rows[len(rows)-1].ID
	}
	for _, r := range rows {
		if r.Kind == routingAttributionKind && r.RoutingAttribution != nil {
			out.Items = append(out.Items, attributionView(r))
		}
	}
	return out, nil
}

func routingAttributionRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/routing/attributions", func(w http.ResponseWriter, r *http.Request) {
		var in RoutingAttributionInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RecordRoutingAttribution(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("GET /v1/routing/attributions", func(w http.ResponseWriter, r *http.Request) {
		limit := 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, e := strconv.Atoi(raw)
			if e != nil {
				respond(w, nil, ErrInvalid)
				return
			}
			limit = n
		}
		out, e := s.ListRoutingAttributions(r.Context(), bearer(r), scopeQuery(r), r.URL.Query().Get("after"), limit)
		respond(w, out, e)
	})
}
