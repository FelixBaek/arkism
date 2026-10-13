package memory

import (
	"context"
	"errors"
	"testing"
)

func TestSkillAvailabilityDeprecatesAndLimitsRuntimes(t *testing.T) {
	checkSkillAvailability(t, newFixture(t), func() {}, func(string) context.Context { return testCtx })
}

// The same flow on Postgres under the app role's forced RLS.
func TestPostgresSkillAvailability(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	for token, principal := range map[string]struct{ id, role string }{f.human: {"human-1", "human"}, f.agent: {"agent-1", "agent"}} {
		if err := p.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: principal.id, Role: principal.role, Token: token, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"*"}}); err != nil {
			t.Fatal(err)
		}
	}
	app := restrictedPostgres(t, p)
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	// Import is setup (its own RLS path is covered elsewhere); every
	// availability, route and resolve call runs as the restricted app role.
	f.svc.Repo, f.svc.Auth = p, auth
	// Like the HTTP middleware, bind each caller's token for RLS.
	checkSkillAvailability(t, f, func() { f.svc.Repo = app }, func(token string) context.Context { return ContextWithPostgresToken(testCtx, token) })
}

func checkSkillAvailability(t *testing.T, f *fixture, afterImport func(), ctx func(token string) context.Context) {
	t.Helper()
	imported, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "design-review", Path: "/skills/design-review/SKILL.md", Content: "---\nname: design-review\n---\nReview.", Analysis: detailedSkillAnalysis()})
	if err != nil {
		t.Fatal("import", err)
	}
	afterImport()
	v, parent := imported.Record, imported.Skill
	set := func(token, status string, compat map[string]string) (Record, error) {
		out, err := f.svc.SetSkillAvailability(ctx(token), token, v.SkillID, SkillAvailabilityInput{Scope: scopeA, ExpectedRevision: parent.Revision, Status: status, Compatibility: compat})
		if err == nil {
			parent = out
		}
		return out, err
	}
	route := func(runtime string) string {
		t.Helper()
		out, err := f.svc.RouteSkills(ctx(f.human), f.human, SearchInput{Scope: scopeA, Query: "design review", Runtime: runtime})
		if err != nil {
			t.Fatal("route", runtime, err)
		}
		return out.SelectedID
	}
	resolve := func(ref string) ResolvedSkill {
		t.Helper()
		out, err := f.svc.ResolveSkills(ctx(f.human), f.human, SkillResolveInput{Scope: scopeA, Refs: []string{ref}})
		if err != nil {
			t.Fatal("resolve", ref, err)
		}
		return out.Items[0]
	}

	if _, err := set(f.agent, statusActive, nil); !errors.Is(err, ErrDenied) {
		t.Fatal("agent changed availability", err)
	}
	if _, err := set(f.human, "rejected", nil); !errors.Is(err, ErrInvalid) {
		t.Fatal("status outside active/deprecated", err)
	}

	if _, err := set(f.human, statusActive, map[string]string{"claude_code": "unsupported"}); err != nil {
		t.Fatal(err)
	}
	if got := route("claude_code"); got != "" {
		t.Fatal("unsupported runtime was routed", got)
	}
	if got := route("codex"); got != v.ID {
		t.Fatal("other runtime lost the skill", got)
	}
	if got := resolve(v.SkillID + "@latest-approved").Compatibility["claude_code"]; got != "unsupported" {
		t.Fatal("resolve ignored the skill-level label", got)
	}
	listed, err := f.svc.ListSkills(ctx(f.human), f.human, scopeA, 50, "")
	if err != nil || len(listed.Items) != 1 || listed.Items[0].SkillCompatibility["claude_code"] != "unsupported" {
		t.Fatal("list ignored the skill-level label", listed.Items, err)
	}

	if _, err := set(f.human, skillStatusDeprecated, nil); err != nil {
		t.Fatal(err)
	}
	if got := route(""); got != "" {
		t.Fatal("deprecated skill was routed", got)
	}
	if got := resolve(v.SkillID + "@latest-approved"); got.Error != "skill_deprecated" {
		t.Fatal("latest-approved of a deprecated skill", got.Error)
	}
	if got := resolve(v.SkillID + "@1"); got.Error != "" || !got.Deprecated {
		t.Fatal("pinned version must still replay", got.Error, got.Deprecated)
	}

	stale := parent.Revision - 1
	if _, err := f.svc.SetSkillAvailability(ctx(f.human), f.human, v.SkillID, SkillAvailabilityInput{Scope: scopeA, ExpectedRevision: stale, Status: statusActive}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale revision", err)
	}
	if _, err := set(f.human, statusActive, nil); err != nil {
		t.Fatal(err)
	}
	if got := route("claude_code"); got != v.ID {
		t.Fatal("restored skill was not routed", got)
	}
}
