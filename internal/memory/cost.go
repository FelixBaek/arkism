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
	return u.Provider != "" && u.Model != "" && u.Requests > 0 && u.InputTokens >= 0 && u.OutputTokens >= 0 && u.TotalTokens >= 0 && u.CachedInputTokens >= 0 && u.CachedInputTokens <= u.InputTokens && (u.TotalTokens == 0 || u.TotalTokens >= u.InputTokens+u.OutputTokens)
}

func embedMeasured(ctx context.Context, embedder Embedder, texts []string, inputType string) (EmbeddingBatch, error) {
	if metered, ok := embedder.(MeteredEmbedder); ok {
		return metered.EmbedMeasured(ctx, texts, inputType)
	}
	items, err := embedder.Embed(ctx, texts, inputType)
	return EmbeddingBatch{Items: items, Usage: ProviderUsage{Provider: embeddingProvider(embedder), Model: embedder.Space(), Requests: 1}}, err
}

// meteredEmbed is the one path for a service embedding call: the call is filed in
// the provider_usage ledger and traced in llm_requests under operation.
// callErr is the provider's failure or a short answer (callers degrade on it);
// err is a ledger failure (callers return it).
func (s *Service) meteredEmbed(ctx context.Context, scope Scope, operation string, texts []string, purpose string) (EmbeddingBatch, error, error) {
	m := modelCallSpec{scope: scope, operation: operation, kind: "embed", provider: embeddingProvider(s.Embedder), model: s.Embedder.Space(), prompt: strings.Join(texts, "\n")}
	batch, _, callErr, err := meteredCall(ctx, s, m, func(ctx context.Context) (EmbeddingBatch, ProviderUsage, error) {
		batch, err := embedMeasured(ctx, s.Embedder, texts, purpose)
		if err == nil && len(batch.Items) != len(texts) {
			err = ErrUnavailable
		}
		if err != nil {
			return EmbeddingBatch{}, ProviderUsage{}, err
		}
		return batch, batch.Usage, nil
	})
	return batch, callErr, err
}

// meteredRerank is meteredEmbed for the reranker: operation "rerank".
func (s *Service) meteredRerank(ctx context.Context, scope Scope, query string, texts []string) ([]float64, error, error) {
	usage := s.Reranker.usage()
	m := modelCallSpec{scope: scope, operation: "rerank", kind: "rerank", provider: usage.Provider, model: usage.Model, prompt: query + "\n" + strings.Join(texts, "\n")}
	scores, _, callErr, err := meteredCall(ctx, s, m, func(ctx context.Context) ([]float64, ProviderUsage, error) {
		scores, err := s.Reranker.Rerank(ctx, query, texts)
		return scores, usage, err
	})
	return scores, callErr, err
}

func embeddingProvider(embedder Embedder) string {
	switch e := embedder.(type) {
	case *TEIEmbedder:
		return e.provider
	case *GeminiEmbedder:
		return geminiProviderName
	default:
		return "embedding"
	}
}

func (s *Service) beginProviderCall(ctx context.Context, scope Scope, operation, provider, model string) (Record, error) {
	rows, err := s.reserveProviderCalls(ctx, scope, operation, provider, model, 1, callCap{})
	if err != nil {
		return Record{}, err
	}
	return rows[0], nil
}

// callCap is a daily call limit: the scope's provider calls of this UTC day whose
// operation starts with prefix. The zero cap limits nothing.
type callCap struct {
	prefix string
	limit  int
}

// operationCap is the daily cap a metered operation reserves under: the server model's
// wiki_ operations (wiki, extraction, media) share one; reflect and model rerank have their own.
func operationCap(operation string) callCap {
	switch {
	case strings.HasPrefix(operation, "wiki_"):
		return callCap{prefix: "wiki_", limit: wikiDailyCallLimit}
	case operation == reflectOperation:
		return callCap{prefix: reflectOperation, limit: reflectDailyCallLimit}
	case operation == modelRerankOperation:
		return callCap{prefix: modelRerankOperation, limit: modelRerankDailyCalls}
	}
	return callCap{}
}

// providerLedgerLocker serializes capped reservations of one scope until the transaction ends.
type providerLedgerLocker interface {
	LockProviderLedger(context.Context, Scope) error
}

// reserveProviderCalls is the one budget reservation: n pending provider_usage rows for
// operation, all or none. Under a cap the rows are inserted only while the day's calls
// under the cap's prefix plus n stay within its limit (else ErrModelBudget). Counting and
// inserting are one step, under ledgerMu in this process and a transaction-scoped database
// lock per scope with Postgres, so neither two requests nor two processes on one database
// can both pass the cap. A row counts from insertion: a call in flight or a batch waiting
// for its answers already holds its place until finishProviderCall settles it.
func (s *Service) reserveProviderCalls(ctx context.Context, scope Scope, operation, provider, model string, n int, c callCap) ([]Record, error) {
	if s.Repo == nil || !validScope(scope) || operation == "" || provider == "" || model == "" || n < 1 {
		return nil, ErrUnavailable
	}
	now := time.Now().UTC()
	rows := make([]Record, n)
	for i := range rows {
		rows[i] = Record{ID: ID(), Scope: scope, Kind: providerUsageKind, Status: JobStatusPending, Revision: 1, Cost: &ProviderCost{Operation: operation, Provider: provider, Model: model, Requests: 1}, CreatedAt: now, UpdatedAt: now, CreatedBy: "memoryd", CreatedByType: "service"}
	}
	if c.limit == 0 {
		for _, r := range rows {
			if err := s.Repo.Insert(ctx, colProviderUsage, r); err != nil {
				return nil, err
			}
		}
		return rows, nil
	}
	s.ledgerMu.Lock()
	defer s.ledgerMu.Unlock()
	return rows, s.Repo.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if l, ok := tx.(providerLedgerLocker); ok {
			if err := l.LockProviderLedger(ctx, scope); err != nil {
				return err
			}
		}
		// Read after the lock, outside the transaction's snapshot: every reservation that held the lock before is committed.
		calls, err := s.dailyModelCalls(ctx, scope, c.prefix, now)
		if err != nil {
			return err
		}
		if calls+n > c.limit {
			return ErrModelBudget
		}
		for _, r := range rows {
			if err = tx.Insert(ctx, colProviderUsage, r); err != nil {
				return err
			}
		}
		return nil
	})
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
