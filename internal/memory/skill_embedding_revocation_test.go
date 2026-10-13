package memory

import (
	"testing"
	"time"
)

func TestSkillEmbeddingRevocation(t *testing.T) {
	for _, mode := range []string{"source_deleted", "parent_missing", "parent_inactive", "parent_expired", "pointer_changed", "relation_deleted"} {
		for _, during := range []bool{false, true} {
			phase := "before"
			if during {
				phase = "during"
			}
			t.Run(mode+"/"+phase, func(t *testing.T) {
				f := newFixture(t)
				src := f.source()
				if e := f.repo.Insert(testCtx, colSkills, Record{ID: "index-helper", Scope: scopeA, Status: "active", Revision: 1, Sources: refs(src)}); e != nil {
					t.Fatal(e)
				}
				v := analyzedSkill(t, f)
				revoke := func() {
					p, e := f.repo.Get(testCtx, colSkills, scopeA, v.SkillID)
					if e != nil {
						t.Fatal(e)
					}
					switch mode {
					case "source_deleted", "relation_deleted":
						if e = f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
							t.Fatal(e)
						}
					case "parent_missing":
						if e = f.repo.Delete(testCtx, colSkills, scopeA, p.ID, p.Revision); e != nil {
							t.Fatal(e)
						}
					default:
						switch mode {
						case "parent_inactive":
							p.Status = "rejected"
						case "parent_expired":
							past := time.Now().Add(-time.Hour)
							p.ExpiresAt = &past
						case "pointer_changed":
							p.ActiveVersion = "another-version"
						}
						if e = f.repo.Replace(testCtx, colSkills, p, p.Revision); e != nil {
							t.Fatal(e)
						}
					}
				}
				if mode == "relation_deleted" {
					// Keep parent and evaluation sources valid; only the version relation fails.
					p, _ := f.repo.Get(testCtx, colSkills, scopeA, v.SkillID)
					p.Sources = v.Sources
					f.repo.store[colSkills][recKey(scopeA, p.ID)] = p
					v.Relations = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}
					// Fixture seeding preserves the queued version revision.
					f.repo.store[colSkillVersions][recKey(scopeA, v.ID)] = v
				}
				if during {
					f.svc.Embedder = evidenceRevokingEmbedder{Embedder: f.emb, revoke: revoke}
				} else {
					revoke()
				}
				if e := f.svc.WorkOnce(testCtx, f.work); e == nil {
					t.Fatal("revoked skill embedded")
				}
				want := 0
				if during {
					want = 1
				}
				current, e := f.repo.Get(testCtx, colSkillVersions, scopeA, v.ID)
				if e != nil || current.Embedding != nil || len(f.emb.calls) != want {
					t.Fatal("revoked skill transferred or stored", e, len(f.emb.calls))
				}
			})
		}
	}
}
