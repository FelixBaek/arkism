package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSourceSkillErasure(t *testing.T) { testSourceSkillErasure(t, nil) }

func testSourceSkillErasure(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	repo = f.svc.Repo
	src := f.source()
	other := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "independent skill origin"}))
	insert := func(col string, r Record) {
		t.Helper()
		r.Revision = 1
		r.Status = "active"
		r.Text = "retained skill body"
		if e := repo.Insert(testCtx, col, r); e != nil {
			t.Fatal(e)
		}
	}
	insert(colSkills, Record{ID: "family", Scope: scopeA, Sources: refs(src), ActiveVersion: "family-103"})
	insert(colSkills, Record{ID: "empty", Scope: scopeA, Sources: refs(src)})
	insert(colSkills, Record{ID: "retained", Scope: scopeA, Sources: refs(other), ActiveVersion: "direct"})
	for i := 0; i < 104; i++ {
		insert(colSkillVersions, Record{ID: fmt.Sprintf("family-%03d", i), SkillID: "family", Scope: scopeA, Sources: refs(other), Embedding: &Embedding{Vector: []float64{1}}})
	}
	insert(colSkillVersions, Record{ID: "direct", SkillID: "retained", Scope: scopeA, Sources: refs(src)})
	insert(colSkillVersions, Record{ID: "orphan", SkillID: "missing", Scope: scopeA, Sources: refs(src)})
	insert(colSkillVersions, Record{ID: "sibling", SkillID: "retained", Scope: scopeA, Sources: refs(other)})
	scopes := []Scope{{WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}}
	for i, scope := range scopes {
		insert(colSkillVersions, Record{ID: fmt.Sprintf("foreign-%d", i), SkillID: "family", Scope: scope, Sources: refs(src)})
	}
	if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	job, e := repo.Get(testCtx, "jobs", scopeA, sourceMemoryErasureID(scopeA, src.ID))
	if e != nil {
		t.Fatal(e)
	}
	job.Kind = sourceSkillErasureKind
	job.Revision++
	if e = repo.Replace(testCtx, "jobs", job, job.Revision-1); e != nil {
		t.Fatal(e)
	}
	token := erasureWorker(f)
	f.svc.Auth = strictSkillErasureAuth{f.svc.Auth}
	var prev int64
	done := false
	for i := 0; i < 8; i++ {
		if e := f.svc.WorkOnce(testCtx, token); e != nil {
			t.Fatal(e)
		}
		status, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
		if e != nil {
			t.Fatal(e)
		}
		total := status.DeletedRecords[colSkills] + status.DeletedRecords[colSkillVersions]
		if total-prev > 100 || (i == 0 && total != 100) || status.FullErasureVerified || status.Coverage != "source_records_and_skills" {
			t.Fatalf("bad progress: %+v", status)
		}
		prev = total
		if status.Status == JobStatusDone {
			if status.DeletedRecords[colSkills] != 2 || status.DeletedRecords[colSkillVersions] != 106 {
				t.Fatalf("counts: %+v", status)
			}
			done = true
			break
		}
	}
	if !done {
		t.Fatal("erasure never completed")
	}
	for _, id := range []string{"family", "empty"} {
		if _, e := repo.Get(testCtx, colSkills, scopeA, id); !errors.Is(e, ErrNotFound) {
			t.Fatal("header remains", id, e)
		}
	}
	for _, id := range []string{"family-000", "family-103", "direct", "orphan"} {
		if _, e := repo.Get(testCtx, colSkillVersions, scopeA, id); !errors.Is(e, ErrNotFound) {
			t.Fatal("body remains", id, e)
		}
	}
	h, e := repo.Get(testCtx, colSkills, scopeA, "retained")
	if e != nil || h.ActiveVersion != "" || h.Revision != 2 {
		t.Fatalf("active pointer: %+v %v", h, e)
	}
	if _, e := repo.Get(testCtx, colSkillVersions, scopeA, "sibling"); e != nil {
		t.Fatal("sibling lost", e)
	}
	for i, scope := range scopes {
		if _, e := repo.Get(testCtx, colSkillVersions, scope, fmt.Sprintf("foreign-%d", i)); e != nil {
			t.Fatal("foreign version lost", e)
		}
	}
	if _, e := repo.Get(testCtx, "sources", scopeA, other.ID); e != nil {
		t.Fatal("other source lost", e)
	}
	if len(f.emb.calls) != 0 {
		t.Fatal("erasure embedded content")
	}
}

