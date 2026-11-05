package memory

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func toolCall(name string, args map[string]any) string {
	raw, _ := json.Marshal(map[string]any{"tool_call": map[string]any{"name": name, "args": args}})
	return string(raw)
}

func TestReflectToolLoopReadsMoreAndTraces(t *testing.T) {
	f, llm := reflectFixture(t)
	first := f.activeMemory("alpha needle: the release train leaves on Fridays", "alpha")
	second := f.activeMemory("bravo hotfix rule: hotfixes skip the train", "bravo")
	llm.answers = []string{toolCall("recall", map[string]any{"query": "bravo"}), toolCall("expand", map[string]any{"id": "mem-not-retrieved"}), "Hotfixes skip it [" + second.ID + "]"}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "alpha", MaxIterations: 4})
	if err != nil {
		t.Fatal(err)
	}
	if out.Iterations != 3 || len(out.ToolTrace) != 2 || out.Usage.Requests != 3 || out.Usage.TotalTokens != 450 {
		t.Fatal("loop", out.Iterations, out.ToolTrace, out.Usage)
	}
	step := out.ToolTrace[0]
	if step.Tool != "recall" || step.Args.Query != "bravo" || !slices.Contains(step.IDs, second.ID) || step.Error != "" {
		t.Fatal("recall step", step)
	}
	if out.ToolTrace[1].Error != "invalid_arguments" || len(out.ToolTrace[1].IDs) != 0 {
		t.Fatal("expand of an unknown id must be refused", out.ToolTrace[1])
	}
	if !slices.Equal(out.CitedMemoryIDs, []string{second.ID}) || !slices.ContainsFunc(out.Retrieved, func(r ReflectRetrieved) bool { return r.ID == first.ID }) {
		t.Fatal("citations", out.CitedMemoryIDs, out.Retrieved)
	}
	if !strings.Contains(llm.prompts[1], "bravo hotfix rule") || !strings.Contains(llm.prompts[1], "Tools already called") {
		t.Fatal("second prompt lacks the tool result", llm.prompts[1])
	}
	// The trace names ids only, never memory text.
	raw, _ := json.Marshal(out.ToolTrace)
	if strings.Contains(string(raw), "hotfixes skip") || strings.Contains(string(raw), "release train") {
		t.Fatal("trace carries memory text", string(raw))
	}
	if n, _ := f.svc.dailyModelCalls(testCtx, projectScope(scopeA), reflectOperation, time.Now()); n != 3 {
		t.Fatal("each call recorded", n)
	}
	if rows, _ := f.repo.ListRecords(testCtx, scopeA, colMemories, 100); len(rows) != 2 {
		t.Fatal("answer stored", len(rows))
	}
}

// One reply may ask for several tools; each is its own trace step and the loop spends one model call.
func TestReflectParallelToolCalls(t *testing.T) {
	f, llm := reflectFixture(t)
	f.activeMemory("alpha needle", "alpha")
	b := f.activeMemory("bravo fact", "bravo")
	c := f.activeMemory("charlie fact", "charlie")
	calls, _ := json.Marshal(map[string]any{"tool_calls": []any{
		map[string]any{"name": "recall", "args": map[string]any{"query": "bravo"}},
		map[string]any{"name": "recall", "args": map[string]any{"query": "charlie"}},
		map[string]any{"name": "write_memory", "args": map[string]any{}},
	}})
	llm.answers = []string{string(calls), "ok [" + c.ID + "]"}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "alpha"})
	if err != nil || out.Iterations != 2 || len(out.ToolTrace) != 3 || out.ToolTrace[2].Error != "unknown_tool" {
		t.Fatal(err, out.Iterations, out.ToolTrace)
	}
	if !strings.Contains(llm.prompts[1], "bravo fact") || !strings.Contains(llm.prompts[1], "charlie fact") || !slices.Equal(out.CitedMemoryIDs, []string{c.ID}) || slices.Contains(out.CitedMemoryIDs, b.ID) {
		t.Fatal(out.CitedMemoryIDs)
	}
	many := []any{}
	for range reflectParallelMax + 2 {
		many = append(many, map[string]any{"name": "mental_models", "args": map[string]any{}})
	}
	raw, _ := json.Marshal(map[string]any{"tool_calls": many})
	got, ok := parseReflectToolCalls(string(raw))
	if !ok || len(got) != reflectParallelMax {
		t.Fatal("parallel bound", len(got))
	}
}

func TestReflectIterationCap(t *testing.T) {
	f, llm := reflectFixture(t)
	f.activeMemory("cap needle", "needle")
	call := toolCall("mental_models", map[string]any{})
	llm.answers = []string{call, call, call}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", MaxIterations: 2})
	if !errors.Is(err, errModelAnswer) || len(llm.prompts) != 2 || out.Iterations != 2 {
		t.Fatal(err, len(llm.prompts), out.Iterations)
	}
	if !strings.Contains(llm.prompts[0], "TOOLS.") || strings.Contains(llm.prompts[1], "TOOLS.") {
		t.Fatal("the last call must offer no tools")
	}
	if _, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", MaxIterations: reflectMaxIter + 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal("iteration bound", err)
	}
	// One iteration is the single-shot answer: no tool text at all.
	llm.answers, llm.prompts = []string{"ok"}, nil
	if _, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", MaxIterations: 1}); err != nil || strings.Contains(llm.prompts[0], "TOOLS.") {
		t.Fatal(err)
	}
}

