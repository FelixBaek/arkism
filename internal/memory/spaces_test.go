package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func (f *fakeRepo) SpaceCounts(_ context.Context, workspace, after string, collections []string, limit int) ([]SpaceCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	counts := map[string]map[string]int64{}
	for _, col := range collections {
		for _, r := range f.store[col] {
			s := r.Scope
			if s.WorkspaceID != workspace || s.UserID != "" || s.AgentID != "" || s.ProjectID <= after {
				continue
			}
			if counts[s.ProjectID] == nil {
				counts[s.ProjectID] = map[string]int64{}
			}
			counts[s.ProjectID][col]++
		}
	}
	projects := make([]string, 0, len(counts))
	for p := range counts {
		projects = append(projects, p)
	}
	slices.Sort(projects)
	out := []SpaceCount{}
	for _, p := range projects[:min(limit, len(projects))] {
		for col, n := range counts[p] {
			out = append(out, SpaceCount{ProjectID: p, Collection: col, Count: n})
		}
	}
	return out, nil
}

func (f *fakeRepo) SpaceAliasOwners(_ context.Context, workspace, alias string) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Record{}
	for _, r := range f.store[colSpaceConfigs] {
		if r.Scope.WorkspaceID == workspace && r.SpaceConfig != nil && slices.Contains(r.SpaceConfig.Aliases, alias) {
			out = append(out, r)
		}
	}
	return out, nil
}

func waitSpaceJob(t *testing.T, f *fixture, scope Scope, id string) SpaceJobView {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		job, err := f.svc.GetSpaceJob(testCtx, f.human, scope, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State != "queued" {
			if job.State != "done" {
				t.Fatalf("job %s failed: %s", id, job.Error)
			}
			return job
		}
	}
	t.Fatal("space job did not finish")
	return SpaceJobView{}
}

func exportBundle(t *testing.T, f *fixture, scope Scope, in SpaceExportInput) string {
	t.Helper()
	job, err := f.svc.ExportSpace(testCtx, f.human, scope, in)
	if err != nil {
		t.Fatal(err)
	}
	waitSpaceJob(t, f, scope, job.ID)
	bundle, err := downloadBundle(f, f.human, scope, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func downloadBundle(f *fixture, token string, scope Scope, id string) (string, error) {
	stream, err := f.svc.DownloadSpaceExport(testCtx, token, scope, id)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	err = stream(&out)
	return out.String(), err
}

func importBundle(t *testing.T, f *fixture, scope Scope, bundle, as string) SpaceReport {
	t.Helper()
	job, err := f.svc.ImportSpace(testCtx, f.human, scope, SpaceImportInput{Bundle: bundle, ImportAs: as})
	if err != nil {
		t.Fatal(err)
	}
	return *waitSpaceJob(t, f, scope, job.ID).Report
}

func scopeRecords(f *fixture, col string, scope Scope) []Record {
	rows, _ := f.repo.ListRecords(testCtx, scope, col, 1000)
	return rows
}

func TestSpaceBundleRoundTripKeepsProvenanceAsCandidates(t *testing.T) {
	f := newFixture(t)
	mem := f.activeMemory("prefer tabs over spaces", "style")
	notes := "team notes"
	if _, err := f.svc.PutSpaceConfig(testCtx, f.human, scopeA, SpaceConfigInput{Notes: &notes}, true); err != nil {
		t.Fatal(err)
	}
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{})
	m, lines, err := parseSpaceBundle(bundle)
	if err != nil || len(lines) == 0 || m.Sections[0].Name != spaceSectionConfig {
		t.Fatalf("bundle manifest %+v lines %d err %v", m, len(lines), err)
	}
	report := importBundle(t, f, scopeB, bundle, "")
	if report.Imported != 1 || report.Approved != 0 || report.Config != "applied" {
		t.Fatalf("report = %+v", report)
	}
	cands := scopeRecords(f, "memory_candidates", scopeB)
	if len(cands) != 1 || cands[0].Status != "candidate" || cands[0].Text != mem.Text {
		t.Fatalf("imported candidates = %+v", cands)
	}
	if !strings.Contains(cands[0].Summary, m.BundleID) || !strings.Contains(cands[0].Summary, "memories/"+mem.ID) || !slices.Contains(cands[0].Tags, "event_id:space-import:"+m.BundleID+":0") {
		t.Fatalf("provenance lost: summary %q tags %v", cands[0].Summary, cands[0].Tags)
	}
	if len(scopeRecords(f, colMemories, scopeB)) != 0 {
		t.Fatal("import approved without a person")
	}
	// Re-import is idempotent: the same bundle is the same capture events.
	again := importBundle(t, f, scopeB, bundle, "")
	if again.Imported != 0 || again.Merged != 1 || len(scopeRecords(f, "memory_candidates", scopeB)) != 1 {
		t.Fatalf("re-import = %+v", again)
	}
}

func TestSpaceImportApprovedOnlyForHumansAndApprovedRecords(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("deploy on fridays is forbidden", "deploy")
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{})
	if _, err := f.svc.ImportSpace(testCtx, f.agent, scopeB, SpaceImportInput{Bundle: bundle, ImportAs: "approved"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent import err = %v, want denied", err)
	}
	if _, err := f.svc.CloneSpace(testCtx, f.agent, scopeA, SpaceCloneInput{Target: scopeB, ImportAs: "approved"}); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent approved clone err = %v, want denied", err)
	}
	report := importBundle(t, f, scopeB, bundle, "approved")
	if report.Approved != 1 {
		t.Fatalf("report = %+v", report)
	}
	mems := scopeRecords(f, colMemories, scopeB)
	if len(mems) != 1 || mems[0].Status != statusActive || mems[0].Review == nil || mems[0].Review.Action != "approve" || mems[0].Review.By != "human-1" {
		t.Fatalf("approved import must go through the candidate review: %+v", mems)
	}
}

