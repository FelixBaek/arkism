package memory

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The embed_texts protocol sends the purpose as input_type and reads the float
// vectors; echoed input text that contains "null" is not mistaken for a null vector.
func TestEmbedTextsProtocol(t *testing.T) {
	var got map[string]any
	vectors := any([]any{teiVector(1), teiVector(2)})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		if r.URL.Path != "/v2/embed" || r.Header.Get("Authorization") != "Bearer key" {
			t.Errorf("%s %v", r.URL.Path, r.Header)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"texts": got["texts"], "embeddings": map[string]any{"float": vectors},
			"meta": map[string]any{"billed_units": map[string]any{"input_tokens": 7}}})
	}))
	defer srv.Close()
	e := teiForTest(t, srv.URL+"/v2/embed", func(c *TEIConfig) {
		c.Protocol, c.APIKey, c.SendDimensions = EmbedTextsProtocol, "key", true
		c.Prefixes = map[string]string{"query": "", "document": ""}
	})
	batch, err := e.EmbedMeasured(testCtx, []string{"null pointer", "b"}, "query")
	if err != nil || len(batch.Items) != 2 || batch.Items[1].Vector[0] != 2 || batch.Usage.InputTokens != 7 || batch.Items[0].Provider != EmbedTextsProtocol {
		t.Fatalf("%+v %v", batch.Usage, err)
	}
	if got["input_type"] != "search_query" || got["output_dimension"] != float64(1024) || got["embedding_types"].([]any)[0] != "float" || got["texts"].([]any)[0] != "null pointer" {
		t.Fatalf("request = %v", got)
	}
	if _, err := e.Embed(testCtx, []string{"a"}, "document"); err == nil || got["input_type"] != "search_document" {
		t.Fatalf("document purpose: %v %v", got["input_type"], err)
	}
	vectors = []any{nil, teiVector(2)}
	if _, err := e.Embed(testCtx, []string{"a", "b"}, "document"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("null vector: %v", err)
	}
	if !strings.HasPrefix(e.Space(), "embed_texts:") {
		t.Fatalf("own vector space: %s", e.Space())
	}
}

// embed_passage is the embeddings shape with input_type query|passage and truncate END.
func TestEmbedPassageProtocol(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": teiVector(3)}},
			"usage": map[string]any{"prompt_tokens": 2, "total_tokens": 2}})
	}))
	defer srv.Close()
	e := teiForTest(t, srv.URL+"/v1/embeddings", func(c *TEIConfig) { c.Protocol = EmbedPassageProtocol })
	batch, err := e.EmbedMeasured(testCtx, []string{"a"}, "document")
	if err != nil || batch.Items[0].Vector[0] != 3 || batch.Usage.TotalTokens != 2 || batch.Items[0].Provider != EmbedPassageProtocol {
		t.Fatalf("%+v %v", batch, err)
	}
	if got["input_type"] != "passage" || got["truncate"] != "END" || got["model"] != "multilingual-e5-large" || got["input"].([]any)[0] != "passage: a" {
		t.Fatalf("request = %v", got)
	}
	if _, err := e.Embed(testCtx, []string{"a"}, "query"); err != nil || got["input_type"] != "query" {
		t.Fatalf("query purpose: %v %v", got, err)
	}
	if _, err := NewTEIEmbedder(TEIConfig{EndpointURL: srv.URL + "/x", Protocol: "other", Model: "m", Revision: "r", Prefixes: map[string]string{"query": "", "document": ""}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown protocol: %v", err)
	}
}
