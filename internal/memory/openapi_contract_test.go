package memory

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

type apiContractCase struct {
	Operation string          `json:"operation"`
	Status    int             `json:"status"`
	Body      json.RawMessage `json:"body"`
	Request   json.RawMessage `json:"request,omitempty"`
	Query     url.Values      `json:"query,omitempty"`
}

// Compare every production registration against the published management API.
// The source parser fails on unsupported registration syntax rather than skipping it.
func TestOpenAPIRouteCoverage(t *testing.T) {
	data, e := os.ReadFile("../../api/openapi.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if e = json.Unmarshal(data, &doc); e != nil {
		t.Fatal(e)
	}
	canonical := func(path string) string { return routeWildcard.ReplaceAllString(strings.TrimSuffix(path, "{$}"), "{}") }
	docs := map[string]bool{}
	for path, methods := range doc.Paths {
		for method := range methods {
			docs[strings.ToUpper(method)+" "+canonical(path)] = true
		}
	}
	actual := map[string]bool{}
	for _, r := range sourceRoutes(t) {
		key := r.method + " " + canonical(r.path)
		actual[key] = true
		if !docs[key] {
			t.Errorf("undocumented production route: %s %s", r.method, r.path)
		}
	}
	for key := range docs {
		if !actual[key] {
			t.Errorf("documented operation has no production route: %s", key)
		}
	}
	t.Logf("%d production operations; no undocumented exceptions", len(actual))
}

// Emit real handler responses for the offline JSON Schema validator in CI.
// Without an evidence destination, status/pagination/authorization assertions
// still execute as ordinary package tests.
func TestOpenAPIHTTPResponses(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	for i := 0; i < 4; i++ {
		r := f.activeMemory(fmt.Sprintf("UPPER record %d", i), "fixture")
		r.Summary = "summary needle"
		if e := f.svc.Repo.Replace(testCtx, colMemories, r, r.Revision); e != nil {
			t.Fatal(e)
		}
	}
	h := Handler(f.svc)
	base := "workspace_id=" + url.QueryEscape(scopeA.WorkspaceID) + "&project_id=" + url.QueryEscape(scopeA.ProjectID) + "&user_id=" + url.QueryEscape(scopeA.UserID) + "&agent_id=" + url.QueryEscape(scopeA.AgentID)
	cases := []apiContractCase{}
	call := func(extra, token string, want int) MemoryPage {
		req := httptest.NewRequest("GET", "/v1/memories?"+base+extra, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != want {
			t.Fatalf("%s: got %d: %s", extra, out.Code, out.Body.String())
		}
		if out.Header().Get("X-Request-ID") == "" {
			t.Fatal("missing documented request ID")
		}
		cases = append(cases, apiContractCase{Operation: "GET /v1/memories", Status: out.Code, Body: append(json.RawMessage(nil), out.Body.Bytes()...)})
		var page MemoryPage
		if want == 200 {
			if e := json.Unmarshal(out.Body.Bytes(), &page); e != nil {
				t.Fatal(e)
			}
		}
		return page
	}
	first := call("&limit=2&ignore_case=true&q=upper", f.human, 200)
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatal("missing actual cursor page")
	}
	second := call("&limit=2&ignore_case=true&q=upper&cursor="+url.QueryEscape(first.NextCursor), f.human, 200)
	if len(second.Items) != 2 || second.NextCursor != "" || first.Items[0].ID == second.Items[0].ID {
		t.Fatal("duplicate or incomplete continuation")
	}
	if p := call("&q=upper", f.human, 200); len(p.Items) != 0 {
		t.Fatal("default literal text search changed")
	}
	if p := call("&q=NEEDLE&ignore_case=true", f.human, 200); len(p.Items) != 4 {
		t.Fatal("display search missed summaries")
	}
	call("&limit=2&q=upper&ignore_case=false&cursor="+url.QueryEscape(first.NextCursor), f.human, 400)
	call("&limit=0", f.human, 400)
	call("&ignore_case=maybe", f.human, 400)
	call("&ignore_case=true&ignore_case=false", f.human, 400)
	call("&limit=2&q=other&ignore_case=true&cursor="+url.QueryEscape(first.NextCursor), f.human, 400)
	call("", "", 401)
	cases = append(cases, managementHTTPResponses(t)...)
	cases = append(cases, flowHTTPContractResponses(t)...)
	if dest := os.Getenv("ARKISM_OPENAPI_CASES"); dest != "" {
		b, e := json.MarshalIndent(cases, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(dest, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
