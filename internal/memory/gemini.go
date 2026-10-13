package memory

// GeminiEmbedder: text embeddings over the Gemini API batchEmbedContents.
// Fail closed like every provider adapter. gemini-embedding-2 takes no
// task_type, so query/document intent travels as the documented text prefix.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	geminiEndpointDefault = "https://generativelanguage.googleapis.com/v1beta"
	geminiModelDefault    = "gemini-embedding-2"
	geminiProviderName    = "gemini"
	geminiBatchMax        = 100
)

var geminiPrefixes = map[string]string{
	embedInputQuery:    "task: search result | query: ",
	embedInputDocument: "title: none | text: ",
}

type GeminiConfig struct {
	// BaseURL is the API root up to the version. Defaults to the Gemini API.
	BaseURL string
	APIKey  string
	Model   string
	// Revision is the caller-owned pipeline revision recorded on every
	// embedding. Required: bump it when model, prefix or chunking changes.
	Revision  string
	Dimension int
	Timeout   time.Duration
	Client    *http.Client
	// IPv4Only dials tcp4. Some hosts' IPv6 egress geolocates to a region the
	// Gemini API rejects ("User location is not supported").
	IPv4Only bool
}

type GeminiEmbedder struct {
	endpoint, apiKey, model, revision, space string
	dimension                                int
	client                                   *http.Client
}

func NewGeminiEmbedder(cfg GeminiConfig) (*GeminiEmbedder, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = geminiEndpointDefault
	}
	if err := validateEndpoint(base); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("gemini api key required: %w", ErrInvalid)
	}
	model := cfg.Model
	if model == "" {
		model = geminiModelDefault
	}
	revision := strings.TrimSpace(cfg.Revision)
	if revision == "" {
		return nil, fmt.Errorf("gemini pipeline revision required: %w", ErrInvalid)
	}
	dimension := cfg.Dimension
	if dimension == 0 {
		dimension = defaultDimension
	}
	// The Postgres vector column is fixed; MRL lets Gemini emit that size.
	if dimension != VectorDimension {
		return nil, fmt.Errorf("gemini output dimension must be %d: %w", VectorDimension, ErrInvalid)
	}
	return &GeminiEmbedder{
		endpoint: base + "/models/" + model + ":batchEmbedContents", apiKey: cfg.APIKey,
		model: model, revision: revision, dimension: dimension,
		space:  fmt.Sprintf("%s:%s:%s:%dd", geminiProviderName, model, revision, dimension),
		client: geminiClient(cfg),
	}, nil
}

func geminiClient(cfg GeminiConfig) *http.Client {
	client := providerClient(cfg.Client, cfg.Timeout)
	if cfg.IPv4Only && cfg.Client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		dialer := &net.Dialer{Timeout: 10 * time.Second}
		transport.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp4", addr)
		}
		client.Transport = transport
	}
	return client
}

func (g *GeminiEmbedder) Space() string { return g.space }

func (g *GeminiEmbedder) Embed(ctx context.Context, texts []string, inputType string) ([]Embedding, error) {
	batch, err := g.EmbedMeasured(ctx, texts, inputType)
	return batch.Items, err
}

func (g *GeminiEmbedder) EmbedMeasured(ctx context.Context, texts []string, inputType string) (EmbeddingBatch, error) {
	prefix, ok := geminiPrefixes[inputType]
	if !ok || len(texts) == 0 || len(texts) > geminiBatchMax {
		return EmbeddingBatch{}, fmt.Errorf("gemini embeddings: invalid input: %w", ErrInvalid)
	}
	type part struct {
		Text string `json:"text"`
	}
	type request struct {
		Model   string `json:"model"`
		Content struct {
			Parts []part `json:"parts"`
		} `json:"content"`
		OutputDimensionality int `json:"outputDimensionality"`
	}
	payload := struct {
		Requests []request `json:"requests"`
	}{}
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			return EmbeddingBatch{}, fmt.Errorf("gemini input text must not be empty: %w", ErrInvalid)
		}
		r := request{Model: "models/" + g.model, OutputDimensionality: g.dimension}
		r.Content.Parts = []part{{Text: prefix + text}}
		payload.Requests = append(payload.Requests, r)
	}
	encoded, err := json.Marshal(payload)
	if err != nil || int64(len(encoded)) > defaultRequestMax {
		return EmbeddingBatch{}, fmt.Errorf("gemini embeddings: request too large: %w", ErrInvalid)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return EmbeddingBatch{}, fmt.Errorf("gemini embeddings: build request: %w", ErrInvalid)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.apiKey)
	resp, err := g.client.Do(req)
	if err != nil {
		// Transport cause stays in the chain for reason-code classification.
		return EmbeddingBatch{}, fmt.Errorf("gemini embeddings: %w: %w", err, ErrUnavailable)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, defaultResponseMax+1))
	if err != nil || int64(len(body)) > defaultResponseMax {
		return EmbeddingBatch{}, fmt.Errorf("gemini embeddings: unreadable response: %w", ErrUnavailable)
	}
	if resp.StatusCode != http.StatusOK {
		return EmbeddingBatch{}, dependencyStatusError("gemini embeddings", resp.StatusCode)
	}
	var decoded struct {
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
	}
	if json.Unmarshal(body, &decoded) != nil || len(decoded.Embeddings) != len(texts) {
		return EmbeddingBatch{}, fmt.Errorf("gemini embeddings: expected %d vectors: %w", len(texts), ErrUnavailable)
	}
	embeddedAt := time.Now().UTC()
	out := make([]Embedding, len(texts))
	for i, text := range texts {
		vector := decoded.Embeddings[i].Values
		if len(vector) != g.dimension {
			return EmbeddingBatch{}, fmt.Errorf("gemini embeddings: expected dimension %d, got %d: %w", g.dimension, len(vector), ErrUnavailable)
		}
		for _, value := range vector {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return EmbeddingBatch{}, fmt.Errorf("gemini embeddings: non finite value: %w", ErrUnavailable)
			}
		}
		sum := sha256.Sum256([]byte(text))
		out[i] = Embedding{Provider: geminiProviderName, Model: g.model, Revision: g.revision, Dimension: g.dimension,
			Space: g.space, EmbeddedAt: embeddedAt, ContentHash: hex.EncodeToString(sum[:]), Vector: vector}
	}
	// batchEmbedContents returns no token count; requests are the metered unit.
	return EmbeddingBatch{Items: out, Usage: ProviderUsage{Provider: geminiProviderName, Model: g.model, Requests: 1}}, nil
}

var _ MeteredEmbedder = (*GeminiEmbedder)(nil)
