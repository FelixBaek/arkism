package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func (x fakeTx) DeletionReferences(_ context.Context, col string, scope Scope, target Ref, limit int) ([]Ref, error) {
	var out []Ref
	for _, r := range x.store[col] {
		if r.Scope != scope || col == target.Collection && r.ID == target.ID {
			continue
		}
		matches := func(ref *Ref) bool { return ref != nil && ref.Collection == target.Collection && ref.ID == target.ID }
		found := matches(r.Target) || matches(r.Proposal) || matches(r.SupersededBy)
		for _, src := range r.Sources {
			if target.Collection == "sources" && src.SourceID == target.ID {
				found = true
			}
		}
		for _, ref := range append(append([]Ref{}, r.Relations...), r.Supersedes...) {
			if matches(&ref) {
				found = true
			}
		}
		if r.Conflict != nil {
			if matches(r.Conflict.Replacement) {
				found = true
			}
			for _, ref := range r.Conflict.Missing {
				if matches(&ref) {
					found = true
				}
			}
		}
		if r.Usage != nil {
			if matches(r.Usage.Run) {
				found = true
			}
			for _, item := range r.Usage.Items {
				if matches(&item.Memory) {
					found = true
				}
			}
		}
		if found {
			out = append(out, Ref{Collection: col, ID: r.ID, Revision: r.Revision})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (f *fakeRepo) DeletionReferences(ctx context.Context, col string, scope Scope, target Ref, limit int) ([]Ref, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().DeletionReferences(ctx, col, scope, target, limit)
}

func TestDeleteDryRunCannotDelete(t *testing.T) {
	for _, query := range []string{"?dry_run=true", "?dry_run=TRUE", "?dry_run=1", "?dry_run=", "?dry_run", "?dry_run=true&dry_run=false", "?dryrun=true", "?dry_run=true&other=1", "?dry_run=%zz", "?dry_run=false;dry_run=true", "?&", "?dry_run=false", ""} {
		t.Run(query, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			before := len(f.repo.rows("audit_events"))
			jobsBefore := len(f.repo.rows("jobs"))
			raw, _ := json.Marshal(ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision})
			req := httptest.NewRequest(http.MethodDelete, "/v1/sources/"+src.ID+query, strings.NewReader(string(raw)))
			req.Header.Set("Authorization", "Bearer "+f.human)
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, req)
			_, e := f.repo.Get(testCtx, "sources", scopeA, src.ID)
			if query == "" || query == "?dry_run=false" {
				if w.Code != 202 || !errors.Is(e, ErrNotFound) {
					t.Fatalf("delete: %d %v", w.Code, e)
				}
				return
			}
			if e != nil {
				t.Fatalf("dry/invalid query deleted source: %v", e)
			}
			want := 400
			if query == "?dry_run=true" {
				want = 200
				var out DeletionPreview
				if json.Unmarshal(w.Body.Bytes(), &out) != nil || out.Target.ID != src.ID || out.CascadePlanAvailable || out.Coverage != "direct_references" {
					t.Fatalf("preview: %s", w.Body.String())
				}
			}
			if w.Code != want {
				t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
			}
			delta := len(f.repo.rows("audit_events")) - before
			if delta != 0 && query != "?dry_run=true" {
				t.Fatal("invalid query audited/executed")
			}
			if len(f.repo.rows("jobs")) != jobsBefore {
				t.Fatal("preview created deletion job")
			}
		})
	}
}

