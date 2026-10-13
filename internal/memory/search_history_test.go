package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func exerciseSearchHistory(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.Embedder = nil
	src := f.source()
	now := time.Now().UTC().Truncate(time.Millisecond)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	base := Record{ID: "active", Scope: scopeA, Kind: "decision", Tags: []string{"match"}, Text: "historyneedle evidence", Keywords: []string{"historyneedle"}, Status: "active", ApprovedBy: "human", Revision: 1, Sources: refs(src), CreatedAt: past, UpdatedAt: past}
	for _, name := range []string{"active", "superseded", "conflicted", "rejected", "expired", "before_start", "past_end", "erased", "deleted", "unapproved", "foreign"} {
		r := base
		r.ID = name
		switch name {
		case "superseded", "conflicted", "rejected", "expired", "deleted":
			r.Status = name
		case "before_start":
			r.EffectiveAt = &future
		case "past_end":
			r.ExpiresAt = &past
		case "erased":
			r.ContentErased = true
			r.Status = "superseded"
		case "unapproved":
			r.ApprovedBy = ""
			r.Status = "superseded"
		case "foreign":
			r.Scope = scopeB
		}
		if e := repo.Insert(testCtx, colMemories, r); e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 40; i++ {
		r := base
		r.ID = fmt.Sprintf("noise-%02d", i)
		r.Tags = []string{"other"}
		if e := repo.Insert(testCtx, colMemories, r); e != nil {
			t.Fatal(e)
		}
	}
	for _, mode := range []string{"exact", "fts"} {
		for _, tc := range []struct {
			name               string
			history            bool
			statuses           []string
			limit, active, old int
			tags               bool
		}{
			{"default", false, nil, 50, 1, 0, true},
			{"all", true, nil, 50, 1, 6, true},
			{"status_prelimit", true, []string{"superseded"}, 1, 0, 1, false},
			{"status_or", true, []string{"superseded", "expired"}, 50, 0, 2, true},
			{"active_status", true, []string{"active"}, 50, 1, 2, true},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				filters := &SearchFilters{Statuses: tc.statuses}
				if tc.tags {
					filters.Tags = []string{"match"}
				}
				in := SearchInput{Scope: scopeA, Query: "historyneedle", IncludeInactive: tc.history, SearchCriteria: SearchCriteria{Filters: filters}, Modes: []string{mode}, Limit: tc.limit, BudgetTokens: 2000, TokenEncoding: "o200k_base"}
				req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(structuredText(in)))
				req.Header.Set("Authorization", "Bearer "+f.human)
				w := httptest.NewRecorder()
				Handler(f.svc).ServeHTTP(w, req)
				var out ContextBundle
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != tc.active || len(out.History) != tc.old {
					t.Fatalf("%d %s", w.Code, w.Body.String())
				}
				receipt, e := repo.Get(testCtx, "jobs", scopeA, out.ContextID)
				if e != nil || len(receipt.Usage.Items) != tc.active {
					t.Fatal("history recorded as usage", e)
				}
				for _, h := range out.History {
					if strings.Contains(out.ContextData, `"id":"`+h.ID+`"`) || h.Authority != AuthorityDataOnly || h.Scope != scopeA {
						t.Fatal("history entered model data")
					}
					if h.ID == "past_end" && (h.ExpiresAt == nil || !h.ExpiresAt.Equal(past)) {
						t.Fatal("missing expiry label")
					}
				}
				for _, h := range out.Items {
					if h.ID != "active" {
						t.Fatal("inactive context", h.ID)
					}
				}
			})
		}
	}
	// The context budget covers only active data. History still obeys max_chars.
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "historyneedle", IncludeInactive: true, SearchCriteria: SearchCriteria{Filters: &SearchFilters{Statuses: []string{"superseded"}}}, Modes: []string{"fts"}, MaxChars: 7, BudgetTokens: 32, TokenEncoding: "o200k_base"})
	if e != nil || len(out.History) != 1 || len([]rune(out.History[0].Text)) != 7 || len(out.Items) != 0 || out.TokensUsed > 32 {
		t.Fatal("history bounds", out, e)
	}
}
func TestSearchHistory(t *testing.T) { exerciseSearchHistory(t, newFakeRepo()) }

func TestSearchHistoryRejectsInvalidHTTP(t *testing.T) {
	for i, patch := range []map[string]any{
		{"filters": map[string]any{"statuses": []string{"superseded"}}},
		{"include_inactive": true, "purpose": "skills"}, {"include_inactive": true, "purpose": "knowledge"},
		{"include_inactive": true, "destination": "external:test"},
		{"include_inactive": true, "filters": map[string]any{"statuses": []string{"deleted"}}},
		{"include_inactive": true, "filters": map[string]any{"statuses": []string{"active", "active"}}},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			f := newFixture(t)
			called := false
			f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error { called = true; return nil }}
			in := map[string]any{"scope": scopeA, "query": "needle", "modes": []string{"fts"}}
			for k, v := range patch {
				in[k] = v
			}
			req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(structuredText(in)))
			req.Header.Set("Authorization", "Bearer "+f.human)
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			if w.Code != 400 || called || len(f.repo.rows("jobs")) != 0 {
				t.Fatal(w.Code, called)
			}
		})
	}
	for _, route := range []string{"/v1/skills/route", "/v1/context/build"} {
		f := newFixture(t)
		req := httptest.NewRequest("POST", route, strings.NewReader(structuredText(map[string]any{"scope": scopeA, "include_inactive": true, "query": "needle"})))
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		if w.Code != 400 {
			t.Fatal(route, w.Code)
		}
	}
}

