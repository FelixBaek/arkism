//go:build linux

package memory

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestReflectSchemaFullKeywords(t *testing.T) {
	cases := []struct {
		schema string
		ok     []string
		bad    []string
	}{
		{`{"type":["string","null"],"pattern":"^a+$"}`, []string{`"aaa"`, `null`}, []string{`"ab"`, `1`}},
		{`{"type":"number","exclusiveMinimum":0,"multipleOf":0.5}`, []string{`1.5`}, []string{`0`, `1.2`}},
		{`{"type":"array","prefixItems":[{"type":"string"}],"items":{"type":"integer"},"uniqueItems":true,"contains":{"const":2},"maxContains":1}`,
			[]string{`["a",1,2]`}, []string{`["a",2,2]`, `[1]`, `["a",1]`}},
		{`{"type":"object","patternProperties":{"^x_":{"type":"integer"}},"additionalProperties":false,"propertyNames":{"maxLength":4},"dependentRequired":{"x_a":["x_b"]}}`,
			[]string{`{"x_a":1,"x_b":2}`}, []string{`{"x_a":1}`, `{"y":1}`, `{"x_abc":1}`}},
		{`{"oneOf":[{"type":"integer"},{"minimum":2}]}`, []string{`1`, `2.5`}, []string{`3`}},
		{`{"if":{"properties":{"k":{"const":"a"}}},"then":{"required":["v"]},"else":{"not":{"required":["v"]}}}`,
			[]string{`{"k":"a","v":1}`, `{"k":"b"}`}, []string{`{"k":"a"}`, `{"k":"b","v":1}`}},
		{`{"$defs":{"n":{"type":"integer","minimum":0}},"type":"array","items":{"$ref":"#/$defs/n"}}`, []string{`[0,3]`}, []string{`[-1]`}},
	}
	for _, c := range cases {
		s, err := parseReflectSchema(json.RawMessage(c.schema))
		if err != nil {
			t.Fatalf("%s: %v", c.schema, err)
		}
		for _, raw := range c.ok {
			var v any
			_ = json.Unmarshal([]byte(raw), &v)
			if !schemaAccepts(s, v) {
				t.Fatalf("%s should accept %s", c.schema, raw)
			}
		}
		for _, raw := range c.bad {
			var v any
			_ = json.Unmarshal([]byte(raw), &v)
			if schemaAccepts(s, v) {
				t.Fatalf("%s should refuse %s", c.schema, raw)
			}
		}
	}
	// A self-referencing schema is bounded, not looped forever.
	loop, err := parseReflectSchema(json.RawMessage(`{"$defs":{"a":{"$ref":"#/$defs/a"}},"$ref":"#/$defs/a"}`))
	if err == nil && schemaAccepts(loop, 1) {
		t.Fatal("a reference loop must not validate")
	}
}

func TestConsolidationScopesAndStrategies(t *testing.T) {
	one := 1
	c := &SpaceConsolidation{
		DefaultScopeMode: "per_tag",
		Strategies: []ConsolidationStrategy{
			{Name: "projects", Scopes: []ObservationTagRule{{Tags: []string{"proj:*"}}}, ObservationsMission: "Track project decisions.", MaxObservationsPerScope: &one},
			{Name: "people", Scopes: []ObservationTagRule{{Tags: []string{"person:*"}, TagsMatch: "exact"}}},
		},
		ScopeLimits: []ObservationScopeLimit{{Scope: []string{"person:*"}, Limit: 0}},
	}
	if got := c.scopesOf([]string{"proj:a", "review:instruction_like", "person:b"}); !slices.EqualFunc(got, [][]string{{"person:b"}, {"proj:a"}}, slices.Equal) {
		t.Fatalf("per_tag scopes drop review marks: %v", got)
	}
	if i, st := c.strategyFor([]string{"proj:a"}); i != 0 || st.Name != "projects" || c.limitFor([]string{"proj:a"}, st) != 1 {
		t.Fatal("first claiming strategy and its cap")
	}
	if i, _ := c.strategyFor([]string{"person:b", "extra"}); i != -1 {
		t.Fatal("exact rule must cover every tag")
	}
	if _, st := c.strategyFor([]string{"person:b"}); c.limitFor([]string{"person:b"}, st) != 0 {
		t.Fatal("scope_limits apply when the strategy sets no cap")
	}
	all := (&SpaceConsolidation{DefaultScopeMode: "all_combinations"}).scopesOf([]string{"a", "b"})
	if len(all) != 3 {
		t.Fatalf("all_combinations %v", all)
	}
	if got := (*SpaceConsolidation)(nil).scopesOf([]string{"a"}); len(got) != 1 || got[0] != nil {
		t.Fatal("no block keeps one shared scope")
	}
	bad := []SpaceConsolidation{{DefaultScopeMode: "nope"}, {Parallel: 9}, {Strategies: []ConsolidationStrategy{{Name: "x"}}}, {ScopeLimits: []ObservationScopeLimit{{Scope: []string{""}}}}}
	for _, b := range bad {
		if b.valid() {
			t.Fatalf("invalid block accepted: %+v", b)
		}
	}
}

