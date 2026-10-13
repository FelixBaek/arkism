package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

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
	case "tei":
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
	case "tei":
		query, querySet := os.LookupEnv("TEI_QUERY_PREFIX")
		document, documentSet := os.LookupEnv("TEI_DOCUMENT_PREFIX")
		if !querySet || !documentSet {
			return nil, memory.ErrInvalid
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
			EndpointURL: os.Getenv("TEI_ENDPOINT_URL"), Model: os.Getenv("TEI_MODEL"), Revision: os.Getenv("TEI_REVISION"),
			Prefixes: map[string]string{"query": query, "document": document}, MaxBatchSize: clientBatch,
		})
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
