package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// embeddingsAPIServer answers the embeddings protocol with
// the data rows reversed, so the client must order them by index.
func embeddingsAPIServer(t *testing.T, got *embeddingsAPIRequest, auth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(got); err != nil || r.URL.Path != "/v1/embeddings" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		type row struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		}
		var data []row
		for i := len(got.Input) - 1; i >= 0; i-- {
			data = append(data, row{i, teiVector(float64(i + 1))})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "usage": map[string]int{"prompt_tokens": 7, "total_tokens": 7}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestEmbeddingsAPIProtocol(t *testing.T) {
	var req embeddingsAPIRequest
	var auth string
	srv := embeddingsAPIServer(t, &req, &auth)
	e := teiForTest(t, srv.URL+"/v1/embeddings", func(c *TEIConfig) { c.APIKey = "key-1"; c.SendDimensions = true })
	batch, err := e.EmbedMeasured(testCtx, []string{"a", "b"}, "query")
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer key-1" || req.Model != "multilingual-e5-large" || req.Dimensions != 1024 || req.EncodingFormat != "float" || req.Input[0] != "query: a" {
		t.Fatalf("request %+v auth %q", req, auth)
	}
	if batch.Items[0].Vector[0] != 1 || batch.Items[1].Vector[0] != 2 || batch.Usage.InputTokens != 7 || batch.Usage.Provider != embeddingsAPIProvider {
		t.Fatalf("batch order or usage %+v", batch.Usage)
	}
	if batch.Items[0].Provider != embeddingsAPIProvider || !e.Local() {
		t.Fatal("provider identity or locality", batch.Items[0].Provider)
	}
	if teiForTest(t, srv.URL+"/embed", nil).Space() == e.Space() {
		t.Fatal("protocols share a vector space")
	}
	for _, bad := range []string{srv.URL + "/v1/embedding", srv.URL + "/embed/x", "http://example.com/v1/embeddings"} {
		if _, err := NewTEIEmbedder(TEIConfig{EndpointURL: bad, Model: "m", Revision: "r", Prefixes: map[string]string{"query": "", "document": ""}}); !errors.Is(err, ErrInvalid) {
			t.Fatal("accepted endpoint", bad)
		}
	}
}

func TestEmbeddingsAPIRejectsBadIndexes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"index": 0, "embedding": teiVector(1)}, {"index": 0, "embedding": teiVector(2)}}})
	}))
	defer srv.Close()
	if _, err := teiForTest(t, srv.URL+"/embeddings", nil).Embed(testCtx, []string{"a", "b"}, "document"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("duplicate index accepted", err)
	}
}

func TestHostedRerankProtocol(t *testing.T) {
	var got documentRerankRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" || r.Header.Get("Authorization") != "Bearer rk" || json.NewDecoder(r.Body).Decode(&got) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"index": 1, "relevance_score": 0.9}, {"index": 0, "relevance_score": 0.2}}})
	}))
	defer srv.Close()
	rr, err := NewTEIReranker(TEIRerankConfig{URL: srv.URL + "/v1", Model: "rerank-model", APIKey: "rk"})
	if err != nil {
		t.Fatal(err)
	}
	scores, err := rr.Rerank(testCtx, "q", []string{"x", "y"})
	if err != nil || scores[0] != 0.2 || scores[1] != 0.9 {
		t.Fatal(scores, err)
	}
	if got.Model != "rerank-model" || got.Query != "q" || len(got.Documents) != 2 || got.TopN != 2 {
		t.Fatalf("request %+v", got)
	}
	if u := rr.usage(); u.Provider != "rerank_api" || u.Model != "rerank-model" {
		t.Fatal("usage", u)
	}
}

func TestEmbedAndRerankCallsAreTraced(t *testing.T) {
	f := newFixture(t)
	var req embeddingsAPIRequest
	var auth string
	f.svc.Embedder = teiForTest(t, embeddingsAPIServer(t, &req, &auth).URL+"/v1/embeddings", nil)
	if _, callErr, err := f.svc.meteredEmbed(testCtx, scopeA, "embedding_query", []string{"hello"}, "query"); callErr != nil || err != nil {
		t.Fatal(callErr, err)
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer down.Close()
	f.svc.Reranker, _ = NewTEIReranker(TEIRerankConfig{URL: down.URL})
	if _, callErr, err := f.svc.meteredRerank(testCtx, scopeA, "q", []string{"a", "b"}); callErr == nil || err != nil {
		t.Fatal("rerank failure", callErr, err)
	}
	embeds, err := f.svc.ListLLMRequests(testCtx, f.human, scopeA, LLMRequestFilter{Operation: "embedding_query"})
	if err != nil || len(embeds.Items) != 1 || embeds.Items[0].Request.InputTokens != 7 || embeds.Items[0].Request.Status != "ok" || embeds.Items[0].Request.Prompt != "" {
		t.Fatal("embed trace", err, embeds)
	}
	reranks, _ := f.svc.ListLLMRequests(testCtx, f.human, scopeA, LLMRequestFilter{Operation: "rerank"})
	if len(reranks.Items) != 1 || reranks.Items[0].Request.Status != "error" {
		t.Fatal("rerank trace", reranks)
	}
}

// traceFailRepo refuses every model call trace write.
type traceFailRepo struct{ *fakeRepo }

func (r traceFailRepo) Insert(ctx context.Context, col string, rec Record) error {
	if col == colLLMRequests {
		return ErrUnavailable
	}
	return r.fakeRepo.Insert(ctx, col, rec)
}

// The extra trace write of a search is best effort, and without
// LLMTraceContent it keeps sizes and hashes, never the query.
func TestSearchTraceWriteIsBestEffortAndContentFree(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("trace 기억 searchable", "trace")
	f.svc.Repo = traceFailRepo{f.repo}
	if out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "trace"}); err != nil || len(out.Items) != 1 {
		t.Fatal("search failed with the trace write", out, err)
	}
	f.svc.Repo = f.repo
	if _, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "trace QZX77"}); err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.ListLLMRequests(testCtx, f.human, scopeA, LLMRequestFilter{Operation: "embedding_query"})
	if err != nil || len(page.Items) == 0 {
		t.Fatal("search embed not traced", page, err)
	}
	for _, it := range page.Items {
		if it.Request.Prompt != "" || it.Request.Response != "" || it.Request.PromptSHA256 == "" {
			t.Fatalf("content stored without LLMTraceContent %+v", it.Request)
		}
	}
}