func TestConsolidationPerTagMissionPreviewScopesAndDelete(t *testing.T) {
	f, llm, ms := obsFixture(t)
	for _, m := range ms[:3] {
		f.editRecord(colMemories, m.ID, func(r *Record) { r.Tags = []string{"proj:deploy"} })
	}
	block := &SpaceConsolidation{DefaultScopeMode: "per_tag", ObservationsMission: "Keep deploy rules.",
		Strategies: []ConsolidationStrategy{{Name: "projects", Scopes: []ObservationTagRule{{Tags: []string{"proj:*"}}}, ObservationsMission: "Only state deploy steps."}}}
	saved, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{Consolidation: block}, false)
	if err != nil {
		t.Fatal(err)
	}
	secret := SpaceConfigInput{ExpectedRevision: saved.Revision, Consolidation: &SpaceConsolidation{ObservationsMission: "api_key=sk-live-0123456789abcdef0123456789"}}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, secret, false); !errors.Is(err, ErrSecretDetected) {
		t.Fatalf("a secret in the mission: %v", err)
	}
	preview, err := f.svc.PreviewConsolidationStrategies(testCtx, f.human, StrategyPreviewInput{Scope: scopeA})
	// The untagged lunch memory forms the shared scope no strategy claims.
	if err != nil || preview.Scopes != 2 || preview.Unclaimed != 1 || len(preview.Strategies) != 1 || preview.Strategies[0].Claimed != 1 || preview.Strategies[0].Rules[0].Samples[0].HandledBy != "projects" {
		t.Fatalf("preview %+v %v", preview, err)
	}
	if len(llm.prompts) != 0 {
		t.Fatal("the preview calls no model")
	}
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID))}
	out, err := f.consolidate(f.human, false)
	if err != nil || len(out.Proposed) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if !strings.Contains(llm.prompts[0], "Only state deploy steps.") || strings.Contains(llm.prompts[0], "lunch") {
		t.Fatal("the strategy mission is in the prompt and only the scope's memories are sent")
	}
	if !slices.Contains(out.Proposed[0].Tags, "proj:deploy") {
		t.Fatalf("the proposal carries its scope: %v", out.Proposed[0].Tags)
	}
	scopes, err := f.svc.ListObservationScopes(testCtx, f.human, scopeA)
	if err != nil || len(scopes.Items) != 1 || scopes.Items[0].Proposed != 1 || !slices.Equal(scopes.Items[0].Tags, []string{"proj:deploy"}) {
		t.Fatalf("scopes %+v %v", scopes, err)
	}
	if _, err := f.svc.DeleteObservations(testCtx, f.agent, ObservationDeleteInput{Scope: scopeA, All: true}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent bulk delete: %v", err)
	}
	if _, err := f.svc.DeleteObservations(testCtx, f.human, ObservationDeleteInput{Scope: scopeA, All: true, IDs: []string{"x"}}); !errors.Is(err, ErrInvalid) {
		t.Fatal("ids and all together are refused")
	}
	del, err := f.svc.DeleteObservations(testCtx, f.human, ObservationDeleteInput{Scope: scopeA, All: true, Status: obsProposed})
	if err != nil || len(del.Deleted) != 1 || del.More || len(f.repo.rows(colObservations)) != 0 {
		t.Fatalf("delete %+v %v", del, err)
	}
}

