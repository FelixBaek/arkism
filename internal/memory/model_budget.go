package memory

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

const picoDollar int64 = 1_000_000_000_000

// One deployment-owned scope/account, shared by all paid harness roles.
// Purpose and origin agents cannot manufacture separate allowances.
type ModelBudgetPolicy struct {
	Scope                  Scope     `json:"scope"`
	Principals             []string  `json:"principals"`
	MonthlyPicoUSD         int64     `json:"monthly_pico_usd"`
	GeneralPicoUSD         int64     `json:"general_pico_usd"`
	DailyPicoUSD           int64     `json:"daily_pico_usd,omitempty"` // Optional legacy hard ceiling; zero disables.
	DailySoftTargetPicoUSD int64     `json:"daily_soft_target_pico_usd,omitempty"`
	ExpiresAt              time.Time `json:"expires_at"`
}

// Informational pacing only. Monthly/general ceilings remain authoritative.
func (p *ModelBudgetPolicy) dailySoftTarget() int64 {
	if p.DailySoftTargetPicoUSD > 0 {
		return p.DailySoftTargetPicoUSD
	}
	if p.MonthlyPicoUSD < 3*picoDollar {
		return p.MonthlyPicoUSD
	}
	return 3 * picoDollar
}

type ModelBudgetState struct {
	Total          int64  `bson:"total"`
	General        int64  `bson:"general"`
	Reserved       int64  `bson:"reserved"`
	Amount         int64  `bson:"amount"`
	Priority       bool   `bson:"priority"`
	Month          string `bson:"month"`
	Day            string `bson:"day"`
	Model          string `bson:"model"`
	Role           string `bson:"role"`
	MaxInput       int64  `bson:"max_input"`
	MaxOutput      int64  `bson:"max_output"`
	SettlementHash string `bson:"settlement_hash,omitempty"`
}

