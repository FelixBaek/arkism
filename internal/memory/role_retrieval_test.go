package memory

import (
	"errors"
	"testing"
	"time"
)

func TestRoleRetrievalNarrowsBeforeLimit(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i, kind := range []string{"raw", "episodic", "procedural", "decision"} {
		r := Record{ID: kind, Scope: scopeA, Kind: kind, Text: "rolefind", Keywords: []string{"rolefind"}, Status: "active", ApprovedBy: "human", Revision: 1, Sources: refs(src), CreatedAt: now, UpdatedAt: now.Add(-time.Duration(i) * time.Minute)}
		if e := f.repo.Insert(testCtx, colMemories, r); e != nil {
			t.Fatal(e)
		}
	}
	in := RoleSearchInput{Role: "implementer", SearchInput: SearchInput{Scope: scopeA, Query: "rolefind", Modes: []string{"exact"}, Limit: 1}}
	out, err := f.svc.SearchForRole(testCtx, f.agent, in)
	if err != nil || len(out.Items) != 1 || (out.Items[0].Kind != "procedural" && out.Items[0].Kind != "decision") {
		t.Fatal(out, err)
	}
	in.Kinds = []string{"raw"}
	if _, err = f.svc.SearchForRole(testCtx, f.agent, in); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty intersection widened", err)
	}
	in.Kinds = []string{"raw", "decision"}
	out, err = f.svc.SearchForRole(testCtx, f.agent, in)
	if err != nil || len(out.Items) != 1 || out.Items[0].Kind != "decision" {
		t.Fatal(out, err)
	}
	a := f.auth.actors[f.agent]
	a.wrongScope = true
	f.auth.actors[f.agent] = a
	in.Scope = scopeA
	if _, err = f.svc.SearchForRole(testCtx, f.agent, in); !errors.Is(err, ErrDenied) {
		t.Fatal("role widened scope", err)
	}
	in.Scope = scopeA
	in.Role = "admin"
	if _, err = f.svc.SearchForRole(testCtx, f.agent, in); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown profile", err)
	}
}
