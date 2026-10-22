package memory

import (
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
	"unicode/utf8"
)

const (
	ollamaProviderName = "ollama"
	// OllamaDefaultModel outputs VectorDimension (1024) vectors; it is the
	// Qwen3-Embedding 0.6B model, the same family as a Qwen3 TEI deployment.
	OllamaDefaultModel = "qwen3-embedding:0.6b"
	// OllamaDefaultQueryPrefix is the Qwen3-Embedding instruction a TEI
	// deployment would use as TEI_QUERY_PREFIX. Documents take no prefix.
	OllamaDefaultQueryPrefix    = "Instruct: Given a user request, retrieve the stored memories, rules and skills that help answer it\nQuery: "
	OllamaDefaultDocumentPrefix = ""
	// OllamaMaxBatchSize caps the inputs of one /api/embed call. Ollama has no
	// TEI-style crash ceiling, but a desktop CPU should not get an unbounded batch.
	OllamaMaxBatchSize = 16
	// A cold call loads the model first, so the default timeout is longer than
	// the shared provider default.
	ollamaDefaultTimeout = 60 * time.Second
	ollamaEmbedPath      = "/api/embed"
)

// OllamaConfig configures a local Ollama server's /api/embed endpoint. The
// adapter does not pull models; the model must already be present.
type OllamaConfig struct {
	// EndpointURL is the loopback http base URL, e.g. http://127.0.0.1:11434.
	EndpointURL string
	// Model defaults to OllamaDefaultModel.
	Model string
	// Prefixes must contain explicit query and document entries; an empty value
	// is valid.
	Prefixes         map[string]string
	MaxBatchSize     int
	Timeout          time.Duration
	MaxRequestBytes  int64
	MaxResponseBytes int64
	Client           *http.Client
}

type OllamaEmbedder struct {
	endpoint, model, queryPrefix, documentPrefix, space string
	maxBatch                                            int
	maxRequest, maxResponse                             int64
	client                                              *http.Client
}

func NewOllamaEmbedder(cfg OllamaConfig) (*OllamaEmbedder, error) {
	endpoint, err := ollamaEmbedURL(cfg.EndpointURL)
	if err != nil {
		return nil, err
	}
	maxBatch := cfg.MaxBatchSize
	if maxBatch == 0 {
		maxBatch = OllamaMaxBatchSize
	}
	if maxBatch < 1 || maxBatch > OllamaMaxBatchSize {
		return nil, fmt.Errorf("ollama batch size %d outside 1..%d: %w", maxBatch, OllamaMaxBatchSize, ErrInvalid)
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = OllamaDefaultModel
	}
	if !validTEIMetadata(model) {
		return nil, fmt.Errorf("ollama model is invalid: %w", ErrInvalid)
	}
	queryPrefix, hasQuery := cfg.Prefixes[embedInputQuery]
	documentPrefix, hasDocument := cfg.Prefixes[embedInputDocument]
	if !hasQuery || !hasDocument {
		return nil, fmt.Errorf("ollama query and document prefixes must be explicit: %w", ErrInvalid)
	}
	prefixJSON, _ := json.Marshal([]string{queryPrefix, documentPrefix})
	prefixHash := sha256.Sum256(prefixJSON)
	timeout := cfg.Timeout
	if timeout <= 0 && (cfg.Client == nil || cfg.Client.Timeout <= 0) {
		timeout = ollamaDefaultTimeout
	}
	return &OllamaEmbedder{
		endpoint: endpoint, model: model,
		queryPrefix: queryPrefix, documentPrefix: documentPrefix,
		// The provider name keeps this space apart from a TEI model even though
		// both are 1024-dimensional Qwen3 models: vectors from different
		// weights and runtimes are never compared.
		space:       fmt.Sprintf("%s:%s:%dd:%x", ollamaProviderName, model, VectorDimension, prefixHash[:]),
		maxBatch:    maxBatch,
		maxRequest:  positiveOr(cfg.MaxRequestBytes, defaultRequestMax),
		maxResponse: positiveOr(cfg.MaxResponseBytes, defaultResponseMax),
		client:      providerClient(cfg.Client, timeout),
	}, nil
}

