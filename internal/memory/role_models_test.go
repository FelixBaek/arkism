package memory

import (
	"context"
	"errors"
	"slices"
	"testing"
)

// 설정 › 역할별 모델: only valid picks are stored, a report keeps them, and the read adds what runs by default.
func TestRoleModelSetReadAndClear(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.SetRoleModel(testCtx, f.agent, ProviderRoleModel{Scope: scopeA, Role: "extract", Model: "haiku 4.5", Provider: "anthropic"}); !errors.Is(err, ErrDenied) {
		t.Fatal("an agent picked a role model", err)
	}
	for _, bad := range []ProviderRoleModel{
		{Scope: scopeA, Role: "nope", Model: "fixed"},
		{Scope: scopeA, Role: "route_judge", Model: "gpt"},
		{Scope: scopeA, Role: "skill_judge", Model: "chat"},
		{Scope: scopeA, Role: "skill_candidates", Model: "9"},
		{Scope: scopeA, Role: "skill_candidates", Model: "04"},
		{Scope: scopeA, Role: "extract", Model: "gpt-6-sol", Provider: "openai"},
		{Scope: scopeA, Role: "extract", Model: "basic"},
		{Scope: scopeA, Role: "skill_scan", Off: []string{"other"}},
		{Scope: scopeA, Role: "skill_scan", Off: []string{"cisco", "cisco"}},
		{Scope: scopeA, Role: "reflect", Model: "sonnet 5.5", Provider: "anthropic", Off: []string{"cisco"}},
		{Scope: scopeA, Role: "embed", Model: "x"},
	} {
		if _, err := f.svc.SetRoleModel(testCtx, f.human, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("bad pick accepted", bad, err)
		}
	}
	for _, ok := range []ProviderRoleModel{
		{Scope: scopeA, Role: "extract", Model: "haiku 4.5", Provider: "anthropic"},
		{Scope: scopeA, Role: "skill_summary", Model: "basic"},
		{Scope: scopeA, Role: "skill_judge", Model: "fixed"},
		{Scope: scopeA, Role: "skill_candidates", Model: "4"},
		{Scope: scopeA, Role: "skill_scan", Off: []string{"cisco"}},
	} {
		if _, err := f.svc.SetRoleModel(testCtx, f.human, ok); err != nil {
			t.Fatal(ok, err)
		}
	}
	defaults := map[string]string{"extract": "haiku", "reflect": "sonnet", "skill_summary": "basic", "skill_scan": "spector,cisco"}
	if _, err := f.svc.ReportProviders(testCtx, f.runner(), ProvidersReport{Scope: scopeA, Providers: &[]Provider{}, RoleDefaults: defaults}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ReportProviders(testCtx, f.runner(), ProvidersReport{Scope: scopeA, RoleDefaults: map[string]string{"route_judge": "clef"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("the runner reported a memoryd role", err)
	}
	f.svc.SkillCandidates = 6
	got, err := f.svc.Providers(testCtx, f.human, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	p := got.Providers
	if p.Roles["extract"].Model != "haiku 4.5" || p.Roles["skill_scan"].Off[0] != "cisco" || p.Roles["skill_candidates"].Model != "4" {
		t.Fatal("picks", p.Roles)
	}
	if p.RoleDefaults["extract"] != "haiku" || p.RoleDefaults["skill_judge"] != "fixed" || p.RoleDefaults["skill_candidates"] != "6" || p.RoleDefaults["embed"] != f.emb.Space() {
		t.Fatal("defaults", p.RoleDefaults)
	}
	if !slices.Equal(p.RoleOptions["route_judge"], []string{"fixed"}) || !slices.Equal(p.RoleOptions["embed"], []string{f.emb.Space()}) {
		t.Fatal("options", p.RoleOptions)
	}
	if r, err := f.svc.SetRoleModel(testCtx, f.human, ProviderRoleModel{Scope: scopeA, Role: "skill_scan"}); err != nil || len(r.Providers.Roles["skill_scan"].Off) != 0 {
		t.Fatal("clear", err)
	}
	// #466: each model change keeps its before and after; an off-list change is not a model change.
	if _, err := f.svc.SetRoleModel(testCtx, f.human, ProviderRoleModel{Scope: scopeA, Role: "extract", Model: "sonnet 5.5", Provider: "anthropic"}); err != nil {
		t.Fatal(err)
	}
	changes, extract := decisionsOf(f.repo, "role_model_changed"), []string{}
	for _, r := range changes {
		if r.Audit.ReasonCode == "extract" {
			extract = append(extract, r.Audit.Reason)
		}
	}
	slices.Sort(extract)
	if len(changes) != 5 || !slices.Equal(extract, []string{"- -> haiku 4.5", "haiku 4.5 -> sonnet 5.5"}) {
		t.Fatalf("role model history: %d changes, extract %q", len(changes), extract)
	}
}

// route_skills runs the judge and the candidate count picked for the scope, and the defaults without a pick.
func TestRouteSkillsUsesPickedJudgeAndCount(t *testing.T) {
	f := newFixture(t)
	for _, id := range []string{"design-review", "design-review-b", "design-review-c"} {
		if _, err := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: id, Path: "/skills/" + id + "/SKILL.md", Content: "---\nname: " + id + "\n---\nReview.", Analysis: detailedSkillAnalysis()}); err != nil {
			t.Fatal(err)
		}
	}
	used, seen := "", 0
	judge := func(name string) SkillJudge {
		return judgeFunc(func(_ context.Context, _ string, c []SkillCandidate) (RouteResult, error) {
			used, seen = name, len(c)
			return RouteResult{SelectedID: c[0].ID, Provider: "test", Judgments: []SkillJudgment{{ID: c[0].ID, Suitability: 3, Confidence: 1}}}, nil
		})
	}
	f.svc.Router, f.svc.Judges, f.svc.SkillCandidates = judge("default"), map[string]SkillJudge{"jev": judge("jev")}, 1
	route := func(want string, n int) {
		t.Helper()
		used, seen = "", 0
		if _, err := f.svc.RouteSkills(testCtx, f.human, SearchInput{Scope: scopeA, Query: "design review"}); err != nil {
			t.Fatal(err)
		}
		if used != want || seen != n {
			t.Fatalf("judge %q saw %d, want %q with %d", used, seen, want, n)
		}
	}
	route("default", 1)
	set := func(role, model string) {
		if _, err := f.svc.SetRoleModel(testCtx, f.human, ProviderRoleModel{Scope: scopeA, Role: role, Model: model}); err != nil {
			t.Fatal(err)
		}
	}
	set("skill_judge", "jev")
	set("skill_candidates", "3") // more than the server's 1: the one search already holds them
	route("jev", 3)
	set("skill_candidates", "2")
	route("jev", 2)
	set("skill_judge", "clef") // not on this server: the default judges
	route("default", 2)
	set("skill_judge", "fixed")
	route("", 0)
}

// 라우팅 판정 uses the picker picked for the scope; fixed skips it.
func TestModelPickerFollowsPick(t *testing.T) {
	def, jev := &fakePicker{}, &fakePicker{}
	s := &Service{ModelPicker: def, Pickers: map[string]ModelPicker{"jev": jev}}
	p := &Providers{Roles: map[string]ProviderRole{"route_judge": {Model: "jev"}}}
	if s.modelPicker(p.role("route_judge")) != jev || s.modelPicker((*Providers)(nil).role("route_judge")) != def {
		t.Fatal("picked picker")
	}
	p.Roles["route_judge"] = ProviderRole{Model: "fixed"}
	if s.modelPicker(p.role("route_judge")) != nil {
		t.Fatal("fixed still picks")
	}
	if got := engineName(s.Pickers, s.Pickers["jev"]); got != "jev" {
		t.Fatal("engine name", got)
	}
}
