//go:build linux

package memory

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// spaceHistoryFixture: memories past one import chunk, an observation, and a model-call trace with text.
func spaceHistoryFixture(t *testing.T) (*fixture, Record) {
	f := newFixture(t)
	var mem Record
	for i := range spaceImportChunk + 3 {
		mem = f.activeMemory("fact number "+strconv.Itoa(i), "facts")
	}
	obs := mem
	obs.ID, obs.Kind, obs.Text, obs.Status = "obs-1", "observation", "the team ships on fridays", statusActive
	now := time.Now().UTC().Truncate(time.Millisecond)
	trace := Record{ID: timeOrderedID(now.UnixMilli()), Scope: scopeA, Kind: llmRequestKind, Status: "ok", Revision: 1, CreatedAt: now, UpdatedAt: now,
		LLMRequest: &LLMRequest{Operation: "reflect", PromptChars: 9, Prompt: "the prompt words", Response: "the answer words", Status: "ok"}}
	for col, r := range map[string]Record{colObservations: obs, colLLMRequests: trace} {
		if err := f.repo.Insert(testCtx, col, r); err != nil {
			t.Fatal(err)
		}
	}
	return f, mem
}

func bundleSkips(r SpaceReport) map[string]int {
	out := map[string]int{}
	for _, s := range r.Skipped {
		out[s.Section+":"+s.Reason] += s.Count
	}
	return out
}

func TestSpaceStreamBundleCarriesPartsAndHistoryWithoutText(t *testing.T) {
	f, _ := spaceHistoryFixture(t)
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{Parts: []string{"history", "data", colObservations}})
	lines := strings.Split(strings.TrimSpace(bundle), "\n")
	if !strings.Contains(lines[len(lines)-1], `"end":`) || !strings.Contains(bundle, "the team ships on fridays") ||
		!strings.Contains(bundle, `"llm_request":`) || !strings.Contains(bundle, `"audit":`) || strings.Contains(bundle, "the prompt words") {
		t.Fatalf("bundle parts wrong:\n%s", bundle)
	}
	m, _, err := parseSpaceBundle(bundle)
	if err != nil || m.FormatVersion != 2 || m.Sections[0].Name != colMemories || m.Sections[len(m.Sections)-1].Name != colLLMRequests {
		t.Fatalf("manifest = %+v %v", m, err)
	}
	// A cut transfer has no trailer; an appended line after it is refused too.
	cut := strings.Join(lines[:len(lines)-1], "\n") + "\n"
	for _, bad := range []string{cut, bundle + lines[1] + "\n"} {
		if _, _, err := parseSpaceBundle(bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("damaged bundle err = %v", err)
		}
	}
	// An agent downloads no history.
	if _, err := validAgentExport(f); err != nil {
		t.Fatal(err)
	}
}

func validAgentExport(f *fixture) (string, error) {
	job, err := f.svc.ExportSpace(testCtx, f.agent, scopeA, SpaceExportInput{Parts: []string{"history"}})
	if err != nil {
		return "", err
	}
	waitSpaceJob(f.t.(*testing.T), f, scopeA, job.ID)
	bundle, err := downloadBundle(f, f.agent, scopeA, job.ID)
	if err == nil && (strings.Contains(bundle, `"audit":`) || !strings.Contains(bundle, "not_human")) {
		return "", errors.New("agent got history: " + bundle)
	}
	return bundle, err
}

