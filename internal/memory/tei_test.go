package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func teiVector(first float64) []float64 {
	vector := make([]float64, teiDimension)
	vector[0] = first
	return vector
}

func teiForTest(t *testing.T, endpoint string, mutate func(*TEIConfig)) *TEIEmbedder {
	t.Helper()
	cfg := TEIConfig{
		EndpointURL: endpoint, Model: "multilingual-e5-large", Revision: "sha-123",
		Prefixes: map[string]string{"query": "query: ", "document": "passage: "},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	embedder, err := NewTEIEmbedder(cfg)
	if err != nil {
		t.Fatalf("NewTEIEmbedder: %v", err)
	}
	return embedder
}

func TestTEIEmbedContractAndMetadata(t *testing.T) {
	var request teiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
			t.Errorf("request = %s %s, authorization = %q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		json.NewEncoder(w).Encode([][]float64{teiVector(1), teiVector(2)})
	}))
	defer srv.Close()

	embedder := teiForTest(t, srv.URL+"/embed", nil)
	got, err := embedder.Embed(context.Background(), []string{"alpha", "beta"}, "document")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(request.Inputs) != 2 || request.Inputs[0] != "passage: alpha" || request.Inputs[1] != "passage: beta" {
		t.Fatalf("inputs = %#v", request.Inputs)
	}
	if len(got) != 2 || got[0].Vector[0] != 1 || got[1].Vector[0] != 2 {
		t.Fatalf("embeddings = %#v", got)
	}
	sum := sha256.Sum256([]byte("alpha"))
	if got[0].ContentHash != hex.EncodeToString(sum[:]) {
		t.Errorf("content hash follows prefixed text: %q", got[0].ContentHash)
	}
	for _, item := range got {
		if item.Provider != "tei" || item.Model != "multilingual-e5-large" || item.Revision != "sha-123" || item.Dimension != 1024 || item.Space != embedder.Space() || item.EmbeddedAt.IsZero() {
			t.Errorf("metadata = %#v", item)
		}
	}
	if !strings.HasPrefix(embedder.Space(), "tei:multilingual-e5-large:sha-123:1024d:") {
		t.Errorf("space = %q", embedder.Space())
	}
}

func TestTEIPurposePrefixes(t *testing.T) {
	var input string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request teiRequest
		json.NewDecoder(r.Body).Decode(&request)
		input = request.Inputs[0]
		json.NewEncoder(w).Encode([][]float64{teiVector(1)})
	}))
	defer srv.Close()
	embedder := teiForTest(t, srv.URL+"/embed", nil)
	if _, err := embedder.Embed(context.Background(), []string{"찾기"}, "query"); err != nil || input != "query: 찾기" {
		t.Fatalf("query input = %q, err = %v", input, err)
	}
	if _, err := embedder.Embed(context.Background(), []string{"문서"}, "document"); err != nil || input != "passage: 문서" {
		t.Fatalf("document input = %q, err = %v", input, err)
	}
	if _, err := embedder.Embed(context.Background(), []string{"x"}, "other"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("purpose error = %v", err)
	}
}

func TestTEIRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	base := TEIConfig{EndpointURL: "http://127.0.0.1:8080/embed", Model: "model", Revision: "revision", Prefixes: map[string]string{"query": "", "document": ""}}
	for _, mutate := range []func(*TEIConfig){
		func(c *TEIConfig) { c.EndpointURL = "" },
		func(c *TEIConfig) { c.EndpointURL = "http://example.com/embed" },
		func(c *TEIConfig) { c.EndpointURL = "http://127.0.0.1:8080/" },
		func(c *TEIConfig) { c.EndpointURL += "?token=secret" },
		func(c *TEIConfig) { c.Model = "" },
		func(c *TEIConfig) { c.Revision = "" },
		func(c *TEIConfig) { delete(c.Prefixes, "query") },
		func(c *TEIConfig) { delete(c.Prefixes, "document") },
	} {
		cfg := base
		cfg.Prefixes = map[string]string{"query": "", "document": ""}
		mutate(&cfg)
		if _, err := NewTEIEmbedder(cfg); !errors.Is(err, ErrInvalid) {
			t.Errorf("config %#v: error = %v", cfg, err)
		}
	}
	if _, err := NewTEIEmbedder(TEIConfig{EndpointURL: "https://embeddings.example/embed", Model: "model", Revision: "revision", Prefixes: base.Prefixes}); err != nil {
		t.Errorf("https endpoint rejected: %v", err)
	}
}

