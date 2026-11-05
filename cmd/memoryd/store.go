package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
)

// syntheticEmbedderAllowed keeps synthetic mode free of embedding providers
// unless the operator opts in explicitly: loopback TEI or Ollama, or Gemini with
// MEMORY_EXTERNAL_EMBEDDER_OPTIN=1. There is no default-on path and no
// external provider fallback.
func syntheticEmbedderAllowed() error {
	embedder := os.Getenv("MEMORY_EMBEDDER")
	if embedder == "none" {
		return nil
	}
	if os.Getenv("MEMORY_SEMANTIC_RETRIEVAL") != "1" {
		return memory.ErrInvalid
	}
	// An external provider is a separate, explicit opt-in: text leaves the
	// host, and each request still needs the decision's external transfer.
	if embedder == "gemini" {
		if os.Getenv("MEMORY_EXTERNAL_EMBEDDER_OPTIN") != "1" {
			return memory.ErrInvalid
		}
		return nil
	}
	endpoint := ""
	switch embedder {
	case "tei", memory.EmbedTextsProtocol, memory.EmbedPassageProtocol:
		endpoint = os.Getenv("TEI_ENDPOINT_URL")
	case "ollama":
		endpoint = os.Getenv("OLLAMA_ENDPOINT_URL")
	default:
		return memory.ErrInvalid
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || !memory.LoopbackHost(parsed.Hostname()) {
		return memory.ErrInvalid
	}
	return nil
}

// Postgres is the only store. MEMORY_STORE may be omitted or "postgres".
func configuredStore() (string, error) {
	if store := os.Getenv("MEMORY_STORE"); store != "" && store != "postgres" || os.Getenv("MEMORY_DATABASE_URL") == "" {
		return "", memory.ErrInvalid
	}
	return "postgres", nil
}

func configuredEmbedder(worker, recovery bool) (memory.Embedder, error) {
	if recovery {
		return nil, nil
	}
	provider := os.Getenv("MEMORY_EMBEDDER")
	if provider == "" {
		// The worker exists to embed; it needs an explicit provider.
		if worker {
			return nil, memory.ErrInvalid
		}
		return nil, nil
	}
	switch provider {
	case "none":
		return nil, nil
	case "tei", memory.EmbedTextsProtocol, memory.EmbedPassageProtocol:
		return configuredTEIEmbedder(provider)
	case "ollama":
		// Loopback only, like TEI: text never leaves the host. The defaults are
		// the Qwen3-Embedding instruction a TEI deployment would use; an
		// override that is set but empty means no prefix.
		prefixes := map[string]string{"query": memory.OllamaDefaultQueryPrefix, "document": memory.OllamaDefaultDocumentPrefix}
		for purpose, name := range map[string]string{"query": "OLLAMA_QUERY_PREFIX", "document": "OLLAMA_DOCUMENT_PREFIX"} {
			if value, set := os.LookupEnv(name); set {
				prefixes[purpose] = strings.ReplaceAll(value, `\n`, "\n")
			}
		}
		return memory.NewOllamaEmbedder(memory.OllamaConfig{EndpointURL: os.Getenv("OLLAMA_ENDPOINT_URL"), Model: os.Getenv("OLLAMA_MODEL"), Prefixes: prefixes})
	case "gemini":
		if dimension := os.Getenv("GEMINI_EMBEDDING_DIMENSION"); dimension != "" && dimension != "1024" {
			return nil, memory.ErrInvalid
		}
		return memory.NewGeminiEmbedder(memory.GeminiConfig{BaseURL: os.Getenv("GEMINI_EMBEDDING_BASE_URL"), APIKey: os.Getenv("GEMINI_API_KEY"), Model: os.Getenv("GEMINI_EMBEDDING_MODEL"), Revision: os.Getenv("GEMINI_EMBEDDING_REVISION"), IPv4Only: os.Getenv("GEMINI_IPV4_ONLY") == "1"})
	default:
		return nil, memory.ErrInvalid
	}
}

// configuredTEIEmbedder reads TEI_* for the tei protocol (picked by URL path)
// or one of the hosted embedding protocols named by MEMORY_EMBEDDER.
func configuredTEIEmbedder(provider string) (memory.Embedder, error) {
	query, querySet := os.LookupEnv("TEI_QUERY_PREFIX")
	document, documentSet := os.LookupEnv("TEI_DOCUMENT_PREFIX")
	// The hosted protocols carry the purpose as input_type, so their prefixes default to none.
	if provider == "tei" && (!querySet || !documentSet) {
		return nil, memory.ErrInvalid
	}
	protocol := provider // "tei" picks the protocol from the URL path
	if provider == "tei" {
		protocol = ""
	}
	// An env file cannot hold a newline, and instruction-tuned models (such as
	// Qwen3) put one between the instruction and the query: write it as \n.
	query, document = strings.ReplaceAll(query, `\n`, "\n"), strings.ReplaceAll(document, `\n`, "\n")
	// The client batch, the service-side index queue batch and the TEI
	// server's max-client-batch-size must all stay at the same CPU backend
	// ceiling. A mismatch is a startup failure, not a runtime surprise.
	clientBatch := memory.TeiMaxBatchSize
	if raw := os.Getenv("TEI_MAX_BATCH"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("TEI_MAX_BATCH: %w", memory.ErrInvalid)
		}
		clientBatch = parsed
	}
	serverBatch := clientBatch
	if raw := os.Getenv("TEI_SERVER_MAX_CLIENT_BATCH"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("TEI_SERVER_MAX_CLIENT_BATCH: %w", memory.ErrInvalid)
		}
		serverBatch = parsed
	}
	if err := memory.ValidateTEIBatchLimits(clientBatch, memory.KnowledgeIndexBatch, serverBatch); err != nil {
		return nil, err
	}
	return memory.NewTEIEmbedder(memory.TEIConfig{
		EndpointURL: os.Getenv("TEI_ENDPOINT_URL"), Protocol: protocol, Model: os.Getenv("TEI_MODEL"), Revision: os.Getenv("TEI_REVISION"),
		Prefixes: map[string]string{"query": query, "document": document}, MaxBatchSize: clientBatch,
		APIKey: os.Getenv("TEI_API_KEY"), SendDimensions: os.Getenv("TEI_SEND_DIMENSIONS") == "1",
	})
}