func TestSpaceStreamImportSpoolsVerifiesAndChunks(t *testing.T) {
	f, _ := spaceHistoryFixture(t)
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{Parts: []string{"data", colObservations, "history"}})
	if _, err := f.svc.ImportSpaceStream(testCtx, f.human, scopeB, SpaceImportInput{}, strings.NewReader(strings.Replace(bundle, "fact number 1", "fact number X", 1))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered stream err = %v", err)
	}
	job, err := f.svc.ImportSpaceStream(testCtx, f.human, scopeB, SpaceImportInput{}, strings.NewReader(bundle))
	if err != nil {
		t.Fatal(err)
	}
	report := *waitSpaceJob(t, f, scopeB, job.ID).Report
	skips := bundleSkips(report)
	// 35 memories and the observation arrive as candidates over two capture events; uploaded history is never written.
	if report.Imported != spaceImportChunk+4 || skips[colLLMRequests+":history_archive_only"] != 1 || skips["audit_events:history_archive_only"] == 0 {
		t.Fatalf("report = %+v", report)
	}
	cands := scopeRecords(f, "memory_candidates", scopeB)
	events := map[string]bool{}
	for _, c := range cands {
		for _, tag := range c.Tags {
			if strings.HasPrefix(tag, "event_id:space-import:") {
				events[tag] = true
			}
		}
	}
	if len(cands) != spaceImportChunk+4 || len(events) != 2 || len(reflectTraces(f, scopeB)) != 0 {
		t.Fatalf("candidates %d events %v", len(cands), events)
	}
	if _, err = f.svc.ImportSpaceStream(testCtx, f.human, scopeB, SpaceImportInput{Bundle: bundle}, strings.NewReader(bundle)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("stream with bundle field err = %v", err)
	}
}

func TestSpaceV1BundleStillImports(t *testing.T) {
	f := newFixture(t)
	rec := SpaceBundleRecord{ID: "m1", Kind: "semantic", Status: "active", Approved: true, Text: "old format fact", CreatedAt: time.Now().UTC()}
	body, _ := json.Marshal(spaceBundleLine{Section: colMemories, Record: &rec})
	b := string(body) + "\n"
	head, _ := json.Marshal(spaceBundleLine{Manifest: &SpaceManifest{Format: spaceBundleFormat, FormatVersion: 1, BundleID: "sb_v1", SHA256: ContentHash(b)}})
	report := importBundle(t, f, scopeB, string(head)+"\n"+b, "")
	if report.Imported != 1 {
		t.Fatalf("v1 report = %+v", report)
	}
}

func TestSpaceCloneHistoryPartIsHumanOnlyAndCopiesTraces(t *testing.T) {
	f, _ := spaceHistoryFixture(t)
	in := SpaceCloneInput{Target: scopeB, Parts: []string{spaceSectionConfig, "history"}}
	if _, err := f.svc.CloneSpace(testCtx, f.agent, scopeA, in); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent history clone err = %v", err)
	}
	if _, err := f.svc.CloneSpace(testCtx, f.human, scopeA, SpaceCloneInput{Target: scopeB, Parts: []string{"bogus"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad part err = %v", err)
	}
	job, err := f.svc.CloneSpace(testCtx, f.human, scopeA, in)
	if err != nil {
		t.Fatal(err)
	}
	done := waitSpaceJob(t, f, scopeA, job.ID)
	traces := reflectTraces(f, scopeB)
	if len(traces) != 1 || traces[0].LLMRequest.Operation != "reflect" || traces[0].LLMRequest.Prompt != "" || len(scopeRecords(f, colMemories, scopeB)) != 0 {
		t.Fatalf("cloned traces %+v report %+v", traces, done.Report)
	}
	cloned := 0
	for _, a := range scopeRecords(f, "audit_events", scopeB) {
		if strings.HasPrefix(a.Kind, "cloned:") {
			cloned++
		}
	}
	if cloned == 0 || done.Manifest.Sections[len(done.Manifest.Sections)-1].Count != 1 {
		t.Fatalf("cloned audit rows %d manifest %+v", cloned, done.Manifest)
	}
}

func TestSpaceImportAsTemplateKeepsOnlySettingsAndDefinitions(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("not part of a template", "x")
	notes := "shared setup"
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{Notes: &notes}, true); err != nil {
		t.Fatal(err)
	}
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{})
	job, err := f.svc.ImportSpace(testCtx, f.human, scopeB, SpaceImportInput{Bundle: bundle, AsTemplate: true})
	if err != nil {
		t.Fatal(err)
	}
	report := *waitSpaceJob(t, f, scopeB, job.ID).Report
	if report.Config != "applied" || bundleSkips(report)[colMemories+":template_only"] != 1 || len(scopeRecords(f, "memory_candidates", scopeB)) != 0 {
		t.Fatalf("template report = %+v", report)
	}
	if _, err = f.svc.ImportSpace(testCtx, f.human, scopeB, SpaceImportInput{Bundle: bundle, AsTemplate: true}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second template err = %v", err)
	}
}

