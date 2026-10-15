package memory

import (
	"errors"
	"testing"
)

func TestSubscriptionUsageIsRecordOnlyAndIdempotent(t *testing.T) {
	f, _ := modelBudgetFixture(t)
	in := SubscriptionUsageInput{Scope: scopeA, OperationID: "cli-1", Operation: "session_processor", Usage: ModelCallUsage{Model: "claude-haiku-5-5", InputTokens: 1000, OutputTokens: 200}}
	for i := 0; i < 2; i++ {
		out, e := f.svc.RecordSubscriptionUsage(testCtx, "budget", in)
		// haiku list price $0.1/$0.5 per MTok: 1000*0.1e6 + 200*0.5e6 pico-USD
		if e != nil || !out.Recorded || !out.PricingAvailable || out.EstimatedCostPicoUSD != 200_000_000 {
			t.Fatal(out, e)
		}
	}
	if len(f.repo.rows(colProviderUsage)) != 1 {
		t.Fatal("duplicate metering")
	}
	in.OperationID = "cli-2"
	if _, e := f.svc.RecordSubscriptionUsage(testCtx, "budget", in); e != nil {
		t.Fatal(e)
	}
	status, e := f.svc.ModelBudgetStatus(testCtx, "budget", scopeA)
	if e != nil || status.MonthlyCommitted != 0 || status.Outstanding != 0 || status.SubscriptionRequests != 2 || status.SubscriptionInputTokens != 2000 || status.SubscriptionOutputTokens != 400 || status.SubscriptionEstimated != 400_000_000 {
		t.Fatal("subscription usage touched the API budget or was lost", status, e)
	}
	in.Usage.OutputTokens++
	if _, e := f.svc.RecordSubscriptionUsage(testCtx, "budget", in); !errors.Is(e, ErrConflict) {
		t.Fatal("changed replay", e)
	}
	in.OperationID, in.Usage = "cli-3", ModelCallUsage{Model: "claude-haiku-5-5"}
	if _, e := f.svc.RecordSubscriptionUsage(testCtx, "budget", in); !errors.Is(e, ErrInvalid) {
		t.Fatal("empty usage", e)
	}
	f.auth.actors["other"] = actor("someone", roleService, true, "settle")
	in.Usage = ModelCallUsage{Model: "claude-haiku-5-5", InputTokens: 1}
	if _, e := f.svc.RecordSubscriptionUsage(testCtx, "other", in); !errors.Is(e, ErrDenied) {
		t.Fatal("non-budget principal recorded", e)
	}
}
