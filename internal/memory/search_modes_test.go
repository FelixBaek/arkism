package memory

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func exerciseSearchModes(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	memory := f.activeMemory("selectionneedle", "selectionneedle")
	// Both native lanes must find the same live, approved record.
	for _, modes := range [][]string{{"exact"}, {"fts"}, {"fts", "exact"}} {
		in := SearchInput{Scope: scopeA, Query: "selectionneedle", Purpose: "knowledge", Modes: modes}
		req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(structuredText(in)))
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		var out ContextBundle
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("mode HTTP", modes, w.Code, w.Body.String())
		}
		if len(f.emb.calls) != 0 || len(out.Degraded) != 0 {
			t.Fatal("unselected vector ran or degraded", modes, out.Degraded)
		}
		if len(out.Items) != 1 || out.Items[0].ID != memory.ID {
			t.Fatal("local selection", modes, out)
		}
		reasons := out.Items[0].Reasons
		if slices.Contains(reasons, reasonExactKeyword) != slices.Contains(modes, "exact") || slices.Contains(reasons, reasonTextSearch) != slices.Contains(modes, "fts") || slices.Contains(reasons, reasonVector) {
			t.Fatal("unselected lane returned", modes, reasons)
		}
	}
	// A selected unavailable vector lane must not silently run text search.
	f.svc.Embedder = nil
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "selectionneedle", Modes: []string{"vector"}})
	if err != nil || len(out.Items) != 0 || !slices.Contains(out.Degraded, "memory_vector_disabled") {
		t.Fatal("vector-only fallback", out, err)
	}
	// Existing callers still get both native lanes; explicit [] retains that default.
	for _, modes := range [][]string{nil, {}} {
		out, err = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "selectionneedle", Modes: modes})
		if err != nil || len(out.Items) != 1 || !slices.Contains(out.Items[0].Reasons, reasonExactKeyword) || !slices.Contains(out.Items[0].Reasons, reasonTextSearch) {
			t.Fatal("legacy default changed", out, err)
		}
	}
}
func TestSearchModes(t *testing.T) { exerciseSearchModes(t, newFakeRepo()) }

func TestSearchModesRejectInvalidBeforePolicy(t *testing.T) {
	for _, modes := range [][]string{{"exact", "exact"}, {"fts", "fts"}, {"unknown"}, {"EXACT"}, {"relation"}, {"hub"}, {"exact", "fts", "vector", "hub", "exact"}} {
		f := newFixture(t)
		called := false
		f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error { called = true; return nil }}
		in := SearchInput{Scope: scopeA, Query: "needle", Modes: modes}
		req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(structuredText(in)))
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != 400 || called || len(f.emb.calls) != 0 || len(f.repo.rows("jobs")) != 0 {
			t.Fatal("invalid mode had side effects", modes, w.Code)
		}
	}
}

func TestSearchModesVectorOnly(t *testing.T) {
	f := newFixture(t)
	row := f.activeMemory("selected vector", "selected")
	if err := f.svc.WorkOnce(testCtx, f.work); err != nil {
		t.Fatal(err)
	}
	f.emb.calls = nil
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "selected", Modes: []string{"vector"}})
	if err != nil || len(out.Items) != 1 || out.Items[0].ID != row.ID || len(f.emb.calls) != 1 || !slices.Equal(out.Items[0].Reasons, []string{reasonVector}) {
		t.Fatal("vector only", out, err)
	}
}

func TestSearchModesSharedInputs(t *testing.T) {
	f := newFixture(t)
	v := analyzedSkill(t, f)
	for _, modes := range []string{`["fts"]`, `["hub"]`, `null`, `[]`} {
		req := httptest.NewRequest("POST", "/v1/skills/route", strings.NewReader(`{"scope":{"workspace_id":"ws-a","project_id":"proj-a"},"query":"index","purpose":"knowledge","modes":`+modes+`}`))
		req.Header.Set("Authorization", "Bearer "+f.human)
		// Keep the embedder available for fts; disable it only for default controls.
		f.svc.Embedder = f.emb
		if modes == `null` || modes == `[]` {
			f.svc.Embedder = nil
		}
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if modes == `["hub"]` {
			if w.Code != 400 {
				t.Fatal("routing accepted an unknown mode", w.Code)
			}
			continue
		}
		var out RouteResult
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.SelectedID != v.ID {
			t.Fatal("routing mode", modes, w.Code, w.Body.String())
		}
	}
	if len(f.emb.calls) != 0 {
		t.Fatal("routing ignored fts-only selection")
	}
}
