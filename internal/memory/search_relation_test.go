package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func (f *fakeRepo) SearchRelatedMemories(_ context.Context, in SearchInput, anchor Record) ([]Hit, error) {
	c, err := normalizeSearchCriteria(in.SearchCriteria)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := []Record{}
	for _, r := range f.rowsLocked(colMemories) {
		if historySearchable(r) && (in.IncludeInactive || live(r)) && c.matchesRecord(r) && relationMatches(in.RelatedTo.Collection, anchor, r) {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].UpdatedAt.Equal(rows[j].UpdatedAt) {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].UpdatedAt.After(rows[j].UpdatedAt)
	})
	out := []Hit{}
	for _, r := range rows {
		if len(out) == in.Limit {
			break
		}
		out = append(out, searchHit(r, colMemories, "relation"))
	}
	return out, nil
}

func exerciseSearchRelation(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	src := f.source()
	other := src
	other.ID = "other-source"
	if e := repo.Insert(testCtx, "sources", other); e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	base := Record{ID: "related", Scope: scopeA, Kind: "decision", Tags: []string{"match"}, Text: "relationneedle", Keywords: []string{"relationneedle"}, Status: "active", ApprovedBy: "human", Revision: 1, Sources: refs(src), CreatedAt: past, UpdatedAt: past}
	for _, name := range []string{"related", "old", "expired", "future", "erased", "unapproved", "foreign"} {
		r := base
		r.ID = name
		switch name {
		case "old":
			r.Status = "superseded"
		case "expired":
			r.ExpiresAt = &past
		case "future":
			r.EffectiveAt = &future
		case "erased":
			r.ContentErased = true
		case "unapproved":
			r.ApprovedBy = ""
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
		r.UpdatedAt = past
		if i%2 == 0 {
			r.Sources = refs(other)
		} else {
			r.Tags = []string{"other"}
		}
		if e := repo.Insert(testCtx, colMemories, r); e != nil {
			t.Fatal(e)
		}
	}
	sourceRef := Ref{Collection: "sources", ID: src.ID, Revision: src.Revision}
	for _, tc := range []struct {
		name               string
		history            bool
		statuses           []string
		limit, active, old int
	}{
		{"prelimit", false, nil, 1, 1, 0}, {"history", true, nil, 50, 1, 3}, {"status_prelimit", true, []string{"superseded"}, 1, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := SearchInput{Scope: scopeA, Query: "unmatched text", RelatedTo: &sourceRef, Modes: []string{"relation"}, IncludeInactive: tc.history, Limit: tc.limit, BudgetTokens: 2000, TokenEncoding: "o200k_base", SearchCriteria: SearchCriteria{Kinds: []string{"decision"}, Filters: &SearchFilters{Tags: []string{"match"}, Statuses: tc.statuses, CreatedFrom: &past, UpdatedTo: &now}}}
			req := httptest.NewRequest("POST", "/v1/search", strings.NewReader(structuredText(in)))
			req.Header.Set("Authorization", "Bearer "+f.human)
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			var out ContextBundle
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || len(out.Items) != tc.active || len(out.History) != tc.old {
				t.Fatal(w.Code, w.Body.String())
			}
			for _, h := range out.Items {
				if h.ID != "related" || !slices.Equal(h.Reasons, []string{"relation"}) {
					t.Fatal(h)
				}
			}
			receipt, e := repo.Get(testCtx, "jobs", scopeA, out.ContextID)
			if e != nil || len(receipt.Usage.Items) != tc.active {
				t.Fatal("usage", e)
			}
			for _, h := range out.History {
				if strings.Contains(out.ContextData, `"id":"`+h.ID+`"`) {
					t.Fatal("history in model context")
				}
			}
			if len(f.emb.calls) != 0 || len(out.Degraded) != 0 {
				t.Fatal("unselected provider called")
			}
		})
	}
	// Stored memory links require exact revision and may address history.
	anchor := base
	anchor.ID = "memory-anchor"
	anchor.Sources = refs(other)
	anchor.Supersedes = []Ref{{Collection: colMemories, ID: "old", Revision: 1}}
	anchor.SupersededBy = &Ref{Collection: colMemories, ID: "related", Revision: 1}
	// An expired reference must never override the live time predicate.
	anchor.Supersedes = append(anchor.Supersedes, Ref{Collection: colMemories, ID: "expired", Revision: 1})
	if e := repo.Insert(testCtx, colMemories, anchor); e != nil {
		t.Fatal(e)
	}
	evaluation := base
	evaluation.ID = "evaluation-anchor"
	evaluation.Target = &Ref{Collection: colMemories, ID: "related", Revision: 1}
	evaluation.Relations = []Ref{{Collection: colMemories, ID: anchor.ID, Revision: 1}}
	if e := repo.Insert(testCtx, "evaluations", evaluation); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		col         string
		r           Record
		history     bool
		active, old int
	}{{colMemories, anchor, false, 1, 0}, {colMemories, anchor, true, 1, 2}, {"evaluations", evaluation, false, 2, 0}} {
		in := SearchInput{Scope: scopeA, Query: "unmatched", RelatedTo: &Ref{Collection: tc.col, ID: tc.r.ID, Revision: tc.r.Revision}, Modes: []string{"relation"}, IncludeInactive: tc.history}
		out, e := f.svc.Search(testCtx, f.human, in)
		if e != nil || len(out.Items) != tc.active || len(out.History) != tc.old {
			t.Fatal(tc.col, tc.history, e, out)
		}
		in.RelatedTo.Revision++
		if _, e = f.svc.Search(testCtx, f.human, in); !errors.Is(e, ErrConflict) {
			t.Fatal("stale anchor", e)
		}
	}
	pack, e := f.svc.BuildContext(testCtx, f.human, ContextBuildInput{Scope: scopeA, Task: "unmatched", RelatedTo: &Ref{Collection: colMemories, ID: anchor.ID, Revision: 1}, Modes: []string{"relation"}, TokenEncoding: "cl100k_base"})
	if e != nil || len(pack.Items) != 1 || pack.Items[0].ID != "related" {
		t.Fatal("context relation", e, pack)
	}
	// A newer record revision cannot satisfy a stored old link.
	changed := base
	changed.Revision = 2
	if e = repo.Replace(testCtx, colMemories, changed, 1); e != nil {
		t.Fatal(e)
	}
	out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "unmatched", RelatedTo: &Ref{Collection: colMemories, ID: anchor.ID, Revision: 1}, Modes: []string{"relation"}})
	if e != nil || len(out.Items) != 0 {
		t.Fatal("stale link followed latest", e, out)
	}
}
func TestSearchRelation(t *testing.T) { exerciseSearchRelation(t, newFakeRepo()) }