func TestSpaceCloneByAgentArrivesAsCandidate(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("use the staging database for tests", "db")
	job, err := f.svc.CloneSpace(testCtx, f.agent, scopeA, SpaceCloneInput{Target: scopeB})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitSpaceJob(t, f, scopeA, job.ID); got.Report.Imported != 1 {
		t.Fatalf("clone report = %+v", got.Report)
	}
	if len(scopeRecords(f, colMemories, scopeB)) != 0 || len(scopeRecords(f, "memory_candidates", scopeB)) != 1 {
		t.Fatal("clone did not arrive as one candidate")
	}
}

func TestSpaceExportLeavesOutErasedAndPrivate(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("keep commits small", "git")
	erased := f.activeMemory("erased fact", "x")
	erased.ContentErased = true
	private := f.activeMemory("private fact", "y")
	private.Sensitivity = "private"
	for _, r := range []Record{erased, private} {
		if err := f.repo.Replace(testCtx, colMemories, r, r.Revision); err != nil {
			t.Fatal(err)
		}
	}
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{Include: []string{colMemories, "observations", "nonsense"}})
	if strings.Contains(bundle, "erased fact") || strings.Contains(bundle, "private fact") || !strings.Contains(bundle, "keep commits small") {
		t.Fatal("bundle content wrong:", bundle)
	}
	m, _, _ := parseSpaceBundle(bundle)
	reasons := []string{}
	for _, s := range m.Skipped {
		reasons = append(reasons, s.Section+":"+s.Reason)
	}
	for _, want := range []string{"memories:erased", "memories:privacy_excluded", "nonsense:not_exportable"} {
		if !slices.Contains(reasons, want) {
			t.Fatalf("skipped %v lacks %s", reasons, want)
		}
	}
}

