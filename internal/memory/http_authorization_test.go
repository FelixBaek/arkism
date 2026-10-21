package memory

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHTTPRoutePolicyCoverage(t *testing.T) {
	public := map[string]bool{
		"GET /{$}": true, "GET /healthz": true, "GET /readyz": true,
		"GET /recovery-identity": true, // recovery listener only; loopback identity
		"GET /v1/auth/google":    true, "POST /v1/auth/google": true,
	}
	seen := map[string]bool{}
	for _, route := range sourceRoutes(t) {
		pattern := route.method + " " + route.path
		seen[pattern] = true
		policy, ok := httpRoutePolicies[pattern]
		if !ok {
			t.Errorf("missing policy: %s (%s)", pattern, route.file)
			continue
		}
		if (policy.scope == routePublic) != public[pattern] {
			t.Errorf("unexpected public policy: %s", pattern)
		}
		if policy.scope != routePublic && (policy.action == "" || policy.scope == routeJSON && policy.limit < 1) {
			t.Errorf("incomplete policy: %s", pattern)
		}
	}
	for pattern := range httpRoutePolicies {
		if !seen[pattern] {
			t.Errorf("stale route policy: %s", pattern)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("new route without policy registered successfully")
		}
	}()
	newAuthorizationMux(newFixture(t).svc).HandleFunc("GET /v1/unmapped", func(http.ResponseWriter, *http.Request) {})
}

func TestHTTPRoutesCannotCreateUnprotectedMux(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "http_authorization.go" || name == "recovery.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if selector, ok := n.(*ast.SelectorExpr); ok && (selector.Sel.Name == "NewServeMux" || selector.Sel.Name == "ServeMux") {
				t.Errorf("%s creates or accepts an unprotected mux; use routeRegistrar", name)
			}
			return true
		})
	}
}

func routePolicyRequest(t *testing.T, pattern string, policy routePolicy) *http.Request {
	t.Helper()
	method, path, _ := strings.Cut(pattern, " ")
	path = strings.ReplaceAll(path, "{collection}", "memories")
	path = concretePath(path)
	query := url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}, "kind": {"topic"}}
	var data []byte
	contentType := "application/json"
	if policy.scope == routeMultipart {
		data, contentType = routeMultipartBody(t, 4096)
	} else {
		data = []byte(structuredText(map[string]any{
			"scope": scopeA, "skill": map[string]any{"scope": scopeA}, "kind": "topic", "memories": []Ref{{Collection: "memories"}, {Collection: "memory_candidates"}},
		}))
	}
	req := httptest.NewRequest(method, path+"?"+query.Encode(), bytes.NewReader(data))
	req.Header.Set("Content-Type", contentType)
	return req
}

// Deliberately use a handler with no service check. Each protected route must
// refuse unauthorized requests before that handler can leak data or mutate it.
func TestHTTPEveryPolicyProtectsUncheckingHandler(t *testing.T) {
	for pattern, policy := range httpRoutePolicies {
		if policy.scope == routePublic {
			continue
		}
		t.Run(pattern, func(t *testing.T) {
			for _, mode := range []string{"missing", "invalid", "denied", "foreign_scope", "unknown_role", "allowed"} {
				f := newFixture(t)
				f.svc.Auth = authorizerFunc(func(_ context.Context, token string, in Authorization) (Decision, error) {
					if mode == "invalid" {
						return Decision{}, ErrDenied
					}
					if token != "route-test" {
						t.Fatal("unexpected token")
					}
					d := Decision{Allowed: mode != "denied", PrincipalID: "route-user", Role: roleHuman, Scope: in.Scope}
					if len(policy.roles) > 0 {
						d.Role = policy.roles[0]
					}
					if mode == "foreign_scope" {
						d.Scope = scopeFar
					}
					if mode == "unknown_role" {
						d.Role = "administrator"
					}
					return d, nil
				})
				ran := false
				mux := newAuthorizationMux(f.svc)
				mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) { ran = true; w.WriteHeader(200) })
				req := routePolicyRequest(t, pattern, policy)
				if mode != "missing" {
					req.Header.Set("Authorization", "Bearer route-test")
				}
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, req)
				want := 403
				if mode == "missing" {
					want = 401
					if w.Header().Get("WWW-Authenticate") != `Bearer realm="arkism"` {
						t.Fatal("missing authentication challenge")
					}
				}
				if mode == "allowed" {
					want = 200
				}
				if w.Code != want || ran != (mode == "allowed") {
					t.Fatalf("%s: status=%d ran=%v body=%s", mode, w.Code, ran, w.Body.String())
				}
			}
		})
	}
}

