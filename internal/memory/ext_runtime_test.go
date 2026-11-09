//go:build linux

package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func extConfigFile(t *testing.T, list ...ProcessExtensionConfig) string {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"extensions": list})
	path := filepath.Join(t.TempDir(), "extensions.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func stdioExtension(t *testing.T, name string) ProcessExtensionConfig {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	return ProcessExtensionConfig{Name: name, Command: []string{self}, Env: map[string]string{"ARKISM_TEST_HELPER": "extension"}}
}

func loadExtensions(t *testing.T, f *fixture, list ...ProcessExtensionConfig) *ProcessExtensions {
	t.Helper()
	ext, err := LoadProcessExtensions(testCtx, extConfigFile(t, list...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ext.Close)
	ext.Apply(&f.svc.Extensions)
	return ext
}

func TestProcessExtensionStdioRefusesAndChecksTenant(t *testing.T) {
	t.Setenv("MEMORY_PRINCIPAL_TOKEN", "server-secret")
	f := newFixture(t)
	loadExtensions(t, f, stdioExtension(t, "house"))
	if _, err := f.svc.Capture(testCtx, f.human, captureInput(scopeA, "evt-x", explicit("앞으로 forbidden 단어를 쓴다."))); !errors.Is(err, ErrInvalid) || !errors.Is(err, ErrExtensionRefused) {
		t.Fatal("capture not refused", err)
	}
	if items := f.capture(f.human, captureInput(scopeA, "evt-y", explicit("앞으로 답은 짧게 한다."))); items[0].Status != "candidate" {
		t.Fatal("an allowed capture stays a candidate", items)
	}
	if _, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "forbidden"}); !errors.Is(err, ErrDenied) || !errors.Is(err, ErrExtensionRefused) {
		t.Fatal("recall not refused", err)
	}
	if _, err := f.svc.Search(testCtx, f.human2, SearchInput{Scope: scopeA, Query: "답"}); !errors.Is(err, ErrDenied) {
		t.Fatal("tenant hook did not refuse human-2", err)
	}
	if _, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "답"}); err != nil {
		t.Fatal(err)
	}
}

// deadlineExtensionTransport observes actual transport deadlines, so fail-open
// success cannot accidentally stand in for the timeout or restart assertion.
type deadlineExtensionTransport struct {
	extTransport
	deadlines int
}

func (t *deadlineExtensionTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, err := t.extTransport.call(ctx, method, params)
	if errors.Is(err, context.DeadlineExceeded) {
		t.deadlines++
	}
	return raw, err
}

func TestProcessExtensionTimeoutFollowsFailSetting(t *testing.T) {
	for _, fail := range []string{"closed", "open"} {
		t.Run(fail, func(t *testing.T) {
			f := newFixture(t)
			cfg := stdioExtension(t, "slow")
			cfg.Timeout, cfg.Fail = "300ms", fail
			extensions := loadExtensions(t, f, cfg)
			extension := extensions.list[0]
			transport := extension.t.(*stdioTransport)
			initialPID := transport.cmd.Process.Pid
			observed := &deadlineExtensionTransport{extTransport: transport}
			extension.t = observed
			_, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "sleepy"})
			if fail == "closed" && !errors.Is(err, ErrExtensionRefused) || fail == "open" && err != nil {
				t.Fatal(fail, err)
			}
			if observed.deadlines == 0 {
				t.Fatal("sleepy operation did not exercise a transport deadline")
			}
			// The 300ms fault budget is intentionally too small for a busy test binary's
			// cold start. Restart is a separate correctness check, not a latency target.
			// Give only this test instance a bounded startup budget, then demand a real
			// reply directly so fail-open cannot hide a broken restarted process.
			extension.timeout = 5 * time.Second
			deadlines := observed.deadlines
			raw, err := extension.ask(testCtx, "describe", nil)
			var description extDescription
			if err != nil || json.Unmarshal(raw, &description) != nil || !slices.Contains(description.Operations, OpRecall) {
				t.Fatal("restart handshake", err, string(raw))
			}
			if transport.cmd == nil || transport.cmd.Process.Pid == initialPID {
				t.Fatal("timed-out process was not replaced")
			}
			if _, err = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "답"}); err != nil {
				t.Fatal(fail, "after restart", err)
			}
			if observed.deadlines != deadlines {
				t.Fatal("healthy restart operation timed out")
			}
		})
	}
}

