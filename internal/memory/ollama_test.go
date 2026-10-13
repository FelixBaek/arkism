package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func ollamaVector(first float64) []float64 {
	vector := make([]float64, VectorDimension)
	vector[0] = first
	return vector
}

func ollamaForTest(t *testing.T, endpoint string, mutate func(*OllamaConfig)) *OllamaEmbedder {
	t.Helper()
	cfg := OllamaConfig{
		EndpointURL: endpoint,
		Prefixes:    map[string]string{"query": OllamaDefaultQueryPrefix, "document": OllamaDefaultDocumentPrefix},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	embedder, err := NewOllamaEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewOllamaEmbedder: %v", err)
	}
	return embedder
}

func ollamaServer(t *testing.T, handler func(ollamaRequest) (int, any)) (*httptest.Server, *[]ollamaRequest) {
	t.Helper()
	var requests []ollamaRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
			t.Errorf("request = %s %s, authorization = %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var request ollamaRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		requests = append(requests, request)
		status, body := handler(request)
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &requests
}

func TestOllamaEmbedHappyPath(t *testing.T) {
	srv, requests := ollamaServer(t, func(req ollamaRequest) (int, any) {
		vectors := make([][]float64, len(req.Input))
		for i := range vectors {
			vectors[i] = ollamaVector(float64(i + 1))
		}
		return http.StatusOK, map[string]any{"model": req.Model, "embeddings": vectors, "prompt_eval_count": 7, "total_duration": 1}
	})
	embedder := ollamaForTest(t, srv.URL, nil)
	batch, err := embedder.EmbedMeasured(context.Background(), []string{"alpha", "beta"}, "document")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	got := (*requests)[0]
	if got.Model != "qwen3-embedding:0.6b" || len(got.Input) != 2 || got.Input[0] != "alpha" || got.Input[1] != "beta" {
		t.Fatalf("request = %#v", got)
	}
	if len(batch.Items) != 2 || batch.Items[0].Vector[0] != 1 || batch.Items[1].Vector[0] != 2 {
		t.Fatalf("items = %d", len(batch.Items))
	}
	for _, item := range batch.Items {
		if item.Provider != "ollama" || item.Model != "qwen3-embedding:0.6b" || item.Dimension != VectorDimension || item.Space != embedder.Space() || item.EmbeddedAt.IsZero() || item.ContentHash == "" {
			t.Errorf("metadata = %+v", item.Space)
		}
	}
	if batch.Usage.Provider != "ollama" || batch.Usage.Requests != 1 || batch.Usage.InputTokens != 7 || !validProviderUsage(batch.Usage) {
		t.Errorf("usage = %+v", batch.Usage)
	}
	if !strings.HasPrefix(embedder.Space(), "ollama:qwen3-embedding:0.6b:1024d:") {
		t.Errorf("space = %q", embedder.Space())
	}
	tei := teiForTest(t, "http://127.0.0.1:8093/embed", func(c *TEIConfig) {
		c.Model, c.Revision = "qwen3-embedding:0.6b", "x"
		c.Prefixes = map[string]string{"query": OllamaDefaultQueryPrefix, "document": ""}
	})
	if tei.Space() == embedder.Space() {
		t.Fatal("ollama and tei must never share a space")
	}
}

func TestOllamaPrefixAppliedToQueriesOnly(t *testing.T) {
	srv, requests := ollamaServer(t, func(ollamaRequest) (int, any) {
		return http.StatusOK, map[string]any{"embeddings": [][]float64{ollamaVector(1)}}
	})
	embedder := ollamaForTest(t, srv.URL+"/", nil)
	if _, err := embedder.Embed(context.Background(), []string{"찾기"}, "query"); err != nil {
		t.Fatal(err)
	}
	if _, err := embedder.Embed(context.Background(), []string{"문서"}, "document"); err != nil {
		t.Fatal(err)
	}
	if got := (*requests)[0].Input[0]; got != OllamaDefaultQueryPrefix+"찾기" || !strings.Contains(got, "\nQuery: ") {
		t.Fatalf("query input = %q", got)
	}
	if got := (*requests)[1].Input[0]; got != "문서" {
		t.Fatalf("document input = %q", got)
	}
	if _, err := embedder.Embed(context.Background(), []string{"x"}, "other"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("purpose error = %v", err)
	}
	other := ollamaForTest(t, srv.URL, func(c *OllamaConfig) { c.Prefixes = map[string]string{"query": "", "document": ""} })
	if other.Space() == embedder.Space() {
		t.Fatal("prefix changes must isolate embedding spaces")
	}
}

func TestOllamaRejectsMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		body any
	}{
		{"wrong dimension", map[string]any{"embeddings": [][]float64{{1, 2, 3}}}},
		{"dimension 1025", map[string]any{"embeddings": [][]float64{append(ollamaVector(1), 1)}}},
		{"count missing", map[string]any{"embeddings": [][]float64{}}},
		{"count extra", map[string]any{"embeddings": [][]float64{ollamaVector(1), ollamaVector(2)}}},
		{"no embeddings field", map[string]any{"embedding": ollamaVector(1)}},
		{"null component", map[string]any{"embeddings": [][]any{append([]any{nil}, make([]any, VectorDimension-1)...)}}},
		{"zero", map[string]any{"embeddings": [][]float64{make([]float64, VectorDimension)}}},
		{"not json", "x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := ollamaServer(t, func(ollamaRequest) (int, any) { return http.StatusOK, tc.body })
			_, err := ollamaForTest(t, srv.URL, nil).Embed(context.Background(), []string{"x"}, "document")
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestOllamaMapsHTTPErrors(t *testing.T) {
	for status, want := range map[int]error{http.StatusInternalServerError: ErrUnavailable, http.StatusTooManyRequests: ErrUnavailable, http.StatusNotFound: ErrInvalid} {
		srv, _ := ollamaServer(t, func(ollamaRequest) (int, any) { return status, map[string]string{"error": "model not found"} })
		_, err := ollamaForTest(t, srv.URL, nil).Embed(context.Background(), []string{"x"}, "query")
		if !errors.Is(err, want) || strings.Contains(err.Error(), "model not found") {
			t.Errorf("status %d: error = %v", status, err)
		}
	}
}

func TestOllamaTimeoutAndContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(200 * time.Millisecond) }))
	defer srv.Close()
	embedder := ollamaForTest(t, srv.URL, func(c *OllamaConfig) { c.Timeout = 10 * time.Millisecond })
	if _, err := embedder.Embed(context.Background(), []string{"x"}, "document"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("timeout error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ollamaForTest(t, srv.URL, nil).Embed(ctx, []string{"x"}, "document"); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("canceled error = %v", err)
	}
}