// A memory the transfer policy refuses never reaches the model through a tool either.
func TestReflectToolSendsOnlyTransferableMemories(t *testing.T) {
	f, llm := reflectFixture(t)
	f.activeMemory("alpha open fact", "alpha")
	f.activeMemory("bravo open fact", "bravo")
	r := f.activeMemory("bravo restricted fact", "bravo")
	r.Sensitivity = "restricted"
	f.repo.store[colMemories][recKey(r.Scope, r.ID)] = r
	llm.answers = []string{toolCall("recall", map[string]any{"query": "bravo"}), toolCall("teleport", map[string]any{}), "done"}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range llm.prompts {
		if strings.Contains(p, "bravo restricted") {
			t.Fatal("restricted memory sent")
		}
	}
	if slices.Contains(out.ToolTrace[0].IDs, r.ID) || !strings.Contains(llm.prompts[1], "bravo open") || out.ToolTrace[1].Error != "unknown_tool" {
		t.Fatal(out.ToolTrace)
	}
}

func TestReflectResponseSchema(t *testing.T) {
	f, llm := reflectFixture(t)
	m := f.activeMemory("schema needle: deploys run on Fridays", "needle")
	schema := json.RawMessage(`{"type":"object","required":["day","sources"],"additionalProperties":false,"properties":{"day":{"type":"string","enum":["Friday","Monday"]},"sources":{"type":"array","items":{"type":"string"},"minItems":1}}}`)
	llm.answers = []string{"```json\n{\"day\":\"Friday\",\"sources\":[\"" + m.ID + "\"]}\n```"}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", ResponseSchema: schema})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := out.StructuredAnswer.(map[string]any)
	if !ok || got["day"] != "Friday" || !slices.Equal(out.CitedMemoryIDs, []string{m.ID}) || !strings.Contains(llm.prompts[0], `"additionalProperties":false`) {
		t.Fatal(out)
	}
	for _, bad := range []string{`{"day":"Sunday","sources":["x"]}`, `{"day":"Friday","sources":[]}`, `{"day":"Friday","sources":["x"],"extra":1}`, `{"day":"Friday"}`, "Friday"} {
		llm.answers, llm.prompts = []string{bad}, nil
		if _, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", ResponseSchema: schema, MaxIterations: 1}); !errors.Is(err, errModelAnswer) {
			t.Fatal("invalid answer accepted", bad, err)
		}
	}
	bounded := json.RawMessage(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":{"n":{"type":"integer","minimum":1,"maximum":5},"s":{"type":"string","minLength":2,"maxLength":3,"format":"x"}}}`)
	for answer, ok := range map[string]bool{`{"n":1,"s":"ab"}`: true, `{"n":0}`: false, `{"n":6}`: false, `{"s":"a"}`: false, `{"s":"abcd"}`: false} {
		llm.answers, llm.prompts = []string{answer}, nil
		if _, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", ResponseSchema: bounded, MaxIterations: 1}); (err == nil) != ok {
			t.Fatal("bounds", answer, err)
		}
	}
	for _, bad := range []string{`{"type":"string","pattern":"^a"}`, `{"type":"number","minimum":"1"}`, `{"type":"date"}`, `[1]`, `{"properties":{"a":1}}`, `{"type":"object"} {}`} {
		llm.prompts = nil
		if _, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", ResponseSchema: json.RawMessage(bad)}); !errors.Is(err, ErrInvalid) || len(llm.prompts) != 0 {
			t.Fatal("unsupported schema accepted", bad, err)
		}
	}
	if rows, _ := f.repo.ListRecords(testCtx, scopeA, colMemories, 100); len(rows) != 1 {
		t.Fatal("answer stored", len(rows))
	}
}