func TestProcessExtensionConfigIsChecked(t *testing.T) {
	good := stdioExtension(t, "house")
	for name, bad := range map[string]ProcessExtensionConfig{
		"relative command": {Name: "a", Command: []string{"ext"}},
		"both":             {Name: "a", Command: good.Command, URL: "https://ext.example.com"},
		"name":             {Name: "Bad-Name", Command: good.Command},
		"timeout":          {Name: "a", Command: good.Command, Timeout: "1m"},
		"fail":             {Name: "a", Command: good.Command, Fail: "maybe"},
		"private ip":       {Name: "a", URL: "https://10.0.0.1/rpc"},
		"loopback":         {Name: "a", URL: "http://127.0.0.1:9/rpc"},
		"credentials":      {Name: "a", URL: "https://u:p@ext.example.com/rpc"},
		"plain http":       {Name: "a", URL: "http://ext.example.com/rpc"},
		"env name":         {Name: "a", Command: good.Command, Env: map[string]string{"bad name": "x"}},
	} {
		if _, err := LoadProcessExtensions(testCtx, extConfigFile(t, bad)); !errors.Is(err, ErrInvalid) {
			t.Fatal(name, err)
		}
	}
	if _, err := LoadProcessExtensions(testCtx, extConfigFile(t, good, good)); !errors.Is(err, ErrInvalid) {
		t.Fatal("duplicate name", err)
	}
}

func TestProcessExtensionHTTPTransport(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": extAnswer(req.Method, req.Params)})
	}))
	defer srv.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("ext-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	loadExtensions(t, f, ProcessExtensionConfig{Name: "corp", URL: srv.URL, TokenFile: token, AllowPrivate: true})
	if _, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "forbidden"}); !errors.Is(err, ErrExtensionRefused) {
		t.Fatal("http extension did not refuse", err)
	}
	if auth != "Bearer ext-token" {
		t.Fatal("token", auth)
	}
}

func extRequest(t *testing.T, h http.Handler, token, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestExtensionRoutesAndToolsUseArkismAuthorization(t *testing.T) {
	f := newFixture(t)
	loadExtensions(t, f, stdioExtension(t, "house"))
	h := handlerMode(f.svc, time.Minute, true)
	q := "?workspace_id=ws-a&project_id=proj-a"
	rec := extRequest(t, h, f.human, http.MethodGet, "/v1/extensions/house/routes/hello"+q, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"principal":"human-1"`) {
		t.Fatal("route", rec.Code, rec.Body)
	}
	if rec = extRequest(t, h, f.human, http.MethodGet, "/v1/extensions/house/routes/missing"+q, ""); rec.Code != 404 {
		t.Fatal("undeclared route", rec.Code)
	}
	if rec = extRequest(t, h, "unknown-token", http.MethodGet, "/v1/extensions/house/routes/missing"+q, ""); rec.Code != 403 {
		t.Fatal("an unknown caller learned that a route is missing", rec.Code)
	}
	if rec = extRequest(t, h, f.agent, http.MethodGet, "/v1/extensions/house/routes/env"+q, ""); rec.Code != 403 {
		t.Fatal("a route for people only answered an agent", rec.Code)
	}
	if rec = extRequest(t, h, "unknown-token", http.MethodGet, "/v1/extensions/house/routes/hello"+q, ""); rec.Code != 403 {
		t.Fatal("unknown token", rec.Code)
	}
	rec = extRequest(t, h, f.human, http.MethodGet, "/v1/extensions/house/routes/env"+q, "")
	var env struct{ Env []string }
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if slices.Contains(env.Env, "MEMORY_PRINCIPAL_TOKEN") || !slices.Contains(env.Env, "ARKISM_TEST_HELPER") {
		t.Fatal("the program saw the server's environment", env.Env)
	}

	post := `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"input":{"x":1}}`
	if rec = extRequest(t, h, f.agent, http.MethodPost, "/v1/extensions/house/routes/echo", post); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"body":{"x":1}`) {
		t.Fatal("post route", rec.Code, rec.Body)
	}
	if rec = extRequest(t, h, f.agent, http.MethodPost, "/v1/extensions/house/routes/echo", `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"x":1}`); rec.Code != 400 {
		t.Fatal("post route took an unknown field", rec.Code, rec.Body)
	}
	rec = extRequest(t, h, f.agent, http.MethodGet, "/v1/extension-tools"+q, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"ext_house_lookup"`) || strings.Contains(rec.Body.String(), "action") {
		t.Fatal("tool list", rec.Code, rec.Body)
	}
	call := `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"name":"ext_house_lookup","arguments":{"q":"x"}}`
	rec = extRequest(t, h, f.agent, http.MethodPost, "/v1/extension-tools/call", call)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"tool":"lookup"`) || !strings.Contains(rec.Body.String(), "data_only_untrusted") {
		t.Fatal("tool call", rec.Code, rec.Body)
	}
	f.svc.MCPTools = []string{"recall"}
	if rec = extRequest(t, h, f.agent, http.MethodPost, "/v1/extension-tools/call", call); rec.Code != 403 {
		t.Fatal("the server allowlist left the tool out", rec.Code)
	}
	if rec = extRequest(t, h, f.agent, http.MethodGet, "/v1/extension-tools"+q, ""); strings.Contains(rec.Body.String(), "ext_house_lookup") {
		t.Fatal("listed a tool the server allowlist leaves out", rec.Body)
	}
	f.svc.MCPTools = nil
	only := []string{"recall"}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{MCPTools: &only}, false); err != nil {
		t.Fatal(err)
	}
	if rec = extRequest(t, h, f.agent, http.MethodPost, "/v1/extension-tools/call", call); rec.Code != 403 {
		t.Fatal("the space allowlist left the tool out", rec.Code)
	}
}

