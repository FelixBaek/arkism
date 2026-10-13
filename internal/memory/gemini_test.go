package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiEmbedderRequestAndResponseContract(t *testing.T) {
	var got struct {
		Requests []struct {
			Model   string `json:"model"`
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			OutputDimensionality int `json:"outputDimensionality"`
		} `json:"requests"`
	}
	status, vectors := http.StatusOK, 2
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-embedding-2:batchEmbedContents" || r.Header.Get("x-goog-api-key") != "k" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		values := make([]float64, VectorDimension)
		values[0] = 1
		out := map[string]any{"embeddings": []any{}}
		for i := 0; i < vectors; i++ {
			out["embeddings"] = append(out["embeddings"].([]any), map[string]any{"values": values})
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	g, err := NewGeminiEmbedder(GeminiConfig{BaseURL: srv.URL + "/v1beta", APIKey: "k", Revision: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := g.EmbedMeasured(context.Background(), []string{"a", "b"}, embedInputDocument)
	if err != nil || len(batch.Items) != 2 || batch.Items[1].Space != "gemini:gemini-embedding-2:r1:1024d" || batch.Usage.Provider != "gemini" {
		t.Fatal(err, batch.Usage)
	}
	if got.Requests[0].Model != "models/gemini-embedding-2" || got.Requests[0].OutputDimensionality != 1024 || got.Requests[1].Content.Parts[0].Text != "title: none | text: b" {
		t.Fatalf("request shape %+v", got)
	}
	if _, err = g.Embed(context.Background(), []string{"q"}, embedInputQuery); err == nil {
		t.Fatal("vector count mismatch accepted") // server still returns 2
	}
	if !strings.HasPrefix(got.Requests[0].Content.Parts[0].Text, "task: search result | query: ") {
		t.Fatal("query prefix missing")
	}
	status, vectors = http.StatusTooManyRequests, 1
	if _, err = g.Embed(context.Background(), []string{"q"}, embedInputQuery); !errors.Is(err, ErrUnavailable) {
		t.Fatal("rate limit must be a dependency outage", err)
	}
	if _, err = g.Embed(context.Background(), []string{"q"}, "classification"); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown input type accepted")
	}
}

func TestExternalEmbedderNeedsTransferDecision(t *testing.T) {
	g, _ := NewGeminiEmbedder(GeminiConfig{BaseURL: "https://example.invalid/v1beta", APIKey: "k", Revision: "r1"})
	if embedderTransferAllowed(g, Decision{}) || !embedderTransferAllowed(g, Decision{ExternalTransfer: true}) {
		t.Fatal("external transfer gate")
	}
	if !embedderTransferAllowed(&TEIEmbedder{}, Decision{}) {
		t.Fatal("loopback TEI must not need external transfer")
	}
	s := &Service{Embedder: g}
	if _, err := s.embedKnowledgeQuery(context.Background(), Decision{}, "q"); !errors.Is(err, ErrDenied) || knowledgeFallbackReason(err) != "embedder_denied" {
		t.Fatal("query left the host without transfer permission", err)
	}
}
