package memory

import (
	"context"
	"errors"
	"testing"
)

func TestDeletedEvaluationEvidenceBlocksSkill(t *testing.T) {
	for _, mode := range []string{"before_search", "during_routing"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			support := f.activeMemory("supporting decision")
			v := analyzedSkill(t, f, Ref{Collection: "memories", ID: support.ID, Revision: support.Revision})
			if got := f.search(f.human, SearchInput{Scope: scopeA, Query: "index", Purpose: "skills"}); len(got.Items) != 1 {
				t.Fatal("valid evidence excluded", got)
			}
			revoke := func() {
				if e := f.svc.Delete(testCtx, f.human, "memories", support.ID, ChangeInput{Scope: scopeA, ExpectedRevision: support.Revision}); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "before_search" {
				revoke()
			} else {
				f.svc.Router = judgeFunc(func(context.Context, string, []SkillCandidate) (RouteResult, error) {
					revoke()
					return RouteResult{}, ErrUnavailable
				})
				out, e := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "index"})
				if !errors.Is(e, ErrConflict) || out.SelectedID != "" {
					t.Fatal("revoked evidence routed", out, e)
				}
			}
			if got := f.search(f.human, SearchInput{Scope: scopeA, Query: "index", Purpose: "skills"}); len(got.Items) != 0 {
				t.Fatal("deleted supporting memory still validates skill", got)
			}
			parent := f.get(f.human, colSkills, scopeA, v.SkillID)
			if _, e := f.svc.ApplySkill(testCtx, f.human, v.SkillID, ChangeInput{Scope: scopeA, ExpectedRevision: parent.Revision, VersionID: v.ID}, false); !errors.Is(e, ErrConflict) {
				t.Fatal("applied revoked evidence", e)
			}
			unchanged := f.get(f.human, colSkills, scopeA, v.SkillID)
			if unchanged.Revision != parent.Revision || unchanged.ActiveVersion != parent.ActiveVersion {
				t.Fatal("failed apply changed parent")
			}
			for _, job := range f.repo.rows("jobs") {
				if job.Kind == "embedding" && job.Target.Collection == colMemories {
					if e := f.repo.Delete(testCtx, "jobs", scopeA, job.ID, job.Revision); e != nil {
						t.Fatal(e)
					}
				}
			}
			calls := len(f.emb.calls)
			if e := f.svc.WorkOnce(testCtx, f.work); !errors.Is(e, ErrConflict) {
				t.Fatal("invalid skill evidence embedded", e)
			}
			if len(f.emb.calls) != calls {
				t.Fatal("invalid skill sent to provider")
			}

		})
	}
}

func TestDeletedEvaluationEvidenceBlocksReflectionAndSummary(t *testing.T) {
	f := newFixture(t)
	support := f.activeMemory("supporting decision")
	r := verifiedReflection(t, f, Ref{Collection: "memories", ID: support.ID, Revision: support.Revision})
	if got := f.search(f.human, SearchInput{Scope: scopeA, Query: "index", Purpose: "self_improvement"}); len(got.Items) != 1 {
		t.Fatal("valid reflection excluded", got)
	}
	target := r.Relations[0]
	before, e := f.svc.SummarizeEvaluations(testCtx, f.human, EvaluationSummaryInput{Scope: scopeA, Target: target})
	if e != nil || before.Effective.Count != 1 {
		t.Fatal("valid summary", before, e)
	}
	if e = f.svc.Delete(testCtx, f.human, "memories", support.ID, ChangeInput{Scope: scopeA, ExpectedRevision: support.Revision}); e != nil {
		t.Fatal(e)
	}
	if e = checkReflection(testCtx, f.repo, r); e == nil {
		t.Fatal("deleted evidence verified reflection")
	}
	if got := f.search(f.human, SearchInput{Scope: scopeA, Query: "index", Purpose: "self_improvement"}); len(got.Items) != 0 {
		t.Fatal("reflection with revoked evidence returned", got)
	}
	out, e := f.svc.SummarizeEvaluations(testCtx, f.human, EvaluationSummaryInput{Scope: scopeA, Target: target})
	if !errors.Is(e, ErrConflict) || out.Effective.Count != 0 {
		t.Fatal("revoked evidence summarized", out, e)
	}
	// Drop the now-obsolete memory embedding job and attempt the reflection job.
	for _, job := range f.repo.rows("jobs") {
		if job.Kind == "embedding" && job.Target.Collection == colMemories {
			if e = f.repo.Delete(testCtx, "jobs", scopeA, job.ID, job.Revision); e != nil {
				t.Fatal(e)
			}
		}
	}
	calls := len(f.emb.calls)
	if e = f.svc.WorkOnce(testCtx, f.work); !errors.Is(e, ErrDenied) {
		t.Fatal("revoked reflection embedded", e)
	}
	if len(f.emb.calls) != calls {
		t.Fatal("invalid reflection sent to provider")
	}
}

