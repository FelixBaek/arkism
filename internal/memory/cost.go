package memory

import (
	"context"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/prices"
)

const providerUsageKind = "provider_usage"

// CostRates are deployment-owned list prices. Pico-dollars preserve small
// per-token prices without floats. Free-tier credits and taxes are reconciled
// against the provider invoice, not guessed here.
type CostRates struct {
	Revision                   string
	SkillLLMInputTokenPicoUSD  int64
	SkillLLMOutputTokenPicoUSD int64
	JEVRequestPicoUSD          int64
}

// listPricedOperations are model calls priced at the public list price
// (internal/prices) by the model they ran on: wiki steps on the skill LLM when
// the deployment set no skill LLM rate, and the digest and merge workers'
// Claude calls, which the host reports with its proposals.
var listPricedOperations = []string{"wiki_analyse", "wiki_write", "digest_topic", "digest_user", "digest_judge", "merge_judge"}

func (r CostRates) price(operation string, usage ProviderUsage) (int64, bool) {
	amount, revision, ok := r.priceAt(operation, usage)
	return amount, ok && revision != ""
}

// listPrice is usage at list price in pico-dollars.
func listPrice(usage ProviderUsage) (int64, bool) {
	usd, ok := prices.Cost(usage.Model, usage.InputTokens, usage.OutputTokens)
	if !ok {
		return 0, false
	}
	return int64(math.Round(usd * 1e12)), true
}

// priceAt is the cost and the pricing revision it came from.
func (r CostRates) priceAt(operation string, usage ProviderUsage) (int64, string, bool) {
	if slices.Contains(listPricedOperations, operation) {
		if strings.HasPrefix(operation, "wiki_") && r.SkillLLMInputTokenPicoUSD > 0 && r.SkillLLMOutputTokenPicoUSD > 0 {
			// The wiki runs on the skill LLM endpoint: the deployment's own rate wins.
			return usage.InputTokens*r.SkillLLMInputTokenPicoUSD + usage.OutputTokens*r.SkillLLMOutputTokenPicoUSD, strings.TrimSpace(r.Revision), true
		}
		amount, ok := listPrice(usage)
		return amount, prices.Revision, ok
	}
	var amount int64
	switch operation {
	case "skill_analysis", "skill_draft":
		if r.SkillLLMInputTokenPicoUSD <= 0 || r.SkillLLMOutputTokenPicoUSD <= 0 {
			return 0, "", false
		}
		amount = usage.InputTokens*r.SkillLLMInputTokenPicoUSD + usage.OutputTokens*r.SkillLLMOutputTokenPicoUSD
	case "skill_routing":
		if r.JEVRequestPicoUSD <= 0 {
			return 0, "", false
		}
		amount = usage.Requests * r.JEVRequestPicoUSD
	default:
		return 0, "", false
	}
	return amount, strings.TrimSpace(r.Revision), true
}

func validProviderUsage(u ProviderUsage) bool {
	return u.Provider != "" && u.Model != "" && u.Requests > 0 && u.InputTokens >= 0 && u.OutputTokens >= 0 && u.TotalTokens >= 0 && (u.TotalTokens == 0 || u.TotalTokens >= u.InputTokens+u.OutputTokens)
}

func embedMeasured(ctx context.Context, embedder Embedder, texts []string, inputType string) (EmbeddingBatch, error) {
	if metered, ok := embedder.(MeteredEmbedder); ok {
		return metered.EmbedMeasured(ctx, texts, inputType)
	}
	items, err := embedder.Embed(ctx, texts, inputType)
	return EmbeddingBatch{Items: items, Usage: ProviderUsage{Provider: embeddingProvider(embedder), Model: embedder.Space(), Requests: 1}}, err
}

func embeddingProvider(embedder Embedder) string {
	switch embedder.(type) {
	case *TEIEmbedder:
		return "tei"
	case *GeminiEmbedder:
		return geminiProviderName
	default:
		return "embedding"
	}
}

func (s *Service) beginProviderCall(ctx context.Context, scope Scope, operation, provider, model string) (Record, error) {
	if s.Repo == nil || !validScope(scope) || operation == "" || provider == "" || model == "" {
		return Record{}, ErrUnavailable
	}
	now := time.Now().UTC()
	r := Record{ID: ID(), Scope: scope, Kind: providerUsageKind, Status: JobStatusPending, Revision: 1, Cost: &ProviderCost{Operation: operation, Provider: provider, Model: model, Requests: 1}, CreatedAt: now, UpdatedAt: now, CreatedBy: "memoryd", CreatedByType: "service"}
	if err := s.Repo.Insert(ctx, colProviderUsage, r); err != nil {
		return Record{}, err
	}
	return r, nil
}

func (s *Service) finishProviderCall(ctx context.Context, reserved Record, usage ProviderUsage, success bool) error {
	if reserved.Cost == nil || !validProviderUsage(usage) {
		return ErrUnavailable
	}
	current, err := s.Repo.Get(ctx, colProviderUsage, reserved.Scope, reserved.ID)
	if err != nil || current.Revision != reserved.Revision || current.Kind != providerUsageKind || current.Status != JobStatusPending || current.Cost == nil {
		return ErrUnavailable
	}
	current.Status = JobStatusDone
	if !success {
		current.Status = JobStatusFailed
	}
	cost, revision, priced := s.CostRates.priceAt(current.Cost.Operation, usage)
	priced = priced && revision != ""
	current.Cost.Provider = usage.Provider
	current.Cost.Model = usage.Model
	current.Cost.Requests = usage.Requests
	current.Cost.InputTokens = usage.InputTokens
	current.Cost.OutputTokens = usage.OutputTokens
	current.Cost.TotalTokens = usage.TotalTokens
	current.Cost.EstimatedCostPicoUSD = cost
	current.Cost.PricingAvailable = priced
	if priced {
		current.Cost.PricingRevision = revision
	}
	current.UpdatedAt = time.Now().UTC()
	return s.Repo.Replace(ctx, colProviderUsage, current, current.Revision)
}