func TestSearchRelationInvalid(t *testing.T) {
	for _, mode := range []string{"missing_ref", "missing_mode", "collection", "revision", "id", "purpose", "destination", "hub", "route"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			called := false
			f.svc.Auth = requestActorAuth{Authorizer: f.auth, payloadOnly: true, change: func(Authorization, *Decision) error { called = true; return nil }}
			in := SearchInput{Scope: scopeA, Query: "needle", Modes: []string{"relation"}, RelatedTo: &Ref{Collection: "sources", ID: "source", Revision: 1}}
			switch mode {
			case "missing_ref":
				in.RelatedTo = nil
			case "missing_mode":
				in.Modes = nil
			case "collection":
				in.RelatedTo.Collection = "jobs"
			case "revision":
				in.RelatedTo.Revision = 0
			case "id":
				in.RelatedTo.ID = " bad"
			case "purpose":
				in.Purpose = "skills"
			case "destination":
				in.Destination = "external:test"
			case "hub":
				in.Modes = append(in.Modes, "hub")
			}
			path := "/v1/search"
			if mode == "route" {
				path = "/v1/skills/route"
			}
			req := httptest.NewRequest("POST", path, strings.NewReader(structuredText(in)))
			req.Header.Set("Authorization", "Bearer "+f.human)
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			if w.Code != 400 || called {
				t.Fatal("invalid relation reached policy", w.Code)
			}
		})
	}
}

func TestSearchRelationFinalChecks(t *testing.T) {
	for _, mode := range []string{"metadata_deny", "body_deny", "unavailable", "actor", "role", "body_changed", "anchor_deleted", "audit"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			m := f.activeMemory("needle")
			src, e := f.repo.Get(testCtx, "sources", scopeA, m.Sources[0].SourceID)
			if e != nil {
				t.Fatal(e)
			}
			before := len(f.repo.rows("jobs"))
			called := false
			f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error {
				if strings.HasSuffix(mode, "deny") || mode == "unavailable" || mode == "actor" || mode == "role" {
					if a.Action != "read:sources" || a.ResourceID != src.ID || (mode != "metadata_deny" && a.Content == "") {
						return nil
					}
					called = true
					switch mode {
					case "metadata_deny", "body_deny":
						d.Allowed = false
					case "unavailable":
						return ErrUnavailable
					case "actor":
						d.PrincipalID = "other"
					case "role":
						d.Role = "other"
					}
					return nil
				}
				if a.Action != "context:read" || a.ResourceID != m.ID || called {
					return nil
				}
				called = true
				switch mode {
				case "body_changed":
					src.Text = "changed"
					return f.repo.Replace(testCtx, "sources", src, src.Revision)
				case "anchor_deleted":
					return f.repo.Delete(testCtx, "sources", scopeA, src.ID, src.Revision)
				case "audit":
					f.repo.failAudit = true
				}
				return nil
			}}
			out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "unmatched", RelatedTo: &Ref{Collection: "sources", ID: src.ID, Revision: src.Revision}, Modes: []string{"relation"}})
			if !called || e == nil || len(out.Items) != 0 || out.ContextID != "" || out.ContextData != "" || len(f.repo.rows("jobs")) != before {
				t.Fatal(mode, called, e, out)
			}
			if mode == "body_changed" && !errors.Is(e, ErrConflict) {
				t.Fatal("change did not fail final comparison", e)
			}
		})
	}
}