func TestSearchHistoryFinalChecks(t *testing.T) {
	for _, mode := range []string{"body", "expiry", "status", "approval", "source", "deny", "unavailable", "actor", "audit"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			m := f.activeMemory("needle old")
			m.Status = "superseded"
			if e := f.repo.Replace(testCtx, colMemories, m, m.Revision); e != nil {
				t.Fatal(e)
			}
			var refreshErr error
			m, refreshErr = f.repo.Get(testCtx, colMemories, scopeA, m.ID)
			if refreshErr != nil {
				t.Fatal(refreshErr)
			}
			mutated := false
			called := false
			f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error {
				if a.Action != "read:memories" || a.ResourceID != m.ID || called {
					return nil
				}
				called = true
				switch mode {
				case "body":
					m.Text = "changed"
				case "expiry":
					at := time.Now().Add(-time.Hour)
					m.ExpiresAt = &at
				case "status":
					m.Status = "active"
				case "approval":
					m.ApprovedBy = ""
				case "source":
					src, err := f.repo.Get(testCtx, "sources", scopeA, m.Sources[0].SourceID)
					if err != nil {
						return err
					}
					err = f.repo.Delete(testCtx, "sources", scopeA, src.ID, src.Revision)
					mutated = err == nil
					return err
				case "deny":
					d.Allowed = false
					return nil
				case "unavailable":
					return ErrUnavailable
				case "actor":
					d.PrincipalID = "other"
					return nil
				case "audit":
					f.repo.failAudit = true
					return nil
				}
				err := f.repo.Replace(testCtx, colMemories, m, m.Revision)
				mutated = err == nil
				return err
			}}
			before := len(f.repo.rows("jobs"))
			out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "needle", IncludeInactive: true, Modes: []string{"fts"}, BudgetTokens: 2000, TokenEncoding: "o200k_base"})
			if !called {
				t.Fatal("history policy not called")
			}
			if slices.Contains([]string{"body", "expiry", "status", "approval", "source"}, mode) && !mutated {
				t.Fatal("fixture mutation failed before final check")
			}
			if slices.Contains([]string{"body", "expiry", "status", "approval"}, mode) && !errors.Is(e, ErrConflict) {
				t.Fatal("final record change not reported as conflict", e)
			}
			if mode == "deny" {
				if e != nil || len(out.Items) != 0 || len(out.History) != 0 {
					t.Fatal("denied history", e)
				}
				return
			}
			if e == nil || len(out.History) != 0 || len(out.Items) != 0 || out.ContextData != "" || out.ContextID != "" || len(f.repo.rows("jobs")) != before {
				t.Fatal("failed history leaked result or receipt", e)
			}
		})
	}
}

// Search state validation owns an independent slice.

func TestSearchHistoryBetweenLanes(t *testing.T) {
	for _, mode := range []string{"active_to_history", "history_update"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			m := f.activeMemory("needle old payload", "needle")
			if mode == "history_update" {
				m.Status = "superseded"
				if e := f.repo.Replace(testCtx, colMemories, m, m.Revision); e != nil {
					t.Fatal(e)
				}
			}
			var refreshErr error
			m, refreshErr = f.repo.Get(testCtx, colMemories, scopeA, m.ID)
			if refreshErr != nil {
				t.Fatal(refreshErr)
			}
			changed := false
			f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error {
				if changed || a.ResourceID != m.ID || a.Action != "context:read" && a.Action != "read:memories" {
					return nil
				}
				changed = true
				revision := m.Revision
				m.Status = "superseded"
				m.Text = "needle replacement payload"
				if mode == "history_update" {
					f.repo.mu.Lock()
					f.repo.store[colMemories][recKey(scopeA, m.ID)] = m
					f.repo.mu.Unlock()
					return nil
				}
				return f.repo.Replace(testCtx, colMemories, m, revision)
			}}
			before := len(f.repo.rows("jobs"))
			out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "needle", IncludeInactive: true, Modes: []string{"exact", "fts"}, BudgetTokens: 2000, TokenEncoding: "o200k_base"})
			if !changed || !errors.Is(e, ErrConflict) || len(out.History) != 0 || len(out.Items) != 0 || out.ContextData != "" || out.ContextID != "" || len(f.repo.rows("jobs")) != before {
				t.Fatal("different-lane snapshot returned", e, out)
			}
		})
	}
}
