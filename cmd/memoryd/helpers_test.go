package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
)

func TestDiagnosticsReportABusyAddress(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if stop, err := startDiagnostics(context.Background(), busy.Addr().String(), strings.Repeat("x", 32)); err == nil {
		stop()
		t.Fatal("diagnostics shared a busy address")
	}
}

func TestSweepIsOffWithoutExpiryDays(t *testing.T) {
	if err := sweepCandidates(context.Background(), nil, "", memory.Scope{}, 0); err != nil {
		t.Fatal(err)
	}
}

func TestCostRatesRefuseUnparsableOrUnboundedPrices(t *testing.T) {
	names := []string{"MEMORY_PRICING_REVISION", "SKILL_LLM_INPUT_USD_PER_MILLION_TOKENS", "SKILL_LLM_OUTPUT_USD_PER_MILLION_TOKENS", "JEV_USD_PER_REQUEST"}
	for _, name := range names[1:] {
		for _, value := range []string{"x", "0", "1001"} {
			for _, n := range names {
				t.Setenv(n, "")
			}
			t.Setenv("MEMORY_PRICING_REVISION", "r1")
			t.Setenv(name, value)
			if _, err := costRatesFromEnv(); !errors.Is(err, memory.ErrInvalid) {
				t.Fatal(name, value, err)
			}
		}
	}
	t.Setenv("SKILL_LLM_INPUT_USD_PER_MILLION_TOKENS", "")
	t.Setenv("SKILL_LLM_OUTPUT_USD_PER_MILLION_TOKENS", "2")
	t.Setenv("JEV_USD_PER_REQUEST", "")
	if rates, err := costRatesFromEnv(); err != nil || rates.SkillLLMOutputTokenPicoUSD != 2_000_000 {
		t.Fatal(rates, err)
	}
}

func TestSyntheticEmbedderNeedsLoopbackOrExplicitOptIn(t *testing.T) {
	cases := []struct {
		env map[string]string
		ok  bool
	}{
		{map[string]string{"MEMORY_EMBEDDER": "none"}, true},
		{map[string]string{"MEMORY_EMBEDDER": "tei"}, false},
		{map[string]string{"MEMORY_EMBEDDER": "gemini", "MEMORY_SEMANTIC_RETRIEVAL": "1"}, false},
		{map[string]string{"MEMORY_EMBEDDER": "gemini", "MEMORY_SEMANTIC_RETRIEVAL": "1", "MEMORY_EXTERNAL_EMBEDDER_OPTIN": "1"}, true},
		{map[string]string{"MEMORY_EMBEDDER": "voyage", "MEMORY_SEMANTIC_RETRIEVAL": "1"}, false},
		{map[string]string{"MEMORY_EMBEDDER": "tei", "MEMORY_SEMANTIC_RETRIEVAL": "1", "TEI_ENDPOINT_URL": "http://127.0.0.1:8089/embed"}, true},
		{map[string]string{"MEMORY_EMBEDDER": "tei", "MEMORY_SEMANTIC_RETRIEVAL": "1", "TEI_ENDPOINT_URL": "https://127.0.0.1:8089/embed"}, false},
		{map[string]string{"MEMORY_EMBEDDER": "ollama", "MEMORY_SEMANTIC_RETRIEVAL": "1", "OLLAMA_ENDPOINT_URL": "http://ollama.example:11434"}, false},
		{map[string]string{"MEMORY_EMBEDDER": "ollama", "MEMORY_SEMANTIC_RETRIEVAL": "1", "OLLAMA_ENDPOINT_URL": "http://localhost:11434"}, true},
	}
	for _, c := range cases {
		for _, name := range []string{"MEMORY_EMBEDDER", "MEMORY_SEMANTIC_RETRIEVAL", "MEMORY_EXTERNAL_EMBEDDER_OPTIN", "TEI_ENDPOINT_URL", "OLLAMA_ENDPOINT_URL"} {
			t.Setenv(name, c.env[name])
		}
		if err := syntheticEmbedderAllowed(); (err == nil) != c.ok {
			t.Errorf("%v: %v", c.env, err)
		}
	}
}