func TestHTTPPolicyDynamicActions(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		input         routeInput
		want          []string
	}{
		{"POST /v1/search", "/v1/search", routeInput{}, []string{"search:memories"}},
		{"POST /v1/search", "/v1/search", routeInput{Purpose: "skills"}, []string{"search:skill_versions"}},
		{"POST /v1/search", "/v1/search", routeInput{Purpose: "risk"}, []string{"search:reflections"}},
		{"GET /v1/digests", "/v1/digests?kind=user", routeInput{}, []string{"read:user_digests"}},
		{"GET /v1/twin/claims", "/v1/twin/claims?status=confirmed,superseded", routeInput{}, []string{"read:memories"}},
		{"GET /v1/twin/claims", "/v1/twin/claims?status=confirmed,candidate", routeInput{}, []string{"review:memory_candidates"}},
		{"POST /v1/digests/proposals", "/v1/digests/proposals", routeInput{Kind: "topic"}, []string{"write:topic_digests"}},
		{"POST /v1/digests/plan", "/v1/digests/plan", routeInput{Kind: "user"}, []string{"write:user_digests"}},
		{"POST /v1/privacy/deletions", "/v1/privacy/deletions", routeInput{TargetType: "work_checkpoint"}, []string{"delete:work_checkpoints"}},
		{"POST /v1/privacy/deletions", "/v1/privacy/deletions", routeInput{TargetType: "source_event"}, []string{"delete:sources"}},
		{"POST /v1/memories/conflicts", "/v1/memories/conflicts", routeInput{Memories: []Ref{{Collection: "memories"}, {Collection: "memory_candidates"}}}, []string{"transition:memories", "transition:memory_candidates"}},
		{"GET /v1/{collection}/{id}", "/v1/memories/one", routeInput{}, []string{"read:memories"}},
	} {
		method, _, _ := strings.Cut(tc.pattern, " ")
		req := httptest.NewRequest(method, tc.path, nil)
		req.SetPathValue("collection", "memories")
		got, err := httpRoutePolicies[tc.pattern].requestActions(req, tc.input)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v %v; want %v", tc.path, got, err, tc.want)
		}
	}
	for _, tc := range []struct {
		action string
		input  routeInput
	}{
		{"search:{input_collection}", routeInput{Purpose: "invalid"}},
		{"read:{digest_collection}", routeInput{Kind: "invalid"}},
		{"transition:{conflict_collection}", routeInput{}},
		{"transition:{conflict_collection}", routeInput{Memories: []Ref{{Collection: "sources"}, {Collection: "memories"}}}},
	} {
		_, err := (routePolicy{action: tc.action}).requestActions(httptest.NewRequest("POST", "/", nil), tc.input)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid dynamic input authorized: %s %v", tc.action, err)
		}
	}
	// Go also matches HEAD to GET routes; its permission must use query kind.
	head := httptest.NewRequest("HEAD", "/v1/digests?kind=user", nil)
	actions, err := httpRoutePolicies["GET /v1/digests"].requestActions(head, routeInput{})
	if err != nil || !reflect.DeepEqual(actions, []string{"read:user_digests"}) {
		t.Fatal("HEAD permission", actions, err)
	}
}

