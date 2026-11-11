package memory

import (
	"strings"
	"testing"
	"time"
)

func TestSearchConceptsKeepUnrelatedPrefixesSeparate(t *testing.T) {
	for _, pair := range [][2]string{{"javascript", "java"}, {"production", "pr"}, {"user", "us"}, {"aim", "ai"}, {"planet", "plan"}} {
		for _, terms := range [][]string{{pair[0], pair[1]}, {pair[1], pair[0]}} {
			if got := searchQueryConcepts(terms); len(got) != 2 {
				t.Errorf("unrelated terms %v became %v", terms, got)
			}
		}
	}
	if got := searchQueryConcepts([]string{"deploys", "deploy"}); len(got) != 1 || !strings.Contains(got[0], "'deploy'") || !strings.Contains(got[0], "'deploys'") {
		t.Fatal("plural must remain one concept", got)
	}
}

func TestDreamRunListKeepsCreationOrderAfterUndo(t *testing.T) {
	f := newFixture(t)
	at := time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC)
	for _, r := range []Record{
		{ID: "old-undone", CreatedAt: at, UpdatedAt: at.Add(2 * time.Hour)},
		{ID: "new", CreatedAt: at.Add(time.Hour), UpdatedAt: at.Add(time.Hour)},
	} {
		r.Scope, r.Kind, r.Status, r.Revision = scopeA, dreamJobKind, dreamRunStatus, 1
		r.DreamRun = &DreamRun{StartedAt: r.CreatedAt}
		if err := f.svc.Repo.Insert(testCtx, colJobs, r); err != nil {
			t.Fatal(err)
		}
	}
	page, err := f.svc.ListDreamRuns(testCtx, f.human, scopeA, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "new" {
		t.Fatal("undo update displaced the newer run", page, err)
	}
}

func TestDreamDetectsMaintenanceArrivingDuringRun(t *testing.T) {
	f := newFixture(t)
	at := time.Date(2026, 11, 11, 1, 0, 0, 0, time.UTC)
	r := Record{ID: "new-pair", Scope: scopeA, Kind: "maintenance", Status: maintenanceOpen, Revision: 1, CreatedAt: at.Add(time.Second), UpdatedAt: at.Add(time.Second)}
	if err := f.svc.Repo.Insert(testCtx, colMaintenance, r); err != nil {
		t.Fatal(err)
	}
	if !f.svc.newCandidateSince(testCtx, scopeA, at) {
		t.Fatal("open maintenance arriving during a run was not scheduled")
	}
	if f.svc.newCandidateSince(testCtx, scopeA, at.Add(2*time.Second)) {
		t.Fatal("old work incorrectly treated as new activity")
	}
}
