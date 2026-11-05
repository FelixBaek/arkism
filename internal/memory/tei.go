package memory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	teiProviderName = "tei"
	// embeddingsAPIProvider is the same client speaking the common
	// POST .../embeddings protocol (guide/server.md lists servers that answer it).
	embeddingsAPIProvider = "embeddings_api"
	teiDimension          = 1024
	// TeiMaxBatchSize is the hard client-batch ceiling for the TEI CPU
	// backend: it forces max_batch_requests=4 and dies with exit 139 (bounded
	// queue panic) when an in-flight batch overflows it. The same ceiling is
	// validated for the client batch, the service-side index batch and the
	// server's max-client-batch-size.
	TeiMaxBatchSize = 4
)

// TEIConfig configures an already-running embedding endpoint: a Text Embeddings
// Inference /embed URL, or any URL ending in /embeddings that speaks the
// embeddings protocol. The adapter does not select or deploy weights.
type TEIConfig struct {
	EndpointURL string
	// APIKey is sent as a bearer token; https (or loopback http) is enforced.
	APIKey string
	// SendDimensions asks an embeddings API server for 1024-dimension vectors
	// (for models that shorten their output); servers that reject the field leave it off.
	SendDimensions bool
	Model          string
	Revision       string
	// Prefixes must contain explicit query and document entries. An empty value
	// is valid for models that do not require a purpose prefix.
	Prefixes map[string]string
	// MaxBatchSize caps how many inputs one Embed call may carry. Zero selects
	// TeiMaxBatchSize; values above it are rejected because they crash the CPU
	// backend instead of failing cleanly.
	MaxBatchSize     int
	Timeout          time.Duration
	MaxRequestBytes  int64
	MaxResponseBytes int64
	Client           *http.Client
}

type TEIEmbedder struct {
	endpoint, model, revision, queryPrefix, documentPrefix, space string
	provider, apiKey                                              string
	sendDimensions, local                                         bool
	maxBatch                                                      int
	maxRequest, maxResponse                                       int64
	client                                                        *http.Client
}

// ValidateTEIBatchLimits checks the three batch ceilings that must stay inside
// the TEI CPU backend's ceiling of 4: the adapter client batch, the
// service-side index queue batch and the server's --max-client-batch-size.
// The client batch may not exceed what the server accepts, because an
// overflowing in-flight batch panics the server (exit 139) instead of
// returning an error.
func ValidateTEIBatchLimits(clientBatch, queueBatch, serverBatch int) error {
	for name, size := range map[string]int{"client": clientBatch, "queue": queueBatch, "server": serverBatch} {
		if size < 1 || size > TeiMaxBatchSize {
			return fmt.Errorf("tei %s batch size %d outside 1..%d: larger client batches panic the CPU backend (exit 139): %w", name, size, TeiMaxBatchSize, ErrInvalid)
		}
	}
	if clientBatch > serverBatch {
		return fmt.Errorf("tei client batch %d exceeds server max-client-batch-size %d: %w", clientBatch, serverBatch, ErrInvalid)
	}
	if queueBatch > clientBatch {
		return fmt.Errorf("tei index queue batch %d exceeds client batch %d: %w", queueBatch, clientBatch, ErrInvalid)
	}
	return nil
}