func TestHTTPPolicyReplaysJSONAndUsesBodyScope(t *testing.T) {
	f := newFixture(t)
	var captured Authorization
	f.svc.Auth = policyDecision(Decision{Allowed: true, PrincipalID: "route-user", Role: roleHuman}, &captured)
	body := " {\n\"scope\":" + structuredText(scopeA) + ",\"content\":\"unchanged\"}\n"
	req := httptest.NewRequest("POST", "/v1/memories?workspace_id=foreign&project_id=foreign", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer route-test")
	mux := newAuthorizationMux(f.svc)
	mux.HandleFunc("POST /v1/{collection}", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != body {
			t.Fatal("body changed", err)
		}
		w.WriteHeader(201)
	})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 201 || captured.Scope != scopeA || captured.Action != "write:memories" {
		t.Fatal(w.Code, captured)
	}
}

func routeMultipartBody(t *testing.T, fileBytes int) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadata, err := writer.CreateFormField("metadata")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(metadata, structuredText(FileIngestInput{Source: CreateInput{Scope: scopeA}}))
	file, err := writer.CreateFormFile("file", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write(bytes.Repeat([]byte("x"), fileBytes))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

type countedRouteBody struct {
	io.Reader
	bytes int
}

func (b *countedRouteBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.bytes += n
	return n, err
}
func (*countedRouteBody) Close() error { return nil }

func TestHTTPMultipartPolicyReadsMetadataThenReplaysUpload(t *testing.T) {
	body, contentType := routeMultipartBody(t, 256<<10)
	counted := &countedRouteBody{Reader: bytes.NewReader(body)}
	req := httptest.NewRequest("POST", "/v1/sources/ingest", nil)
	req.Body = counted
	req.Header.Set("Content-Type", contentType)
	scope, _, err := readMultipartRouteScope(httptest.NewRecorder(), req)
	if err != nil || scope != scopeA {
		t.Fatal(scope, err)
	}
	if counted.bytes > 16<<10 {
		t.Fatalf("preflight buffered file: %d", counted.bytes)
	}
	reader, err := req.MultipartReader()
	if err != nil {
		t.Fatal("cannot replay multipart", err)
	}
	part, err := reader.NextPart()
	if err != nil || part.FormName() != "metadata" {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, part); err != nil {
		t.Fatal(err)
	}
	part, err = reader.NextPart()
	if err != nil || part.FormName() != "file" {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, part)
	if err != nil || n != 256<<10 {
		t.Fatal("upload changed", n, err)
	}
}

func TestHTTPActorRemainsBoundAfterPreflight(t *testing.T) {
	for _, field := range []string{"principal", "role"} {
		f := newFixture(t)
		calls := 0
		f.svc.Auth = authorizerFunc(func(_ context.Context, _ string, in Authorization) (Decision, error) {
			calls++
			d := Decision{Allowed: true, PrincipalID: "route-user", Role: roleHuman, Scope: in.Scope}
			if calls > 1 {
				if field == "principal" {
					d.PrincipalID = "other"
				} else {
					d.Role = roleService
				}
			}
			return d, nil
		})
		mux := newAuthorizationMux(f.svc)
		mux.HandleFunc("GET /v1/memories", func(w http.ResponseWriter, r *http.Request) {
			_, err := f.svc.authorize(r.Context(), bearer(r), "read:memories", scopeA, "", "")
			respond(w, nil, err)
		})
		req := routePolicyRequest(t, "GET /v1/memories", httpRoutePolicies["GET /v1/memories"])
		req.Header.Set("Authorization", "Bearer route-test")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != 403 || calls != 2 {
			t.Fatal(field, w.Code, calls, w.Body.String())
		}
	}
}

func TestHTTPHumanPolicyRefusesGrantedAgent(t *testing.T) {
	f := newFixture(t)
	mux := newAuthorizationMux(f.svc)
	ran := false
	mux.HandleFunc("POST /v1/crew", func(http.ResponseWriter, *http.Request) { ran = true })
	req := routePolicyRequest(t, "POST /v1/crew", httpRoutePolicies["POST /v1/crew"])
	req.Header.Set("Authorization", "Bearer "+f.agent)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 403 || ran {
		t.Fatal("agent entered human-only handler", w.Code, ran)
	}
}

func TestHTTPDiscardsMaskedActorChange(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.svc.Auth = authorizerFunc(func(_ context.Context, _ string, in Authorization) (Decision, error) {
		calls++
		d := Decision{Allowed: true, PrincipalID: "owner", Role: roleHuman, Scope: in.Scope}
		if calls > 1 {
			d.PrincipalID = "other"
		}
		return d, nil
	})
	mux := newAuthorizationMux(f.svc)
	mux.HandleFunc("GET /v1/memories", func(w http.ResponseWriter, r *http.Request) {
		_, _ = f.svc.authorize(r.Context(), bearer(r), "read:memories", scopeA, "", "")
		w.Header().Set("X-Private", "private-header")
		writeJSON(w, 200, map[string]string{"content": "private-response"})
	})
	req := routePolicyRequest(t, "GET /v1/memories", httpRoutePolicies["GET /v1/memories"])
	req.Header.Set("Authorization", "Bearer route-test")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 403 || w.Header().Get("X-Private") != "" || strings.Contains(w.Body.String(), "private-response") {
		t.Fatal("masked identity change leaked", w.Code, w.Body.String())
	}
}

// Supply valid route metadata but no operation payload, and revoke the grant
// immediately after preflight. Typed validation and service authorization must
// remain independent; neither may be replaced by the coarse route decision.
func TestHTTPMetadataGrantCannotBypassValidationOrRevocation(t *testing.T) {
	reachedPolicy := 0
	for pattern, policy := range httpRoutePolicies {
		if policy.scope != routeJSON {
			continue
		}
		t.Run(pattern, func(t *testing.T) {
			f := newFixture(t)
			f.svc.Repo = denialBoundaryRepo{}
			calls := 0
			f.svc.Auth = authorizerFunc(func(_ context.Context, _ string, in Authorization) (Decision, error) {
				calls++
				if calls > 1 {
					return Decision{}, ErrDenied
				}
				return Decision{Allowed: true, PrincipalID: "boundary-user", Role: roleHuman, Scope: in.Scope}, nil
			})
			method, path, _ := strings.Cut(pattern, " ")
			path = concretePath(strings.ReplaceAll(path, "{collection}", "memories"))
			input := map[string]any{"scope": scopeA}
			if path == "/v1/skills/propose" {
				input = map[string]any{"skill": map[string]any{"scope": scopeA}}
			}
			if strings.Contains(policy.action, "{digest_collection}") {
				input["kind"] = "topic"
			}
			if strings.Contains(policy.action, "{conflict_collection}") {
				input["memories"] = []Ref{{Collection: "memories", ID: "first", Revision: 1}, {Collection: "memories", ID: "second", Revision: 1}}
			}
			req := httptest.NewRequest(method, path, strings.NewReader(structuredText(input)))
			req.Header.Set("Authorization", "Bearer boundary-token")
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			if w.Code < 400 || w.Code >= 500 && w.Code != 503 {
				t.Fatal("metadata grant became operation success", w.Code, w.Body.String())
			}
			if len(f.emb.calls) != 0 {
				t.Fatal("incomplete or revoked request reached provider")
			}
			if calls > 1 {
				reachedPolicy++
			}
		})
	}
	if reachedPolicy < 20 {
		t.Fatalf("only %d operations reached a second policy check", reachedPolicy)
	}
	t.Logf("%d operations exercised revocation after route preflight", reachedPolicy)
}