// configuredReranker reads ARKISM_RERANK_URL (base URL of a POST /rerank service), the
// optional ARKISM_RERANK_MODEL (switches to the hosted model/documents protocol),
// ARKISM_RERANK_API_KEY (or ARKISM_RERANK_API_KEY_FILE, re-read per call),
// ARKISM_RERANK_PROTOCOL (passages or records: URL is then the full endpoint)
// and ARKISM_RERANK_TIMEOUT (Go duration). Unset URL disables
// reranking unless ARKISM_RERANK_LOCAL=1 asks for the in-process lexical reranker.
func configuredReranker() (*memory.TEIReranker, error) {
	raw := os.Getenv("ARKISM_RERANK_URL")
	if raw == "" {
		if os.Getenv("ARKISM_RERANK_LOCAL") == "1" {
			return memory.NewLocalReranker(), nil
		}
		return nil, nil
	}
	var timeout time.Duration
	if v := os.Getenv("ARKISM_RERANK_TIMEOUT"); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil || parsed <= 0 || parsed > time.Minute {
			return nil, fmt.Errorf("ARKISM_RERANK_TIMEOUT: %w", memory.ErrInvalid)
		}
		timeout = parsed
	}
	return memory.NewTEIReranker(memory.TEIRerankConfig{URL: raw, Model: os.Getenv("ARKISM_RERANK_MODEL"), APIKey: os.Getenv("ARKISM_RERANK_API_KEY"),
		APIKeyFile: os.Getenv("ARKISM_RERANK_API_KEY_FILE"), Protocol: os.Getenv("ARKISM_RERANK_PROTOCOL"), Timeout: timeout})
}

// PostgreSQL permissions are canonical; the other authorizers serve non-Postgres test wiring.
func configuredAuthorizer(repo memory.Repository, standalone string) (memory.Authorizer, error) {
	if pg, ok := repo.(*memory.Postgres); ok {
		return memory.NewPostgresAuthorizer(pg)
	}
	if standalone == "true" {
		return memory.NewLocalStandaloneAuthorizer(memory.LocalStandaloneConfig{OwnerTokenSHA256: os.Getenv("MEMORY_LOCAL_OWNER_TOKEN_SHA256"), WorkerTokenSHA256: os.Getenv("MEMORY_LOCAL_WORKER_TOKEN_SHA256")})
	}
	return nil, fmt.Errorf("authorizer: use Postgres storage or set MEMORY_LOCAL_STANDALONE=true: %w", memory.ErrInvalid)
}
