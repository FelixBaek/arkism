package memory

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"
)

// ST-813: per-memory trust signals (pass rate, sample size, return count, last
// server return), recomputed daily from memory_outcome evaluations and context_usage
// receipts. Signals are derived rows in the jobs collection (kind
// memory_signal) that a recompute can rebuild at any time; they are inputs for
// ranking and proposals only and never change a memory's state. They carry ids,
// counts and times — never memory text, queries or secrets.

const (
	memorySignalKind  = "memory_signal"
	memorySignalBatch = 100
)

// MemoryUseCount is the repository aggregate of context_usage receipts for one memory id.
type MemoryUseCount struct {
	MemoryID string
	Uses     int
	Last     time.Time
	// Projects are the distinct (memory revision, non-empty project key) pairs of those receipts (ST-825).
	Projects []MemoryProjectUse
}

type MemoryProjectUse struct {
	Revision   int64  `json:"revision"`
	ProjectKey string `json:"project_key"`
}

type MemorySignalCounts struct {
	Samples int `json:"samples" bson:"samples"`
	Passed  int `json:"passed" bson:"passed"`
}

// MemorySignal is the stored derived state; ComputedAt moves only when a value changes.
type MemorySignal struct {
	Memory             Ref                `json:"memory" bson:"memory"`
	Samples            int                `json:"samples" bson:"samples"`
	Passed             int                `json:"passed" bson:"passed"`
	PassRate           *float64           `json:"pass_rate" bson:"pass_rate,omitempty"`
	LastJudgedAt       *time.Time         `json:"last_judged_at" bson:"last_judged_at,omitempty"`
	PersonalCompliance MemorySignalCounts `json:"personal_compliance" bson:"personal_compliance"`
	WorkTask           MemorySignalCounts `json:"work_task" bson:"work_task"`
	Uses               int                `json:"uses" bson:"uses"`
	LastUsedAt         *time.Time         `json:"last_used_at" bson:"last_used_at,omitempty"`
	ComputedAt         time.Time          `json:"computed_at" bson:"computed_at"`
}

type MemorySignalView struct {
	UseMeasurementBasis string        `json:"use_measurement_basis"`
	ActualUseStatus     string        `json:"actual_use_status"`
	MemoryID            string        `json:"memory_id"`
	Available           bool          `json:"available"`
	Signal              *MemorySignal `json:"signal"`
}
type MemorySignalPage struct {
	Items     []MemorySignalView `json:"items"`
	Authority string             `json:"authority"`
}

type MemorySignalQuery struct {
	Scope     Scope    `json:"scope"`
	MemoryIDs []string `json:"memory_ids"`
}

func (v *MemorySignalQuery) UnmarshalJSON(data []byte) error {
	type plain MemorySignalQuery
	return decodeComparison(data, (*plain)(v), []string{"scope", "memory_ids"})
}

