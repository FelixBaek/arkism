package memory

import "testing"

func TestProviderCostReservationAndFinalization(t *testing.T) {
	f := newFixture(t)
	f.svc.CostRates = CostRates{Revision: "price-2026-09-21", JEVRequestPicoUSD: 3_000_000}
	reserved, err := f.svc.beginProviderCall(testCtx, scopeA, "skill_routing", "jev", "jev-router")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.repo.Get(testCtx, colProviderUsage, scopeA, reserved.ID)
	if err != nil || pending.Status != JobStatusPending || pending.Cost.Requests != 1 || pending.Text != "" || pending.CreatedBy != "memoryd" {
		t.Fatalf("pending ledger: %+v %v", pending, err)
	}
	usage := ProviderUsage{Provider: "jev", Model: "jev-router", Requests: 1, InputTokens: 25, TotalTokens: 25}
	if err = f.svc.finishProviderCall(testCtx, reserved, usage, true); err != nil {
		t.Fatal(err)
	}
	done, err := f.repo.Get(testCtx, colProviderUsage, scopeA, reserved.ID)
	if err != nil || done.Status != JobStatusDone || done.Cost.EstimatedCostPicoUSD != 3_000_000 || !done.Cost.PricingAvailable || done.Cost.PricingRevision != "price-2026-09-21" {
		t.Fatalf("completed ledger: %+v %v", done, err)
	}
	if err = f.svc.finishProviderCall(testCtx, reserved, usage, true); err != ErrUnavailable {
		t.Fatalf("duplicate finalization: %v", err)
	}
	if _, err = f.svc.Get(testCtx, f.human, colProviderUsage, scopeA, reserved.ID); err != ErrInvalid {
		t.Fatalf("internal ledger exposed: %v", err)
	}
}

func TestEmbeddingsAreNotPriced(t *testing.T) {
	rates := CostRates{Revision: "known-rate", JEVRequestPicoUSD: 100}
	if amount, available := rates.price("embedding_query", ProviderUsage{Provider: "tei", Model: "local", Requests: 1, TotalTokens: 42}); available || amount != 0 {
		t.Fatal("local TEI embedding was charged")
	}
	if embeddingProvider(&TEIEmbedder{}) != "tei" {
		t.Fatal("TEI provider identity lost")
	}
}
