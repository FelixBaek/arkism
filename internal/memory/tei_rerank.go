package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

const defaultRerankTimeout = 2 * time.Second

// TEIRerankConfig points at an already-running cross-encoder service. Without
// Model it answers POST {URL}/rerank {query, texts, raw_scores} with [{index, score}];
// with Model it speaks the hosted rerank protocol many services share:
// POST {URL}/rerank {model, query, documents, top_n} answered by
// {results|data: [{index, relevance_score}]}.
// Protocol picks one of two other hosted shapes, where URL is the full endpoint:
// RerankPassages POST {URL} {model, query: {text}, passages: [{text}], truncate}
// answered by {rankings: [{index, logit}]} (logits pass through a sigmoid), and
// RerankRecords POST {URL} {model, query, records: [{id, content}], topN}
// answered by {records: [{id, score}]}.
type TEIRerankConfig struct {
	URL      string
	Model    string
	APIKey   string
	Protocol string
	// APIKeyFile, when set, is read on every call instead of APIKey, so a
	// short-lived access token can be rotated on disk without a restart.
	APIKeyFile string
	Timeout    time.Duration
	Client     *http.Client
}

const (
	RerankPassages = "passages"
	RerankRecords  = "records"
	// rerankKeyFileMax bounds a key file read on every call.
	rerankKeyFileMax = 16 << 10
)

// TEIReranker scores (query, text) pairs. Search never fails on it: an error or
// timeout serves the fused order and is counted in metrics.
// A local reranker (NewLocalReranker) scores in process with lexical BM25 and sends
// nothing anywhere.
type TEIReranker struct {
	local                                      bool
	endpoint, model, apiKey, protocol, keyFile string
	client                                     *http.Client
	metrics                                    rerankCounters
	// next is tried when this reranker fails (WithFallback).
	next *TEIReranker
}

// WithFallback sets the reranker tried when t fails, and returns t.
func (t *TEIReranker) WithFallback(next *TEIReranker) *TEIReranker {
	t.next = next
	return t
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
	endpoint := base + "/rerank"
	switch cfg.Protocol {
	case "":
	case RerankPassages, RerankRecords:
		if model == "" {
			return nil, fmt.Errorf("rerank protocol %s needs a model: %w", cfg.Protocol, ErrInvalid)
		}
		endpoint = base
	default:
		return nil, fmt.Errorf("rerank protocol %q: %w", cfg.Protocol, ErrInvalid)
	}
	return &TEIReranker{endpoint: endpoint, model: model, apiKey: strings.TrimSpace(cfg.APIKey), protocol: cfg.Protocol, keyFile: cfg.APIKeyFile,
		client: providerClient(cfg.Client, timeout)}, nil
}

// key is the bearer key: the key file's current content when one is set.
func (t *TEIReranker) key() (string, error) {
	if t.keyFile == "" {
		return t.apiKey, nil
	}
	f, err := os.Open(t.keyFile)
	if err != nil {
		return "", fmt.Errorf("rerank key file: %w", ErrUnavailable)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, rerankKeyFileMax))
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		return "", fmt.Errorf("rerank key file: %w", ErrUnavailable)
	}
	return strings.TrimSpace(string(raw)), nil
}

// NewLocalReranker is the fallback when no rerank service is configured: BM25 over
// the hit texts, scaled so the best text scores 1.
func NewLocalReranker() *TEIReranker { return &TEIReranker{local: true} }

// usage is the ledger entry of one rerank call.
func (t *TEIReranker) usage() ProviderUsage {
	if t.local {
		return ProviderUsage{Provider: "local", Model: "lexical", Requests: 1}
	}
	if t.protocol != "" {
		return ProviderUsage{Provider: "rerank_" + t.protocol, Model: t.model, Requests: 1}
	}
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

type rerankText struct {
	Text string `json:"text"`
}

type rerankRecord struct {
	ID      string   `json:"id"`
	Content string   `json:"content,omitempty"`
	Score   *float64 `json:"score,omitempty"`
}

func (t *TEIReranker) request(query string, texts []string) any {
	switch t.protocol {
	case RerankPassages:
		passages := make([]rerankText, len(texts))
		for i, text := range texts {
			passages[i].Text = text
		}
		return map[string]any{"model": t.model, "query": rerankText{query}, "passages": passages, "truncate": "END"}
	case RerankRecords:
		records := make([]rerankRecord, len(texts))
		for i, text := range texts {
			records[i] = rerankRecord{ID: strconv.Itoa(i), Content: text}
		}
		return map[string]any{"model": t.model, "query": query, "records": records, "topN": len(texts), "ignoreRecordDetailsInResponse": true}
	}
	if t.model != "" {
		return documentRerankRequest{Model: t.model, Query: query, Documents: texts, TopN: len(texts)}
	}
	return teiRerankRequest{Query: query, Texts: texts, Truncate: true}
}

// protocolRows reads the passages and records answers into index/score rows.
func (t *TEIReranker) protocolRows(body []byte) ([]teiRerankRow, error) {
	var res struct {
		Rankings []struct {
			Index *int     `json:"index"`
			Logit *float64 `json:"logit"`
		} `json:"rankings"`
		Records []rerankRecord `json:"records"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	var rows []teiRerankRow
	for _, r := range res.Rankings {
		row := teiRerankRow{Index: r.Index}
		if r.Logit != nil {
			score := 1 / (1 + math.Exp(-*r.Logit))
			row.Score = &score
		}
		rows = append(rows, row)
	}
	for _, r := range res.Records {
		index, err := strconv.Atoi(r.ID)
		if err != nil || strconv.Itoa(index) != r.ID {
			return nil, ErrUnavailable
		}
		rows = append(rows, teiRerankRow{Index: &index, Score: r.Score})
	}
	return rows, nil
}

func (t *TEIReranker) rows(body []byte) ([]teiRerankRow, error) {
	if t.protocol != "" {
		return t.protocolRows(body)
	}
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
	if t.local {
		return localRerank(query, texts), nil
	}
	key, err := t.key()
	if err != nil {
		return nil, err
	}
	body, status, err := postJSON(ctx, t.client, t.endpoint, key, t.request(query, texts), defaultRequestMax, defaultResponseMax)
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

func localRerank(query string, texts []string) []float64 {
	docs := make([]bm25Doc, len(texts))
	for i, text := range texts {
		docs[i].text = text
	}
	scores := bm25(query, docs)
	best := slices.Max(scores)
	for i := range scores {
		if best > 0 {
			scores[i] /= best
		}
	}
	return scores
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
