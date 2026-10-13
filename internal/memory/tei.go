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
	teiDimension    = 1024
	// TeiMaxBatchSize is the hard client-batch ceiling for the TEI CPU
	// backend: it forces max_batch_requests=4 and dies with exit 139 (bounded
	// queue panic) when an in-flight batch overflows it. The same ceiling is
	// validated for the client batch, the service-side index batch and the
	// server's max-client-batch-size.
	TeiMaxBatchSize = 4
)

// TEIConfig configures an already-running Hugging Face Text Embeddings
// Inference /embed endpoint. The adapter does not select or deploy weights.
type TEIConfig struct {
	EndpointURL string
	Model       string
	Revision    string
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
	if err := validateTEIEndpoint(cfg.EndpointURL); err != nil {
		return nil, err
	}
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
		space:       fmt.Sprintf("%s:%s:%s:%dd:%x", teiProviderName, model, revision, teiDimension, prefixHash[:]),
		maxBatch:    maxBatch,
		maxRequest:  positiveOr(cfg.MaxRequestBytes, defaultRequestMax),
		maxResponse: positiveOr(cfg.MaxResponseBytes, defaultResponseMax),
		client:      providerClient(cfg.Client, cfg.Timeout),
	}, nil
}

func validTEIMetadata(value string) bool {
	return value != "" && len(value) <= 256 && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validateTEIEndpoint(raw string) error {
	if err := validateEndpoint(raw); err != nil {
		return fmt.Errorf("tei endpoint: %w", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Path != "/embed" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return fmt.Errorf("tei endpoint must be the exact /embed URL: %w", ErrInvalid)
	}
	return nil
}

func (t *TEIEmbedder) Space() string { return t.space }

type teiRequest struct {
	Inputs []string `json:"inputs"`
}

func (t *TEIEmbedder) Embed(ctx context.Context, texts []string, purpose string) ([]Embedding, error) {
	prefix := ""
	switch purpose {
	case embedInputQuery:
		prefix = t.queryPrefix
	case embedInputDocument:
		prefix = t.documentPrefix
	default:
		return nil, fmt.Errorf("tei purpose must be query or document: %w", ErrInvalid)
	}
	if len(texts) == 0 || len(texts) > t.maxBatch {
		return nil, fmt.Errorf("tei requires 1 to %d inputs per request: %w", t.maxBatch, ErrInvalid)
	}
	inputs := make([]string, len(texts))
	for i, text := range texts {
		if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("tei input text must be non-empty UTF-8: %w", ErrInvalid)
		}
		inputs[i] = prefix + text
	}
	body, status, err := postJSON(ctx, t.client, t.endpoint, "", teiRequest{Inputs: inputs}, t.maxRequest, t.maxResponse)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, dependencyStatusError("tei embeddings", status)
	}
	var vectors [][]float64
	// encoding/json turns null numeric components into zero; a numeric vector must not contain null.
	if err := json.Unmarshal(body, &vectors); err != nil || bytes.Contains(body, []byte("null")) {
		return nil, fmt.Errorf("tei embeddings: unreadable response: %w", ErrUnavailable)
	}
	if len(vectors) != len(texts) {
		return nil, fmt.Errorf("tei embeddings: expected %d vectors, got %d: %w", len(texts), len(vectors), ErrUnavailable)
	}
	now := time.Now().UTC()
	out := make([]Embedding, len(texts))
	for i, vector := range vectors {
		if len(vector) != teiDimension {
			return nil, fmt.Errorf("tei embeddings: expected dimension %d, got %d: %w", teiDimension, len(vector), ErrUnavailable)
		}
		nonzero := false
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("tei embeddings: non-finite value: %w", ErrUnavailable)
			}
			nonzero = nonzero || value != 0
		}
		if !nonzero {
			return nil, fmt.Errorf("tei embeddings: zero vector: %w", ErrUnavailable)
		}
		sum := sha256.Sum256([]byte(texts[i]))
		out[i] = Embedding{
			Provider: teiProviderName, Model: t.model, Revision: t.revision,
			Dimension: teiDimension, Space: t.space, EmbeddedAt: now,
			ContentHash: hex.EncodeToString(sum[:]), Vector: vector,
		}
	}
	return out, nil
}

var _ Embedder = (*TEIEmbedder)(nil)
