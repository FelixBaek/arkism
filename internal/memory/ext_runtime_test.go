package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// extAnswer is the fake extension both transports serve: it refuses an operation
// whose input says "forbidden", sleeps on "sleepy", refuses tenant checks for
// human-2, and echoes routes and tools.
func extAnswer(method string, params json.RawMessage) any {
	var p struct {
		Phase, Path, Tool string
		Actor             map[string]string
		Body              json.RawMessage
	}
	_ = json.Unmarshal(params, &p)
	text := string(params)
	switch method {
	case "describe":
		return map[string]any{"operations": []string{OpCapture, OpRecall}, "tenant": true,
			"routes": []map[string]any{{"method": "GET", "path": "/hello", "action": "read:memories"}, {"method": "GET", "path": "/env", "action": "read:memories", "roles": []string{"human"}}, {"method": "POST", "path": "/echo", "action": "read:memories"}},
			"tools":  []map[string]any{{"name": "lookup", "description": "Look a thing up", "inputSchema": map[string]any{"type": "object"}}}}
	case "operation":
		if strings.Contains(text, "sleepy") && p.Phase == "before" {
			time.Sleep(2 * time.Second)
		}
		return map[string]any{"allow": !strings.Contains(text, "forbidden")}
	case "tenant":
		return map[string]any{"allow": p.Actor["principal_id"] != "human-2"}
	case "route":
		body := map[string]any{"path": p.Path, "principal": p.Actor["principal_id"], "body": p.Body}
		if p.Path == "/env" {
			names := []string{}
			for _, kv := range os.Environ() {
				names = append(names, strings.SplitN(kv, "=", 2)[0])
			}
			body["env"] = names
		}
		return map[string]any{"status": 200, "body": body}
	case "tool":
		return map[string]any{"result": map[string]any{"tool": p.Tool, "args": p.Body}}
	}
	return nil
}

// fakeExtension is the stdio extension program (the test binary, ARKISM_TEST_HELPER=extension).
func fakeExtension() int {
	in, out := bufio.NewScanner(os.Stdin), json.NewEncoder(os.Stdout)
	for in.Scan() {
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil {
			return 1
		}
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": extAnswer(req.Method, req.Params)})
	}
	return 0
}

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

func TestProcessExtensionTimeoutFollowsFailSetting(t *testing.T) {
	for _, fail := range []string{"closed", "open"} {
		f := newFixture(t)
		cfg := stdioExtension(t, "slow")
		cfg.Timeout, cfg.Fail = "300ms", fail
		loadExtensions(t, f, cfg)
		_, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "sleepy"})
		if fail == "closed" && !errors.Is(err, ErrExtensionRefused) || fail == "open" && err != nil {
			t.Fatal(fail, err)
		}
		// The stopped program starts again for the next call.
		if _, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "답"}); err != nil {
			t.Fatal(fail, "after restart", err)
		}
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