type MemorySignalRecomputeInput struct {
	Scope  Scope  `json:"scope"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

func (v *MemorySignalRecomputeInput) UnmarshalJSON(data []byte) error {
	type plain MemorySignalRecomputeInput
	return decodeComparison(data, (*plain)(v), []string{"scope"})
}

type MemorySignalRecomputeOutput struct {
	Computed   int    `json:"computed"`
	Changed    int    `json:"changed"`
	NextCursor string `json:"next_cursor"`
}

func memorySignalID(scope Scope, memoryID string) string {
	return "memory-signal-" + ContentHash(structuredText(scope)+"/"+memoryID)
}

// deleteMemorySignal drops the derived signal in the memory's erasure
// transaction, so no orphan keeps a deleted id or its counts.
func deleteMemorySignal(ctx context.Context, tx Tx, scope Scope, memoryID string) error {
	r, e := tx.Get(ctx, colJobs, scope, memorySignalID(scope, memoryID))
	if errors.Is(e, ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	if r.Kind != memorySignalKind {
		return ErrConflict
	}
	return tx.Delete(ctx, colJobs, scope, r.ID, r.Revision)
}

// buildMemorySignal folds the two per-dimension outcome aggregates and the use aggregate.
func buildMemorySignal(m Record, pc, wt MemoryOutcomeCount, use MemoryUseCount) MemorySignal {
	sig := MemorySignal{Memory: Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision},
		PersonalCompliance: MemorySignalCounts{pc.Total, pc.Passed}, WorkTask: MemorySignalCounts{wt.Total, wt.Passed},
		Samples: pc.Total + wt.Total, Passed: pc.Passed + wt.Passed, Uses: use.Uses}
	if sig.Samples > 0 {
		rate, last := float64(sig.Passed)/float64(sig.Samples), pc.Last
		if wt.Last.After(last) {
			last = wt.Last
		}
		last = last.UTC()
		sig.PassRate, sig.LastJudgedAt = &rate, &last
	}
	if use.Uses > 0 {
		last := use.Last.UTC()
		sig.LastUsedAt = &last
	}
	return sig
}

func sameMemorySignal(a, b MemorySignal) bool {
	a.ComputedAt, b.ComputedAt = time.Time{}, time.Time{}
	return structuredText(a) == structuredText(b)
}

func (s *Service) signalCounts(ctx context.Context, scope Scope, ids []string) (map[string]MemoryOutcomeCount, map[string]MemoryOutcomeCount, map[string]MemoryUseCount, error) {
	byDim := []map[string]MemoryOutcomeCount{{}, {}}
	for i, dim := range []string{"personal_compliance", "work_task"} {
		counts, e := s.Repo.MemoryOutcomeCounts(ctx, MemoryOutcomeQuery{Scope: scope, MemoryIDs: ids, Dimension: dim})
		if e != nil {
			return nil, nil, nil, e
		}
		for _, c := range counts {
			if _, dup := byDim[i][c.MemoryID]; dup || !slices.Contains(ids, c.MemoryID) || c.Total < 1 || c.Passed < 0 || c.Passed > c.Total || c.Last.IsZero() {
				return nil, nil, nil, ErrUnavailable
			}
			byDim[i][c.MemoryID] = c
		}
	}
	uses, e := s.Repo.MemoryUseCounts(ctx, scope, ids)
	if e != nil {
		return nil, nil, nil, e
	}
	byUse := map[string]MemoryUseCount{}
	for _, u := range uses {
		if _, dup := byUse[u.MemoryID]; dup || !slices.Contains(ids, u.MemoryID) || u.Uses < 1 || u.Last.IsZero() {
			return nil, nil, nil, ErrUnavailable
		}
		byUse[u.MemoryID] = u
	}
	return byDim[0], byDim[1], byUse, nil
}

// signalComputer binds write:memory_signals to the principal that may read every input.
func (s *Service) signalComputer(ctx context.Context, token string, scope Scope) (Decision, error) {
	if s.Repo == nil {
		return Decision{}, ErrUnavailable
	}
	who, e := s.authorize(ctx, token, "write:memory_signals", scope, "", "")
	if e != nil {
		return Decision{}, e
	}
	if who.Role != roleHuman && who.Role != roleService && who.Role != rolePipeline {
		return Decision{}, ErrDenied
	}
	for _, action := range []string{"read:memories", "read:evaluations", "read:usage"} {
		d, e := s.authorize(ctx, token, action, scope, "", "")
		if e != nil {
			return Decision{}, e
		}
		if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
			return Decision{}, ErrDenied
		}
	}
	return who, nil
}

// RecomputeMemorySignals rebuilds signals for one page of active memories (id
// order). Unchanged signals are not rewritten, so a repeat run is a no-op.
func (s *Service) RecomputeMemorySignals(ctx context.Context, token string, in MemorySignalRecomputeInput) (MemorySignalRecomputeOutput, error) {
	if in.Limit == 0 {
		in.Limit = memorySignalBatch
	}
	list, _, e := normalizeMemoryList(MemoryListInput{Scope: in.Scope, Status: "active", Limit: in.Limit, Cursor: in.Cursor})
	if e != nil || in.Limit > memorySignalBatch {
		return MemorySignalRecomputeOutput{}, ErrInvalid
	}
	who, e := s.signalComputer(ctx, token, in.Scope)
	if e != nil {
		return MemorySignalRecomputeOutput{}, e
	}
	rows, e := s.Repo.ListMemories(ctx, list)
	if e != nil {
		return MemorySignalRecomputeOutput{}, e
	}
	if len(rows) > list.Limit+1 {
		return MemorySignalRecomputeOutput{}, ErrUnavailable
	}
	out := MemorySignalRecomputeOutput{}
	if len(rows) > list.Limit {
		rows = rows[:list.Limit]
		out.NextCursor = memoryListCursor(list, rows[len(rows)-1].ID)
	}
	ids := make([]string, 0, len(rows))
	for i, m := range rows {
		if !memoryListMatches(m, list) || m.Revision < 1 || i > 0 && m.ID <= rows[i-1].ID {
			return MemorySignalRecomputeOutput{}, ErrUnavailable
		}
		ids = append(ids, m.ID)
	}
	out.Computed = len(ids)
	if len(ids) == 0 {
		return out, nil
	}
	pc, wt, uses, e := s.signalCounts(ctx, in.Scope, ids)
	if e != nil {
		return MemorySignalRecomputeOutput{}, e
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		out.Changed = 0
		for _, m := range rows {
			sig := buildMemorySignal(m, pc[m.ID], wt[m.ID], uses[m.ID])
			sig.ComputedAt = now
			changed, e := writeMemorySignal(ctx, tx, in.Scope, sig, who, now)
			if e != nil {
				return e
			}
			if changed {
				out.Changed++
			}
		}
		return audit(ctx, tx, in.Scope, who.PrincipalID, "write:memory_signals", "")
	})
	if e != nil {
		return MemorySignalRecomputeOutput{}, e
	}
	return out, nil
}

// writeMemorySignal stores sig unless the stored values already match; it
// reports whether it wrote.
func writeMemorySignal(ctx context.Context, tx Tx, scope Scope, sig MemorySignal, who Decision, now time.Time) (bool, error) {
	id := memorySignalID(scope, sig.Memory.ID)
	prev, e := tx.Get(ctx, colJobs, scope, id)
	found := e == nil
	if e != nil && !errors.Is(e, ErrNotFound) {
		return false, e
	}
	if found && (prev.Kind != memorySignalKind || prev.MemorySignal == nil) {
		return false, ErrConflict
	}
	if found && sameMemorySignal(*prev.MemorySignal, sig) {
		return false, nil
	}
	// Lock the memory row before writing: a concurrent delete either lands
	// first (conflict) or waits and then drops this signal with the memory.
	if e = tx.FenceRelationMember(ctx, scope, colMemories, sig.Memory.ID); e != nil {
		return false, e
	}
	if !found {
		return true, tx.Insert(ctx, colJobs, Record{ID: id, Scope: scope, Kind: memorySignalKind, Status: JobStatusDone, Revision: 1, Target: &sig.Memory, MemorySignal: &sig, CreatedBy: who.PrincipalID, CreatedByType: who.Role, CreatedAt: now, UpdatedAt: now})
	}
	rev := prev.Revision
	prev.Revision, prev.MemorySignal, prev.Target, prev.UpdatedAt = rev+1, &sig, &sig.Memory, now
	return true, tx.Replace(ctx, colJobs, prev, rev)
}

// MemorySignals reads stored signals for up to 100 ids. An unreadable, erased
// or missing memory is available=false; a memory not computed yet has signal=null.
func (s *Service) MemorySignals(ctx context.Context, token string, in MemorySignalQuery) (MemorySignalPage, error) {
	// Same id-list rules as ST-812 summaries (1..100, unique, scoped).
	if _, _, e := normalizeMemoryOutcomeQuery(MemoryOutcomeQuery{Scope: in.Scope, MemoryIDs: in.MemoryIDs}, false); e != nil {
		return MemorySignalPage{}, e
	}
	who, e := s.outcomeReader(ctx, token, in.Scope)
	if e != nil {
		return MemorySignalPage{}, e
	}
	if d, e := s.authorize(ctx, token, "read:usage", in.Scope, "", ""); e != nil {
		return MemorySignalPage{}, e
	} else if d.PrincipalID != who.PrincipalID || d.Role != who.Role {
		return MemorySignalPage{}, ErrDenied
	}
	out := MemorySignalPage{Items: make([]MemorySignalView, 0, len(in.MemoryIDs)), Authority: AuthorityDataOnly}
	for _, id := range in.MemoryIDs {
		view := MemorySignalView{MemoryID: id, UseMeasurementBasis: "server_return", ActualUseStatus: "not_measured"}
		_, e := s.readableOutcomeMemory(ctx, token, who, in.Scope, id)
		if e != nil && !errors.Is(e, ErrNotFound) && !errors.Is(e, ErrDenied) {
			return MemorySignalPage{}, e
		}
		if view.Available = e == nil; view.Available {
			r, e := s.Repo.Get(ctx, colJobs, in.Scope, memorySignalID(in.Scope, id))
			switch {
			case errors.Is(e, ErrNotFound):
			case e != nil:
				return MemorySignalPage{}, e
			case r.Scope != in.Scope || r.Kind != memorySignalKind || r.MemorySignal == nil || r.MemorySignal.Memory.ID != id:
				return MemorySignalPage{}, ErrUnavailable
			default:
				view.Signal = r.MemorySignal
			}
		}
		out.Items = append(out.Items, view)
	}
	if e = s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		return audit(ctx, tx, in.Scope, who.PrincipalID, "read:memory_signals", "")
	}); e != nil {
		return MemorySignalPage{}, e
	}
	return out, nil
}

func memorySignalRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("GET /v1/memories/{id}/signal", func(w http.ResponseWriter, r *http.Request) {
		scope, e := parseSourceTraceQuery(r.URL.RawQuery)
		if e != nil {
			respond(w, nil, e)
			return
		}
		page, e := s.MemorySignals(r.Context(), bearer(r), MemorySignalQuery{Scope: scope, MemoryIDs: []string{r.PathValue("id")}})
		if e != nil {
			respond(w, nil, e)
			return
		}
		respond(w, page.Items[0], nil)
	})
	mux.HandleFunc("POST /v1/memory-signals", func(w http.ResponseWriter, r *http.Request) {
		var in MemorySignalQuery
		if !decode(w, r, &in) {
			return
		}
		out, e := s.MemorySignals(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/memory-signals/recompute", func(w http.ResponseWriter, r *http.Request) {
		var in MemorySignalRecomputeInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RecomputeMemorySignals(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
}