func TestSearchRelationSupersessionAndUnion(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	old := f.activeMemory("old decision")
	pair, e := f.svc.Supersede(testCtx, f.human, old.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: old.Revision, Reason: "updated", NewContent: "new decision", Sources: refs(src)})
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		anchor, want Record
		history      bool
	}{{pair.Old, pair.New, false}, {pair.New, pair.Old, true}} {
		out, e := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "unmatched", Modes: []string{"relation"}, RelatedTo: &Ref{Collection: colMemories, ID: tc.anchor.ID, Revision: tc.anchor.Revision}, IncludeInactive: tc.history})
		if e != nil {
			t.Fatal(e)
		}
		if tc.history {
			if len(out.Items) != 0 || len(out.History) != 1 || out.History[0].ID != tc.want.ID {
				t.Fatal(out)
			}
		} else {
			if len(out.Items) != 1 || out.Items[0].ID != tc.want.ID {
				t.Fatal(out)
			}
		}
	}
	unrelated := f.activeMemory("unionneedle", "unionneedle")
	in := SearchInput{Scope: scopeA, Query: "unionneedle", Modes: []string{"relation", "fts", "exact"}, RelatedTo: &Ref{Collection: colMemories, ID: pair.Old.ID, Revision: pair.Old.Revision}}
	out, e := f.svc.Search(testCtx, f.human, in)
	if e != nil || len(out.Items) != 2 {
		t.Fatal("union", e, out)
	}
	ids := []string{out.Items[0].ID, out.Items[1].ID}
	if !slices.Contains(ids, unrelated.ID) || !slices.Contains(ids, pair.New.ID) {
		t.Fatal(ids)
	}
	in.Query = "new decision"
	out, e = f.svc.Search(testCtx, f.human, in)
	if e != nil || len(out.Items) != 1 || !slices.Contains(out.Items[0].Reasons, "relation") || !slices.Contains(out.Items[0].Reasons, reasonTextSearch) {
		t.Fatal("fusion", e, out)
	}
}

func TestSearchRelationAnchorEvidence(t *testing.T) {
	for _, mode := range []string{"memory_link_changed", "evaluation_link_changed", "evaluation_evidence_deleted"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			m := f.activeMemory("target")
			anchor := m
			anchor.ID = "anchor"
			col := colMemories
			anchor.SupersededBy = &Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}
			evidence := f.activeMemory("evidence")
			if strings.HasPrefix(mode, "evaluation") {
				col = "evaluations"
				anchor.SupersededBy = nil
				anchor.Target = &Ref{Collection: colMemories, ID: m.ID, Revision: m.Revision}
				anchor.Relations = []Ref{{Collection: colMemories, ID: evidence.ID, Revision: evidence.Revision}}
			}
			if e := f.repo.Insert(testCtx, col, anchor); e != nil {
				t.Fatal(e)
			}
			in := SearchInput{Scope: scopeA, Query: "unmatched", Modes: []string{"relation"}, RelatedTo: &Ref{Collection: col, ID: anchor.ID, Revision: anchor.Revision}}
			before := len(f.repo.rows("jobs"))
			changed := false
			f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(a Authorization, d *Decision) error {
				if a.Action != "context:read" || a.ResourceID != m.ID || changed {
					return nil
				}
				changed = true
				if mode == "evaluation_evidence_deleted" {
					return f.repo.Delete(testCtx, colMemories, scopeA, evidence.ID, evidence.Revision)
				}
				// Same-revision mutations must also invalidate the authorized anchor snapshot.
				f.repo.mu.Lock()
				defer f.repo.mu.Unlock()
				if col == colMemories {
					anchor.SupersededBy = nil
				} else {
					anchor.Target = nil
				}
				for key, r := range f.repo.store[col] {
					if r.ID == anchor.ID && r.Scope == scopeA {
						f.repo.store[col][key] = anchor
						return nil
					}
				}
				return ErrNotFound
			}}
			out, e := f.svc.Search(testCtx, f.human, in)
			if !changed || !errors.Is(e, ErrConflict) || len(out.Items) != 0 || out.ContextID != "" || len(f.repo.rows("jobs")) != before {
				t.Fatal(mode, changed, e, out)
			}
		})
	}
}