func TestDeletionPreviewCountsAndPreserves(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	memory := f.activeMemory("private content")
	memory.Sources = append(memory.Sources, sourceReference(src, "original"))
	memory.Embedding = &Embedding{Vector: []float64{1}}
	f.repo.store["memories"][recKey(scopeA, memory.ID)] = memory
	ref := Ref{Collection: "memories", ID: memory.ID, Revision: memory.Revision - 1}
	past := time.Now().Add(-time.Hour)
	rows := map[string]Record{
		"skill_versions": {ID: "private-skill", Scope: scopeA, Revision: 1, Status: "expired", ExpiresAt: &past, Relations: []Ref{ref, ref}, Text: "private skill text"},
		"evaluations":    {ID: "private-evaluation", Scope: scopeA, Revision: 1, Target: &ref},
		"jobs":           {ID: "private-job", Scope: scopeA, Revision: 1, Kind: "context_usage", Usage: &ContextUsage{Items: []MemoryUse{{Memory: ref}}}},
	}
	for col, row := range rows {
		if e := f.repo.Insert(testCtx, col, row); e != nil {
			t.Fatal(e)
		}
	}
	f.repo.store["memories"][recKey(scopeA, memory.ID)] = func() Record { r := memory; r.Relations = []Ref{ref}; return r }()
	original := map[string][]Record{}
	for _, col := range Collections {
		if col != "audit_events" {
			original[col] = f.repo.rows(col)
		}
	}
	out, e := f.svc.PreviewDeletion(testCtx, f.human, "memories", memory.ID, ChangeInput{Scope: scopeA, ExpectedRevision: memory.Revision})
	if e != nil || !out.TargetHasStoredVector || out.DirectReferences["memories"] != 0 || out.DirectReferences["skill_versions"] != 1 || out.DirectReferences["evaluations"] != 1 || out.DirectReferences["jobs"] != 3 {
		t.Fatalf("preview %+v %v", out, e)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "private-") || strings.Contains(string(raw), "private content") {
		t.Fatalf("body/ID leak: %s", raw)
	}
	for col, before := range original {
		after := f.repo.rows(col)
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("modified %s", col)
		}
	}
	out, e = f.svc.PreviewDeletion(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision})
	if e != nil || out.DirectReferences["memories"] != 1 {
		t.Fatalf("source refs: %+v %v", out, e)
	}
}

func TestDeletionPreviewGuards(t *testing.T) {
	for _, name := range []string{"agent", "denied_row", "changed_principal", "stale", "missing", "audit", "overflow", "unsupported"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			token := f.human
			col := "sources"
			id := src.ID
			revision := src.Revision
			want := ErrDenied
			row := Record{ID: "dependent", Scope: scopeA, Revision: 1, Sources: refs(src)}
			if e := f.repo.Insert(testCtx, "memories", row); e != nil {
				t.Fatal(e)
			}
			switch name {
			case "agent":
				token = f.agent
			case "denied_row":
				f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: row.ID}
			case "changed_principal":
				f.svc.Auth = usagePrincipalAuth{Authorizer: f.auth, action: "read:memories"}
			case "stale":
				revision++
				want = ErrConflict
			case "missing":
				id = "missing"
				want = ErrNotFound
			case "audit":
				f.repo.failAudit = true
				want = ErrUnavailable
			case "overflow":
				want = ErrUnavailable
				for i := 0; i < deletionPreviewLimit; i++ {
					r := row
					r.ID = fmt.Sprintf("row-%04d", i)
					if e := f.repo.Insert(testCtx, "memories", r); e != nil {
						t.Fatal(e)
					}
				}
			case "unsupported":
				col = "evaluations"
				want = ErrInvalid
				if e := f.repo.Insert(testCtx, col, Record{ID: id, Scope: scopeA, Revision: revision}); e != nil {
					t.Fatal(e)
				}
			}
			out, e := f.svc.PreviewDeletion(testCtx, token, col, id, ChangeInput{Scope: scopeA, ExpectedRevision: revision})
			if !errors.Is(e, want) || out.DirectReferences != nil || out.Target.ID != "" {
				t.Fatalf("guard %+v %v want %v", out, e, want)
			}
			if _, e = f.repo.Get(testCtx, "sources", scopeA, src.ID); e != nil {
				t.Fatalf("deleted %v", e)
			}
			if name == "unsupported" {
				raw, _ := json.Marshal(ChangeInput{Scope: scopeA, ExpectedRevision: revision})
				req := httptest.NewRequest(http.MethodDelete, "/v1/"+col+"/"+id+"?dry_run=true", strings.NewReader(string(raw)))
				req.Header.Set("Authorization", "Bearer "+f.human)
				w := httptest.NewRecorder()
				Handler(f.svc).ServeHTTP(w, req)
				_, err := f.repo.Get(testCtx, col, scopeA, id)
				if w.Code != 400 || err != nil {
					t.Fatalf("unsupported preview deleted: %d %v", w.Code, err)
				}
			}
		})
	}
}