func TestSpaceStreamHTTPRoutes(t *testing.T) {
	f, _ := spaceHistoryFixture(t)
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{})
	req := httptest.NewRequest("POST", "/v1/spaces/ws-a/proj-b/import?import_as=candidate", strings.NewReader(bundle))
	req.Header.Set("Authorization", "Bearer "+f.human)
	req.Header.Set("Content-Type", "application/x-ndjson")
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	var job SpaceJobView
	if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &job) != nil || job.Kind != spaceJobImport {
		t.Fatalf("ndjson import = %d %s", w.Code, w.Body.String())
	}
	waitSpaceJob(t, f, scopeB, job.ID)
	exp, err := f.svc.ExportSpace(testCtx, f.human, scopeA, SpaceExportInput{})
	if err != nil {
		t.Fatal(err)
	}
	waitSpaceJob(t, f, scopeA, exp.ID)
	req = httptest.NewRequest("GET", "/v1/spaces/exports/"+exp.ID+"/download?workspace_id=ws-a&project_id=proj-a", nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	w = httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/x-ndjson" || !strings.Contains(w.Body.String(), `"end":`) {
		t.Fatalf("download = %d %s", w.Code, w.Body.String())
	}
}

// reflectTraces leaves out the embed traces the import's own capture writes.
func reflectTraces(f *fixture, scope Scope) []Record {
	out := []Record{}
	for _, r := range scopeRecords(f, colLLMRequests, scope) {
		if r.LLMRequest != nil && r.LLMRequest.Operation == "reflect" {
			out = append(out, r)
		}
	}
	return out
}

func TestRepairSpacePreviewsThenStalesOrphansAndClearsLegacyBundles(t *testing.T) {
	f, llm, ms := obsFixture(t)
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID, ms[2].ID))}
	out, err := f.consolidate(f.human, false)
	if err != nil || len(out.Proposed) != 1 {
		t.Fatal(err)
	}
	// A source erased behind the cascade's back (an older release) leaves an orphan.
	gone, _ := f.repo.Get(testCtx, colMemories, scopeA, ms[0].ID)
	gone.ContentErased = true
	legacy := Record{ID: "spj_old", Scope: scopeA, Kind: spaceJobExport, Status: JobStatusDone, Revision: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		SpaceJob: &SpaceJob{Kind: spaceJobExport, State: "done", Bundle: "old bundle text"}}
	if err = f.repo.Replace(testCtx, colMemories, gone, gone.Revision); err != nil {
		t.Fatal(err)
	}
	if err = f.repo.Insert(testCtx, colJobs, legacy); err != nil {
		t.Fatal(err)
	}
	preview, err := f.svc.RepairSpace(testCtx, scopeA, false)
	obs, _ := f.repo.Get(testCtx, colObservations, scopeA, out.Proposed[0].ID)
	job, _ := f.repo.Get(testCtx, colJobs, scopeA, legacy.ID)
	if err != nil || preview.StaleObservations != 1 || preview.LegacyBundles != 1 || obs.Status != obsProposed || job.SpaceJob.Bundle == "" {
		t.Fatalf("preview %+v %v changed something: %s %q", preview, err, obs.Status, job.SpaceJob.Bundle)
	}
	done, err := f.svc.RepairSpace(testCtx, scopeA, true)
	obs, _ = f.repo.Get(testCtx, colObservations, scopeA, out.Proposed[0].ID)
	job, _ = f.repo.Get(testCtx, colJobs, scopeA, legacy.ID)
	if err != nil || done.StaleObservations != 1 || obs.Status != obsStale || job.SpaceJob.Bundle != "" {
		t.Fatalf("execute %+v %v: %s %q", done, err, obs.Status, job.SpaceJob.Bundle)
	}
	if again, _ := f.svc.RepairSpace(testCtx, scopeA, false); again.StaleObservations != 0 || again.LegacyBundles != 0 {
		t.Fatalf("repair not idempotent: %+v", again)
	}
	if _, err = f.svc.RepairSpace(testCtx, Scope{WorkspaceID: "ws-a"}, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad scope err = %v", err)
	}
}