func NewTEIEmbedder(cfg TEIConfig) (*TEIEmbedder, error) {
	provider, err := validateTEIEndpoint(cfg.EndpointURL)
	if err != nil {
		return nil, err
	}
	parsed, _ := url.Parse(cfg.EndpointURL)
	maxBatch := cfg.MaxBatchSize
	if maxBatch == 0 {
		maxBatch = TeiMaxBatchSize
	}
	if maxBatch < 1 || maxBatch > TeiMaxBatchSize {
		return nil, fmt.Errorf("tei client batch size %d outside 1..%d: larger client batches panic the CPU backend (exit 139): %w", maxBatch, TeiMaxBatchSize, ErrInvalid)
	}
	model, revision := strings.TrimSpace(cfg.Model), strings.TrimSpace(cfg.Revision)
	if !validTEIMetadata(model) || !validTEIMetadata(revision) {
		return nil, fmt.Errorf("tei model and revision are required: %w", ErrInvalid)
	}
	queryPrefix, hasQuery := cfg.Prefixes[embedInputQuery]
	documentPrefix, hasDocument := cfg.Prefixes[embedInputDocument]
	if !hasQuery || !hasDocument {
		return nil, fmt.Errorf("tei query and document prefixes must be explicit: %w", ErrInvalid)
	}
	prefixJSON, _ := json.Marshal([]string{queryPrefix, documentPrefix})
	prefixHash := sha256.Sum256(prefixJSON)
	return &TEIEmbedder{
		endpoint: cfg.EndpointURL, model: model, revision: revision,
		queryPrefix: queryPrefix, documentPrefix: documentPrefix,
		provider: provider, apiKey: strings.TrimSpace(cfg.APIKey), sendDimensions: cfg.SendDimensions,
		local:       isPrivateNetworkHost(parsed.Hostname()),
		space:       fmt.Sprintf("%s:%s:%s:%dd:%x", provider, model, revision, teiDimension, prefixHash[:]),
		maxBatch:    maxBatch,
		maxRequest:  positiveOr(cfg.MaxRequestBytes, defaultRequestMax),
		maxResponse: positiveOr(cfg.MaxResponseBytes, defaultResponseMax),
		client:      providerClient(cfg.Client, cfg.Timeout),
	}, nil
}

func validTEIMetadata(value string) bool {
	return value != "" && len(value) <= 256 && strings.IndexFunc(value, unicode.IsControl) < 0
}