func TestTEIRejectsMalformedVectors(t *testing.T) {
	for _, tc := range []struct {
		name string
		body any
	}{
		{"missing", [][]float64{}},
		{"extra", [][]float64{teiVector(1), teiVector(2)}},
		{"wrong dimension", [][]float64{{1}}},
		{"zero", [][]float64{make([]float64, teiDimension)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(tc.body) }))
			defer srv.Close()
			_, err := teiForTest(t, srv.URL+"/embed", nil).Embed(context.Background(), []string{"x"}, "document")
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestTEIBoundsTimeoutAndRedirects(t *testing.T) {
	t.Run("response cap", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { strings.NewReader(strings.Repeat("x", 20)).WriteTo(w) }))
		defer srv.Close()
		embedder := teiForTest(t, srv.URL+"/embed", func(c *TEIConfig) { c.MaxResponseBytes = 10 })
		if _, err := embedder.Embed(context.Background(), []string{"x"}, "document"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("response cap error = %v", err)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(100 * time.Millisecond) }))
		defer srv.Close()
		embedder := teiForTest(t, srv.URL+"/embed", func(c *TEIConfig) { c.Timeout = 10 * time.Millisecond })
		if _, err := embedder.Embed(context.Background(), []string{"x"}, "document"); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("timeout error = %v", err)
		}
	})
	t.Run("redirect", func(t *testing.T) {
		var followed atomic.Bool
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Store(true) }))
		defer target.Close()
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/embed", http.StatusTemporaryRedirect)
		}))
		defer source.Close()
		_, err := teiForTest(t, source.URL+"/embed", nil).Embed(context.Background(), []string{"x"}, "document")
		if !errors.Is(err, ErrInvalid) || followed.Load() {
			t.Fatalf("redirect error = %v, followed = %v", err, followed.Load())
		}
	})
}

func TestTEISpaceChangesWithPrefixes(t *testing.T) {
	cfg := TEIConfig{EndpointURL: "http://127.0.0.1:8080/embed", Model: "e5", Revision: "r1", Prefixes: map[string]string{"query": "q:", "document": "d:"}}
	a, err := NewTEIEmbedder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Prefixes["document"] = "passage:"
	b, err := NewTEIEmbedder(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a.Space() == b.Space() {
		t.Fatal("prefix changes must isolate embedding spaces")
	}
}

func TestTEIBatchConfigValidation(t *testing.T) {
	if err := ValidateTEIBatchLimits(4, 4, 4); err != nil {
		t.Fatalf("matched ceilings must pass: %v", err)
	}
	for name, limits := range map[string][3]int{
		"client over ceiling": {5, 4, 5},
		"queue over ceiling":  {4, 5, 4},
		"server over ceiling": {4, 4, 5},
		"client over server":  {4, 4, 2},
		"queue over client":   {2, 4, 4},
		"zero client":         {0, 1, 4},
		"negative server":     {4, 4, -1},
	} {
		if err := ValidateTEIBatchLimits(limits[0], limits[1], limits[2]); err == nil {
			t.Fatalf("%s: expected rejection", name)
		} else if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: wrong error: %v", name, err)
		}
	}
	if _, err := NewTEIEmbedder(TEIConfig{
		EndpointURL: "http://127.0.0.1:9/embed", Model: "m", Revision: "r",
		Prefixes: map[string]string{"query": "", "document": ""}, MaxBatchSize: 5,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("batch=5 config must be refused at construction, got %v", err)
	}
	embedder := teiForTest(t, "http://127.0.0.1:9/embed", nil)
	if _, err := embedder.Embed(context.Background(), make([]string, 5), "query"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("5-text embed call must be refused, got %v", err)
	}
}
