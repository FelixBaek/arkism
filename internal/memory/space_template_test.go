package memory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"
)

// spaceTemplateJob waits for the one import job the auto apply started in scope.
func spaceTemplateJob(t *testing.T, f *fixture, scope Scope) SpaceReport {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		for _, r := range scopeRecords(f, colJobs, scope) {
			if r.SpaceJob != nil && r.SpaceJob.Kind == spaceJobImport {
				return *waitSpaceJob(t, f, scope, r.ID).Report
			}
		}
	}
	t.Fatal("no template job started")
	return SpaceReport{}
}

func TestSpaceTemplateAutoApply(t *testing.T) {
	f := newFixture(t)
	in := directiveInput("Units", "Always use metric units.", "both", 7)
	in.Scope = scopeA
	if _, err := f.svc.CreateDirective(testCtx, f.human, in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CreateMentalModel(testCtx, f.human, MentalModelInput{Scope: scopeA, Title: "Units", Query: "units", Refresh: MentalModelRefresh{Mode: mmRefreshDaily, AfterConsolidation: true}}); err != nil {
		t.Fatal(err)
	}
	putSettings(t, f, scopeA, SpaceConfigInput{ExtractionMode: ptr(spaceExtractVerbose)})
	path := t.TempDir() + "/template.jsonl"
	if err := os.WriteFile(path, []byte(exportBundle(t, f, scopeA, SpaceExportInput{Template: true})), 0o600); err != nil {
		t.Fatal(err)
	}
	tpl, err := LoadSpaceTemplate(path)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.SpaceTemplate = tpl
	// Off by default: a first config changes nothing else.
	putSettings(t, f, scopeB, SpaceConfigInput{DisplayName: ptr("B")})
	if len(scopeRecords(f, colDirectives, scopeB)) != 0 {
		t.Fatal("template applied without the opt-in")
	}
	tpl.Auto = true
	// An agent's capture never applies human-written definitions.
	scopeC, scopeD := Scope{WorkspaceID: "ws-a", ProjectID: "proj-c"}, Scope{WorkspaceID: "ws-a", ProjectID: "proj-d"}
	f.capture(f.agent, captureInput(scopeC, "evt-agent", explicit("the team prefers metric units")))
	if slices.ContainsFunc(scopeRecords(f, colJobs, scopeC), func(r Record) bool { return r.SpaceJob != nil }) {
		t.Fatal("agent capture started a template job")
	}
	// A person's first capture into a space without settings: settings and definitions.
	f.capture(f.human, captureInput(scopeC, "evt-human", explicit("the team prefers metric units always")))
	if report := spaceTemplateJob(t, f, scopeC); report.Config != "applied" || report.Imported != 2 {
		t.Fatalf("capture apply report = %+v", report)
	}
	// No person chose the definitions for this space: they arrive switched off.
	for _, r := range scopeRecords(f, colDirectives, scopeC) {
		if r.Status == "active" {
			t.Fatalf("auto-applied directive is active: %+v", r)
		}
	}
	for _, r := range scopeRecords(f, colMentalModels, scopeC) {
		if m := r.MentalModel.Refresh; m.Mode != mmRefreshManual || m.AfterConsolidation || m.Cron != "" {
			t.Fatalf("auto-applied mental model refreshes on its own: %+v", m)
		}
	}
	if cfg := f.svc.mustSpace(t, scopeC); cfg.ExtractionMode != spaceExtractVerbose {
		t.Fatalf("template settings not stored: %+v", cfg)
	}
	f.capture(f.human, captureInput(scopeC, "evt-human-2", explicit("units are metric in every report")))
	if n := len(scopeRecords(f, colDirectives, scopeC)); n != 1 {
		t.Fatalf("template applied twice: %d directives", n)
	}
	// A person's first settings win; only the definitions are added.
	putSettings(t, f, scopeD, SpaceConfigInput{DisplayName: ptr("D")})
	if report := spaceTemplateJob(t, f, scopeD); report.Config != "" || report.Imported != 2 {
		t.Fatalf("config apply report = %+v", report)
	}
	if cfg := f.svc.mustSpace(t, scopeD); cfg.DisplayName != "D" || cfg.ExtractionMode != "" {
		t.Fatalf("person's settings replaced: %+v", cfg)
	}
}

func (s *Service) mustSpace(t *testing.T, scope Scope) SpaceConfig {
	t.Helper()
	r, err := s.Repo.Get(testCtx, colSpaceConfigs, scope, spaceConfigID)
	if err != nil || r.SpaceConfig == nil {
		t.Fatalf("space config: %v", err)
	}
	return *r.SpaceConfig
}

func TestSpaceTemplateSchemaRoute(t *testing.T) {
	f := newFixture(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/spaces/template-schema?workspace_id=ws-a&project_id=proj-a", nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	handlerMode(f.svc, time.Minute, true).ServeHTTP(rec, req)
	var got map[string]any
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	props, _ := got["properties"].(map[string]any)
	record, _ := props["record"].(map[string]any)
	fields, _ := record["properties"].(map[string]any)
	if got["x-format"] != spaceBundleFormat || props["manifest"] == nil || props["config"] == nil || fields["mental_model"] == nil || fields["directive"] == nil {
		t.Fatalf("schema = %v", got)
	}
	created, _ := fields["created_at"].(map[string]any)
	if created["format"] != "date-time" || slices.Contains(record["required"].([]any), any("summary")) {
		t.Fatalf("record schema = %v", record)
	}
}