func TestReflectMissionAndDisposition(t *testing.T) {
	f, llm := reflectFixture(t)
	f.activeMemory("mission needle", "needle")
	mission := "Answer for the on-call engineer."
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ReflectMission: &mission, ReflectDisposition: &ReflectDisposition{Skepticism: 5}}, false); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.Reflect(testCtx, f.agent, ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true})
	if err != nil || !strings.Contains(out.Prompt.System, mission) || !strings.Contains(out.Prompt.System, "skepticism 5/5") || len(llm.prompts) != 0 {
		t.Fatal("space guidance", out.Prompt, err)
	}
	// prompt_only returns the prompt, so the space's mission needs read:space_configs; a request's own guidance does not.
	f.svc.Auth = denyAction{f.auth, "read:space_configs"}
	if _, err = f.svc.Reflect(testCtx, f.agent, ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true}); !errors.Is(err, ErrDenied) {
		t.Fatal("space mission shown without read:space_configs", err)
	}
	if out, err = f.svc.Reflect(testCtx, f.agent, ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true, Mission: "own", Disposition: &ReflectDisposition{}}); err != nil || strings.Contains(out.Prompt.System, mission) {
		t.Fatal("request guidance", err)
	}
	f.svc.Auth = f.auth
	llm.answers = []string{"ok"}
	if _, err = f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", Mission: "Answer for a new hire.", Disposition: &ReflectDisposition{Empathy: 1}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.prompts[0], "new hire") || strings.Contains(llm.prompts[0], "on-call") || !strings.Contains(llm.prompts[0], "empathy 1/5") || strings.Contains(llm.prompts[0], "skepticism") {
		t.Fatal("request guidance must override", llm.prompts[0])
	}
	bad := strings.Repeat("x", reflectMissionMax+1)
	if _, err = f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: 1, ReflectMission: &bad}, false); !errors.Is(err, ErrInvalid) {
		t.Fatal("long mission", err)
	}
	if _, err = f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: 1, ReflectDisposition: &ReflectDisposition{Literalism: 6}}, false); !errors.Is(err, ErrInvalid) {
		t.Fatal("disposition range", err)
	}
	if _, err = f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", Mission: bad}); !errors.Is(err, ErrInvalid) {
		t.Fatal("request mission bound", err)
	}
	view, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExpectedRevision: 1, ReflectDisposition: &ReflectDisposition{}}, false)
	if err != nil || view.Config.ReflectDisposition != nil || view.Config.ReflectMission != mission {
		t.Fatal("clear disposition", view.Config, err)
	}
}

func TestReflectEntityToolChecksEachMemory(t *testing.T) {
	f, repo := entityFixture(t)
	a := f.auth.actors[f.human]
	a.verbs["generate"] = true
	f.auth.actors[f.human] = a
	llm := &fakeWikiLLM{answers: []string{toolCall("entity", map[string]any{"name": "postgres"}), "ok"}}
	f.svc.Wiki, f.svc.Embedder = llm, nil
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	open := entityMemory(t, f, repo, "m1", "Arkism stores memories in Postgres.", day)
	open.Sensitivity = "internal"
	f.repo.store[colMemories][recKey(open.Scope, open.ID)] = open
	entityMemory(t, f, repo, "m2", "Postgres root password rotation is manual.", day) // unclassified: never sent
	bare := entityMemory(t, f, repo, "m3", "Postgres backups run nightly.", day)
	bare.Sensitivity, bare.Sources = "internal", nil // no sources: skipped, not a failed tool
	f.repo.store[colMemories][recKey(bare.Scope, bare.ID)] = bare
	if _, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA}); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "unrelated question"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.ToolTrace[0].IDs, []string{"m1"}) || out.ToolTrace[0].Error != "" || strings.Contains(llm.prompts[1], "rotation") || strings.Contains(llm.prompts[1], "backups") || !strings.Contains(llm.prompts[1], "stores memories in Postgres") {
		t.Fatal(out.ToolTrace, llm.prompts[1])
	}
}

// An approved summary reaches the model only while every memory it cites may.
func TestReflectMentalModelToolFollowsCitations(t *testing.T) {
	f, llm, ms := obsFixture(t)
	mm, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "Staging", Query: "staging flag", Refresh: mmRefreshManual})
	if err != nil {
		t.Fatal(err)
	}
	summary, _ := json.Marshal(mentalModelAnswer{Summary: "Staging deploys need the flag.", Citations: []string{ms[0].ID}})
	llm.answers = []string{string(summary)}
	v, err := f.svc.RefreshMentalModel(testCtx, f.human, mm.ID, MentalModelRefreshInput{Scope: scopeA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.ReviewMentalModel(testCtx, f.human, mm.ID, MentalModelReviewInput{Scope: scopeA, ExpectedRevision: v.Revision, Action: mmReviewApprove}); err != nil {
		t.Fatal(err)
	}
	ask := func() ReflectOutput {
		t.Helper()
		llm.answers, llm.prompts = []string{toolCall("mental_models", map[string]any{}), "see [" + mm.ID + "]"}, nil
		out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "lunch"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := ask()
	if !slices.Equal(out.ToolTrace[0].IDs, []string{mm.ID}) || !strings.Contains(llm.prompts[1], "Staging deploys need the flag.") || !slices.Equal(out.CitedMemoryIDs, []string{mm.ID}) {
		t.Fatal(out.ToolTrace, out.CitedMemoryIDs)
	}
	r := f.repo.store[colMemories][recKey(ms[0].Scope, ms[0].ID)]
	r.Sensitivity = "restricted"
	f.repo.store[colMemories][recKey(r.Scope, r.ID)] = r
	out = ask()
	if len(out.ToolTrace[0].IDs) != 0 || strings.Contains(llm.prompts[1], "Staging deploys") {
		t.Fatal("summary of a restricted memory sent", out.ToolTrace)
	}
}
