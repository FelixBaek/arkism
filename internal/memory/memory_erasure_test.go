package memory

import (
	"context"
	"errors"
	"testing"
)

func TestMemoryDeleteErasesProposal(t *testing.T) {
	for _, name := range []string{"success", "missing", "denied", "principal", "audit", "stale", "bad_link", "legacy", "changed", "old_candidate", "invalid_revision"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			c := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "original proposal copy", Sources: refs(src)}))
			m, e := f.svc.ReviewCandidate(testCtx, f.human, c.ID, CandidateReviewInput{Scope: scopeA, ExpectedRevision: c.Revision, Action: "edit_approve", Content: "edited approved memory"})
			if e != nil {
				t.Fatal(e)
			}
			in := ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision}
			want := error(nil)
			switch name {
			case "missing":
				delete(f.repo.store["memory_candidates"], recKey(scopeA, c.ID))
			case "denied":
				f.svc.Auth = memoryProposalDeny{Authorizer: f.auth}
				want = ErrDenied
			case "principal":
				f.svc.Auth = usagePrincipalAuth{Authorizer: f.auth, action: "delete:memory_candidates"}
				want = ErrDenied
			case "audit":
				f.repo.failAudit = true
				want = ErrUnavailable
			case "changed":
				f.svc.Repo = evaluationChangeRepo{Repository: f.repo, change: func() {
					updated := m
					updated.Revision++
					if e := f.repo.Replace(testCtx, "memories", updated, m.Revision); e != nil {
						t.Fatal(e)
					}
				}}
				want = ErrConflict
			case "old_candidate":
				f.repo.store["memory_candidates"][recKey(scopeA, c.ID)] = c
				want = ErrConflict
			case "invalid_revision":
				in.ExpectedRevision = 0
				want = ErrInvalid
			case "stale":
				in.ExpectedRevision--
				want = ErrConflict
			case "bad_link", "legacy":
				if name == "bad_link" {
					m.Proposal = &Ref{Collection: "sources", ID: src.ID, Revision: 1}
					want = ErrUnavailable
				} else {
					m.Proposal = nil
				}
				// Simulate legacy/corrupt stored data; API provenance is immutable.
				f.repo.store["memories"][recKey(scopeA, m.ID)] = m
			}
			e = f.svc.Delete(testCtx, f.human, "memories", m.ID, in)
			if !errors.Is(e, want) {
				t.Fatalf("got %v want %v", e, want)
			}
			_, me := f.repo.Get(testCtx, "memories", scopeA, m.ID)
			_, ce := f.repo.Get(testCtx, "memory_candidates", scopeA, c.ID)
			if want != nil {
				if me != nil || ce != nil {
					t.Fatalf("failed deletion changed records %v %v", me, ce)
				}
			} else {
				if !errors.Is(me, ErrNotFound) {
					t.Fatal("memory remained", me)
				}
				if name == "legacy" {
					if ce != nil {
						t.Fatal("unproven candidate deleted", ce)
					}
				} else if !errors.Is(ce, ErrNotFound) {
					t.Fatal("proposal remained", ce)
				}
			}
			if _, e = f.repo.Get(testCtx, "sources", scopeA, src.ID); e != nil {
				t.Fatal("source deleted", e)
			}
		})
	}
}

type memoryProposalDeny struct{ Authorizer }

func (a memoryProposalDeny) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if in.Action == "delete:memory_candidates" {
		return Decision{}, nil
	}
	return a.Authorizer.Authorize(ctx, token, in)
}

func TestMemoryErasureKeepsForeignProposal(t *testing.T) {
	for _, dimension := range []string{"workspace", "project", "user", "agent"} {
		t.Run(dimension, func(t *testing.T) {
			f := newFixture(t)
			m := f.activeMemory("scope isolated memory")
			candidate, e := f.repo.Get(testCtx, "memory_candidates", scopeA, m.ID)
			if e != nil {
				t.Fatal(e)
			}
			foreign := scopeA
			switch dimension {
			case "workspace":
				foreign.WorkspaceID = "elsewhere"
			case "project":
				foreign.ProjectID = "elsewhere"
			case "user":
				foreign.UserID = "elsewhere"
			case "agent":
				foreign.AgentID = "elsewhere"
			}
			delete(f.repo.store["memory_candidates"], recKey(scopeA, m.ID))
			candidate.Scope = foreign
			if e = f.repo.Insert(testCtx, "memory_candidates", candidate); e != nil {
				t.Fatal(e)
			}
			if e = f.svc.Delete(testCtx, f.human, "memories", m.ID, ChangeInput{Scope: scopeA, ExpectedRevision: m.Revision}); e != nil {
				t.Fatal(e)
			}
			if _, e = f.repo.Get(testCtx, "memory_candidates", foreign, m.ID); e != nil {
				t.Fatal("foreign candidate deleted", e)
			}
		})
	}
}
