package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultRerankTimeout = 2 * time.Second

// TEIRerankConfig points at an already-running cross-encoder service. Without
// Model it answers POST {URL}/rerank {query, texts, raw_scores} with [{index, score}];
// with Model it speaks the hosted rerank protocol many services share:
// POST {URL}/rerank {model, query, documents, top_n} answered by
// {results|data: [{index, relevance_score}]}.
type TEIRerankConfig struct {
	URL     string
	Model   string
	APIKey  string
	Timeout time.Duration
	Client  *http.Client
}

// TEIReranker scores (query, text) pairs. Search never fails on it: an error or
// timeout serves the fused order and is counted in metrics.
type TEIReranker struct {
	endpoint, model, apiKey string
	client                  *http.Client
	metrics                 rerankCounters
}

func NewTEIReranker(cfg TEIRerankConfig) (*TEIReranker, error) {
	base := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	if err := validateEndpoint(base); err != nil {
		return nil, fmt.Errorf("rerank url: %w", err)
	}
	if parsed, err := url.Parse(base); err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("rerank url must be a plain base URL: %w", ErrInvalid)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultRerankTimeout
	}
	model := strings.TrimSpace(cfg.Model)
	if model != "" && !validTEIMetadata(model) {
		return nil, fmt.Errorf("rerank model: %w", ErrInvalid)
	}
	return &TEIReranker{endpoint: base + "/rerank", model: model, apiKey: strings.TrimSpace(cfg.APIKey), client: providerClient(cfg.Client, timeout)}, nil
}

// usage is the ledger entry of one rerank call.
func (t *TEIReranker) usage() ProviderUsage {
	if t.model != "" {
		return ProviderUsage{Provider: "rerank_api", Model: t.model, Requests: 1}
	}
	return ProviderUsage{Provider: "tei", Model: "rerank", Requests: 1}
}

type documentRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

type documentRerankResponse struct {
	Results []teiRerankRow `json:"results"`
	Data    []teiRerankRow `json:"data"`
}

type teiRerankRequest struct {
	Query     string   `json:"query"`
	Texts     []string `json:"texts"`
	RawScores bool     `json:"raw_scores"`
	Truncate  bool     `json:"truncate"`
}

type teiRerankRow struct {
	Index *int     `json:"index"`
	Score *float64 `json:"score"`
	// RelevanceScore is the hosted protocol's name for Score.
	RelevanceScore *float64 `json:"relevance_score"`
}

func (t *TEIReranker) request(query string, texts []string) any {
	if t.model != "" {
		return documentRerankRequest{Model: t.model, Query: query, Documents: texts, TopN: len(texts)}
	}
	return teiRerankRequest{Query: query, Texts: texts, Truncate: true}
}

func (t *TEIReranker) rows(body []byte) ([]teiRerankRow, error) {
	var rows []teiRerankRow
	if t.model == "" {
		err := json.Unmarshal(body, &rows)
		return rows, err
	}
	var res documentRerankResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	rows = append(res.Results, res.Data...)
	for i := range rows {
		if rows[i].Score == nil {
			rows[i].Score = rows[i].RelevanceScore
		}
	}
	return rows, nil
}

// Rerank returns one score in [0,1] per text, in input order.
func (t *TEIReranker) Rerank(ctx context.Context, query string, texts []string) ([]float64, error) {
	t.metrics.calls.Add(1)
	body, status, err := postJSON(ctx, t.client, t.endpoint, t.apiKey, t.request(query, texts), defaultRequestMax, defaultResponseMax)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, dependencyStatusError("tei rerank", status)
	}
	rows, err := t.rows(body)
	if err != nil || len(rows) != len(texts) {
		return nil, fmt.Errorf("tei rerank: unreadable response: %w", ErrUnavailable)
	}
	scores, seen := make([]float64, len(texts)), make([]bool, len(texts))
	for _, r := range rows {
		if r.Index == nil || r.Score == nil || *r.Index < 0 || *r.Index >= len(texts) || seen[*r.Index] || math.IsNaN(*r.Score) || math.IsInf(*r.Score, 0) {
			return nil, fmt.Errorf("tei rerank: malformed row: %w", ErrUnavailable)
		}
		seen[*r.Index] = true
		scores[*r.Index] = min(max(*r.Score, 0), 1)
	}
	return scores, nil
}

// fellBack counts a rerank failure that served the fused order instead.
func (t *TEIReranker) fellBack(err error) {
	t.metrics.fallbacks.Add(1)
	if embedderFailureKind(err) == "embedder_timeout" {
		t.metrics.timeouts.Add(1)
	}
}

// Metrics reports this process's rerank calls and fallbacks.
func (t *TEIReranker) Metrics() RerankMetrics { return t.metrics.snapshot() }