func TestSourceSkillErasureGuards(t *testing.T) {
	for _, mode := range []string{"parent_denied", "parent_principal", "audit", "parent_changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			token := erasureWorker(f)
			parent := Record{ID: "parent", Scope: scopeA, Revision: 1, Status: "active", ActiveVersion: "version", Sources: refs(src)}
			if e := f.repo.Insert(testCtx, colSkills, parent); e != nil {
				t.Fatal(e)
			}
			if e := f.repo.Insert(testCtx, colSkillVersions, Record{ID: "version", SkillID: parent.ID, Scope: scopeA, Revision: 1, Sources: refs(src), Text: "body"}); e != nil {
				t.Fatal(e)
			}
			if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "parent_denied":
				f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: parent.ID}
			case "parent_principal":
				f.svc.Auth = usagePrincipalAuth{Authorizer: f.auth, action: "erase:skills"}
			case "audit":
				f.repo.failAudit = true
			case "parent_changed":
				f.svc.Repo = evaluationChangeRepo{Repository: f.repo, change: func() {
					parent.Summary = "concurrent edit"
					updated := parent
					updated.Revision++
					if e := f.repo.Replace(testCtx, colSkills, updated, parent.Revision); e != nil {
						t.Fatal(e)
					}
				}}
			}
			if e := f.svc.WorkOnce(testCtx, token); e == nil {
				t.Fatal("guard bypassed")
			}
			h, e := f.repo.Get(testCtx, colSkills, scopeA, parent.ID)
			if e != nil || h.ActiveVersion != "version" {
				t.Fatal("failure cleared pointer", e)
			}
			if _, e = f.repo.Get(testCtx, colSkillVersions, scopeA, "version"); e != nil {
				t.Fatal("failure erased version", e)
			}
			job, e := f.repo.Get(testCtx, "jobs", scopeA, sourceMemoryErasureID(scopeA, src.ID))
			if e != nil || len(job.SourceMemoryErasure.DeletedRecords) != 0 {
				t.Fatal("failed counts", job, e)
			}
		})
	}
}

func TestErasedSkillAnalysisCannotResurrect(t *testing.T) {
	testErasedSkillAnalysisCannotResurrect(t, nil)
}
func testErasedSkillAnalysisCannotResurrect(t *testing.T, repo Repository) {
	t.Helper()
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	repo = f.svc.Repo
	v := analyzedSkill(t, f)
	if e := f.svc.WorkOnce(testCtx, f.work); e != nil {
		t.Fatal(e)
	}
	src, e := repo.Get(testCtx, "sources", scopeA, v.Sources[0].SourceID)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	token := erasureWorker(f)
	done := false
	for i := 0; i < 5; i++ {
		if e = f.svc.WorkOnce(testCtx, token); e != nil {
			t.Fatal(e)
		}
		status, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
		if e != nil {
			t.Fatal(e)
		}
		if status.Status == JobStatusDone {
			done = true
			break
		}
	}
	if !done {
		t.Fatal("erasure did not complete")
	}
	_, e = f.svc.AnalyzeSkill(testCtx, f.human, AnalyzeSkillInput{Scope: scopeA, SkillID: "index-helper", Content: "# index helper\nVerify indexes.", Path: "/skills/index-helper/SKILL.md"})
	if !errors.Is(e, ErrConflict) {
		t.Fatal("deleted analysis recreated", e)
	}
	if f.svc.Analyzer.(*countingAnalyzer).calls != 1 {
		t.Fatal("paid analysis repeated for erased source")
	}
	for col, id := range map[string]string{"sources": src.ID, colSkills: v.SkillID, colSkillVersions: v.ID} {
		if _, e = repo.Get(testCtx, col, scopeA, id); !errors.Is(e, ErrNotFound) {
			t.Fatal("erased body resurrected", col, e)
		}
	}
}

// The normal fake accepts verbs only; pin the actual deployed policy actions.
type strictSkillErasureAuth struct{ Authorizer }

func (a strictSkillErasureAuth) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if strings.HasPrefix(in.Action, "erase:") && in.Action != "erase:source_skills" && in.Action != "erase:skills" && in.Action != "erase:skill_versions" {
		return Decision{}, nil
	}
	return a.Authorizer.Authorize(ctx, token, in)
}
