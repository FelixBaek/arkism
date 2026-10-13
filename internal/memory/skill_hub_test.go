package memory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestSkillVersionsResolveAndUsage(t *testing.T) {
	f := newFixture(t)
	origin := &SkillOrigin{SourceType: "import", Repository: "example/agent-skills", Commit: "170439e", OriginalFormat: "skill_md", OriginalRuntime: "codex"}
	in := ImportSkillInput{Scope: scopeA, SkillID: "code-review", Path: "/skills/code-review/SKILL.md", Content: "---\nname: code-review\n---\nReview v1.", Analysis: detailedSkillAnalysis(), Origin: origin, Compatibility: map[string]string{"codex": "supported", "demo-app": "requires_adapter"}}
	v1, err := f.svc.ImportSkill(testCtx, f.human, in)
	if err != nil || v1.Version != 1 || v1.Record.Status != statusActive {
		t.Fatal("v1", err, v1.Version)
	}
	in.Content = "---\nname: code-review\n---\nReview v2."
	v2, err := f.svc.ImportSkill(testCtx, f.human, in)
	if err != nil || v2.Version != 2 || v2.Record.Status != "candidate" || v2.Record.BaseVersion != v1.Record.ID || v2.Skill.ActiveVersion != v1.Record.ID {
		t.Fatal("changed content must become candidate @2", err, v2.Version, v2.Record.Status)
	}
	if again, err := f.svc.ImportSkill(testCtx, f.human, in); err != nil || !again.Reused || again.Version != 2 {
		t.Fatal("candidate re-import must be idempotent", err)
	}
	resolve := func(refs ...string) []ResolvedSkill {
		t.Helper()
		out, err := f.svc.ResolveSkills(testCtx, f.agent, SkillResolveInput{Scope: scopeA, Refs: refs})
		if err != nil {
			t.Fatal(err)
		}
		return out.Items
	}
	got := resolve("code-review@latest-approved", "code-review@2", "code-review@9", "missing@1", "bad-ref")
	if got[0].Version != 1 || got[0].Instructions != "---\nname: code-review\n---\nReview v1." || got[0].Provenance.Origin.Commit != "170439e" || got[0].Requirements.Authority != "requirements_only" || got[0].Compatibility["demo-app"] != "requires_adapter" {
		t.Fatalf("latest before approval: %+v", got[0])
	}
	if got[1].ValidationStatus != "candidate" || got[1].LatestApproved || got[2].Error != "version_not_found" || got[3].Error != "skill_not_found" || got[4].Error != "invalid_ref" {
		t.Fatalf("pins and typed failures: %+v", got[1:])
	}

	src := f.source()
	evaluation := f.create(f.human2, "evaluations", ratingInput(CreateInput{Scope: scopeA, Text: "independent check passed", Passed: true, Sources: refs(src),
		Target: &Ref{Collection: "skill_versions", ID: v2.Record.ID, Revision: v2.Record.Revision}}))
	f.transition(f.human, "skill_versions", v2.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: v2.Record.Revision, Status: "approved", ValidationID: evaluation.ID})
	f.apply(f.human, "code-review", ChangeInput{Scope: scopeA, ExpectedRevision: v2.Skill.Revision, VersionID: v2.Record.ID}, false)
	got = resolve("code-review@latest-approved", "code-review@1")
	if got[0].Version != 2 || !got[0].LatestApproved || got[1].ValidationStatus != "superseded" || got[1].Instructions != "---\nname: code-review\n---\nReview v1." {
		t.Fatalf("after apply: %+v", got)
	}

	usage := func(u SkillUsage) error {
		raw, _ := json.Marshal(u)
		return f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
			return checkSkillUsageEvents(ctx, tx, scopeA, []TraceEventInput{{Kind: skillUsageEventKind, Payload: raw}}, Scope{})
		})
	}
	now := time.Now().UTC()
	ok := SkillUsage{SkillID: "code-review", Version: 1, VersionID: v1.Record.ID, ContentHash: got[1].ContentHash, Runtime: "codex", Status: "success", StartedAt: now, CompletedAt: now}
	if err = usage(ok); err != nil {
		t.Fatal("valid usage rejected", err)
	}
	bad := ok
	bad.ContentHash = got[0].ContentHash
	if err = usage(bad); !errors.Is(err, ErrConflict) {
		t.Fatal("usage with another version's hash accepted", err)
	}
	bad = ok
	bad.Status = "maybe"
	if err = usage(bad); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown usage status accepted", err)
	}
	if !validSkillOrigin(&SkillOrigin{SourceType: "derived", DerivedFrom: "code-review@1"}) || validSkillOrigin(&SkillOrigin{SourceType: "derived"}) || validSkillCompatibility(map[string]string{"codex": "yes"}) {
		t.Fatal("origin/compatibility validation")
	}
}

// A runtime can rewrite its bundled skill back to older content. Rolling back to
// an imported baseline (which never had a validation) needs a fresh independent
// evaluation of that exact version; without one it is refused.
func TestRollbackToImportedBaselineNeedsFreshEvaluation(t *testing.T) {
	f := newFixture(t)
	in := ImportSkillInput{Scope: scopeA, SkillID: "docs", Path: "/skills/docs/SKILL.md", Content: "---\nname: docs\n---\nv1.", Analysis: detailedSkillAnalysis()}
	v1, err := f.svc.ImportSkill(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Content = "---\nname: docs\n---\nv2."
	v2, err := f.svc.ImportSkill(testCtx, f.human, in)
	if err != nil {
		t.Fatal(err)
	}
	src := f.source()
	ev2 := f.create(f.human2, "evaluations", ratingInput(CreateInput{Scope: scopeA, Text: "ok", Passed: true, Sources: refs(src),
		Target: &Ref{Collection: "skill_versions", ID: v2.Record.ID, Revision: v2.Record.Revision}}))
	f.transition(f.human, "skill_versions", v2.Record.ID, ChangeInput{Scope: scopeA, ExpectedRevision: v2.Record.Revision, Status: "approved", ValidationID: ev2.ID})
	parent := f.apply(f.human, "docs", ChangeInput{Scope: scopeA, ExpectedRevision: v2.Skill.Revision, VersionID: v2.Record.ID}, false)

	old, _ := f.repo.Get(testCtx, colSkillVersions, scopeA, v1.Record.ID)
	if _, err = f.svc.ApplySkill(testCtx, f.human, "docs", ChangeInput{Scope: scopeA, ExpectedRevision: parent.Revision, VersionID: old.ID}, true); err == nil {
		t.Fatal("rollback to an unvalidated baseline accepted")
	}
	ev1 := f.create(f.human2, "evaluations", ratingInput(CreateInput{Scope: scopeA, Text: "ok", Passed: true, Sources: refs(src),
		Target: &Ref{Collection: "skill_versions", ID: old.ID, Revision: old.Revision}}))
	got := f.apply(f.human, "docs", ChangeInput{Scope: scopeA, ExpectedRevision: parent.Revision, VersionID: old.ID, ValidationID: ev1.ID}, true)
	if got.ActiveVersion != v1.Record.ID {
		t.Fatal("rollback did not activate v1", got.ActiveVersion)
	}
}