func TestParseMCPTools(t *testing.T) {
	if got, err := ParseMCPTools(" recall, ext_house_lookup ,,"); err != nil || !slices.Equal(got, []string{"recall", "ext_house_lookup"}) {
		t.Fatal(got, err)
	}
	for _, bad := range []string{"Recall", "recall,recall", "a b"} {
		if _, err := ParseMCPTools(bad); !errors.Is(err, ErrInvalid) {
			t.Fatal(bad, err)
		}
	}
}

func TestExtensionsDisabledWithoutConfig(t *testing.T) {
	f := newFixture(t)
	h := handlerMode(f.svc, time.Minute, true)
	if rec := extRequest(t, h, f.human, http.MethodGet, "/v1/extensions/house/routes/hello?workspace_id=ws-a&project_id=proj-a", ""); rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	tools, err := f.svc.ExtensionTools(context.Background(), f.agent, scopeA)
	if err != nil || len(tools) != 0 {
		t.Fatal(tools, err)
	}
}

func TestProcessExtensionEnvRefusesCredentialNamesUnlessListed(t *testing.T) {
	good := stdioExtension(t, "house")
	for _, name := range []string{"API_KEY", "GH_TOKEN", "DB_PASSWORD", "MEMORY_DATABASE_URL", "AUTH_HEADER", "CLIENT_SECRET"} {
		cfg := good
		cfg.Env = map[string]string{"ARKISM_TEST_HELPER": "extension", name: "x"}
		if _, err := LoadProcessExtensions(testCtx, extConfigFile(t, cfg)); !errors.Is(err, ErrInvalid) {
			t.Fatal(name, err)
		}
		cfg.SecretEnv = []string{name}
		ext, err := LoadProcessExtensions(testCtx, extConfigFile(t, cfg))
		if err != nil {
			t.Fatal(name, "listed in secret_env", err)
		}
		ext.Close()
	}
	cfg := good
	cfg.SecretEnv = []string{"NOT_IN_ENV"}
	if _, err := LoadProcessExtensions(testCtx, extConfigFile(t, cfg)); !errors.Is(err, ErrInvalid) {
		t.Fatal("secret_env without the env entry", err)
	}
}

func TestProcessExtensionCloseStopsTheProgram(t *testing.T) {
	ext, err := LoadProcessExtensions(testCtx, extConfigFile(t, stdioExtension(t, "house")))
	if err != nil {
		t.Fatal(err)
	}
	proc := ext.list[0].t.(*stdioTransport).cmd.Process
	ext.Close()
	if err := proc.Signal(syscall.Signal(0)); err == nil {
		t.Fatal("the program still runs after Close")
	}
}