type ModelBudgetReserveInput struct {
	Scope           Scope  `json:"scope"`
	OperationID     string `json:"operation_id"`
	Model           string `json:"model"`
	Role            string `json:"role"`
	MaxInputTokens  int64  `json:"max_input_tokens"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
	Priority        bool   `json:"priority"`
}
type ModelBudgetReservation struct {
	ReservationID   string `json:"reservation_id"`
	Status          string `json:"status"`
	DispatchAllowed bool   `json:"dispatch_allowed"`
	ReservedPicoUSD int64  `json:"reserved_pico_usd"`
}
type ModelBudgetSettleInput struct {
	Scope                      Scope  `json:"scope"`
	ReservationID              string `json:"reservation_id"`
	Status                     string `json:"status"`
	InputTokens                int64  `json:"input_tokens"`
	OutputTokens               int64  `json:"output_tokens"`
	CacheReadInputTokens       int64  `json:"cache_read_input_tokens"`
	CacheCreationInputTokens   int64  `json:"cache_creation_input_tokens"`
	CacheCreation1hInputTokens int64  `json:"cache_creation_1h_input_tokens"`
}
type ModelBudgetStatus struct {
	// Configured is false when this server has no deployment budget for the scope: a human reader then gets
	// "no budget" (zero limits) plus the owner's 월 예산 for routed requests, instead of a denial.
	Configured              bool   `json:"configured"`
	Month                   string `json:"month"`
	Day                     string `json:"day"`
	MonthlyLimit            int64  `json:"monthly_limit_pico_usd"`
	GeneralLimit            int64  `json:"general_limit_pico_usd"`
	DailyLimit              int64  `json:"daily_limit_pico_usd,omitempty"`
	DailySoftTarget         int64  `json:"daily_soft_target_pico_usd"`
	DailySoftTargetExceeded bool   `json:"daily_soft_target_exceeded"`
	MonthlyCommitted        int64  `json:"monthly_committed_pico_usd"`
	GeneralCommitted        int64  `json:"general_committed_pico_usd"`
	DailyCommitted          int64  `json:"daily_committed_pico_usd"`
	Outstanding             int64  `json:"outstanding_pico_usd"`
	PricingRevision         string `json:"pricing_revision"`
	// Subscription CLI calls this month: recorded only, never part of the limits above.
	SubscriptionRequests     int64 `json:"subscription_requests"`
	SubscriptionInputTokens  int64 `json:"subscription_input_tokens"`
	SubscriptionOutputTokens int64 `json:"subscription_output_tokens"`
	SubscriptionEstimated    int64 `json:"subscription_estimated_pico_usd"`
	// Without a deployment budget only: the 월 예산 set in 라우팅 (0 = none), this UTC month's routed cost and
	// the routing revision a change to it must name (POST /v1/routing/policy monthly_budget_usd).
	RoutingBudgetUSD    float64 `json:"routing_budget_usd,omitempty"`
	RoutingMonthCostUSD float64 `json:"routing_month_cost_usd,omitempty"`
	RoutingRevision     int64   `json:"routing_revision,omitempty"`
}

func ParseModelBudgetPolicy(raw string) (*ModelBudgetPolicy, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var p ModelBudgetPolicy
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, ErrInvalid
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, ErrInvalid
	}
	if !validScope(p.Scope) || len(p.Principals) < 1 || len(p.Principals) > 32 || p.ExpiresAt.IsZero() || p.MonthlyPicoUSD <= 0 || p.MonthlyPicoUSD > 100*picoDollar || p.GeneralPicoUSD <= 0 || p.GeneralPicoUSD > 90*picoDollar || p.GeneralPicoUSD > p.MonthlyPicoUSD || p.DailyPicoUSD < 0 || p.DailyPicoUSD > p.MonthlyPicoUSD || p.DailySoftTargetPicoUSD < 0 || p.DailySoftTargetPicoUSD > p.MonthlyPicoUSD {
		return nil, ErrInvalid
	}
	p.DailySoftTargetPicoUSD = p.dailySoftTarget()
	for _, id := range p.Principals {
		if !validRunLabel(id, 256) {
			return nil, ErrInvalid
		}
	}
	return &p, nil
}

func (s *Service) budgetActor(ctx context.Context, token string, scope Scope, action string) (Decision, error) {
	p := s.ModelBudgetPolicy
	if p == nil || p.Scope != scope || !p.ExpiresAt.After(time.Now()) {
		return Decision{}, ErrDenied
	}
	d, e := s.authorize(ctx, token, action+":model_budget", scope, "", "")
	if e != nil {
		return d, e
	}
	if action == "read" && d.Role == roleHuman {
		return d, nil
	}
	if d.Role != roleService || !slices.Contains(p.Principals, d.PrincipalID) {
		return Decision{}, ErrDenied
	}
	return d, nil
}

// USD/MTok translated into integer pico-USD/token. Prices checked 2026-10-14.
// Inputs are limited to the <=100k tier; worst-case reservation includes 1h cache writes.
func modelBudgetRates(model, role string) (int64, int64, bool) {
	switch model {
	case "claude-haiku-5-5":
		if role == "session_processor" || role == "retrieval_planner" {
			return 100_000, 500_000, true
		}
	case "claude-sonnet-5-5":
		if role == "session_processor" || role == "consolidator" || role == "skill_drafter" || role == "evaluator" {
			return 2_000_000, 10_000_000, true
		}
	case "claude-opus-5-5":
		if role == "conflict_analyst" {
			return 4_000_000, 20_000_000, true
		}
	}
	return 0, 0, false
}
func budgetCounterID(scope Scope, period string) string {
	return "mbc_" + ContentHash(structuredText(scope)+"\n"+period)
}
func budgetCounter(ctx context.Context, tx Tx, scope Scope, period string) (Record, error) {
	r, e := tx.Get(ctx, "jobs", scope, budgetCounterID(scope, period))
	if errors.Is(e, ErrNotFound) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		r = Record{ID: budgetCounterID(scope, period), Scope: scope, Kind: "model_budget_counter", Status: JobStatusDone, Revision: 1, CreatedAt: now, UpdatedAt: now, ModelBudget: &ModelBudgetState{}}
		e = tx.Insert(ctx, "jobs", r)
	}
	if e == nil && (r.Kind != "model_budget_counter" || r.ModelBudget == nil) {
		e = ErrUnavailable
	}
	return r, e
}
func updateBudgetCounter(ctx context.Context, tx Tx, r Record, delta, reservedDelta int64, priority bool) error {
	clone := *r.ModelBudget
	r.ModelBudget = &clone
	clone.Total += delta
	clone.Reserved += reservedDelta
	if !priority {
		clone.General += delta
	}
	if clone.Total < 0 || clone.General < 0 || clone.Reserved < 0 {
		return ErrUnavailable
	}
	rev := r.Revision
	r.Revision++
	r.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
	return tx.Replace(ctx, "jobs", r, rev)
}
func reservationOutput(r Record, dispatch bool) ModelBudgetReservation {
	return ModelBudgetReservation{ReservationID: r.ID, Status: r.Status, DispatchAllowed: dispatch, ReservedPicoUSD: r.ModelBudget.Amount}
}

func (s *Service) ReserveModelBudget(ctx context.Context, token string, in ModelBudgetReserveInput) (ModelBudgetReservation, error) {
	rateIn, rateOut, ok := modelBudgetRates(in.Model, in.Role)
	if !ok || !idempotencyPattern.MatchString(in.OperationID) || in.MaxInputTokens < 1 || in.MaxInputTokens > 100000 || in.MaxOutputTokens < 1 || in.MaxOutputTokens > 24576 {
		return ModelBudgetReservation{}, ErrInvalid
	}
	d, e := s.budgetActor(ctx, token, in.Scope, "reserve")
	if e != nil {
		return ModelBudgetReservation{}, e
	}
	ctx = bindRequestActor(ctx, d)
	if in.Priority {
		if _, e = s.authorize(ctx, token, "priority:model_budget", in.Scope, "", ""); e != nil {
			return ModelBudgetReservation{}, e
		}
	}
	id := "mbr_" + ContentHash(structuredText(in.Scope)+"\n"+d.PrincipalID+"\n"+in.OperationID)
	hash := ContentHash(structuredText(in))
	amount := in.MaxInputTokens*rateIn*2 + in.MaxOutputTokens*rateOut
	var out ModelBudgetReservation
	e = s.modelBudgetTransaction(ctx, func(ctx context.Context, tx Tx) error {
		prior, err := tx.Get(ctx, "jobs", in.Scope, id)
		if err == nil {
			if prior.Kind != "model_budget_reservation" || prior.ModelBudget == nil || prior.RequestHash != hash || prior.CreatedBy != d.PrincipalID {
				return ErrConflict
			}
			out = reservationOutput(prior, false)
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		now := time.Now().UTC().Truncate(time.Millisecond)
		month, day := now.Format("2006-01"), now.Format("2006-01-02")
		monthly, err := budgetCounter(ctx, tx, in.Scope, month)
		if err != nil {
			return err
		}
		daily, err := budgetCounter(ctx, tx, in.Scope, day)
		if err != nil {
			return err
		}
		p := s.ModelBudgetPolicy
		if monthly.ModelBudget.Total+amount > p.MonthlyPicoUSD || p.DailyPicoUSD > 0 && daily.ModelBudget.Total+amount > p.DailyPicoUSD || !in.Priority && monthly.ModelBudget.General+amount > p.GeneralPicoUSD {
			return ErrModelBudget
		}
		if err = updateBudgetCounter(ctx, tx, monthly, amount, amount, in.Priority); err != nil {
			return err
		}
		if err = updateBudgetCounter(ctx, tx, daily, amount, amount, in.Priority); err != nil {
			return err
		}
		row := Record{ID: id, Scope: in.Scope, Kind: "model_budget_reservation", Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: d.PrincipalID, CreatedByType: d.Role, RequestHash: hash, ModelBudget: &ModelBudgetState{Amount: amount, Priority: in.Priority, Month: month, Day: day, Model: in.Model, Role: in.Role, MaxInput: in.MaxInputTokens, MaxOutput: in.MaxOutputTokens}}
		if err = tx.Insert(ctx, "jobs", row); err != nil {
			return err
		}
		out = reservationOutput(row, true)
		return audit(ctx, tx, in.Scope, d.PrincipalID, "reserve:model_budget", id)
	})
	if e != nil {
		return ModelBudgetReservation{}, e
	}
	return out, nil
}

func (s *Service) SettleModelBudget(ctx context.Context, token string, in ModelBudgetSettleInput) (ModelBudgetReservation, error) {
	if !strings.HasPrefix(in.ReservationID, "mbr_") || len(in.ReservationID) != 68 || in.Status != "completed" && in.Status != "unknown" && in.Status != "not_sent" {
		return ModelBudgetReservation{}, ErrInvalid
	}
	values := []int64{in.InputTokens, in.OutputTokens, in.CacheReadInputTokens, in.CacheCreationInputTokens, in.CacheCreation1hInputTokens}
	for _, n := range values {
		if n < 0 || n > 1_000_000 {
			return ModelBudgetReservation{}, ErrInvalid
		}
	}
	if in.Status != "completed" && slices.ContainsFunc(values, func(n int64) bool { return n != 0 }) {
		return ModelBudgetReservation{}, ErrInvalid
	}
	d, e := s.budgetActor(ctx, token, in.Scope, "settle")
	if e != nil {
		return ModelBudgetReservation{}, e
	}
	ctx = bindRequestActor(ctx, d)
	var out ModelBudgetReservation
	e = s.modelBudgetTransaction(ctx, func(ctx context.Context, tx Tx) error {
		row, err := tx.Get(ctx, "jobs", in.Scope, in.ReservationID)
		if err != nil {
			return err
		}
		if row.Kind != "model_budget_reservation" || row.ModelBudget == nil || row.CreatedBy != d.PrincipalID {
			return ErrDenied
		}
		state := *row.ModelBudget
		row.ModelBudget = &state
		hash := ContentHash(structuredText(in))
		if row.Status == JobStatusDone {
			if state.SettlementHash != hash {
				return ErrConflict
			}
			out = reservationOutput(row, false)
			return nil
		}
		if row.Status != JobStatusPending && row.Status != "unknown" {
			return ErrConflict
		}
		if in.Status == "unknown" {
			if row.Status != "unknown" {
				rev := row.Revision
				row.Status = "unknown"
				row.Revision++
				row.UpdatedAt = time.Now().UTC().Truncate(time.Millisecond)
				if err = tx.Replace(ctx, "jobs", row, rev); err != nil {
					return err
				}
			}
			out = reservationOutput(row, false)
			return audit(ctx, tx, in.Scope, d.PrincipalID, "settle:model_budget:unknown", row.ID)
		}
		// An uncertain dispatch cannot later be asserted not-sent to free its allowance.
		if row.Status == "unknown" && in.Status == "not_sent" {
			return ErrConflict
		}
		totalInput, actual, err := modelBudgetActualCost(in, state)
		if err != nil {
			return err
		}
		if err = settleBudgetCounters(ctx, tx, in.Scope, state, actual); err != nil {
			return err
		}
		cost := Record{ID: row.ID, Scope: row.Scope, Kind: providerUsageKind, Status: JobStatusDone, Revision: 1, CreatedAt: row.CreatedAt, UpdatedAt: time.Now().UTC().Truncate(time.Millisecond), CreatedBy: d.PrincipalID, CreatedByType: d.Role, Cost: &ProviderCost{Operation: state.Role, Provider: "anthropic", Model: state.Model, Requests: 1, InputTokens: totalInput, OutputTokens: in.OutputTokens, TotalTokens: totalInput + in.OutputTokens, EstimatedCostPicoUSD: actual, PricingAvailable: true, PricingRevision: "anthropic-2026-10-14"}}
		if in.Status == "not_sent" {
			cost.Cost.Requests = 0
			cost.Status = JobStatusFailed
		}
		if err = tx.Insert(ctx, colProviderUsage, cost); err != nil {
			return err
		}
		state.SettlementHash = hash
		rev := row.Revision
		row.Status = JobStatusDone
		row.Revision++
		row.UpdatedAt = cost.UpdatedAt
		if err = tx.Replace(ctx, "jobs", row, rev); err != nil {
			return err
		}
		out = reservationOutput(row, false)
		return audit(ctx, tx, in.Scope, d.PrincipalID, "settle:model_budget", row.ID)
	})
	if e != nil {
		return ModelBudgetReservation{}, e
	}
	return out, nil
}

// modelBudgetActualCost prices the settled usage, within the reserved limits.
func modelBudgetActualCost(in ModelBudgetSettleInput, state ModelBudgetState) (int64, int64, error) {
	rateIn, rateOut, ok := modelBudgetRates(state.Model, state.Role)
	if !ok {
		return 0, 0, ErrUnavailable
	}
	totalInput := in.InputTokens + in.CacheReadInputTokens + in.CacheCreationInputTokens + in.CacheCreation1hInputTokens
	if totalInput > state.MaxInput || in.OutputTokens > state.MaxOutput {
		return 0, 0, ErrInvalid
	}
	cacheReadRate := rateIn / 20
	if state.Model == "claude-haiku-5-5" {
		cacheReadRate = rateIn / 10
	}
	actual := in.InputTokens*rateIn + in.OutputTokens*rateOut + in.CacheReadInputTokens*cacheReadRate + in.CacheCreationInputTokens*rateIn*5/4 + in.CacheCreation1hInputTokens*rateIn*2
	if actual > state.Amount {
		return 0, 0, ErrInvalid
	}
	return totalInput, actual, nil
}

// settleBudgetCounters moves the reservation into actual spend on both counters.
func settleBudgetCounters(ctx context.Context, tx Tx, scope Scope, state ModelBudgetState, actual int64) error {
	monthly, err := budgetCounter(ctx, tx, scope, state.Month)
	if err != nil {
		return err
	}
	daily, err := budgetCounter(ctx, tx, scope, state.Day)
	if err != nil {
		return err
	}
	if err = updateBudgetCounter(ctx, tx, monthly, actual-state.Amount, -state.Amount, state.Priority); err != nil {
		return err
	}
	return updateBudgetCounter(ctx, tx, daily, actual-state.Amount, -state.Amount, state.Priority)
}

func (s *Service) ModelBudgetStatus(ctx context.Context, token string, scope Scope) (ModelBudgetStatus, error) {
	if p := s.ModelBudgetPolicy; p == nil || p.Scope != scope || !p.ExpiresAt.After(time.Now()) {
		return s.noModelBudget(ctx, token, scope)
	}
	d, e := s.budgetActor(ctx, token, scope, "read")
	if e != nil {
		return ModelBudgetStatus{}, e
	}
	ctx = bindRequestActor(ctx, d)
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := s.ModelBudgetPolicy
	out := ModelBudgetStatus{Configured: true, Month: now.Format("2006-01"), Day: now.Format("2006-01-02"), MonthlyLimit: p.MonthlyPicoUSD, GeneralLimit: p.GeneralPicoUSD, DailyLimit: p.DailyPicoUSD, DailySoftTarget: p.dailySoftTarget(), PricingRevision: "anthropic-2026-10-14"}
	e = s.modelBudgetTransaction(ctx, func(ctx context.Context, tx Tx) error {
		for i, period := range []string{out.Month, out.Day} {
			row, err := tx.Get(ctx, "jobs", scope, budgetCounterID(scope, period))
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if row.ModelBudget == nil {
				return ErrUnavailable
			}
			if i == 0 {
				out.MonthlyCommitted = row.ModelBudget.Total
				out.GeneralCommitted = row.ModelBudget.General
				out.Outstanding = row.ModelBudget.Reserved
			} else {
				out.DailyCommitted = row.ModelBudget.Total
			}
		}
		sub, err := subscriptionMonth(ctx, tx, scope, out.Month)
		out.SubscriptionRequests, out.SubscriptionInputTokens, out.SubscriptionOutputTokens, out.SubscriptionEstimated = sub.Requests, sub.InputTokens, sub.OutputTokens, sub.EstimatedCostPicoUSD
		return err
	})
	out.DailySoftTargetExceeded = out.DailyCommitted > out.DailySoftTarget
	return out, e
}

// noModelBudget answers a human reader when no deployment budget applies; services stay denied, as reserve and settle do.
func (s *Service) noModelBudget(ctx context.Context, token string, scope Scope) (ModelBudgetStatus, error) {
	if !validScope(scope) {
		return ModelBudgetStatus{}, ErrInvalid
	}
	d, e := s.authorize(ctx, token, "read:model_budget", scope, "", "")
	if e != nil {
		return ModelBudgetStatus{}, e
	}
	if d.Role != roleHuman {
		return ModelBudgetStatus{}, ErrDenied
	}
	if _, e = s.authorize(ctx, token, "read:"+colRouting, scope, routingID(scope), ""); e != nil {
		return ModelBudgetStatus{}, e
	}
	doc, rev, e := s.routingDoc(ctx, scope)
	if e != nil {
		return ModelBudgetStatus{}, e
	}
	rows, e := s.Repo.ListRecords(ctx, scope, colRoutingCalls, routingCallsRead)
	if e != nil {
		return ModelBudgetStatus{}, e
	}
	now := time.Now().UTC()
	return ModelBudgetStatus{Month: now.Format("2006-01"), Day: now.Format("2006-01-02"), RoutingBudgetUSD: doc.MonthlyBudgetUSD,
		RoutingMonthCostUSD: routingMonthCost(rows, now), RoutingRevision: rev}, nil
}

func modelBudgetRoutes(mux routeRegistrar, s *Service) {
	mux.HandleFunc("POST /v1/model-budget/reserve", func(w http.ResponseWriter, r *http.Request) {
		var in ModelBudgetReserveInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ReserveModelBudget(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/model-budget/settle", func(w http.ResponseWriter, r *http.Request) {
		var in ModelBudgetSettleInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.SettleModelBudget(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/model-budget/subscription-usage", func(w http.ResponseWriter, r *http.Request) {
		var in SubscriptionUsageInput
		if !decode(w, r, &in) {
			return
		}
		out, e := s.RecordSubscriptionUsage(r.Context(), bearer(r), in)
		respond(w, out, e)
	})
	mux.HandleFunc("POST /v1/model-budget/status", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Scope Scope `json:"scope"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, e := s.ModelBudgetStatus(r.Context(), bearer(r), in.Scope)
		respond(w, out, e)
	})
}

// First use of a period can race on the unique counter/reservation key. The
// repository surfaces those collisions as ErrConflict, unlike SQL serialization
// failures it already retries. Re-read the entire atomic operation, never just
// its final write; all counters and dispatch authorization are recomputed.
func (s *Service) modelBudgetTransaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	for attempt := 0; attempt < 8; attempt++ {
		err := s.Repo.Transaction(ctx, fn)
		if !errors.Is(err, ErrConflict) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Millisecond):
		}
	}
	return ErrConflict
}