type evidenceRevokingEmbedder struct {
	Embedder
	revoke func()
}

func (e evidenceRevokingEmbedder) Embed(ctx context.Context, texts []string, purpose string) ([]Embedding, error) {
	out, err := e.Embedder.Embed(ctx, texts, purpose)
	e.revoke()
	return out, err
}
func TestEvaluationEvidenceRevokedDuringEmbedding(t *testing.T) {
	for _, col := range []string{colSkillVersions, colReflections} {
		t.Run(col, func(t *testing.T) {
			f := newFixture(t)
			support := f.activeMemory("supporting decision")
			ref := Ref{Collection: colMemories, ID: support.ID, Revision: support.Revision}
			var r Record
			want := ErrConflict
			if col == colSkillVersions {
				r = analyzedSkill(t, f, ref)
			} else {
				r = verifiedReflection(t, f, ref)
				want = ErrDenied
			}
			for _, job := range f.repo.rows("jobs") {
				if job.Kind == "embedding" && job.Target.Collection == colMemories {
					if e := f.repo.Delete(testCtx, "jobs", scopeA, job.ID, job.Revision); e != nil {
						t.Fatal(e)
					}
				}
			}
			f.svc.Embedder = evidenceRevokingEmbedder{Embedder: f.emb, revoke: func() {
				if e := f.svc.Delete(testCtx, f.human, colMemories, support.ID, ChangeInput{Scope: scopeA, ExpectedRevision: support.Revision}); e != nil {
					t.Fatal(e)
				}
			}}
			if e := f.svc.WorkOnce(testCtx, f.work); !errors.Is(e, want) {
				t.Fatal("revocation during embed accepted", e)
			}
			current, e := f.repo.Get(testCtx, col, scopeA, r.ID)
			if e != nil || current.Embedding != nil || len(f.emb.calls) != 1 {
				t.Fatal("revoked result stored", current, e)
			}
		})
	}
}

func TestSkillEvidenceMutationRollsBack(t *testing.T) {
	for _, mode := range []string{"deleted_before_approval", "parent_changed_by_apply"} {
		t.Run(mode, func(t *testing.T) { testSkillEvidenceMutationRollsBack(t, nil, mode) })
	}
}
func testSkillEvidenceMutationRollsBack(t *testing.T, repo Repository, mode string) {
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	f.svc.Analyzer = &countingAnalyzer{}
	out, e := f.svc.AnalyzeSkill(testCtx, f.human, AnalyzeSkillInput{Scope: scopeA, SkillID: "index-helper", Content: "# index helper\nVerify indexes.", Path: "/skills/index-helper/SKILL.md"})
	if e != nil {
		t.Fatal(e)
	}
	v := out.Record
	parent := f.get(f.human, colSkills, scopeA, v.SkillID)
	evidence := Ref{Collection: colSkills, ID: parent.ID, Revision: parent.Revision}
	if mode == "deleted_before_approval" {
		support := f.activeMemory("support")
		evidence = Ref{Collection: colMemories, ID: support.ID, Revision: support.Revision}
	}
	ev := f.create(f.human2, "evaluations", ratingInput(CreateInput{Scope: scopeA, Text: "independent verification", Sources: v.Sources, Relations: []Ref{evidence}, Target: &Ref{Collection: colSkillVersions, ID: v.ID, Revision: v.Revision}, Passed: true}))
	if mode == "deleted_before_approval" {
		if e = f.svc.Delete(testCtx, f.human, colMemories, evidence.ID, ChangeInput{Scope: scopeA, ExpectedRevision: evidence.Revision}); e != nil {
			t.Fatal(e)
		}
	}
	approved, e := f.svc.Transition(testCtx, f.human, colSkillVersions, v.ID, ChangeInput{Scope: scopeA, ExpectedRevision: v.Revision, Status: "approved", ValidationID: ev.ID})
	if mode == "deleted_before_approval" {
		if !errors.Is(e, ErrConflict) {
			t.Fatal("revoked approval", e)
		}
	} else {
		if e != nil {
			t.Fatal(e)
		}
		v = approved
		if _, e = f.svc.ApplySkill(testCtx, f.human, parent.ID, ChangeInput{Scope: scopeA, ExpectedRevision: parent.Revision, VersionID: v.ID}, false); !errors.Is(e, ErrConflict) {
			t.Fatal("self-invalidating apply", e)
		}
	}
	after := f.get(f.human, colSkillVersions, scopeA, v.ID)
	header := f.get(f.human, colSkills, scopeA, parent.ID)
	if after.Status != v.Status || after.Revision != v.Revision || header.Revision != parent.Revision || header.ActiveVersion != parent.ActiveVersion {
		t.Fatal("failed mutation changed state")
	}
}