func TestMentalModelClearCronPreviewAndOutcome(t *testing.T) {
	f, llm, ms := obsFixture(t)
	mm, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "Staging", Query: "staging flag", Refresh: MentalModelRefresh{Mode: mmRefreshManual}})
	if err != nil {
		t.Fatal(err)
	}
	answer, _ := json.Marshal(mentalModelAnswer{Summary: "Staging needs the flag.", Citations: []string{ms[0].ID}})
	llm.answers = []string{string(answer), string(answer)}
	v, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA})
	if err != nil || v.Draft == nil || v.LastOutcome == nil || v.LastOutcome.Code != mmOutcomeWritten || v.LastOutcome.Trigger != mmTriggerManual {
		t.Fatalf("%+v %v", v, err)
	}
	v, err = f.svc.ReviewMentalModel(testCtx, f.human, mm.ID, MentalModelReviewInput{Scope: scopeA, ExpectedRevision: v.Revision, Action: mmReviewApprove})
	if err != nil {
		t.Fatal(err)
	}
	v, err = f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA})
	if err != nil || v.Draft != nil || v.LastOutcome.Code != mmOutcomeSame {
		t.Fatalf("an unchanged summary stores no draft: %+v %v", v, err)
	}
	if _, err := f.svc.ClearMentalModel(testCtx, f.agent, mm.ID, MentalModelReviewInput{Scope: scopeA, ExpectedRevision: v.Revision}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent clear: %v", err)
	}
	cleared, err := f.svc.ClearMentalModel(testCtx, f.human, mm.ID, MentalModelReviewInput{Scope: scopeA, ExpectedRevision: v.Revision, Reason: "start over"})
	if err != nil || cleared.Active != nil || cleared.Draft != nil || cleared.LastOutcome != nil || cleared.Title != "Staging" {
		t.Fatalf("clear %+v %v", cleared, err)
	}
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p, err := f.svc.PreviewCron(testCtx, f.human, CronPreviewInput{Scope: scopeA, Cron: "30 9 * * 1", Count: 2, After: &after})
	if err != nil || len(p.Next) != 2 || !p.Next[0].Equal(time.Date(2026, 1, 5, 9, 30, 0, 0, time.UTC)) || !p.Next[1].Equal(time.Date(2026, 1, 12, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("cron preview %+v %v", p, err)
	}
	if _, err := f.svc.PreviewCron(testCtx, f.human, CronPreviewInput{Scope: scopeA, Cron: "* *"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("a bad cron is refused")
	}
}

func TestMentalModelHoldAndNextAt(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	last := now.Add(-2 * time.Hour)
	m := &MentalModelData{Refresh: MentalModelRefresh{Mode: mmRefreshChange, MinInterval: "6h"}, LastRefreshAt: &last}
	at, ok := mentalModelNextAt(m, now, time.Time{}, now, 0)
	if !ok || !at.Equal(last.Add(6*time.Hour)) {
		t.Fatalf("a fired trigger waits for min_interval: %v %v", at, ok)
	}
	m.Refresh.MinInterval = ""
	if at, _ := mentalModelNextAt(m, now, time.Time{}, now, 3*time.Hour); !at.Equal(last.Add(3 * time.Hour)) {
		t.Fatalf("the space gap applies when the model sets none: %v", at)
	}
	m.LastOutcome = &MentalModelOutcome{Code: mmOutcomeEmpty, At: now.Add(-time.Minute)}
	if at, _ := mentalModelNextAt(m, now, time.Time{}, now, 0); !at.Equal(now.Add(-time.Minute).Add(mmRetryAfter)) {
		t.Fatalf("a failed attempt holds for the retry period: %v", at)
	}
	m.Draft = &DerivedRevision{}
	if _, ok := mentalModelNextAt(m, now, time.Time{}, now, 0); ok {
		t.Fatal("a model awaiting review is not scheduled")
	}
}

func TestDeriveScheduleQueuesAndRuns(t *testing.T) {
	t.Setenv("MEMORY_DERIVE_SCHEDULE", "on")
	f, llm, ms := obsFixture(t)
	worker := f.auth.actors[f.work]
	for _, verb := range []string{"write", "search", "context"} { // the grants guide/server.md lists for the worker
		worker.verbs[verb] = true
	}
	f.auth.actors[f.work] = worker
	mm, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "Staging", Query: "staging flag", Refresh: MentalModelRefresh{Mode: mmRefreshDaily}})
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.repo.Get(testCtx, colJobs, scopeA, deriveScheduleJobID(scopeA))
	if err != nil || job.Kind != deriveScheduleKind || job.Status != JobStatusPending || job.RunAt == nil {
		t.Fatalf("creating a scheduled model queues the scope's job: %+v %v", job, err)
	}
	later := job.RunAt.Add(time.Hour)
	if err := f.repo.Transaction(testCtx, func(ctx2 context.Context, tx Tx) error { return queueDerive(ctx2, tx, scopeA, later, true, time.Now()) }); err != nil {
		t.Fatal(err)
	}
	if again, _ := f.repo.Get(testCtx, colJobs, scopeA, job.ID); !again.RunAt.Equal(*job.RunAt) {
		t.Fatal("a later due time never moves a pending job back")
	}
	answer, _ := json.Marshal(mentalModelAnswer{Summary: "Staging needs the flag.", Citations: []string{ms[0].ID}})
	llm.answers = []string{string(answer)}
	state := DeriveScheduleJob{}
	outcome, err := f.svc.derivePass(testCtx, f.work, scopeA, &state, time.Now().UTC())
	if err != nil || outcome != "ok" {
		t.Fatalf("%s %v", outcome, err)
	}
	v, _ := f.svc.GetMentalModel(testCtx, f.human, scopeA, mm.ID)
	if v.Draft == nil || v.Active != nil || v.LastOutcome == nil || v.LastOutcome.Trigger != "initial" {
		t.Fatalf("the scheduler writes a draft for review, never an approval: %+v %+v", v, v.LastOutcome)
	}
	t.Setenv("MEMORY_DERIVE_SCHEDULE", "")
	if n, err := f.svc.SeedDeriveSchedules(testCtx); n != 0 || err != nil {
		t.Fatal("the scheduler is off unless the operator turns it on")
	}
}

