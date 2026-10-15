package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

func handoffTransferFixture(t *testing.T) (*fixture, HandoffTransferInput) {
	t.Helper()
	f := newFixture(t)
	f.auth.actors[f.agent] = actor("agent-1", roleAgent, true, "read", "write", "transfer")
	src := f.source()
	pkg := workCheckpointPackage()
	pkg.SourceRefs = []Ref{{Collection: "sources", ID: src.ID, Revision: src.Revision}}
	work, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: "handoff-parent", EventID: "first", Package: pkg})
	if err != nil {
		t.Fatal(err)
	}
	return f, HandoffTransferInput{Scope: scopeA, Work: Ref{Collection: colWorkCheckpoints, ID: work.ID, Revision: work.Revision}, Destination: "external:openai", Content: structuredText(map[string]string{"task": "Review current work", "instructions": "Do not modify files"})}
}
func TestHandoffTransferRequiresExplicitEgress(t *testing.T) {
	for _, mode := range []string{"allowed", "no-external", "no-transfer", "stale-parent", "restricted-source", "deleted-source", "unsupported-destination"} {
		t.Run(mode, func(t *testing.T) {
			f, in := handoffTransferFixture(t)
			switch mode {
			case "no-external":
				a := f.auth.actors[f.agent]
				a.external = false
				f.auth.actors[f.agent] = a
			case "no-transfer":
				a := f.auth.actors[f.agent]
				delete(a.verbs, "transfer")
				f.auth.actors[f.agent] = a
			case "stale-parent":
				in.Work.Revision++
			case "restricted-source", "deleted-source":
				r := f.repo.rows("sources")[0]
				if mode == "restricted-source" {
					r.Sensitivity = "restricted"
					if e := f.repo.Replace(testCtx, "sources", r, r.Revision); e != nil {
						t.Fatal(e)
					}
				} else {
					if e := f.repo.Delete(testCtx, "sources", scopeA, r.ID, r.Revision); e != nil {
						t.Fatal(e)
					}
				}
			case "unsupported-destination":
				in.Destination = "external:other"
			}
			out, err := f.svc.AuthorizeHandoffTransfer(testCtx, f.agent, in)
			if mode == "allowed" {
				if err != nil || !out.Authorized || out.ContentHash != ContentHash(in.Content) {
					t.Fatal(out, err)
				}
			} else if err == nil || out.Authorized {
				t.Fatal("unauthorized transfer", out, err)
			}
			for _, row := range f.repo.rows("audit_events") {
				if row.Kind == "transfer:model_input" && strings.Contains(structuredText(row), "Review current work") {
					t.Fatal("stored prompt")
				}
			}
		})
	}
}
func TestHandoffTransferChecksCanonicalArchiveAndSkills(t *testing.T) {
	f, now, archive := modelTransferFixture(t)
	f.auth.actors[f.agent] = actor("agent-1", roleAgent, true, "read", "write", "transfer")
	pkg := workCheckpointPackage()
	saved, e := f.svc.SaveWorkCheckpoint(testCtx, f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: "archive-parent", EventID: "first", Package: pkg, ArchiveMessages: archive.Refs})
	if e != nil {
		t.Fatal(e)
	}
	body := "# Review\nCheck evidence first."
	imported, e := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "handoff-review", Path: "/synthetic/SKILL.md", Content: body, Analysis: detailedSkillAnalysis()})
	if e != nil {
		t.Fatal(e)
	}
	in := HandoffTransferInput{Scope: scopeA, Work: Ref{Collection: colWorkCheckpoints, ID: saved.ID, Revision: saved.Revision}, Destination: "external:anthropic", Content: structuredText(map[string]string{"task": "Review evidence", "instructions": body}), Skills: []HandoffTransferSkill{{SkillID: "handoff-review", VersionID: imported.Record.ID, ContentHash: ContentHash(body)}}}
	out, e := f.svc.AuthorizeHandoffTransfer(testCtx, f.agent, in)
	if e != nil || !out.Authorized {
		t.Fatal(out, e)
	}
	in.Skills[0].ContentHash = ContentHash("changed")
	if out, e = f.svc.AuthorizeHandoffTransfer(testCtx, f.agent, in); e == nil || out.Authorized {
		t.Fatal("changed skill accepted")
	}
	in.Skills[0].ContentHash = ContentHash(body)
	*now = now.Add(400 * 24 * time.Hour)
	if out, e = f.svc.AuthorizeHandoffTransfer(testCtx, f.agent, in); e == nil || out.Authorized {
		t.Fatal("expired archive accepted")
	}
}
func TestPostgresHandoffTransferSingleConnection(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	saved, e := f.svc.SaveWorkCheckpoint(testCtx, f.agent, WorkCheckpointSaveInput{Scope: scopeA, ID: "handoff-parent", EventID: "first", Package: workCheckpointPackage()})
	if e != nil {
		t.Fatal(e)
	}
	// PostgreSQL policy itself decides ExternalTransfer through external:transfer.
	if e = p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "single-agent", Role: roleAgent, Token: f.agent, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"*"}}); e != nil {
		t.Fatal(e)
	}
	app := restrictedPostgres(t, p)
	auth, e := NewPostgresAuthorizer(app)
	if e != nil {
		t.Fatal(e)
	}
	f.svc.Repo, f.svc.Auth = app, auth
	ctx, cancel := context.WithTimeout(ContextWithPostgresToken(testCtx, f.agent), 5*time.Second)
	defer cancel()
	in := HandoffTransferInput{Scope: scopeA, Work: Ref{Collection: colWorkCheckpoints, ID: saved.ID, Revision: saved.Revision}, Destination: "external:openai", Content: structuredText(map[string]string{"task": "Review work", "instructions": "Read only"})}
	out, e := f.svc.AuthorizeHandoffTransfer(ctx, f.agent, in)
	if e != nil || !out.Authorized {
		t.Fatal(out, e)
	}
	if e = p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "single-agent", Role: roleAgent, Token: f.agent, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"read:work_checkpoints", "transfer:model_input"}}); e != nil {
		t.Fatal(e)
	}
	out, e = f.svc.AuthorizeHandoffTransfer(ctx, f.agent, in)
	if e == nil || out.Authorized {
		t.Fatal("missing external:transfer grant accepted", out, e)
	}
}