// ollamaEmbedURL accepts only a loopback http base URL: text never leaves the
// host, so this provider needs no external opt-in.
func ollamaEmbedURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || !isLoopbackHost(parsed.Hostname()) {
		return "", fmt.Errorf("ollama endpoint must be a loopback http URL: %w", ErrInvalid)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("ollama endpoint must be the server base URL: %w", ErrInvalid)
	}
	parsed.Path = ollamaEmbedPath
	return parsed.String(), nil
}

func (o *OllamaEmbedder) Space() string { return o.space }

type ollamaRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaResponse struct {
	Embeddings      [][]*float64 `json:"embeddings"`
	PromptEvalCount int64        `json:"prompt_eval_count"`
}

func (o *OllamaEmbedder) Embed(ctx context.Context, texts []string, purpose string) ([]Embedding, error) {
	batch, err := o.EmbedMeasured(ctx, texts, purpose)
	return batch.Items, err
}

func (o *OllamaEmbedder) EmbedMeasured(ctx context.Context, texts []string, purpose string) (EmbeddingBatch, error) {
	prefix := ""
	switch purpose {
	case embedInputQuery:
		prefix = o.queryPrefix
	case embedInputDocument:
		prefix = o.documentPrefix
	default:
		return EmbeddingBatch{}, fmt.Errorf("ollama purpose must be query or document: %w", ErrInvalid)
	}
	if len(texts) == 0 || len(texts) > o.maxBatch {
		return EmbeddingBatch{}, fmt.Errorf("ollama requires 1 to %d inputs per request: %w", o.maxBatch, ErrInvalid)
	}
	inputs := make([]string, len(texts))
	for i, text := range texts {
		if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
			return EmbeddingBatch{}, fmt.Errorf("ollama input text must be non-empty UTF-8: %w", ErrInvalid)
		}
		inputs[i] = prefix + text
	}
	body, status, err := postJSON(ctx, o.client, o.endpoint, "", ollamaRequest{Model: o.model, Input: inputs}, o.maxRequest, o.maxResponse)
	if err != nil {
		return EmbeddingBatch{}, err
	}
	if status != http.StatusOK {
		return EmbeddingBatch{}, dependencyStatusError("ollama embeddings", status)
	}
	var decoded ollamaResponse
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.PromptEvalCount < 0 {
		return EmbeddingBatch{}, fmt.Errorf("ollama embeddings: unreadable response: %w", ErrUnavailable)
	}
	if len(decoded.Embeddings) != len(texts) {
		return EmbeddingBatch{}, fmt.Errorf("ollama embeddings: expected %d vectors, got %d: %w", len(texts), len(decoded.Embeddings), ErrUnavailable)
	}
	now := time.Now().UTC()
	out := make([]Embedding, len(texts))
	for i, raw := range decoded.Embeddings {
		if len(raw) != VectorDimension {
			return EmbeddingBatch{}, fmt.Errorf("ollama embeddings: expected dimension %d, got %d: %w", VectorDimension, len(raw), ErrUnavailable)
		}
		vector, err := decodeOllamaVector(raw)
		if err != nil {
			return EmbeddingBatch{}, err
		}
		sum := sha256.Sum256([]byte(texts[i]))
		out[i] = Embedding{
			Provider: ollamaProviderName, Model: o.model,
			Dimension: VectorDimension, Space: o.space, EmbeddedAt: now,
			ContentHash: hex.EncodeToString(sum[:]), Vector: vector,
		}
	}
	usage := ProviderUsage{Provider: ollamaProviderName, Model: o.model, Requests: 1, InputTokens: decoded.PromptEvalCount, TotalTokens: decoded.PromptEvalCount}
	return EmbeddingBatch{Items: out, Usage: usage}, nil
}

func decodeOllamaVector(raw []*float64) ([]float64, error) {
	vector := make([]float64, len(raw))
	nonzero := false
	for j, value := range raw {
		// encoding/json would turn a null component into zero.
		if value == nil || math.IsNaN(*value) || math.IsInf(*value, 0) {
			return nil, fmt.Errorf("ollama embeddings: non-finite value: %w", ErrUnavailable)
		}
		vector[j] = *value
		nonzero = nonzero || *value != 0
	}
	if !nonzero {
		return nil, fmt.Errorf("ollama embeddings: zero vector: %w", ErrUnavailable)
	}
	return vector, nil
}

var _ MeteredEmbedder = (*OllamaEmbedder)(nil)
