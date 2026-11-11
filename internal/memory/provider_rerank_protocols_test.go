package memory

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The passages protocol posts to the full URL and turns logits into 0..1 scores.
func TestRerankPassagesProtocol(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		if r.URL.Path != "/v1/retrieval/m/reranking" || r.Header.Get("Authorization") != "Bearer pk" || json.NewDecoder(r.Body).Decode(&got) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"rankings": []any{map[string]any{"index": 1, "logit": 2.0}, map[string]any{"index": 0, "logit": -1.0}}})
	}))
	defer srv.Close()
	rr, err := NewTEIReranker(TEIRerankConfig{URL: srv.URL + "/v1/retrieval/m/reranking", Model: "m", APIKey: "pk", Protocol: RerankPassages})
	if err != nil {
		t.Fatal(err)
	}
	scores, err := rr.Rerank(testCtx, "q", []string{"x", "y"})
	if err != nil || math.Abs(scores[1]-1/(1+math.Exp(-2))) > 1e-9 || math.Abs(scores[0]-1/(1+math.Exp(1))) > 1e-9 {
		t.Fatal(scores, err)
	}
	if got["query"].(map[string]any)["text"] != "q" || got["passages"].([]any)[1].(map[string]any)["text"] != "y" || got["truncate"] != "END" || got["model"] != "m" {
		t.Fatalf("request %v", got)
	}
	if u := rr.usage(); u.Provider != "rerank_passages" || u.Model != "m" {
		t.Fatal("usage", u)
	}
	if _, err := NewTEIReranker(TEIRerankConfig{URL: srv.URL, Protocol: RerankPassages}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a protocol needs a model", err)
	}
	if _, err := NewTEIReranker(TEIRerankConfig{URL: srv.URL, Model: "m", Protocol: "other"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown protocol", err)
	}
}

// The records protocol keys rows by id; the bearer token is re-read from its file on every call.
func TestRerankRecordsProtocolAndKeyFile(t *testing.T) {
	var got map[string]any
	var auth string
	reply := []any{map[string]any{"id": "1", "score": 0.8}, map[string]any{"id": "0", "score": 0.1}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, auth = nil, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"records": reply})
	}))
	defer srv.Close()
	keyFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(keyFile, []byte("t1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rr, err := NewTEIReranker(TEIRerankConfig{URL: srv.URL + "/v1/rankingConfigs/default:rank", Model: "rank-model", APIKeyFile: keyFile, Protocol: RerankRecords})
	if err != nil {
		t.Fatal(err)
	}
	scores, err := rr.Rerank(testCtx, "q", []string{"x", "y"})
	if err != nil || scores[0] != 0.1 || scores[1] != 0.8 || auth != "Bearer t1" {
		t.Fatal(scores, err, auth)
	}
	records := got["records"].([]any)
	if got["query"] != "q" || got["topN"] != float64(2) || records[1].(map[string]any)["id"] != "1" || records[1].(map[string]any)["content"] != "y" {
		t.Fatalf("request %v", got)
	}
	_ = os.WriteFile(keyFile, []byte("t2"), 0o600)
	if _, err := rr.Rerank(testCtx, "q", []string{"x", "y"}); err != nil || auth != "Bearer t2" {
		t.Fatal("rotated token", err, auth)
	}
	reply = []any{map[string]any{"id": "01", "score": 0.8}, map[string]any{"id": "0", "score": 0.1}}
	if _, err := rr.Rerank(testCtx, "q", []string{"x", "y"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a non-canonical id is refused", err)
	}
	_ = os.Remove(keyFile)
	if _, err := rr.Rerank(testCtx, "q", []string{"x", "y"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing key file", err)
	}
}

// A self-hosted reranker refuses more than 32 texts per request, so a longer list goes in batches.
func TestRerankBatchesLongLists(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in teiRerankRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Texts) > teiRerankBatch {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		calls++
		rows := make([]map[string]any, len(in.Texts))
		for i, text := range in.Texts {
			rows[i] = map[string]any{"index": i, "score": float64(len(text)) / 100}
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()
	rr, err := NewTEIReranker(TEIRerankConfig{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 2*teiRerankBatch+3)
	for i := range texts {
		texts[i] = string(make([]byte, i+1))
	}
	scores, err := rr.Rerank(testCtx, "q", texts)
	if err != nil || len(scores) != len(texts) || calls != 3 {
		t.Fatal(len(scores), calls, err)
	}
	for i, s := range scores {
		if math.Abs(s-float64(i+1)/100) > 1e-9 {
			t.Fatalf("score %d = %v", i, s)
		}
	}
}
