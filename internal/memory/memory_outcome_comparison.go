package memory

import (
	"context"
	"net/http"
)

// ST-819: describe independently judged sessions by observed context delivery.
// A search response is not delivery, delivery is not actual use, and missing
// evidence is not non-use. No used-versus-unused effect can be inferred here.

const memoryOutcomeMinSessions = 10

// MemoryOutcomeSessionCount is the repository aggregate for one side.
type MemoryOutcomeSessionCount struct {
	Used        bool
	Sessions    int
	Passed      int
	Observation string
}

const (
	observationDelivered = "delivered"
	observationReturned  = "returned_only"
	observationUnknown   = "unknown"
)

type MemoryOutcomeGroup struct {
	Sessions int      `json:"sessions"`
	Status   string   `json:"status"`
	Passed   *int     `json:"passed"`
	PassRate *float64 `json:"pass_rate"`
}
type MemoryOutcomeComparison struct {
	Memory           Ref                `json:"memory"`
	Dimension        string             `json:"dimension"`
	MinSessions      int                `json:"min_sessions"`
	Used             MemoryOutcomeGroup `json:"used"`
	NotUsed          MemoryOutcomeGroup `json:"not_used"`
	Delivered        MemoryOutcomeGroup `json:"delivered"`
	ReturnedOnly     MemoryOutcomeGroup `json:"returned_only"`
	Unknown          MemoryOutcomeGroup `json:"unknown"`
	MeasurementBasis string             `json:"measurement_basis"`
	ActualUseStatus  string             `json:"actual_use_status"`
	SessionUnit      string             `json:"session_unit"`
	// Legacy fields remain decodable. Used aliases Delivered; NotUsed has no
	// observations, and Difference is null without measured non-use evidence.
	Difference *float64 `json:"difference"`
	Authority  string   `json:"authority"`
}

func memoryOutcomeGroup(c MemoryOutcomeSessionCount) MemoryOutcomeGroup {
	g := MemoryOutcomeGroup{Sessions: c.Sessions, Status: "insufficient_data"}
	if c.Sessions >= memoryOutcomeMinSessions {
		passed, rate := c.Passed, float64(c.Passed)/float64(c.Sessions)
		g.Status, g.Passed, g.PassRate = "ok", &passed, &rate
	}
	return g
}

func memoryOutcomeComparisonRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/memories/{id}/outcome-comparison", func(w http.ResponseWriter, r *http.Request) {
		in, e := parseMemoryOutcomeQuery(r.URL.RawQuery, r.PathValue("id"))
		if e != nil {
			respond(w, nil, e)
			return
		}
		out, e := s.CompareMemoryOutcomes(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}

// CompareMemoryOutcomes needs one memory id and a dimension: mixing personal
// compliance with work results would make one pass rate mean two things.
func (s *Service) CompareMemoryOutcomes(ctx context.Context, token string, in MemoryOutcomeQuery) (MemoryOutcomeComparison, error) {
	in, _, e := normalizeMemoryOutcomeQuery(in, false)
	if e != nil || len(in.MemoryIDs) != 1 || in.Dimension == "" {
		return MemoryOutcomeComparison{}, ErrInvalid
	}
	who, e := s.outcomeReader(ctx, token, in.Scope)
	if e != nil {
		return MemoryOutcomeComparison{}, e
	}
	usage, e := s.authorize(ctx, token, "read:usage", in.Scope, "", "")
	if e != nil {
		return MemoryOutcomeComparison{}, e
	}
	if usage.PrincipalID != who.PrincipalID || usage.Role != who.Role {
		return MemoryOutcomeComparison{}, ErrDenied
	}
	id := in.MemoryIDs[0]
	m, e := s.readableOutcomeMemory(ctx, token, who, in.Scope, id)
	if e != nil {
		return MemoryOutcomeComparison{}, e
	}
	counts, e := s.Repo.MemoryOutcomeSessionCounts(ctx, in)
	if e != nil {
		return MemoryOutcomeComparison{}, e
	}
	sides := map[string]MemoryOutcomeSessionCount{}
	for _, c := range counts {
		if _, dup := sides[c.Observation]; dup || c.Sessions < 1 || c.Passed < 0 || c.Passed > c.Sessions {
			return MemoryOutcomeComparison{}, ErrUnavailable
		}
		if c.Observation != observationDelivered && c.Observation != observationReturned && c.Observation != observationUnknown {
			return MemoryOutcomeComparison{}, ErrUnavailable
		}
		sides[c.Observation] = c
	}
	out := MemoryOutcomeComparison{Memory: Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}, Dimension: in.Dimension, MinSessions: memoryOutcomeMinSessions,
		Delivered: memoryOutcomeGroup(sides[observationDelivered]), ReturnedOnly: memoryOutcomeGroup(sides[observationReturned]), Unknown: memoryOutcomeGroup(sides[observationUnknown]),
		NotUsed: memoryOutcomeGroup(MemoryOutcomeSessionCount{}), MeasurementBasis: "context_delivery", ActualUseStatus: "not_measured", SessionUnit: "agent_session_or_legacy_trace", Authority: AuthorityDataOnly}
	out.Used = out.Delivered
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, in.Scope, who.PrincipalID, "read:memory_outcome_comparison", id)
	}); e != nil {
		return MemoryOutcomeComparison{}, e
	}
	return out, nil
}