func TestTenantCacheKeysOnCallerAndSkipsFailures(t *testing.T) {
	f := newFixture(t)
	cfg := stdioExtension(t, "house")
	cfg.Fail = "open"
	ext := loadExtensions(t, f, cfg)
	if _, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "답"}); err != nil {
		t.Fatal(err)
	}
	// human-1's cached allow must not answer for human-2 in the same space and action.
	if _, err := f.svc.Search(testCtx, f.human2, SearchInput{Scope: scopeA, Query: "답"}); !errors.Is(err, ErrDenied) {
		t.Fatal("a cached answer stood in for another principal", err)
	}
	n := len(ext.tenant)
	// A failed call (fail open) lets the request through but is not kept.
	ext.list[0].t.close()
	ext.list[0].t = brokenTransport{}
	ext.mu.Lock()
	clear(ext.tenant)
	ext.mu.Unlock()
	if _, err := f.svc.Search(testCtx, f.human2, SearchInput{Scope: scopeA, Query: "답"}); err != nil {
		t.Fatal("fail open", err)
	}
	if len(ext.tenant) != 0 || n == 0 {
		t.Fatal("a failed tenant answer was cached", len(ext.tenant), n)
	}
}

type brokenTransport struct{}

func (brokenTransport) call(context.Context, string, any) (json.RawMessage, error) {
	return nil, errExtFailed
}
func (brokenTransport) close() {}

func TestExtensionRoutePathsAndUntrustedAnswers(t *testing.T) {
	f := newFixture(t)
	loadExtensions(t, f, stdioExtension(t, "house"))
	h := handlerMode(f.svc, time.Minute, true)
	q := "?workspace_id=ws-a&project_id=proj-a"
	rec := extRequest(t, h, f.human, http.MethodGet, "/v1/extensions/house/routes/hello"+q, "")
	if rec.Header().Get("X-Arkism-Authority") != AuthorityDataOnly || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("route answer not marked untrusted", rec.Header())
	}
	for _, path := range []string{"/v1/extensions/house/routes/..%2Fenv", "/v1/extensions/house/routes/hello%2F..%2Fenv", "/v1/extensions/house/routes/x/../env", "/v1/extensions/house/routes/HELLO"} {
		if rec := extRequest(t, h, f.agent, http.MethodGet, path+q, ""); rec.Code == 200 {
			t.Fatal("a path outside the declared routes answered", path, rec.Code, rec.Body)
		}
	}
	big := `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"input":"` + strings.Repeat("x", extensionBodyLimit) + `"}`
	if rec := extRequest(t, h, f.agent, http.MethodPost, "/v1/extensions/house/routes/echo", big); rec.Code != http.StatusRequestEntityTooLarge && rec.Code != 400 {
		t.Fatal("an oversized body was forwarded", rec.Code)
	}
}

