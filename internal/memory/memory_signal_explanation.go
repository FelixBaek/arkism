package memory

import (
	"context"
	"net/http"
	"time"
)

// MemorySignalExplanation describes the baseline factor at server time. It is
// observation data, not proof that a memory caused success or a model used it.
type MemorySignalExplanation struct {
	Memory               Ref           `json:"memory"`
	Authority            string        `json:"authority"`
	Mode                 string        `json:"mode"`
	AsOf                 time.Time     `json:"as_of"`
	Policy               *SignalPolicy `json:"policy,omitempty"`
	HistoricalTotal      int           `json:"historical_total"`
	WindowEvents         int           `json:"window_events"`
	EligibleTotal        int           `json:"eligible_total"`
	EligiblePassed       int           `json:"eligible_passed"`
	ExcludedWindowEvents int           `json:"excluded_window_events"`
	MinimumMet           bool          `json:"minimum_met"`
	EvaluationFactor     float64       `json:"evaluation_factor"`
	WorkFactor           float64       `json:"work_factor"`
	EffectiveFactor      float64       `json:"effective_factor"`
	// Applicable means the memory itself is active for baseline weighting.
	// It does not assert query eligibility: source visibility, expiry, transfer
	// rules, and relevance are checked independently by Search.
	Applicable       bool       `json:"applicable"`
	SignalComputedAt *time.Time `json:"signal_computed_at,omitempty"`
}

func (s *Service) ExplainMemorySignal(ctx context.Context, token string, scope Scope, id string) (MemorySignalExplanation, error) {
	if _, _, err := normalizeMemoryOutcomeQuery(MemoryOutcomeQuery{Scope: scope, MemoryIDs: []string{id}}, false); err != nil {
		return MemorySignalExplanation{}, err
	}
	who, err := s.outcomeReader(ctx, token, scope)
	if err != nil {
		return MemorySignalExplanation{}, err
	}
	actor, err := s.authorize(ctx, token, "read:usage", scope, "", "")
	if err != nil {
		return MemorySignalExplanation{}, err
	}
	if actor.PrincipalID != who.PrincipalID || actor.Role != who.Role {
		return MemorySignalExplanation{}, ErrDenied
	}
	m, err := s.readableOutcomeMemory(ctx, token, who, scope, id)
	if err != nil {
		return MemorySignalExplanation{}, err
	}
	settings, err := s.spaceSettings(ctx, scope)
	if err != nil {
		return MemorySignalExplanation{}, err
	}
	now := s.recallClock(SearchInput{})
	out := MemorySignalExplanation{Memory: Ref{Collection: colMemories, ID: id, Revision: m.Revision}, Authority: AuthorityDataOnly, Mode: "legacy", AsOf: now, Applicable: live(m) && m.Status == "active", EvaluationFactor: 1, WorkFactor: 1, EffectiveFactor: 1}
	q := MemoryOutcomeQuery{Scope: scope, MemoryIDs: []string{id}}
	all, err := s.memorySignalCount(ctx, q, id)
	if err != nil {
		return MemorySignalExplanation{}, err
	}
	out.HistoricalTotal = all.Total
	records, err := s.visibleSignalRecords(ctx, scope, []string{id})
	if err != nil {
		return MemorySignalExplanation{}, err
	}
	if r, ok := records[id]; ok {
		out.WorkFactor = workFactor(r.MemorySignal.Work)
		out.EvaluationFactor = outcomeFactor(r.MemorySignal)
		at := r.MemorySignal.ComputedAt
		out.SignalComputedAt = &at
	}
	if policy := settings.recallQuality().SignalPolicy; policy != nil && !policy.Disabled {
		if err = s.explainSignalWindow(ctx, &out, policy, q, m, now); err != nil {
			return MemorySignalExplanation{}, err
		}
	}
	out.EffectiveFactor = out.EvaluationFactor * out.WorkFactor
	if s.SignalRankingOff || !out.Applicable {
		out.Mode = "disabled"
		out.EffectiveFactor = 1
	}
	if err = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, scope, who.PrincipalID, "read:memory_signals", id)
	}); err != nil {
		return MemorySignalExplanation{}, err
	}
	return out, nil
}

// memorySignalCount returns the one outcome count row of memory id, or zero.
func (s *Service) memorySignalCount(ctx context.Context, q MemoryOutcomeQuery, id string) (MemoryOutcomeCount, error) {
	rows, e := s.Repo.MemoryOutcomeCounts(ctx, q)
	if e != nil {
		return MemoryOutcomeCount{}, e
	}
	if len(rows) == 0 {
		return MemoryOutcomeCount{}, nil
	}
	if len(rows) != 1 || rows[0].MemoryID != id || rows[0].Total < 0 || rows[0].Passed < 0 || rows[0].Passed > rows[0].Total {
		return MemoryOutcomeCount{}, ErrUnavailable
	}
	return rows[0], nil
}

// explainSignalWindow fills the recent-window part of out under policy.
func (s *Service) explainSignalWindow(ctx context.Context, out *MemorySignalExplanation, policy *SignalPolicy, q MemoryOutcomeQuery, m Record, now time.Time) error {
	id := out.Memory.ID
	out.Mode = "recent_first_trace"
	out.Policy = policy
	from := now.Add(-time.Duration(policy.WindowDays) * 24 * time.Hour)
	q.Dimension = policy.Dimension
	q.From = &from
	q.To = &now
	q.ContentHashes = map[string]string{id: signalContentHash(HitFromRecord(colMemories, m))}
	events, e := s.memorySignalCount(ctx, q, id)
	if e != nil {
		return e
	}
	out.WindowEvents = events.Total
	q.IndependentFirst = true
	eligible, e := s.memorySignalCount(ctx, q, id)
	if e != nil {
		return e
	}
	out.EligibleTotal, out.EligiblePassed = eligible.Total, eligible.Passed
	out.ExcludedWindowEvents = out.WindowEvents - out.EligibleTotal
	if out.ExcludedWindowEvents < 0 {
		return ErrUnavailable
	}
	out.EvaluationFactor = 1
	out.MinimumMet = eligible.Total >= policy.MinSamples
	if out.MinimumMet {
		out.EvaluationFactor = 0.5 + float64(eligible.Passed+1)/float64(eligible.Total+2)
	}
	return nil
}

func memorySignalExplanationRoute(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/memories/{id}/signal-policy", func(w http.ResponseWriter, r *http.Request) {
		scope, err := parseSourceTraceQuery(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		out, err := s.ExplainMemorySignal(r.Context(), bearer(r), scope, r.PathValue("id"))
		respond(w, out, err)
	})
}
