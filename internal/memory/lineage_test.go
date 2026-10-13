package memory

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestMemoryLineageProposalAndReplacement(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "original proposal", Sources: refs(src)}))
	m, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "edit_approve", Content: "corrected decision"})
	if e != nil || m.Proposal == nil {
		t.Fatal(m, e)
	}
	out, e := f.svc.MemoryLineage(testCtx, f.human, scopeA, m.ID)
	if e != nil || len(out.Links) != 1 || out.Links[0].State != "available" || out.Links[0].Record.Text != c.Text || out.Links[0].Reference.Revision != c.Revision+1 {
		t.Fatal(out, e)
	}
	changed := m
	changed.Proposal = nil
	if e = f.repo.Replace(testCtx, "memories", changed, m.Revision); !errors.Is(e, ErrConflict) {
		t.Fatal("proposal link mutated", e)
	}
	replacement, e := f.svc.Supersede(testCtx, f.human, m.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: m.Revision, Reason: "new decision", NewContent: "replacement", Sources: m.Sources})
	if e != nil {
		t.Fatal(e)
	}
	out, e = f.svc.MemoryLineage(testCtx, f.human, scopeA, m.ID)
	if e != nil || out.Status != "superseded" || len(out.Links) != 2 || out.Links[1].Relation != "superseded_by" || out.Links[1].Record.ID != replacement.New.ID {
		t.Fatal(out, e)
	}
	out, e = f.svc.MemoryLineage(testCtx, f.human, scopeA, replacement.New.ID)
	if e != nil || len(out.Links) != 1 || out.Links[0].Relation != "supersedes" || out.Links[0].Record.ID != m.ID {
		t.Fatal(out, e)
	}
}

func TestMemoryLineageGuards(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("lineage visibility")
	for _, test := range []struct {
		name   string
		change func()
		state  string
	}{
		{"hidden", func() { f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: r.ID} }, "root_denied"},
		{"available", func() { f.svc.Auth = f.auth }, "available"},
		{"changed", func() {
			row := f.repo.store["memory_candidates"][recKey(scopeA, r.ID)]
			row.Revision++
			f.repo.store["memory_candidates"][recKey(scopeA, r.ID)] = row
		}, "changed"},
		{"invalid_sources", func() {
			row := f.repo.store["memory_candidates"][recKey(scopeA, r.ID)]
			row.Revision--
			f.repo.store["memory_candidates"][recKey(scopeA, r.ID)] = row
			delete(f.repo.store["sources"], recKey(scopeA, r.Sources[0].SourceID))
		}, "invalid_sources"},
		{"unavailable", func() { delete(f.repo.store["memory_candidates"], recKey(scopeA, r.ID)) }, "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.change()
			out, e := f.svc.MemoryLineage(testCtx, f.human, scopeA, r.ID)
			if test.state == "root_denied" {
				if !errors.Is(e, ErrDenied) || out.Memory.ID != "" {
					t.Fatal(out, e)
				}
				return
			}
			if e != nil || len(out.Links) != 1 || out.Links[0].State != test.state {
				t.Fatal(out, e)
			}
			if test.state != "available" && out.Links[0].Record != nil {
				t.Fatal("body leak", out)
			}
		})
	}
	for _, scope := range []Scope{scopeB, {WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}} {
		if _, e := f.svc.MemoryLineage(testCtx, f.human, scope, r.ID); !errors.Is(e, ErrNotFound) {
			t.Fatal("scope leak", e)
		}
	}
	f.repo.failAudit = true
	if out, e := f.svc.MemoryLineage(testCtx, f.human, scopeA, r.ID); !errors.Is(e, ErrUnavailable) || out.Memory.ID != "" {
		t.Fatal(out, e)
	}
}

func TestMemoryLineageHiddenLinkAndHTTP(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("old")
	newer, e := f.svc.Supersede(testCtx, f.human, r.ID, SupersedeInput{Scope: scopeA, ExpectedRevision: r.Revision, Reason: "new", NewContent: "new", Sources: r.Sources})
	if e != nil {
		t.Fatal(e)
	}
	f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: newer.New.ID}
	out, e := f.svc.MemoryLineage(testCtx, f.human, scopeA, r.ID)
	if e != nil || out.Links[1].State != "hidden" || out.Links[1].Record != nil {
		t.Fatal(out, e)
	}
	for _, q := range []string{"workspace_id=ws-a&project_id=proj-a", "workspace_id=ws-a&project_id=proj-a&bad=1"} {
		req := httptest.NewRequest("GET", "/v1/memories/"+r.ID+"/lineage?"+q, nil)
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		want := 200
		if q != "workspace_id=ws-a&project_id=proj-a" {
			want = 400
		}
		if w.Code != want || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}

type lineageAuth struct {
	Authorizer
	outage bool
}

func (a lineageAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, e := a.Authorizer.Authorize(ctx, token, in)
	if in.Action == "read:memory_candidates" {
		if a.outage {
			return Decision{}, ErrUnavailable
		}
		d.PrincipalID = "other"
	}
	return d, e
}
func TestMemoryLineagePolicyAndLegacy(t *testing.T) {
	f := newFixture(t)
	r := f.activeMemory("lineage policy")
	for _, outage := range []bool{false, true} {
		f.svc.Auth = lineageAuth{Authorizer: f.auth, outage: outage}
		out, e := f.svc.MemoryLineage(testCtx, f.human, scopeA, r.ID)
		want := ErrDenied
		if outage {
			want = ErrUnavailable
		}
		if !errors.Is(e, want) || out.Memory.ID != "" || len(out.Links) != 0 {
			t.Fatal(out, e)
		}
	}
	f.svc.Auth = f.auth
	// A same-ID candidate is not proof that an older memory came from that candidate.
	r.Proposal = nil
	f.repo.store["memories"][recKey(scopeA, r.ID)] = r
	out, e := f.svc.MemoryLineage(testCtx, f.human, scopeA, r.ID)
	if e != nil || len(out.Links) != 0 {
		t.Fatal("inferred legacy lineage", out, e)
	}
	r.Supersedes = make([]Ref, 65)
	f.repo.store["memories"][recKey(scopeA, r.ID)] = r
	if _, e = f.svc.MemoryLineage(testCtx, f.human, scopeA, r.ID); !errors.Is(e, ErrInvalid) {
		t.Fatal("unbounded lineage", e)
	}
	r.Supersedes = nil
	r.Proposal = &Ref{Collection: "sources", ID: r.ID, Revision: 1}
	f.repo.store["memories"][recKey(scopeA, r.ID)] = r
	if _, e = f.svc.MemoryLineage(testCtx, f.human, scopeA, r.ID); !errors.Is(e, ErrInvalid) {
		t.Fatal("wrong collection", e)
	}
}