func TestSpaceDownloadStreamsFreshAfterErasure(t *testing.T) {
	f := newFixture(t)
	mem := f.activeMemory("short lived", "z")
	job, err := f.svc.ExportSpace(testCtx, f.human, scopeA, SpaceExportInput{Include: []string{colMemories}})
	if err != nil {
		t.Fatal(err)
	}
	if done := waitSpaceJob(t, f, scopeA, job.ID); done.Manifest.Sections[0].Count != 1 {
		t.Fatalf("export counted %+v", done.Manifest)
	}
	mem.ContentErased = true
	if err = f.repo.Replace(testCtx, colMemories, mem, mem.Revision); err != nil {
		t.Fatal(err)
	}
	bundle, err := downloadBundle(f, f.human, scopeA, job.ID)
	if err != nil || strings.Contains(bundle, "short lived") {
		t.Fatalf("download after erasure = %v %q", err, bundle)
	}
	if m, _, err := parseSpaceBundle(bundle); err != nil || m.Sections[0].Count != 0 || m.Skipped[0].Reason != "erased" {
		t.Fatalf("fresh manifest = %+v %v", m, err)
	}
}

func TestSpaceImportRejectsTamperedBundle(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("original wording", "w")
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{})
	tampered := strings.Replace(bundle, "original wording", "altered wording", 1)
	if _, err := f.svc.ImportSpace(testCtx, f.human, scopeB, SpaceImportInput{Bundle: tampered}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered import err = %v", err)
	}
}

