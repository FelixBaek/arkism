package memory

import (
	"context"
	"net/http"
)

// ST-819: per memory, compare the pass rate of sessions that used it with the
// sessions that did not. A side with fewer than memoryOutcomeMinSessions
// sessions returns "insufficient_data" instead of numbers. Computed from
// memory_outcome evaluations and context_usage receipts; nothing is stored.
// It is a correlation over existing verdicts, not a causal effect.

const memoryOutcomeMinSessions = 10

// MemoryOutcomeSessionCount is the repository aggregate for one side.
type MemoryOutcomeSessionCount struct {
	Used     bool
	Sessions int
	Passed   int
}

type MemoryOutcomeGroup struct {
	Sessions int      `json:"sessions"`
	Status   string   `json:"status"`
	Passed   *int     `json:"passed"`
	PassRate *float64 `json:"pass_rate"`
}
type MemoryOutcomeComparison struct {
	Memory      Ref                `json:"memory"`
	Dimension   string             `json:"dimension"`
	MinSessions int                `json:"min_sessions"`
	Used        MemoryOutcomeGroup `json:"used"`
	NotUsed     MemoryOutcomeGroup `json:"not_used"`
	// Difference is used − not_used pass rate, only when both sides have enough sessions.
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

func memoryOutcomeComparisonRoutes(mux *http.ServeMux, s *Service) {
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
	sides := map[bool]MemoryOutcomeSessionCount{}
	for _, c := range counts {
		if _, dup := sides[c.Used]; dup || c.Sessions < 1 || c.Passed < 0 || c.Passed > c.Sessions {
			return MemoryOutcomeComparison{}, ErrUnavailable
		}
		sides[c.Used] = c
	}
	out := MemoryOutcomeComparison{Memory: Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}, Dimension: in.Dimension, MinSessions: memoryOutcomeMinSessions, Used: memoryOutcomeGroup(sides[true]), NotUsed: memoryOutcomeGroup(sides[false]), Authority: AuthorityDataOnly}
	if out.Used.PassRate != nil && out.NotUsed.PassRate != nil {
		d := *out.Used.PassRate - *out.NotUsed.PassRate
		out.Difference = &d
	}
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, in.Scope, who.PrincipalID, "read:memory_outcome_comparison", id)
	}); e != nil {
		return MemoryOutcomeComparison{}, e
	}
	return out, nil
}
