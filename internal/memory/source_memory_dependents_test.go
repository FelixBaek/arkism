package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSourceMemoryDependents(t *testing.T) { testSourceMemoryDependents(t, nil) }
func testSourceMemoryDependents(t *testing.T, repo Repository) {
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	repo = f.svc.Repo
	src := f.source()
	other := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "independent supporting origin"}))
	m := Record{ID: "source-child", Scope: scopeA, Status: "active", Revision: 1, Sources: refs(src)}
	if e := repo.Insert(testCtx, colMemories, m); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 102; i++ {
		r := Record{ID: fmt.Sprintf("dependent-%03d", i), Scope: scopeA, Revision: 1, Status: "expired", Sources: refs(other), Text: "derived personal evaluation", Target: &Ref{Collection: colMemories, ID: m.ID, Revision: int64(i + 1)}}
		if e := repo.Insert(testCtx, "evaluations", r); e != nil {
			t.Fatal(e)
		}
	}
	rel := Record{ID: "dependent-relation", Scope: scopeA, Revision: 1, Status: "active", Sources: refs(other), Text: "derived relation", Relations: []Ref{{Collection: colMemories, ID: m.ID, Revision: 1}}}
	if e := repo.Insert(testCtx, "memory_relations", rel); e != nil {
		t.Fatal(e)
	}
	foreignScopes := []Scope{scopeB, {WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}}
	for i, scope := range foreignScopes {
		foreign := rel
		foreign.ID = fmt.Sprintf("foreign-%d", i)
		foreign.Scope = scope
		if e := repo.Insert(testCtx, "memory_relations", foreign); e != nil {
			t.Fatal(e)
		}
	}

	if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	token := erasureWorker(f)
	job, e := repo.ClaimJob(testCtx, time.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	f.svc.Auth = strictSourceDependentAuth{Authorizer: f.auth, denyRoot: true}
	if e = f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(e, ErrDenied) {
		t.Fatal("source root policy bypass", e)
	}
	f.svc.Auth = strictSourceDependentAuth{Authorizer: f.auth}
	f.svc.Repo = relationAuditFailureRepo{Repository: repo, action: "erase:source_memory_dependents_batch"}
	if e = f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(e, ErrUnavailable) {
		t.Fatal("audit rollback not exercised", e)
	}
	if _, e = repo.Get(testCtx, colMemories, scopeA, m.ID); e != nil {
		t.Fatal("failed batch deleted memory", e)
	}
	if _, e = repo.Get(testCtx, "jobs", scopeA, deletionJobID(colMemories, scopeA, m.ID)); !errors.Is(e, ErrNotFound) {
		t.Fatal("failed batch left child job", e)
	}
	unchanged, e := repo.Get(testCtx, "jobs", scopeA, job.ID)
	if e != nil || unchanged.SourceMemoryErasure.QueuedMemoryJobs != 0 || unchanged.SourceMemoryErasure.DeletedMemories != 0 {
		t.Fatal("failed batch changed progress", e)
	}
	f.svc.Repo = repo
	if e = f.svc.eraseSourceMemories(testCtx, token, job); e != nil {
		t.Fatal(e)
	}
	registered, e := repo.Get(testCtx, "jobs", scopeA, deletionJobID(colMemories, scopeA, m.ID))
	if e != nil || registered.CreatedBy != job.CreatedBy || registered.CreatedByType != "human" {
		t.Fatal("child lost requester", e)
	}

	for i := 0; i < 10; i++ {
		e := f.svc.WorkOnce(testCtx, token)
		if errors.Is(e, ErrNotFound) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	for i := 0; i < 102; i++ {
		if _, e := repo.Get(testCtx, "evaluations", scopeA, fmt.Sprintf("dependent-%03d", i)); !errors.Is(e, ErrNotFound) {
			t.Fatal("source deletion left memory evaluation", i, e)
		}
	}
	if _, e := repo.Get(testCtx, "memory_relations", scopeA, rel.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal("source deletion left relation", e)
	}
	for i, scope := range foreignScopes {
		if _, e := repo.Get(testCtx, "memory_relations", scope, fmt.Sprintf("foreign-%d", i)); e != nil {
			t.Fatal("foreign relation lost", e)
		}
	}

	if _, e := repo.Get(testCtx, "sources", scopeA, other.ID); e != nil {
		t.Fatal("independent source lost", e)
	}
	root, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
	if e != nil || root.QueuedMemoryJobs != 1 || root.Coverage != sourceRecordsMemoryDependents || root.Status != JobStatusDone || root.FullErasureVerified {
		t.Fatal("root progress", root, e)
	}
	child, e := f.svc.deletionStatus(testCtx, f.human, colMemories, scopeA, m.ID)
	if e != nil || child.Status != JobStatusDone || child.DeletedRecords["evaluations"] != 102 || child.DeletedRecords["memory_relations"] != 1 || child.FullErasureVerified {
		t.Fatal("child progress", child, e)
	}
}

func TestSourceMemoryDependentProposal(t *testing.T) {
	for _, mode := range []string{"absent", "existing", "unlinked", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			other := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "independent origin"}))
			m := Record{ID: "proposal-child", Scope: scopeA, Revision: 1, Sources: refs(src), Proposal: &Ref{Collection: "memory_candidates", ID: "proposal-child", Revision: 1}}
			if mode == "unlinked" {
				m.Proposal = nil
			}
			if mode == "malformed" {
				m.Proposal.ID = "different"
			}
			if e := f.repo.Insert(testCtx, colMemories, m); e != nil {
				t.Fatal(e)
			}
			if mode == "existing" || mode == "unlinked" {
				if e := f.repo.Insert(testCtx, "memory_candidates", Record{ID: m.ID, Scope: scopeA, Revision: 1, Sources: refs(other)}); e != nil {
					t.Fatal(e)
				}
			}
			if e := f.repo.Insert(testCtx, "evaluations", Record{ID: "candidate-evaluation", Scope: scopeA, Revision: 1, Sources: refs(other), Target: &Ref{Collection: "memory_candidates", ID: m.ID, Revision: 1}}); e != nil {
				t.Fatal(e)
			}
			if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
				t.Fatal(e)
			}
			token := erasureWorker(f)
			if mode == "malformed" {
				if e := f.svc.WorkOnce(testCtx, token); !errors.Is(e, ErrUnavailable) {
					t.Fatal("malformed proposal accepted", e)
				}
				if _, e := f.repo.Get(testCtx, colMemories, scopeA, m.ID); e != nil {
					t.Fatal("malformed proposal lost memory", e)
				}
				return
			}
			for i := 0; i < 8; i++ {
				e := f.svc.WorkOnce(testCtx, token)
				if errors.Is(e, ErrNotFound) {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
			}
			job, e := f.repo.Get(testCtx, "jobs", scopeA, deletionJobID(colMemories, scopeA, m.ID))
			if e != nil || (job.Proposal != nil) != (mode == "absent") {
				t.Fatal("wrong proposal proof", job.Proposal, e)
			}
			_, e = f.repo.Get(testCtx, "evaluations", scopeA, "candidate-evaluation")
			if mode == "absent" {
				if !errors.Is(e, ErrNotFound) {
					t.Fatal("linked absent candidate eval remains", e)
				}
			} else {
				if e != nil {
					t.Fatal("unproven candidate eval deleted", e)
				}
				if _, e = f.repo.Get(testCtx, "memory_candidates", scopeA, m.ID); e != nil {
					t.Fatal("live candidate deleted", e)
				}
			}
		})
	}
}

type strictSourceDependentAuth struct {
	Authorizer
	denyRoot bool
}

func (a strictSourceDependentAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if strings.HasPrefix(in.Action, "erase:") {
		switch in.Action {
		case "erase:source_memory_dependents":
			if a.denyRoot {
				return Decision{}, nil
			}
		case "erase:memories", "erase:memory_records", "erase:evaluations", "erase:memory_relations":
		default:
			return Decision{}, nil
		}
	}
	return a.Authorizer.Authorize(ctx, token, in)
}