func TestSpaceAliasResolution(t *testing.T) {
	f := newFixture(t)
	if _, err := f.svc.SetSpaceAlias(testCtx, f.agent, scopeA, "front", true); !errors.Is(err, ErrDenied) {
		t.Fatalf("agent alias err = %v", err)
	}
	if _, err := f.svc.SetSpaceAlias(testCtx, f.human, scopeA, "front", true); err != nil {
		t.Fatal(err)
	}
	alias := Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: "@front"}
	if got, err := f.svc.resolveSpace(testCtx, alias); err != nil || got != scopeA {
		t.Fatalf("resolve = %+v %v", got, err)
	}
	if _, err := f.svc.SetSpaceAlias(testCtx, f.human, scopeB, "front", true); !errors.Is(err, ErrHandleTaken) {
		t.Fatalf("duplicate alias err = %v", err)
	}
	req := httptest.NewRequest("GET", "/v1/spaces/ws-a/@front/config", nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"project_id":"proj-a"`) {
		t.Fatalf("alias route = %d %s", w.Code, w.Body.String())
	}
	page, err := f.svc.ListSpaces(testCtx, f.human, scopeA, "", 10)
	if err != nil || len(page.Items) != 1 || !slices.Contains(page.Items[0].Config.Aliases, "front") {
		t.Fatalf("list = %+v %v", page, err)
	}
	if _, err = f.svc.SetSpaceAlias(testCtx, f.human, scopeA, "front", false); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.resolveSpace(testCtx, alias); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed alias resolve err = %v", err)
	}
}

// transferAuth grants external transfer only for an external destination, as
// the Postgres authorizer does (external:transfer is checked only then).
type transferAuth struct{ inner *fakeAuth }

func (a transferAuth) Authorize(ctx context.Context, token string, req Authorization) (Decision, error) {
	d, err := a.inner.Authorize(ctx, token, req)
	d.ExternalTransfer = d.ExternalTransfer && strings.HasPrefix(req.Context.Destination, "external:")
	return d, err
}

func TestSpaceExportNeedsExternalTransferGrant(t *testing.T) {
	f := newFixture(t)
	f.svc.Auth = transferAuth{f.auth}
	f.activeMemory("ship small changes", "flow")
	if bundle := exportBundle(t, f, scopeA, SpaceExportInput{}); !strings.Contains(bundle, "ship small changes") {
		t.Fatal("export lost the memory")
	}
	local := testPrincipalToken("tok-local")
	f.auth.actors[local] = actor("human-3", "human", false, allVerbs...)
	if _, err := f.svc.ExportSpace(testCtx, local, scopeA, SpaceExportInput{}); !errors.Is(err, ErrDenied) {
		t.Fatalf("export without external transfer err = %v", err)
	}
}

func TestSpaceLegacyBundleIsDroppedAndDeadJobReported(t *testing.T) {
	f := newFixture(t)
	f.activeMemory("temporary rule", "t")
	job, err := f.svc.ExportSpace(testCtx, f.human, scopeA, SpaceExportInput{Include: []string{colMemories}})
	if err != nil {
		t.Fatal(err)
	}
	waitSpaceJob(t, f, scopeA, job.ID)
	row, _ := f.repo.Get(testCtx, colJobs, scopeA, job.ID)
	legacy := *row.SpaceJob
	legacy.Bundle = "old bundle text"
	row.SpaceJob = &legacy
	if err = f.repo.Replace(testCtx, colJobs, row, row.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = downloadBundle(f, f.human, scopeA, job.ID); err != nil {
		t.Fatal(err)
	}
	if row, _ = f.repo.Get(testCtx, colJobs, scopeA, job.ID); row.SpaceJob.Bundle != "" {
		t.Fatal("legacy bundle text stayed on the job row")
	}
	old := time.Now().Add(-time.Hour)
	dead := Record{ID: "spj_dead", Scope: scopeA, Kind: spaceJobExport, Status: JobStatusDone, Revision: 1, CreatedAt: old, UpdatedAt: old, SpaceJob: &SpaceJob{Kind: spaceJobExport, State: "queued"}}
	if err = f.repo.Insert(testCtx, colJobs, dead); err != nil {
		t.Fatal(err)
	}
	if got, err := f.svc.GetSpaceJob(testCtx, f.human, scopeA, dead.ID); err != nil || got.State != "failed" || got.Error != "interrupted" {
		t.Fatalf("dead job = %+v %v", got, err)
	}
}

// SpaceAliasOwners queries doc->'space_config'->'aliases' ? alias: the stored
// form must keep aliases as a plain JSON string array under those bson names.
func TestSpaceAliasStoredShapeMatchesSQL(t *testing.T) {
	cfg := defaultSpaceConfig()
	cfg.Aliases = []string{"front"}
	b, err := storeMarshal(Record{ID: spaceConfigID, Scope: scopeA, SpaceConfig: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SpaceConfig struct {
			Aliases []string `json:"aliases"`
		} `json:"space_config"`
	}
	if err = json.Unmarshal(b, &doc); err != nil || !slices.Equal(doc.SpaceConfig.Aliases, []string{"front"}) {
		t.Fatalf("stored doc %s: %v", b, err)
	}
}

func TestSpaceTemplateCarriesDefinitionsForHumansOnly(t *testing.T) {
	f := newFixture(t)
	in := directiveInput("Units", "Always use metric units.", "both", 7)
	in.Scope = scopeA
	if _, err := f.svc.CreateDirective(testCtx, f.human, in); err != nil {
		t.Fatal(err)
	}
	mm := MentalModelInput{Scope: scopeA, Title: "Deploy habits", Query: "how do we deploy", Refresh: MentalModelRefresh{Mode: "manual"}}
	if _, err := f.svc.CreateMentalModel(testCtx, f.human, mm); err != nil {
		t.Fatal(err)
	}
	bundle := exportBundle(t, f, scopeA, SpaceExportInput{Template: true})
	// An agent may clone a space, but a directive is a human rule and stays behind.
	job, err := f.svc.CloneSpace(testCtx, f.agent, scopeA, SpaceCloneInput{Target: scopeB, Include: []string{colDirectives}})
	if err != nil {
		t.Fatal(err)
	}
	if got := waitSpaceJob(t, f, scopeA, job.ID).Report; got.Imported != 0 || len(scopeRecords(f, colDirectives, scopeB)) != 0 {
		t.Fatalf("agent clone wrote a directive: %+v", got)
	}
	if report := importBundle(t, f, scopeB, bundle, ""); report.Imported != 2 {
		t.Fatalf("human import = %+v", report)
	}
	got := scopeRecords(f, colDirectives, scopeB)
	if len(got) != 1 || got[0].Text != in.Rule || got[0].Directive == nil || got[0].Directive.Priority != 7 || got[0].Directive.AppliesTo != "both" || got[0].Status != "active" {
		t.Fatalf("imported directive = %+v", got)
	}
	models := scopeRecords(f, colMentalModels, scopeB)
	if len(models) != 1 || models[0].MentalModel == nil || models[0].MentalModel.Query != mm.Query || models[0].MentalModel.Active != nil {
		t.Fatalf("imported mental model = %+v", models)
	}
}
