package memory

import (
	"context"
	"testing"
	"time"
)

func TestSearchRejectsSelectionChangedDuringPolicy(t *testing.T) {
	for _, target := range []string{"memory", "source", "body", "effective_at", "sensitivity", "skill_parent", "skill_evidence", "reflection_evidence", "source_sensitivity"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t)
			support := f.activeMemory("index support needle")
			selected := support
			query, purpose := "needle", ""
			if target == "skill_parent" || target == "skill_evidence" {
				selected = analyzedSkill(t, f, Ref{Collection: colMemories, ID: support.ID, Revision: support.Revision})
				query, purpose = "index", "skills"
			}
			if target == "reflection_evidence" {
				selected = verifiedReflection(t, f, Ref{Collection: colMemories, ID: support.ID, Revision: support.Revision})
				query, purpose = "index", "self_improvement"
			}
			changed := false
			f.svc.Auth = sourceListChangeAuth{Authorizer: f.auth, change: func(in Authorization, d *Decision) error {
				if in.Action != "context:read" || in.ResourceID != selected.ID || changed {
					return nil
				}
				changed = true
				switch target {
				case "memory", "skill_evidence", "reflection_evidence":
					return f.repo.Delete(testCtx, colMemories, scopeA, support.ID, support.Revision)
				case "source_sensitivity":
					src := f.repo.rows("sources")[0]
					src.Sensitivity = "restricted"
					return f.repo.Replace(testCtx, "sources", src, src.Revision)
				case "source":
					src := f.repo.rows("sources")[0]
					return f.repo.Delete(testCtx, "sources", scopeA, src.ID, src.Revision)
				case "skill_parent":
					p, e := f.repo.Get(testCtx, colSkills, scopeA, selected.SkillID)
					if e != nil {
						return e
					}
					p.ActiveVersion = "different"
					return f.repo.Replace(testCtx, colSkills, p, p.Revision)
				case "effective_at":
					at := time.Now().Add(time.Hour)
					support.EffectiveAt = &at
					return f.repo.Replace(testCtx, colMemories, support, support.Revision)
				case "body":
					support.Text = "changed body"
					return f.repo.Replace(testCtx, colMemories, support, support.Revision)
				case "sensitivity":
					support.Sensitivity = "restricted"
					return f.repo.Replace(testCtx, colMemories, support, support.Revision)
				}
				return nil
			}}
			input := SearchInput{Scope: scopeA, Query: query, Purpose: purpose}
			if target == "source_sensitivity" {
				input.Destination = "external:test"
			}
			out, e := f.svc.Search(testCtx, f.human, input)
			if !changed {
				t.Fatal("did not reach selected-item policy")
			}
			if e == nil || len(out.Items) != 0 || out.ContextID != "" || out.ContextData != "" {
				t.Fatal("changed selection returned", e, out)
			}
			for _, job := range f.repo.rows("jobs") {
				if job.Kind == "context_usage" {
					t.Fatal("failed search persisted usage")
				}
			}
		})
	}
}

func (x fakeTx) FenceSearchRead(ctx context.Context, scope Scope, col, id string, revision int64) error {
	if !knownCollection(col) || col == "jobs" || col == "audit_events" || !validRunLabel(id, 256) || revision < 1 {
		return ErrInvalid
	}
	r, e := x.Get(ctx, col, scope, id)
	if e != nil || r.Revision != revision || !live(r) {
		return ErrConflict
	}
	r.MemoryReferenceFence++
	x.store[col][recKey(scope, id)] = r
	return nil
}
func (f *fakeRepo) FenceSearchRead(ctx context.Context, scope Scope, col, id string, revision int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.view().FenceSearchRead(ctx, scope, col, id, revision)
}