func TestExtensionDataStore(t *testing.T) {
	f := newOpsFixture(t)
	loadExtensions(t, f, stdioExtension(t, "house"))
	h := handlerMode(f.svc, time.Minute, true)
	q := "?workspace_id=ws-a&project_id=proj-a"
	body := func(changes string) string {
		return `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"changes":` + changes + `}`
	}
	if rec := extRequest(t, h, f.human, http.MethodPost, "/v1/extensions/house/data", body(`[{"op":"put","key":"color","value":{"v":"blue"}}]`)); rec.Code != 200 {
		t.Fatal("put", rec.Code, rec.Body)
	}
	rec := extRequest(t, h, f.agent, http.MethodGet, "/v1/extensions/house/data/color"+q, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"value":{"v":"blue"}`) || !strings.Contains(rec.Body.String(), `"revision":1`) {
		t.Fatal("get", rec.Code, rec.Body)
	}
	if rec := extRequest(t, h, f.agent, http.MethodPost, "/v1/extensions/house/data", body(`[{"op":"put","key":"color","value":1}]`)); rec.Code != 403 {
		t.Fatal("an agent changed data over HTTP", rec.Code)
	}
	if rec := extRequest(t, h, f.human, http.MethodPost, "/v1/extensions/house/data", body(`[{"op":"put","key":"color","value":2,"expected_revision":7}]`)); rec.Code != 409 {
		t.Fatal("stale revision", rec.Code)
	}
	for name, change := range map[string]string{
		"secret":  `[{"op":"put","key":"k","value":"sk-ant-api03-` + strings.Repeat("a", 90) + `"}]`,
		"private": `[{"op":"put","key":"k","value":"<private>x</private>"}]`,
		"large":   `[{"op":"put","key":"k","value":"` + strings.Repeat("x", extDataDefaultValueBytes) + `"}]`,
		"key":     `[{"op":"put","key":"../k","value":1}]`,
		"op":      `[{"op":"drop","key":"k"}]`,
	} {
		if rec := extRequest(t, h, f.human, http.MethodPost, "/v1/extensions/house/data", body(change)); rec.Code != 400 && rec.Code != 422 {
			t.Fatal(name, rec.Code, rec.Body)
		}
	}
	// Other spaces see nothing; the generic record routes never serve the collection.
	if rec := extRequest(t, h, f.human, http.MethodGet, "/v1/extensions/house/data?workspace_id=ws-a&project_id=proj-b", ""); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatal("another space saw the data", rec.Body)
	}
	if rec := extRequest(t, h, f.human, http.MethodGet, "/v1/ext_data/house:color"+q, ""); rec.Code == 200 {
		t.Fatal("generic read served extension data", rec.Body)
	}
	if _, err := f.svc.Create(testCtx, f.human, colExtData, CreateInput{Scope: scopeA, Kind: "house", Text: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("generic create wrote extension data", err)
	}

	// A data route reads its entries and its answer's changes are applied with the caller's permission.
	post := `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"input":{}}`
	for want := 0; want < 2; want++ {
		rec := extRequest(t, h, f.agent, http.MethodPost, "/v1/extensions/house/routes/count", post)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), fmt.Sprintf(`"seen":%d`, want)) {
			t.Fatal("data route", want, rec.Code, rec.Body)
		}
	}
	if rec := extRequest(t, h, f.agent, http.MethodPost, "/v1/extensions/house/routes/sneaky", post); rec.Code != 503 {
		t.Fatal("a route without data changed data", rec.Code, rec.Body)
	}
	delete(f.auth.actors[f.agent].verbs, "write")
	if rec := extRequest(t, h, f.agent, http.MethodPost, "/v1/extensions/house/routes/count", post); rec.Code != 403 {
		t.Fatal("an extension wrote data the caller may not write", rec.Code, rec.Body)
	}

	// Removing an entry is the erasure path; the key count is bounded.
	if rec := extRequest(t, h, f.human, http.MethodPost, "/v1/extensions/house/data", body(`[{"op":"delete","key":"color"}]`)); rec.Code != 200 {
		t.Fatal("delete", rec.Code, rec.Body)
	}
	if rec := extRequest(t, h, f.human, http.MethodGet, "/v1/extensions/house/data/color"+q, ""); rec.Code != 404 {
		t.Fatal("deleted entry still served", rec.Code)
	}
	for i := 0; i < extDataDefaultKeys; i += 25 {
		changes := []string{}
		for j := i; j < i+25; j++ {
			changes = append(changes, fmt.Sprintf(`{"op":"put","key":"k%d","value":%d}`, j, j))
		}
		want := 200
		if i+25 > extDataDefaultKeys-1 { // "n" from the data route already holds one key
			want = 400
		}
		if rec := extRequest(t, h, f.human, http.MethodPost, "/v1/extensions/house/data", body("["+strings.Join(changes, ",")+"]")); rec.Code != want {
			t.Fatal("key cap", i, rec.Code, rec.Body)
		}
	}
}

// An llm_request extension refuses one operation and moves the others to another
// model on the same endpoint; a model_call extension hears each outcome.
func TestModelCallExtensionRewritesDeniesAndRecords(t *testing.T) {
	var mu sync.Mutex
	var recorded []json.RawMessage
	ext := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "describe":
			result = map[string]any{"llm_request": true, "model_call": true}
		case "llm_request":
			var call ModelCall
			_ = json.Unmarshal(req.Params, &call)
			result = map[string]any{"allow": call.Operation != "blocked_op" && call.PromptChars > 0 && call.PromptSHA256 != "", "model": "small-model"}
		case "model_call":
			mu.Lock()
			recorded = append(recorded, req.Params)
			mu.Unlock()
			result = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer ext.Close()
	var sent []string
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		sent = append(sent, req.Model)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": "ok"}}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12, "prompt_tokens_details": map[string]any{"cached_tokens": 4}}})
	}))
	defer chat.Close()
	f := newFixture(t)
	loadExtensions(t, f, ProcessExtensionConfig{Name: "gate", URL: ext.URL, AllowPrivate: true})
	cfg := LLMConfig{Endpoint: chat.URL, APIKey: "k", Model: "big-model"}
	call := func(op, kind string) (string, error) {
		m := modelCallSpec{scope: scopeA, operation: op, kind: kind, provider: "skill_llm", model: cfg.Model, prompt: "hello"}
		text, _, err := f.svc.ledgerCall(testCtx, m, func(ctx context.Context) (string, ProviderUsage, error) {
			reply, err := cfg.chat(ctx, chatRequest{op: op, provider: "skill_llm", user: "hello", maxTokens: 16})
			return reply.text, reply.usage, err
		})
		return text, err
	}
	if text, err := call("allowed_op", "analyze"); err != nil || text != "ok" || len(sent) != 1 || sent[0] != "small-model" {
		t.Fatalf("rewrite: %q %v %v", text, err, sent)
	}
	if _, err := call("blocked_op", "analyze"); !errors.Is(err, ErrExtensionRefused) || len(sent) != 1 {
		t.Fatalf("refused call was made: %v %v", err, sent)
	}
	if _, err := call("allowed_op", "judge"); err != nil || sent[1] != "big-model" {
		t.Fatalf("a kind that is not rewritable kept its model: %v %v", err, sent)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recorded) != 2 || !strings.Contains(string(recorded[0]), `"model":"small-model"`) || !strings.Contains(string(recorded[0]), `"cached_input_tokens":4`) ||
		strings.Contains(string(recorded[0]), "hello") {
		t.Fatalf("model_call records %s", recorded)
	}
	rows := scopeRecords(f, colLLMRequests, scopeA)
	if len(rows) != 2 {
		t.Fatalf("traced %d calls, want 2 (the refused one was never made)", len(rows))
	}
}

func TestExtensionDataLimits(t *testing.T) {
	for _, c := range []struct {
		keys, bytes string
		ok          bool
	}{{"", "", true}, {"1000", "16384", true}, {"0", "", false}, {"5001", "", false}, {"", "63", false}, {"", "262145", false}, {"5000", "65536", false}, {"x", "", false}} {
		if _, err := ParseExtDataLimits(c.keys, c.bytes); (err == nil) != c.ok {
			t.Fatal(c, err)
		}
	}
	f := newOpsFixture(t)
	loadExtensions(t, f, stdioExtension(t, "house"))
	h := handlerMode(f.svc, time.Minute, true)
	body := func(changes string) string {
		return `{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"changes":` + changes + `}`
	}
	big := `[{"op":"put","key":"big","value":"` + strings.Repeat("x", 6<<10) + `"}]`
	if rec := extRequest(t, h, f.human, http.MethodPost, "/v1/extensions/house/data", body(big)); rec.Code != 400 {
		t.Fatal("default limit took a 6 KiB value", rec.Code)
	}
	f.svc.ExtData = ExtDataLimits{MaxKeys: 250, ValueBytes: 8 << 10}
	if rec := extRequest(t, h, f.human, http.MethodPost, "/v1/extensions/house/data", body(big)); rec.Code != 200 {
		t.Fatal("raised limit refused a 6 KiB value", rec.Code, rec.Body)
	}
	for i := 1; i < 250; i += 25 {
		changes := []string{}
		for j := i; j < min(i+25, 250); j++ {
			changes = append(changes, fmt.Sprintf(`{"op":"put","key":"k%d","value":%d}`, j, j))
		}
		if rec := extRequest(t, h, f.human, http.MethodPost, "/v1/extensions/house/data", body("["+strings.Join(changes, ",")+"]")); rec.Code != 200 {
			t.Fatal("raised key cap", i, rec.Code, rec.Body)
		}
	}
	if rec := extRequest(t, h, f.human, http.MethodPost, "/v1/extensions/house/data", body(`[{"op":"put","key":"one-more","value":1}]`)); rec.Code != 400 {
		t.Fatal("key 251 accepted", rec.Code)
	}
	snap, err := f.svc.extDataSnapshot(testCtx, f.human, scopeA, "house")
	if err != nil || len(snap) != 250 || len(snap["big"]) < 6<<10 {
		t.Fatal("snapshot past one page", len(snap), err)
	}
}