// validateTEIEndpoint names the protocol by the path: exactly /embed, or a path
// ending in /embeddings (the shared embeddings API shape).
func validateTEIEndpoint(raw string) (string, error) {
	if err := validateEndpoint(raw); err != nil {
		return "", fmt.Errorf("tei endpoint: %w", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", fmt.Errorf("embedding endpoint must be a plain URL: %w", ErrInvalid)
	}
	switch {
	case parsed.Path == "/embed":
		return teiProviderName, nil
	case strings.HasSuffix(parsed.Path, "/embeddings"):
		return embeddingsAPIProvider, nil
	}
	return "", fmt.Errorf("embedding endpoint must be the /embed or .../embeddings URL: %w", ErrInvalid)
}

// Local reports whether text stays on this host or its private network
// (a loopback or literal private address endpoint).
func (t *TEIEmbedder) Local() bool { return t.local }

func (t *TEIEmbedder) Space() string { return t.space }

type teiRequest struct {
	Inputs []string `json:"inputs"`
}

func (t *TEIEmbedder) Embed(ctx context.Context, texts []string, purpose string) ([]Embedding, error) {
	batch, err := t.EmbedMeasured(ctx, texts, purpose)
	return batch.Items, err
}

type embeddingsAPIRequest struct {
	Model          string   `json:"model"`
	Input          []string `json:"input"`
	EncodingFormat string   `json:"encoding_format"`
	Dimensions     int      `json:"dimensions,omitempty"`
}

type embeddingsAPIResponse struct {
	Data []struct {
		Index     *int      `json:"index"`
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
	Usage struct {
		PromptTokens int64 `json:"prompt_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	} `json:"usage"`
}

// post sends inputs in this endpoint's protocol and returns vectors in input order.
func (t *TEIEmbedder) post(ctx context.Context, inputs []string) ([][]float64, ProviderUsage, error) {
	usage := ProviderUsage{Provider: t.provider, Model: t.space, Requests: 1}
	var payload any = teiRequest{Inputs: inputs}
	if t.provider == embeddingsAPIProvider {
		req := embeddingsAPIRequest{Model: t.model, Input: inputs, EncodingFormat: "float"}
		if t.sendDimensions {
			req.Dimensions = teiDimension
		}
		payload = req
	}
	body, status, err := postJSON(ctx, t.client, t.endpoint, t.apiKey, payload, t.maxRequest, t.maxResponse)
	if err != nil {
		return nil, usage, err
	}
	if status != http.StatusOK {
		return nil, usage, dependencyStatusError(t.provider+" embeddings", status)
	}
	// encoding/json turns null numeric components into zero; a numeric vector must not contain null.
	if bytes.Contains(body, []byte("null")) {
		return nil, usage, fmt.Errorf("embeddings: unreadable response: %w", ErrUnavailable)
	}
	if t.provider != embeddingsAPIProvider {
		var vectors [][]float64
		if err := json.Unmarshal(body, &vectors); err != nil {
			return nil, usage, fmt.Errorf("tei embeddings: unreadable response: %w", ErrUnavailable)
		}
		return vectors, usage, nil
	}
	var res embeddingsAPIResponse
	if err := json.Unmarshal(body, &res); err != nil || len(res.Data) != len(inputs) {
		return nil, usage, fmt.Errorf("embeddings: unreadable response: %w", ErrUnavailable)
	}
	vectors := make([][]float64, len(inputs))
	for _, d := range res.Data {
		if d.Index == nil || *d.Index < 0 || *d.Index >= len(inputs) || vectors[*d.Index] != nil {
			return nil, usage, fmt.Errorf("embeddings: malformed index: %w", ErrUnavailable)
		}
		vectors[*d.Index] = d.Embedding
	}
	if res.Usage.PromptTokens >= 0 && res.Usage.TotalTokens >= res.Usage.PromptTokens {
		usage.InputTokens, usage.TotalTokens = res.Usage.PromptTokens, res.Usage.TotalTokens
	}
	return vectors, usage, nil
}

// EmbedMeasured embeds texts and reports the provider's token count when it sends one.
func (t *TEIEmbedder) EmbedMeasured(ctx context.Context, texts []string, purpose string) (EmbeddingBatch, error) {
	prefix := ""
	switch purpose {
	case embedInputQuery:
		prefix = t.queryPrefix
	case embedInputDocument:
		prefix = t.documentPrefix
	default:
		return EmbeddingBatch{}, fmt.Errorf("tei purpose must be query or document: %w", ErrInvalid)
	}
	if len(texts) == 0 || len(texts) > t.maxBatch {
		return EmbeddingBatch{}, fmt.Errorf("tei requires 1 to %d inputs per request: %w", t.maxBatch, ErrInvalid)
	}
	inputs := make([]string, len(texts))
	for i, text := range texts {
		if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
			return EmbeddingBatch{}, fmt.Errorf("tei input text must be non-empty UTF-8: %w", ErrInvalid)
		}
		inputs[i] = prefix + text
	}
	vectors, usage, err := t.post(ctx, inputs)
	if err != nil {
		return EmbeddingBatch{Usage: usage}, err
	}
	if len(vectors) != len(texts) {
		return EmbeddingBatch{Usage: usage}, fmt.Errorf("embeddings: expected %d vectors, got %d: %w", len(texts), len(vectors), ErrUnavailable)
	}
	now := time.Now().UTC()
	out := make([]Embedding, len(texts))
	for i, vector := range vectors {
		if err := checkEmbeddingVector(vector); err != nil {
			return EmbeddingBatch{Usage: usage}, err
		}
		sum := sha256.Sum256([]byte(texts[i]))
		out[i] = Embedding{
			Provider: t.provider, Model: t.model, Revision: t.revision,
			Dimension: teiDimension, Space: t.space, EmbeddedAt: now,
			ContentHash: hex.EncodeToString(sum[:]), Vector: vector,
		}
	}
	return EmbeddingBatch{Items: out, Usage: usage}, nil
}

func checkEmbeddingVector(vector []float64) error {
	if len(vector) != teiDimension {
		return fmt.Errorf("embeddings: expected dimension %d, got %d: %w", teiDimension, len(vector), ErrUnavailable)
	}
	nonzero := false
	for _, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("embeddings: non-finite value: %w", ErrUnavailable)
		}
		nonzero = nonzero || value != 0
	}
	if !nonzero {
		return fmt.Errorf("embeddings: zero vector: %w", ErrUnavailable)
	}
	return nil
}

var _ MeteredEmbedder = (*TEIEmbedder)(nil)
