package memory

import (
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func putSettings(t *testing.T, f *fixture, scope Scope, in SpaceConfigInput) {
	t.Helper()
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scope, in, false); err != nil {
		t.Fatal(err)
	}
}

func TestSpaceSettingsInheritance(t *testing.T) {
	f := newFixture(t)
	f.svc.SpaceTemplate = &SpaceTemplate{Config: SpaceConfig{ExtractionMode: spaceExtractVerbose, ReflectMaxTokens: 512,
		DefenseRules: []SpaceDefenseRule{{Name: "codename", Pattern: `(?i)project\s+falcon`, Action: "redact"}}}}
	off, budget := false, 900
	putSettings(t, f, scopeA, SpaceConfigInput{Lanes: &SpaceLanes{Graph: &off}, RecallBudgetTokens: &budget,
		DefenseRules: &[]SpaceDefenseRule{{Pattern: `\bINC-\d+\b`, Action: "block"}}})
	view, err := f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	c, src := view.Config, view.Sources
	if c.ExtractionMode != spaceExtractVerbose || src["extraction_mode"] != spaceSourceTemplate || c.ReflectMaxTokens != 512 ||
		c.RecallBudgetTokens != 900 || src["recall_budget_tokens"] != spaceSourceSpace || c.lane("graph") || src["lanes.graph"] != spaceSourceSpace ||
		!c.lane("fts") || src["lanes.fts"] != spaceSourceServer || src["rerank"] != spaceSourceServer {
		t.Fatalf("effective = %+v sources %v", c, src)
	}
	// Defense rules add up: the template's stay, the space adds its own.
	if len(c.DefenseRules) != 2 || src["defense_rules"] != "template+space" {
		t.Fatalf("rules = %+v from %s", c.DefenseRules, src["defense_rules"])
	}
	// Rules (names and patterns) and notes are for people; the agent still sees where rules come from.
	agentView, err := f.svc.EffectiveSpaceConfig(testCtx, f.agent, scopeA)
	if err != nil || agentView.Config.DefenseRules != nil || agentView.Sources["defense_rules"] != "template+space" || agentView.Config.RecallBudgetTokens != 900 {
		t.Fatalf("agent view: %+v %v", agentView, err)
	}
	if got, err := f.svc.GetSpaceConfig(testCtx, f.agent, scopeA); err == nil && got.Config.DefenseRules != nil {
		t.Fatalf("agent config read shows rules: %+v", got.Config.DefenseRules)
	}
	// A space without stored settings gets the template, then the server defaults.
	b, _ := f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeB)
	if b.Config.RecallBudgetTokens != 2000 || len(b.Config.DefenseRules) != 1 || b.Revision != 0 {
		t.Fatalf("empty space = %+v", b)
	}
	req := httptest.NewRequest("GET", "/v1/spaces/ws-a/proj-a/config/effective", nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"recall_budget_tokens":"space"`) {
		t.Fatalf("route = %d %s", w.Code, w.Body.String())
	}
}

func TestSpaceSettingsValidationAndHumanOnly(t *testing.T) {
	f := newFixture(t)
	for _, rules := range [][]SpaceDefenseRule{
		{{Pattern: `a*`, Action: "redact"}},       // matches nothing-at-all
		{{Pattern: `redacted`, Action: "redact"}}, // matches its own mark
		{{Pattern: `(?=x)`, Action: "block"}},     // not RE2
		{{Pattern: `x`, Action: "drop"}},          // unknown action
		{{Pattern: strings.Repeat("x", 600), Action: "block"}},
	} {
		if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{DefenseRules: &rules}, false); !errors.Is(err, ErrInvalid) {
			t.Fatalf("rules %+v err = %v", rules, err)
		}
	}
	custom := spaceExtractCustom
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{ExtractionMode: &custom}, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("custom without instructions err = %v", err)
	}
	tools := []string{"recall", "Bad Tool"}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{MCPTools: &tools}, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad tool err = %v", err)
	}
	ok := []SpaceDefenseRule{{Pattern: `x\d`, Action: "block"}}
	if _, err := f.svc.PutSpaceConfig(testCtx, f.agent, scopeA, SpaceConfigInput{DefenseRules: &ok}, false); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent edit err = %v", err)
	}
	putSettings(t, f, scopeA, SpaceConfigInput{DefenseRules: &ok})
	if len(scopeRecords(f, "audit_events", scopeA)) == 0 {
		t.Fatal("settings edit not audited")
	}
}

func TestSpaceDefenseOnCapture(t *testing.T) {
	f := newFixture(t)
	putSettings(t, f, scopeA, SpaceConfigInput{DefenseRules: &[]SpaceDefenseRule{
		{Name: "ticket", Pattern: `\bINC-\d+\b`, Action: "block"},
		{Name: "codename", Pattern: `(?i)project\s+falcon`, Action: "redact"},
	}})
	items := f.capture(f.human, captureInput(scopeA, "evt-def", explicit("Project Falcon ships on Fridays"), explicit("INC-4411 was caused by the cache")))
	if items[0].Status != "candidate" || items[1].Status != "rejected" || items[1].Reason != "defense_blocked" {
		t.Fatalf("items = %+v", items)
	}
	cands := scopeRecords(f, "memory_candidates", scopeA)
	if len(cands) != 1 || cands[0].Text != "[redacted] ships on Fridays" {
		t.Fatalf("candidates = %+v", cands)
	}
	// Direct creates go through the same rules.
	if _, err := f.svc.Create(testCtx, f.human, "memory_candidates", CreateInput{Scope: scopeA, Kind: "semantic", Text: "see INC-12"}); !errors.Is(err, ErrDefenseBlocked) {
		t.Fatalf("create err = %v", err)
	}
	// Other spaces keep their own (no) rules.
	if items := f.capture(f.human, captureInput(scopeB, "evt-def-b", explicit("Project Falcon ships on Fridays"))); items[0].Status != "candidate" {
		t.Fatalf("other space = %+v", items)
	}
}

func TestSpaceDefenseAndGuidanceOnFileExtract(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(extractRuleClaim))
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionInstructions: ptr("Keep only design rules."),
		DefenseRules: &[]SpaceDefenseRule{{Pattern: `배포 전에`, Action: "redact"}, {Pattern: `TOP-SECRET`, Action: "block"}}})
	if _, err := f.svc.ExtractPreview(testCtx, f.human, ExtractPreviewInput{Scope: scopeA, Text: extractKoreanSource}); err != nil {
		t.Fatal(err)
	}
	if len(llm.prompts) != 1 || strings.Contains(llm.prompts[0], "배포 전에") || !strings.Contains(llm.prompts[0], spaceRedactMark) || !strings.Contains(llm.prompts[0], "Keep only design rules.") {
		t.Fatalf("prompt = %v", llm.prompts)
	}
	in := FileMemoriesInput{Scope: scopeA, Filename: "rules.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte(extractKoreanSource + "\nTOP-SECRET")), DryRun: true}
	if _, err := f.svc.ExtractFileMemories(testCtx, f.human, in); !errors.Is(err, ErrDefenseBlocked) || len(llm.prompts) != 1 {
		t.Fatalf("blocked file err = %v prompts %d", err, len(llm.prompts))
	}
}

func TestSpaceLanesShapeRecall(t *testing.T) {
	f := newFixture(t)
	off := false
	putSettings(t, f, scopeA, SpaceConfigInput{Lanes: &SpaceLanes{Vector: &off}})
	in := SearchInput{Scope: scopeA, Query: "tabs"}
	if err := f.svc.applySpaceSearch(testCtx, colMemories, &in); err != nil || !in.spaceModes || strings.Join(in.Modes, ",") != "exact,fts" {
		t.Fatalf("modes = %v %v", in.Modes, err)
	}
	asked := SearchInput{Scope: scopeA, Query: "tabs", Modes: []string{"vector"}}
	if err := f.svc.applySpaceSearch(testCtx, colMemories, &asked); err != nil || asked.spaceModes || asked.Modes[0] != "vector" {
		t.Fatalf("caller modes changed: %v", asked.Modes)
	}
	if spaceRerankOn(&SpaceConfig{Rerank: &off}) || !spaceRerankOn(nil) || spaceLaneOn(&SpaceConfig{Lanes: &SpaceLanes{Temporal: &off}}, "temporal", true) {
		t.Fatal("rerank/lane switches")
	}
}

func TestSpaceTemplateRoundTripAndDefault(t *testing.T) {
	f := newFixture(t)
	in := directiveInput("Units", "Always use metric units.", "both", 7)
	in.Scope = scopeA
	if _, err := f.svc.CreateDirective(testCtx, f.human, in); err != nil {
		t.Fatal(err)
	}
	f.activeMemory("prefer tabs over spaces", "style")
	tools := []string{"recall", "remember"}
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionMode: ptr(spaceExtractVerbose), MCPTools: &tools,
		DefenseRules: &[]SpaceDefenseRule{{Pattern: `INC-\d+`, Action: "block"}}})
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{Template: true})
	m, lines, err := parseSpaceBundle(bundle)
	if err != nil || !m.Template {
		t.Fatalf("manifest %+v %v", m, err)
	}
	for _, line := range lines {
		if line.Section == colMemories || line.Section == "memory_candidates" {
			t.Fatal("template carries memories")
		}
	}
	path := t.TempDir() + "/template.jsonl"
	if err = os.WriteFile(path, []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}
	tpl, err := LoadSpaceTemplate(path)
	if err != nil || tpl.Config.ExtractionMode != spaceExtractVerbose || len(tpl.Config.MCPTools) != 2 {
		t.Fatalf("template = %+v %v", tpl, err)
	}
	f.svc.SpaceTemplate = tpl
	job, err := f.svc.ImportSpace(testCtx, f.human, scopeB, SpaceImportInput{DefaultTemplate: true})
	if err != nil {
		t.Fatal(err)
	}
	if report := waitSpaceJob(t, f, scopeB, job.ID).Report; report.Config != "applied" || report.Imported != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(scopeRecords(f, colDirectives, scopeB)) != 1 || len(scopeRecords(f, "memory_candidates", scopeB)) != 0 {
		t.Fatal("template import wrote the wrong records")
	}
	// A template sets up a new space only: a second apply is refused, nothing doubles.
	if _, err = f.svc.ImportSpace(testCtx, f.human, scopeB, SpaceImportInput{DefaultTemplate: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second apply err = %v", err)
	}
	if _, err = f.svc.ImportSpace(testCtx, f.human, scopeB, SpaceImportInput{DefaultTemplate: true, Bundle: bundle}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("both sources err = %v", err)
	}
	if _, err = LoadSpaceTemplate(t.TempDir() + "/missing"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing template err = %v", err)
	}
}

func TestSpaceDefenseOnEveryIngestionPath(t *testing.T) {
	f := handoffFixture(t)
	putSettings(t, f, scopeA, SpaceConfigInput{DefenseRules: &[]SpaceDefenseRule{
		{Pattern: `INC-\d+`, Action: "block"}, {Pattern: `(?i)falcon`, Action: "redact"}}})
	// Knowledge documents.
	kin := knowledgeFixtureInput()
	kin.Content += " Falcon ships."
	ing, err := f.svc.IngestKnowledge(testCtx, f.human, kin)
	if err != nil || len(ing.Chunks) != 1 || strings.Contains(ing.Chunks[0].Text, "Falcon") || !strings.Contains(ing.Chunks[0].Text, spaceRedactMark) {
		t.Fatalf("knowledge = %+v %v", ing.Chunks, err)
	}
	kin.SourceKey, kin.Content = "other", "see INC-7"
	if _, err = f.svc.IngestKnowledge(testCtx, f.human, kin); !errors.Is(err, ErrDefenseBlocked) {
		t.Fatalf("knowledge block err = %v", err)
	}
	// Uploaded text files: the stored bytes are the redacted ones.
	file := FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA}), Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}
	out, err := f.svc.IngestFile(testCtx, f.human, file, []byte("falcon download"))
	if err != nil || out.Artifact.Digest != ContentHash(spaceRedactMark+" download") || out.Artifact.Bytes != int64(len(spaceRedactMark+" download")) {
		t.Fatalf("file = %+v %v", out.Artifact, err)
	}
	if _, err = f.svc.IngestFile(testCtx, f.human, file, []byte("INC-9 log")); !errors.Is(err, ErrDefenseBlocked) {
		t.Fatalf("file block err = %v", err)
	}
	// A block is audited without its text.
	for _, r := range scopeRecords(f, "audit_events", scopeA) {
		if strings.Contains(structuredText(r), "INC-") {
			t.Fatal("audit kept the blocked text")
		}
	}
	// Bounded work: an oversized text is refused before any rule runs.
	big := strings.Repeat("x", spaceDefenseMaxText+1)
	if err = defendAll([]SpaceDefenseRule{{Pattern: `y`, Action: "block"}}, &big); !errors.Is(err, errBlobTooLarge) {
		t.Fatalf("oversized err = %v", err)
	}
}

func TestSpaceExtractionDisabledAndVerbatim(t *testing.T) {
	f, llm := wikiFixture(t, extractAnswer(extractRuleClaim))
	off := false
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionEnabled: &off})
	items := f.capture(f.human, captureInput(scopeA, "evt-off", explicit("prefer tabs")))
	if items[0].Status != "rejected" || items[0].Reason != "extraction_disabled" {
		t.Fatalf("automatic capture = %+v", items)
	}
	in := captureInput(scopeA, "evt-remember", explicit("prefer tabs"))
	in.Event.Type = "explicit_remember"
	if items = f.capture(f.human, in); items[0].Status != "candidate" {
		t.Fatalf("explicit remember = %+v", items)
	}
	if _, err := f.svc.ExtractPreview(testCtx, f.human, ExtractPreviewInput{Scope: scopeA, Text: extractKoreanSource}); !errors.Is(err, ErrExtractionDisabled) || len(llm.prompts) != 0 {
		t.Fatalf("extract with extraction off err = %v", err)
	}
	on, verbatim := true, spaceExtractVerbatim
	putSettings(t, f, scopeA, SpaceConfigInput{ExpectedRevision: -1, ExtractionEnabled: &on, ExtractionMode: &verbatim})
	if _, err := f.svc.ExtractPreview(testCtx, f.human, ExtractPreviewInput{Scope: scopeA, Text: extractKoreanSource}); err != nil {
		t.Fatal(err)
	}
	if len(llm.prompts) != 1 || !strings.Contains(llm.prompts[0], "never a paraphrase") {
		t.Fatalf("verbatim prompt = %v", llm.prompts)
	}
}

func TestSpaceExportHidesRulesFromAgents(t *testing.T) {
	f := newFixture(t)
	putSettings(t, f, scopeA, SpaceConfigInput{DefenseRules: &[]SpaceDefenseRule{{Name: "codename", Pattern: `(?i)falcon`, Action: "redact"}}})
	job, err := f.svc.ExportSpace(testCtx, f.agent, scopeA, SpaceExportInput{})
	if err != nil {
		t.Skipf("agent cannot export in this fixture: %v", err)
	}
	waitSpaceJob(t, f, scopeA, job.ID)
	bundle, err := f.svc.DownloadSpaceExport(testCtx, f.agent, scopeA, job.ID)
	if err != nil || strings.Contains(bundle, "falcon") || strings.Contains(bundle, "codename") {
		t.Fatalf("agent export carries rules: %v", err)
	}
	if human := exportBundle(t, f, scopeA, SpaceExportInput{}); !strings.Contains(human, "falcon") {
		t.Fatal("human export lost its rules")
	}
}

func TestSpaceDefenseBlockHiddenFromUnauthorizedCallers(t *testing.T) {
	f := newFixture(t)
	putSettings(t, f, scopeA, SpaceConfigInput{DefenseRules: &[]SpaceDefenseRule{{Pattern: `INC-\d+`, Action: "block"}}})
	blocked := CreateInput{Scope: scopeA, Kind: "semantic", Text: "see INC-12"}
	if _, err := f.svc.Create(testCtx, "unknown-token", "memory_candidates", blocked); err == nil || errors.Is(err, ErrDefenseBlocked) {
		t.Fatalf("unauthenticated create err = %v", err)
	}
	if _, err := f.svc.ReviewCandidate(testCtx, f.agent, "cand-1", CandidateReviewInput{Scope: scopeA, ExpectedRevision: 1, Action: "edit_approve", Content: "INC-1"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent review err = %v", err)
	}
	sup := SupersedeInput{Scope: scopeA, ExpectedRevision: 1, Reason: "INC-2 fixed", NewContent: "new", Sources: []SourceRef{{SourceID: "s"}}}
	if _, err := f.svc.Supersede(testCtx, f.agent, "mem-1", sup); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent supersede err = %v", err)
	}
	// The same texts from a person who may write are refused by the rule.
	if _, err := f.svc.Create(testCtx, f.human, "memory_candidates", blocked); !errors.Is(err, ErrDefenseBlocked) {
		t.Fatalf("human create err = %v", err)
	}
	if _, err := f.svc.Supersede(testCtx, f.human, "mem-1", sup); !errors.Is(err, ErrDefenseBlocked) {
		t.Fatalf("human supersede err = %v", err)
	}
	// Over HTTP an unauthenticated caller gets 401, not defense_blocked.
	req := httptest.NewRequest("POST", "/v1/memory_candidates", strings.NewReader(structuredText(blocked)))
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 401 || strings.Contains(w.Body.String(), "defense_blocked") {
		t.Fatalf("http = %d %s", w.Code, w.Body.String())
	}
}

// The reflect mission and disposition inherit like the other settings: space, then template.
func TestSpaceReflectGuidanceInherits(t *testing.T) {
	f, _ := reflectFixture(t)
	f.activeMemory("guidance needle", "needle")
	f.svc.SpaceTemplate = &SpaceTemplate{Config: SpaceConfig{ReflectMission: "Answer for the auditors.", ReflectDisposition: &ReflectDisposition{Skepticism: 4}}}
	view, err := f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA)
	if err != nil || view.Config.ReflectMission != "Answer for the auditors." || view.Sources["reflect_mission"] != spaceSourceTemplate || view.Sources["reflect_disposition"] != spaceSourceTemplate {
		t.Fatalf("effective = %+v %v %v", view.Config, view.Sources, err)
	}
	out, err := f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true})
	if err != nil || !strings.Contains(out.Prompt.System, "auditors") || !strings.Contains(out.Prompt.System, "skepticism 4/5") {
		t.Fatal("template guidance", out.Prompt, err)
	}
	mission := "Answer for the on-call engineer."
	putSettings(t, f, scopeA, SpaceConfigInput{ReflectMission: &mission})
	view, _ = f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA)
	out, err = f.svc.Reflect(testCtx, f.human, ReflectInput{Scope: scopeA, Query: "needle", PromptOnly: true})
	if view.Sources["reflect_mission"] != spaceSourceSpace || view.Sources["reflect_disposition"] != spaceSourceTemplate || err != nil ||
		!strings.Contains(out.Prompt.System, "on-call") || strings.Contains(out.Prompt.System, "auditors") || !strings.Contains(out.Prompt.System, "skepticism 4/5") {
		t.Fatal("space mission over template", view.Sources, out.Prompt, err)
	}
}

// Booleans have no empty value, so a PATCH sends null to hand lanes, one lane,
// rerank or rerank_model back to the template or server default.
func TestSpaceSettingsPatchNullInherits(t *testing.T) {
	f := newFixture(t)
	f.svc.SpaceTemplate = &SpaceTemplate{Config: SpaceConfig{Rerank: new(bool)}}
	patch := func(body string) (int, string) {
		req := httptest.NewRequest("PATCH", "/v1/spaces/ws-a/proj-a/config", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.human)
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if code, body := patch(`{"expected_revision":-1,"lanes":{"fts":false,"graph":false},"rerank":true,"rerank_model":true}`); code != 200 {
		t.Fatal(code, body)
	}
	view, _ := f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA)
	if view.Sources["rerank"] != spaceSourceSpace || !*view.Config.Rerank || view.Sources["lanes.fts"] != spaceSourceSpace {
		t.Fatalf("set: %+v %v", view.Config, view.Sources)
	}
	if code, body := patch(`{"expected_revision":-1,"lanes":{"fts":null},"rerank":null,"rerank_model":null}`); code != 200 {
		t.Fatal(code, body)
	}
	got, _ := f.svc.GetSpaceConfig(testCtx, f.human, scopeA)
	if c := got.Config; c.Rerank != nil || c.RerankModel != nil || c.Lanes == nil || c.Lanes.FTS != nil || c.Lanes.Graph == nil || *c.Lanes.Graph {
		t.Fatalf("after null: %+v lanes %+v", c, c.Lanes)
	}
	view, _ = f.svc.EffectiveSpaceConfig(testCtx, f.human, scopeA)
	if view.Sources["rerank"] != spaceSourceTemplate || *view.Config.Rerank || view.Sources["lanes.fts"] != spaceSourceServer || view.Sources["lanes.graph"] != spaceSourceSpace {
		t.Fatalf("inherits: %+v %v", view.Config, view.Sources)
	}
	// An absent key stays; null or {} hands every lane back.
	for _, body := range []string{`{"expected_revision":-1,"lanes":null}`, `{"expected_revision":-1,"lanes":{}}`} {
		patch(`{"expected_revision":-1,"lanes":{"vector":false}}`)
		if code, out := patch(body); code != 200 {
			t.Fatal(code, out)
		}
		if got, _ = f.svc.GetSpaceConfig(testCtx, f.human, scopeA); got.Config.Lanes != nil {
			t.Fatalf("%s left %+v", body, got.Config.Lanes)
		}
	}
	if code, _ := patch(`{"expected_revision":-1,"lanes":{"fts":null,"bogus":true}}`); code != 400 {
		t.Fatal("unknown lane accepted", code)
	}
	if code, _ := patch(`{"expected_revision":-1,"other":null}`); code != 400 {
		t.Fatal("unknown key accepted", code)
	}
}
