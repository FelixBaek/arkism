package memory

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
)

func contextViewHTTP(t *testing.T, s *Service, token string, in ContextBuildInput) (ContextPack, int) {
	t.Helper()
	raw, e := json.Marshal(in)
	if e != nil {
		t.Fatal(e)
	}
	request := httptest.NewRequest("POST", "/v1/context/build", bytes.NewReader(raw))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	Handler(s).ServeHTTP(response, request)
	var p ContextPack
	if response.Code == 200 {
		if e = json.Unmarshal(response.Body.Bytes(), &p); e != nil {
			t.Fatal(e)
		}
	}
	return p, response.Code
}
func TestContextViewHTTPCurrentNextAndNewSession(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	m := viewMemory(t, f, scopeA, "viewcanary HTTP evidence")
	in := viewInput(scopeA)
	in.ContextView = excludeView(scopeA, m)
	off, code := contextViewHTTP(t, f.svc, f.human, in)
	if code != 200 || viewHas(off.Items, m.ID) || off.ContextView == nil {
		t.Fatal("HTTP view", code)
	}
	f.svc.NativeSearchShadow = nativeSearchTestRuntime(t, "B0")
	on, code := contextViewHTTP(t, f.svc, f.human, in)
	if code != 200 {
		t.Fatal(code)
	}
	off.ContextID = ""
	on.ContextID = ""
	if !reflect.DeepEqual(off, on) {
		t.Fatal("HTTP shadow changed masked response")
	}
	for _, session := range []string{"same-next", "brand-new"} {
		in.ContextView = nil
		p, status := contextViewHTTP(t, f.svc, f.human, in)
		if status != 200 || !viewHas(p.Items, m.ID) || p.ContextView != nil {
			t.Fatal("HTTP nonpersistent", session, status)
		}
		viewReadPack(t, session, "HTTP delivered", p)
	}
}
func TestContextViewHTTPRejectsScopeAndUnknownFields(t *testing.T) {
	f := newFixture(t)
	m := viewMemory(t, f, scopeA, "viewcanary HTTP")
	in := viewInput(scopeA)
	in.ContextView = excludeView(scopeB, m)
	if _, code := contextViewHTTP(t, f.svc, f.human, in); code != 400 {
		t.Fatal("foreign scope", code)
	}
	for _, path := range []string{"/v1/context/build", "/v1/search"} {
		t.Run(path, func(t *testing.T) {
			var payload any
			if path == "/v1/search" {
				payload = map[string]any{"scope": scopeA, "query": "viewcanary", "context_view": excludeView(scopeA, m)}
			} else {
				payload = map[string]any{"scope": scopeA, "task": "viewcanary", "token_encoding": "cl100k_base", "context_view": map[string]any{"protocol": ContextViewProtocol, "context_scope": scopeA, "exclude": []Ref{}, "persistent": true}}
			}
			raw, _ := json.Marshal(payload)
			req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+f.human)
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			if w.Code != 400 {
				t.Fatal("new unsupported wire field accepted", w.Code)
			}
		})
	}
}

func TestContextViewHTTPExplicitNullLegacyInput(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	m := viewMemory(t, f, scopeA, "viewcanary explicit-null")
	raw, _ := json.Marshal(map[string]any{"scope": scopeA, "task": "viewcanary", "modes": []string{"fts"}, "token_encoding": "cl100k_base", "context_view": nil})
	req := httptest.NewRequest("POST", "/v1/context/build", bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+f.human)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	var p ContextPack
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.ContextView != nil || !viewHas(p.Items, m.ID) {
		t.Fatal("explicit null is not legacy nil", w.Code)
	}
}
