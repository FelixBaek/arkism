package memory

import (
	"encoding/json"
	"errors"

	"testing"
	"time"
)

func modelBudgetFixture(t *testing.T) (*fixture, ModelBudgetReserveInput) {
	f := newFixture(t)
	f.auth.actors["budget"] = actor("harness", roleService, true, "reserve", "settle", "read", "priority")
	f.svc.ModelBudgetPolicy = &ModelBudgetPolicy{Scope: scopeA, Principals: []string{"harness"}, MonthlyPicoUSD: 100 * picoDollar, GeneralPicoUSD: 90 * picoDollar, DailyPicoUSD: 5 * picoDollar, ExpiresAt: time.Now().Add(time.Hour)}
	return f, ModelBudgetReserveInput{Scope: scopeA, OperationID: "segment-1", Model: "claude-haiku-5-5", Role: "session_processor", MaxInputTokens: 1000, MaxOutputTokens: 1000}
}
func TestModelBudgetIdempotencyAndSettlement(t *testing.T) {
	f, in := modelBudgetFixture(t)
	dispatched := 0
	var reservation ModelBudgetReservation
	for i := 0; i < 10; i++ {
		out, e := f.svc.ReserveModelBudget(testCtx, "budget", in)
		if e != nil {
			t.Fatal(e)
		}
		reservation = out
		if out.DispatchAllowed {
			dispatched++
		}
	}

	if dispatched != 1 {
		t.Fatal("repeated paid dispatch", dispatched)
	}
	status, e := f.svc.ModelBudgetStatus(testCtx, "budget", scopeA)
	if e != nil || status.MonthlyCommitted != reservation.ReservedPicoUSD || status.Outstanding != reservation.ReservedPicoUSD {
		t.Fatal(status, e)
	}
	settle := ModelBudgetSettleInput{Scope: scopeA, ReservationID: reservation.ReservationID, Status: "completed", InputTokens: 100, OutputTokens: 200, CacheReadInputTokens: 100, CacheCreationInputTokens: 50, CacheCreation1hInputTokens: 50}
	if _, e = f.svc.SettleModelBudget(testCtx, "budget", settle); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.SettleModelBudget(testCtx, "budget", settle); e != nil {
		t.Fatal("settlement retry", e)
	}
	status, e = f.svc.ModelBudgetStatus(testCtx, "budget", scopeA)
	want := int64(100*100000 + 200*500000 + 100*10000 + 50*125000 + 50*200000)
	if e != nil || status.MonthlyCommitted != want || status.Outstanding != 0 || status.GeneralCommitted != want || status.DailyCommitted != want {
		t.Fatal(status, want, e)
	}
	if len(f.repo.rows(colProviderUsage)) != 1 {
		t.Fatal("duplicate metering")
	}
	settle.OutputTokens++
	if _, e = f.svc.SettleModelBudget(testCtx, "budget", settle); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	in.MaxOutputTokens++
	if _, e = f.svc.ReserveModelBudget(testCtx, "budget", in); !errors.Is(e, ErrConflict) {
		t.Fatal("changed request", e)
	}
}
func TestModelBudgetCapsAndUncertainty(t *testing.T) {
	f, in := modelBudgetFixture(t)
	amount := int64(700_000_000)
	f.svc.ModelBudgetPolicy.GeneralPicoUSD = amount
	r, e := f.svc.ReserveModelBudget(testCtx, "budget", in)
	if e != nil {
		t.Fatal(e)
	}
	in.OperationID = "segment-2"
	if _, e = f.svc.ReserveModelBudget(testCtx, "budget", in); !errors.Is(e, ErrModelBudget) {
		t.Fatal("exceeded general", e)
	}
	if _, e = f.svc.SettleModelBudget(testCtx, "budget", ModelBudgetSettleInput{Scope: scopeA, ReservationID: r.ReservationID, Status: "unknown"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.SettleModelBudget(testCtx, "budget", ModelBudgetSettleInput{Scope: scopeA, ReservationID: r.ReservationID, Status: "not_sent"}); !errors.Is(e, ErrConflict) {
		t.Fatal("released uncertain charge", e)
	}
	in.Priority = true
	if _, e = f.svc.ReserveModelBudget(testCtx, "budget", in); e != nil {
		t.Fatal("priority reserve", e)
	}
	f.svc.ModelBudgetPolicy.MonthlyPicoUSD = 2 * amount
	in.OperationID = "segment-3"
	if _, e = f.svc.ReserveModelBudget(testCtx, "budget", in); !errors.Is(e, ErrModelBudget) {
		t.Fatal("exceeded total", e)
	}
	f.svc.ModelBudgetPolicy.MonthlyPicoUSD = 100 * picoDollar
	f.svc.ModelBudgetPolicy.DailyPicoUSD = 2 * amount
	if _, e = f.svc.ReserveModelBudget(testCtx, "budget", in); !errors.Is(e, ErrModelBudget) {
		t.Fatal("exceeded day", e)
	}
}
func TestModelBudgetFailsClosedAndRollsBack(t *testing.T) {
	for _, mode := range []string{"unset", "expired", "scope", "principal", "role", "audit", "model", "tokens"} {
		t.Run(mode, func(t *testing.T) {
			f, in := modelBudgetFixture(t)
			switch mode {
			case "unset":
				f.svc.ModelBudgetPolicy = nil
			case "expired":
				f.svc.ModelBudgetPolicy.ExpiresAt = time.Now().Add(-time.Second)
			case "scope":
				in.Scope.AgentID = "other"
			case "principal":
				f.svc.ModelBudgetPolicy.Principals = []string{"other"}
			case "role":
				f.auth.actors["budget"] = actor("harness", roleAgent, true, "reserve")
			case "audit":
				f.repo.failAudit = true
			case "model":
				in.Model = "claude-opus-5-5"
			case "tokens":
				in.MaxInputTokens = 100001
			}
			if _, e := f.svc.ReserveModelBudget(testCtx, "budget", in); e == nil {
				t.Fatal("uncontrolled spend")
			}
			if len(f.repo.rows("jobs")) != 0 {
				t.Fatal("partial reserve")
			}
		})
	}
	f, _ := modelBudgetFixture(t)
	raw, _ := json.Marshal(f.svc.ModelBudgetPolicy)
	if _, e := ParseModelBudgetPolicy(string(raw)); e != nil {
		t.Fatal(e)
	}
	for _, raw := range []string{"{}", "null", string(raw) + " {}", `{"monthly_pico_usd":100000000000001}`} {
		if _, e := ParseModelBudgetPolicy(raw); !errors.Is(e, ErrInvalid) {
			t.Fatal(raw, e)
		}
	}
}
func TestModelBudgetNotSentAndSettlementRollback(t *testing.T) {
	f, in := modelBudgetFixture(t)
	r, e := f.svc.ReserveModelBudget(testCtx, "budget", in)
	if e != nil {
		t.Fatal(e)
	}
	settle := ModelBudgetSettleInput{Scope: scopeA, ReservationID: r.ReservationID, Status: "not_sent"}
	f.repo.failAudit = true
	if _, e = f.svc.SettleModelBudget(testCtx, "budget", settle); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	f.repo.failAudit = false
	status, e := f.svc.ModelBudgetStatus(testCtx, "budget", scopeA)
	if e != nil || status.Outstanding != r.ReservedPicoUSD || len(f.repo.rows(colProviderUsage)) != 0 {
		t.Fatal("partially released", e)
	}
	if _, e = f.svc.SettleModelBudget(testCtx, "budget", settle); e != nil {
		t.Fatal(e)
	}
	status, e = f.svc.ModelBudgetStatus(testCtx, "budget", scopeA)
	if e != nil || status.MonthlyCommitted != 0 || status.Outstanding != 0 {
		t.Fatal(status, e)
	}
}

func TestModelBudgetUnknownAuditAndPrincipalBoundary(t *testing.T) {
	f, in := modelBudgetFixture(t)
	r, e := f.svc.ReserveModelBudget(testCtx, "budget", in)
	if e != nil {
		t.Fatal(e)
	}
	unknown := ModelBudgetSettleInput{Scope: scopeA, ReservationID: r.ReservationID, Status: "unknown"}
	f.repo.failAudit = true
	if _, e = f.svc.SettleModelBudget(testCtx, "budget", unknown); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	f.repo.failAudit = false
	row, e := f.repo.Get(testCtx, "jobs", scopeA, r.ReservationID)
	if e != nil || row.Status != JobStatusPending || row.Revision != 1 {
		t.Fatal("unknown escaped audit rollback", e)
	}
	if _, e = f.svc.ModelBudgetStatus(testCtx, f.human, scopeA); e != nil {
		t.Fatal("human read", e)
	}
	if _, e = f.svc.ReserveModelBudget(testCtx, f.human, in); !errors.Is(e, ErrDenied) {
		t.Fatal("human dispatch", e)
	}
	if _, e = f.svc.SettleModelBudget(testCtx, f.human, unknown); !errors.Is(e, ErrDenied) {
		t.Fatal("human settlement", e)
	}
	f.auth.actors["other"] = actor("other-harness", roleService, true, "settle")
	f.svc.ModelBudgetPolicy.Principals = append(f.svc.ModelBudgetPolicy.Principals, "other-harness")
	if _, e = f.svc.SettleModelBudget(testCtx, "other", unknown); !errors.Is(e, ErrDenied) {
		t.Fatal("foreign settlement", e)
	}
}

func TestModelBudgetDailyTargetIsSoftAndLegacyHardIsOptional(t *testing.T) {
	f, in := modelBudgetFixture(t)
	p := f.svc.ModelBudgetPolicy
	p.DailyPicoUSD = 0
	p.DailySoftTargetPicoUSD = 1
	raw, _ := json.Marshal(p)
	parsed, e := ParseModelBudgetPolicy(string(raw))
	if e != nil || parsed.DailyPicoUSD != 0 {
		t.Fatal(parsed, e)
	}
	for i, priority := range []bool{false, true, false} {
		in.OperationID = string(rune('a' + i))
		in.Priority = priority
		if _, e = f.svc.ReserveModelBudget(testCtx, "budget", in); e != nil {
			t.Fatal("soft target blocked dispatch", e)
		}
	}
	status, e := f.svc.ModelBudgetStatus(testCtx, "budget", scopeA)
	if e != nil || status.DailySoftTarget != 1 || !status.DailySoftTargetExceeded || status.DailyLimit != 0 {
		t.Fatal(status, e)
	}
	encoded, _ := json.Marshal(status)
	var fields map[string]any
	json.Unmarshal(encoded, &fields)
	if _, ok := fields["daily_limit_pico_usd"]; ok {
		t.Fatal("disabled hard limit serialized")
	}
	p.DailyPicoUSD = status.DailyCommitted
	in.OperationID = "hard"
	if _, e = f.svc.ReserveModelBudget(testCtx, "budget", in); !errors.Is(e, ErrModelBudget) {
		t.Fatal("explicit hard cap ignored", e)
	}
	p.DailyPicoUSD = 0
	p.DailySoftTargetPicoUSD = 0
	raw, _ = json.Marshal(p)
	parsed, e = ParseModelBudgetPolicy(string(raw))
	if e != nil || parsed.DailySoftTargetPicoUSD != 3*picoDollar {
		t.Fatal("default target", parsed, e)
	}
	p.DailyPicoUSD = -1
	raw, _ = json.Marshal(p)
	if _, e = ParseModelBudgetPolicy(string(raw)); !errors.Is(e, ErrInvalid) {
		t.Fatal("negative hard cap", e)
	}
	p.DailyPicoUSD = 0
	p.DailySoftTargetPicoUSD = -1
	raw, _ = json.Marshal(p)
	if _, e = ParseModelBudgetPolicy(string(raw)); !errors.Is(e, ErrInvalid) {
		t.Fatal("negative target", e)
	}
}

// Without an active budget a human reader is told there is none; anyone else is still denied.
func TestModelBudgetStatusWithoutPolicy(t *testing.T) {
	f, _ := modelBudgetFixture(t)
	f.svc.ModelBudgetPolicy = nil
	if _, e := f.svc.ModelBudgetStatus(testCtx, f.human, scopeA); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
	if _, e := f.svc.ModelBudgetStatus(testCtx, "budget", scopeA); !errors.Is(e, ErrDenied) {
		t.Fatal("service reader", e)
	}
	if _, e := f.svc.ModelBudgetStatus(testCtx, "nobody", scopeA); !errors.Is(e, ErrDenied) {
		t.Fatal("unknown token", e)
	}
}