func TestReflectDefaultsAndDirectiveScoping(t *testing.T) {
	in := ReflectInput{}
	run := &reflectRun{}
	space := SpaceConfig{ReflectDefaults: &SpaceReflectDefaults{Budget: "high", FactTypes: []string{"observation"}, Tools: ReflectToolBudget{RecallMaxTokens: 500}}}
	applyReflectDefaults(&in, run, space, false, false)
	if in.Budget != "high" || in.MaxIterations != 6 || in.Limit != 20 || !slices.Equal(in.FactTypes, []string{"observation"}) || run.tools.RecallMaxTokens != 500 || run.tools.ExpandMaxTokens != reflectToolDefaults.ExpandMaxTokens {
		t.Fatalf("%+v %+v", in, run.tools)
	}
	in = ReflectInput{Budget: "low", MaxIterations: 4}
	applyReflectDefaults(&in, &reflectRun{}, SpaceConfig{}, true, false)
	if in.MaxIterations != 4 || in.Limit != 5 {
		t.Fatalf("a set value wins over the budget: %+v", in)
	}
	if validReflectOptions(&ReflectInput{FactTypes: []string{"chunk"}}) || validReflectOptions(&ReflectInput{Budget: "max"}) {
		t.Fatal("unknown fact types and budgets are refused")
	}
	rows := []Record{{ID: "all"}, {ID: "eng", Tags: []string{"eng"}}, {ID: "ops", Tags: []string{"ops"}}}
	got := directivesForTags(rows, []string{"eng"})
	if len(got) != 2 || got[0].ID != "all" || got[1].ID != "eng" {
		t.Fatalf("tagged directives apply only to matching tags: %+v", got)
	}
}

func TestPromptPreviewCallsNoModel(t *testing.T) {
	f, llm, _ := obsFixture(t)
	mission := &SpaceConsolidation{ObservationsMission: "Keep deploy rules."}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{Consolidation: mission}, false); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"retain", "consolidation", "reflect"} {
		p, err := f.svc.PreviewPrompt(testCtx, f.human, PromptPreviewInput{Scope: scopeA, Operation: op})
		if err != nil || p.System == "" || !strings.Contains(p.User+p.System, promptPlaceholder) {
			t.Fatalf("%s: %+v %v", op, p, err)
		}
		if op == "consolidation" && !strings.Contains(p.System, "Keep deploy rules.") {
			t.Fatal("the consolidation preview carries the space mission")
		}
		if strings.Contains(p.User+p.System, "staging flag") {
			t.Fatal("no record text is returned")
		}
	}
	if _, err := f.svc.PreviewPrompt(testCtx, f.human, PromptPreviewInput{Scope: scopeA, Operation: "other"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown operation")
	}
	if len(llm.prompts) != 0 {
		t.Fatal("previews call no model")
	}
}

func TestReflectModelSettingsRoundTrip(t *testing.T) {
	r := MentalModelRefresh{Mode: mmRefreshManual, Budget: "low", ResponseSchema: json.RawMessage(`{"type": "object", "required": ["a"]}`)}
	raw, err := storeMarshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back MentalModelRefresh
	if err := storeUnmarshal(raw, &back); err != nil || back.Budget != "low" {
		t.Fatalf("%+v %v", back, err)
	}
	if s, err := parseReflectSchema(back.ResponseSchema); err != nil || !schemaAccepts(s, map[string]any{"a": 1.0}) || schemaAccepts(s, map[string]any{}) {
		t.Fatalf("response_schema survives the store: %v %v", back.ResponseSchema, err)
	}
	if validMentalModelRecall(MentalModelRefresh{ResponseSchema: json.RawMessage(`{"$ref":"https://example.invalid"}`)}) {
		t.Fatal("a remote ref is refused")
	}
}
