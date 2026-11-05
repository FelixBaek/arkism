package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/memory"
)

// TestSidecarHelperProcess is the fake model server the sidecar test starts.
func TestSidecarHelperProcess(t *testing.T) {
	addr := os.Getenv("SIDECAR_TEST_ADDR")
	if addr == "" {
		t.Skip("helper process only")
	}
	if os.Getenv("MEMORY_DATABASE_URL") != "" {
		os.Exit(3) // the server's secrets must not reach the sidecar
	}
	_ = http.ListenAndServe(addr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
}

func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func TestModelSidecarStartsWaitsAndStops(t *testing.T) {
	addr := freeLoopbackAddr(t)
	argv, _ := json.Marshal([]string{os.Args[0], "-test.run=^TestSidecarHelperProcess$"})
	t.Setenv("ARKISM_MODEL_SIDECAR", string(argv))
	t.Setenv("ARKISM_MODEL_SIDECAR_READY_URL", "http://"+addr+"/health")
	t.Setenv("ARKISM_MODEL_SIDECAR_ENV", "SIDECAR_TEST_ADDR")
	t.Setenv("SIDECAR_TEST_ADDR", addr)
	t.Setenv("MEMORY_DATABASE_URL", "postgres://secret")
	t.Setenv("ARKISM_MODEL_SIDECAR_WAIT", "30s")
	stop, err := startModelSidecar(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sidecarReady(context.Background(), "http://"+addr+"/health") {
		t.Fatal("sidecar not answering")
	}
	// A second process with the same env finds it running and starts nothing.
	again, err := startModelSidecar(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	again()
	stop()
	deadline := time.Now().Add(5 * time.Second)
	for sidecarReady(context.Background(), "http://"+addr+"/health") && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if sidecarReady(context.Background(), "http://"+addr+"/health") {
		t.Fatal("sidecar still running after stop")
	}
}

func TestModelSidecarRefusesBadSettings(t *testing.T) {
	t.Setenv("ARKISM_MODEL_SIDECAR_ENV", "")
	t.Setenv("ARKISM_MODEL_SIDECAR", "")
	if stop, err := startModelSidecar(context.Background()); err != nil || stop == nil {
		t.Fatal("unset is off", err)
	}
	for _, c := range [][2]string{{"server --port 1", "http://127.0.0.1:1/"}, {`["x"]`, "http://models.example.com/health"}, {`[]`, "http://127.0.0.1:1/"}} {
		t.Setenv("ARKISM_MODEL_SIDECAR", c[0])
		t.Setenv("ARKISM_MODEL_SIDECAR_READY_URL", c[1])
		if _, err := startModelSidecar(context.Background()); !errors.Is(err, memory.ErrInvalid) {
			t.Fatal(c, err)
		}
	}
	// Names that could carry a server secret are refused, whatever the case.
	for _, name := range []string{"SKILL_LLM_API_KEY", "MEMORY_DATABASE_URL", "HF_TOKEN", "my_secret", "PGPASSWORD"} {
		t.Setenv("ARKISM_MODEL_SIDECAR", `["x"]`)
		t.Setenv("ARKISM_MODEL_SIDECAR_READY_URL", "http://127.0.0.1:1/")
		t.Setenv("ARKISM_MODEL_SIDECAR_ENV", "CUDA_VISIBLE_DEVICES, "+name)
		if _, err := startModelSidecar(context.Background()); !errors.Is(err, memory.ErrInvalid) {
			t.Fatal(name, err)
		}
	}
	t.Setenv("ARKISM_MODEL_SIDECAR_ENV", "")
	// A command that exits at once is reported, not waited on.
	t.Setenv("ARKISM_MODEL_SIDECAR", `["`+os.Args[0]+`","-test.run=^$"]`)
	t.Setenv("ARKISM_MODEL_SIDECAR_READY_URL", "http://"+freeLoopbackAddr(t)+"/")
	if _, err := startModelSidecar(context.Background()); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatal("early exit", err)
	}
}

func TestConfiguredServerLLMPresets(t *testing.T) {
	for _, k := range []string{"SKILL_LLM_ENDPOINT_URL", "SKILL_LLM_API_KEY", "SKILL_LLM_MODEL", "SKILL_LLM_PROTOCOL", "SKILL_LLM_PRESET", "SKILL_LLM_BASE_URL", "ARKISM_GATEWAY_TOKEN"} {
		t.Setenv(k, "")
	}
	if c, err := configuredServerLLM(false); err != nil || c.Configured() {
		t.Fatal("nothing set means no server model", c, err)
	}
	t.Setenv("SKILL_LLM_PRESET", "local")
	if c, err := configuredServerLLM(false); err != nil || !c.Configured() || c.Endpoint != "http://127.0.0.1:8080/v1/chat/completions" {
		t.Fatal("local preset", c, err)
	}
	if c, _ := configuredServerLLM(true); c.Configured() {
		t.Fatal("recovery has no server model")
	}
	// The gateway preset reads the host's token file when no key is set.
	file := filepath.Join(t.TempDir(), "gateway.token")
	token := strings.Repeat("ab", 32)
	if err := os.WriteFile(file, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	old := host.GatewayTokenFile
	host.GatewayTokenFile = file
	t.Cleanup(func() { host.GatewayTokenFile = old })
	t.Setenv("SKILL_LLM_PRESET", "gateway")
	t.Setenv("ARKISM_GATEWAY_LISTEN", "127.0.0.1:8199")
	t.Setenv("SKILL_LLM_PROTOCOL", "responses")
	c, err := configuredServerLLM(false)
	if err != nil || c.APIKey != token || c.Endpoint != "http://127.0.0.1:8199/v1/responses" || c.KeyHeader != host.GatewayTokenHeader {
		t.Fatal("gateway preset", c, err)
	}
	host.GatewayTokenFile = filepath.Join(t.TempDir(), "missing")
	if _, err := configuredServerLLM(false); err == nil {
		t.Fatal("gateway preset without a token")
	}
	t.Setenv("SKILL_LLM_PRESET", "")
	t.Setenv("SKILL_LLM_API_KEY", "k")
	t.Setenv("SKILL_LLM_PROTOCOL", "other")
	if _, err := configuredServerLLM(false); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("bad protocol stops startup", err)
	}
}

func TestHostedEmbedderAndRerankerSettings(t *testing.T) {
	os.Unsetenv("TEI_QUERY_PREFIX")
	os.Unsetenv("TEI_DOCUMENT_PREFIX")
	t.Setenv("MEMORY_EMBEDDER", memory.EmbedTextsProtocol)
	t.Setenv("TEI_ENDPOINT_URL", "https://embed.example.com/v2/embed")
	t.Setenv("TEI_MODEL", "m")
	t.Setenv("TEI_REVISION", "r")
	t.Setenv("TEI_MAX_BATCH", "")
	t.Setenv("TEI_SERVER_MAX_CLIENT_BATCH", "")
	e, err := configuredEmbedder(true, false)
	if err != nil || !strings.HasPrefix(e.Space(), memory.EmbedTextsProtocol+":") {
		t.Fatal("embed_texts without prefixes", err)
	}
	t.Setenv("MEMORY_EMBEDDER", "tei")
	if _, err := configuredEmbedder(true, false); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("tei still needs explicit prefixes", err)
	}
	t.Setenv("ARKISM_RERANK_URL", "https://rank.example.com/v1/rank")
	t.Setenv("ARKISM_RERANK_MODEL", "m")
	t.Setenv("ARKISM_RERANK_PROTOCOL", memory.RerankRecords)
	if r, err := configuredReranker(); err != nil || r == nil {
		t.Fatal("records reranker", err)
	}
	t.Setenv("ARKISM_RERANK_PROTOCOL", "other")
	if _, err := configuredReranker(); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("unknown rerank protocol", err)
	}
}