func TestOllamaRejectsUnsafeConfigurationAndBatches(t *testing.T) {
	prefixes := map[string]string{"query": "", "document": ""}
	for _, endpoint := range []string{
		"", "http://example.com:11434", "https://127.0.0.1:11434", "http://10.0.0.5:11434",
		"http://127.0.0.1:11434/api/embed", "http://127.0.0.1:11434?x=1", "http://user:pw@127.0.0.1:11434", "127.0.0.1:11434",
	} {
		if _, err := NewOllamaEmbedder(OllamaConfig{EndpointURL: endpoint, Prefixes: prefixes}); !errors.Is(err, ErrInvalid) {
			t.Errorf("endpoint %q: error = %v", endpoint, err)
		}
	}
	for _, ok := range []string{"http://127.0.0.1:11434", "http://localhost:11434/", "http://[::1]:11434"} {
		if _, err := NewOllamaEmbedder(OllamaConfig{EndpointURL: ok, Prefixes: prefixes}); err != nil {
			t.Errorf("endpoint %q rejected: %v", ok, err)
		}
	}
	if _, err := NewOllamaEmbedder(OllamaConfig{EndpointURL: "http://127.0.0.1:11434", Prefixes: map[string]string{"query": ""}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing document prefix accepted: %v", err)
	}
	if _, err := NewOllamaEmbedder(OllamaConfig{EndpointURL: "http://127.0.0.1:11434", Prefixes: prefixes, MaxBatchSize: OllamaMaxBatchSize + 1}); !errors.Is(err, ErrInvalid) {
		t.Errorf("oversized batch accepted: %v", err)
	}
	embedder := ollamaForTest(t, "http://127.0.0.1:9", func(c *OllamaConfig) { c.MaxBatchSize = 2 })
	for _, texts := range [][]string{nil, {"a", "b", "c"}, {" "}} {
		if _, err := embedder.Embed(context.Background(), texts, "document"); !errors.Is(err, ErrInvalid) {
			t.Errorf("texts %q: error = %v", texts, err)
		}
	}
	if KnowledgeIndexBatch > OllamaMaxBatchSize {
		t.Fatal("the knowledge index batch must fit one ollama call")
	}
}
