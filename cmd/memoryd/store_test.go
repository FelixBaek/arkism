package main

import (
	"strings"
	"testing"
)

func TestStoreSelectionFailsClosed(t *testing.T) {
	t.Setenv("MEMORY_STORE", "")
	t.Setenv("MEMORY_DATABASE_URL", "")
	if _, err := configuredStore(); err == nil {
		t.Fatal("missing database URL accepted")
	}
	t.Setenv("MEMORY_DATABASE_URL", "postgres://localhost/local")
	if got, err := configuredStore(); err != nil || got != "postgres" {
		t.Fatal(got, err)
	}
	for _, retired := range []string{"atlas", "postgre"} {
		t.Setenv("MEMORY_STORE", retired)
		if _, err := configuredStore(); err == nil {
			t.Fatal("non-Postgres store accepted:", retired)
		}
	}
}

func TestLocalEmbedderSelectionDoesNotFallBackToCloud(t *testing.T) {
	t.Setenv("MEMORY_EMBEDDER", "tei")
	t.Setenv("VOYAGE_API_KEY", "configured-but-must-not-be-used")
	t.Setenv("TEI_ENDPOINT_URL", "")
	if _, err := configuredEmbedder(true, false); err == nil {
		t.Fatal("missing TEI accepted")
	}
	t.Setenv("TEI_ENDPOINT_URL", "http://127.0.0.1:8089/embed")
	t.Setenv("TEI_MODEL", "explicit-model")
	t.Setenv("TEI_REVISION", "explicit-revision")
	t.Setenv("TEI_QUERY_PREFIX", "")
	t.Setenv("TEI_DOCUMENT_PREFIX", "")
	embed, err := configuredEmbedder(true, false)
	if err != nil || embed == nil || !strings.HasPrefix(embed.Space(), "tei:explicit-model:explicit-revision:1024d:") {
		t.Fatal("TEI not selected", err)
	}
	t.Setenv("TEI_QUERY_PREFIX", `Instruct: find\nQuery: `)
	escaped, err := configuredEmbedder(true, false)
	if err != nil || escaped.Space() == embed.Space() {
		t.Fatal("query prefix must be part of the space", err)
	}
	t.Setenv("TEI_QUERY_PREFIX", "Instruct: find\nQuery: ")
	if real, err := configuredEmbedder(true, false); err != nil || real.Space() != escaped.Space() {
		t.Fatal(`an escaped \n must equal a real newline`, err)
	}
	t.Setenv("MEMORY_EMBEDDER", "none")
	if embed, err = configuredEmbedder(true, false); err != nil || embed != nil {
		t.Fatal("none was ignored", err)
	}
}

func TestTEIBatchOverCeilingRefusedAtStartup(t *testing.T) {
	t.Setenv("MEMORY_EMBEDDER", "tei")
	t.Setenv("TEI_ENDPOINT_URL", "http://127.0.0.1:8089/embed")
	t.Setenv("TEI_MODEL", "explicit-model")
	t.Setenv("TEI_REVISION", "explicit-revision")
	t.Setenv("TEI_QUERY_PREFIX", "")
	t.Setenv("TEI_DOCUMENT_PREFIX", "")
	t.Setenv("TEI_MAX_BATCH", "5")
	if _, err := configuredEmbedder(true, false); err == nil {
		t.Fatal("batch=5 config must refuse to start")
	}
	t.Setenv("TEI_MAX_BATCH", "4")
	t.Setenv("TEI_SERVER_MAX_CLIENT_BATCH", "2")
	if _, err := configuredEmbedder(true, false); err == nil {
		t.Fatal("client batch above the server batch must refuse to start")
	}
	t.Setenv("TEI_SERVER_MAX_CLIENT_BATCH", "4")
	if _, err := configuredEmbedder(true, false); err != nil {
		t.Fatal("batch=4 across client, queue and server must start", err)
	}
	t.Setenv("TEI_MAX_BATCH", "not-a-number")
	if _, err := configuredEmbedder(true, false); err == nil {
		t.Fatal("unparsable batch config must refuse to start")
	}
}

func TestGeminiEmbedderIsAnExplicitExternalOptIn(t *testing.T) {
	t.Setenv("MEMORY_EMBEDDER", "gemini")
	t.Setenv("MEMORY_SEMANTIC_RETRIEVAL", "1")
	t.Setenv("MEMORY_EXTERNAL_EMBEDDER_OPTIN", "")
	if syntheticEmbedderAllowed() == nil {
		t.Fatal("synthetic mode accepted an external embedder without opt-in")
	}
	t.Setenv("MEMORY_EXTERNAL_EMBEDDER_OPTIN", "1")
	if err := syntheticEmbedderAllowed(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEMINI_API_KEY", "test-key")
	t.Setenv("GEMINI_EMBEDDING_REVISION", "")
	if _, err := configuredEmbedder(true, false); err == nil {
		t.Fatal("missing revision accepted")
	}
	t.Setenv("GEMINI_EMBEDDING_REVISION", "arkism-v1")
	t.Setenv("GEMINI_EMBEDDING_DIMENSION", "3072")
	if _, err := configuredEmbedder(true, false); err == nil {
		t.Fatal("dimension other than the vector column accepted")
	}
	t.Setenv("GEMINI_EMBEDDING_DIMENSION", "")
	embed, err := configuredEmbedder(true, false)
	if err != nil || embed.Space() != "gemini:gemini-embedding-2:arkism-v1:1024d" {
		t.Fatal("gemini not selected", err)
	}
}