func TestEmbedderSettingsAreCheckedAtStartup(t *testing.T) {
	t.Setenv("MEMORY_EMBEDDER", "")
	if _, err := configuredEmbedder(true, false); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("worker without a provider", err)
	}
	if embed, err := configuredEmbedder(false, true); embed != nil || err != nil {
		t.Fatal("recovery uses no embedder", embed, err)
	}
	t.Setenv("MEMORY_EMBEDDER", "tei")
	t.Setenv("TEI_ENDPOINT_URL", "http://127.0.0.1:8089/embed")
	t.Setenv("TEI_QUERY_PREFIX", "")
	t.Setenv("TEI_DOCUMENT_PREFIX", "")
	t.Setenv("TEI_MAX_BATCH", "")
	t.Setenv("TEI_SERVER_MAX_CLIENT_BATCH", "many")
	if _, err := configuredEmbedder(true, false); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("unparsable server batch", err)
	}
	t.Setenv("MEMORY_EMBEDDER", "ollama")
	t.Setenv("OLLAMA_ENDPOINT_URL", "http://127.0.0.1:11434")
	t.Setenv("OLLAMA_MODEL", "qwen3-embedding")
	t.Setenv("OLLAMA_QUERY_PREFIX", `Query:\n`)
	t.Setenv("OLLAMA_DOCUMENT_PREFIX", "")
	withPrefix, err := configuredEmbedder(true, false)
	if err != nil {
		t.Fatal("ollama", err)
	}
	t.Setenv("OLLAMA_QUERY_PREFIX", "")
	if plain, err := configuredEmbedder(true, false); err != nil || plain.Space() == withPrefix.Space() {
		t.Fatal("ollama prefix override is not part of the space", err)
	}
	t.Setenv("MEMORY_EMBEDDER", "gemini")
	t.Setenv("GEMINI_EMBEDDING_DIMENSION", "768")
	if _, err := configuredEmbedder(true, false); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("gemini dimension", err)
	}
}

func TestAuthorizerWithoutPostgresNeedsStandalone(t *testing.T) {
	if _, err := configuredAuthorizer(nil, ""); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("non-Postgres authorizer without standalone", err)
	}
	t.Setenv("MEMORY_LOCAL_OWNER_TOKEN_SHA256", "")
	t.Setenv("MEMORY_LOCAL_WORKER_TOKEN_SHA256", "")
	if _, err := configuredAuthorizer(nil, "true"); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("standalone authorizer without token hashes", err)
	}
	t.Setenv("MEMORY_LOCAL_OWNER_TOKEN_SHA256", strings.Repeat("a", 64))
	t.Setenv("MEMORY_LOCAL_WORKER_TOKEN_SHA256", strings.Repeat("b", 64))
	if auth, err := configuredAuthorizer(nil, "true"); auth == nil || err != nil {
		t.Fatal("standalone authorizer", auth, err)
	}
}

func TestMemoryWorkerLoopStopsBeforeAndBetweenRounds(t *testing.T) {
	stop := make(chan struct{})
	close(stop)
	memoryWorkerLoop(context.Background(), nil, stop, time.Hour, func(context.Context) { t.Error("round after stop") })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	memoryWorkerLoop(ctx, nil, nil, time.Hour, func(context.Context) { t.Error("round after cancel") })
	stop = make(chan struct{})
	rounds := 0
	memoryWorkerLoop(context.Background(), nil, stop, time.Hour, func(context.Context) { rounds++; close(stop) })
	if rounds != 1 {
		t.Fatal("rounds", rounds)
	}
	stop, rounds = make(chan struct{}), 0
	memoryWorkerLoop(context.Background(), nil, stop, time.Millisecond, func(context.Context) {
		if rounds++; rounds == 2 {
			close(stop)
		}
	})
	if rounds != 2 {
		t.Fatal("the interval did not start another round", rounds)
	}
}

func TestMemoryWorkerWithControlEndsWithItsContext(t *testing.T) {
	f := newMemoryControlFixture(t, "worker")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		done <- runMemoryWorker(ctx, f.control, time.Hour, func(context.Context) { t.Error("round after cancel") })
	}()
	f.receive("ready")
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestMemoryControlWithoutParentFailsToStart(t *testing.T) {
	f := newMemoryControlFixture(t, "worker")
	_ = f.parent.Close()
	if err := runMemoryWorker(context.Background(), f.control, time.Hour, func(context.Context) { t.Error("round without control") }); err == nil {
		t.Fatal("worker started without its control channel")
	}
	s := newMemoryControlFixture(t, "server")
	_ = s.parent.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := serveMemoryServer(context.Background(), &http.Server{Handler: http.NotFoundHandler()}, ln, s.control); err == nil {
		t.Fatal("server started without its control channel")
	}
	if _, err := net.Dial("tcp", ln.Addr().String()); err == nil {
		t.Fatal("listener left open")
	}
}
