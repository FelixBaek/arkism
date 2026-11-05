package memory

import (
	"context"
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
	bundle, err := f.svc.DownloadSpaceExport(testCtx, f.human, scope, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	return bundle
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
	if len(mems) != 1 || mems[0].Status != "approved" {
		t.Fatalf("approved import = %+v", mems)
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
	for _, want := range []string{"memories:erased", "memories:privacy_excluded", "observations:unknown_collection", "nonsense:not_exportable"} {
		if !slices.Contains(reasons, want) {
			t.Fatalf("skipped %v lacks %s", reasons, want)
		}
	}
}

func TestSpaceDownloadRefusesBundleStaleAfterErasure(t *testing.T) {
	f := newFixture(t)
	mem := f.activeMemory("short lived", "z")
	job, err := f.svc.ExportSpace(testCtx, f.human, scopeA, SpaceExportInput{Include: []string{colMemories}})
	if err != nil {
		t.Fatal(err)
	}
	waitSpaceJob(t, f, scopeA, job.ID)
	mem.ContentErased = true
	if err = f.repo.Replace(testCtx, colMemories, mem, mem.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DownloadSpaceExport(testCtx, f.human, scopeA, job.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale download err = %v", err)
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
