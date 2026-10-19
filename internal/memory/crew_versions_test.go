package memory

import (
	"errors"
	"fmt"
	"testing"
)

func TestCrewVersionsKeepEarlierDefinitions(t *testing.T) {
	f := newFixture(t)
	w := f.crew(crewWorkflow("리서치", "리서치"))
	c := *w.Crew
	c.Model = "opus 5.5"
	if _, err := f.svc.UpdateCrew(testCtx, f.human, w.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: 1, Crew: &c}); err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.CrewVersions(testCtx, f.agent, scopeA, w.ID)
	if err != nil || len(page.Items) != 2 || page.Items[0].Revision != 2 || page.Items[0].Crew.Model != "opus 5.5" ||
		page.Items[1].Revision != 1 || page.Items[1].Crew.Model != "deepseek-v4-flash" {
		t.Fatal("versions", page.Items, err)
	}
	if _, err := f.svc.UpdateCrew(testCtx, f.human, w.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: 2, Archive: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.CrewVersions(testCtx, f.human, scopeA, w.ID); err == nil {
		t.Fatal("an archived item still shows versions")
	}
}

func TestCrewVersionsKeepOnlyTheWindow(t *testing.T) {
	f := newFixture(t)
	w := f.crew(crewWorkflow("리서치", "리서치"))
	for rev := int64(1); rev <= 25; rev++ {
		c := *w.Crew
		c.Role = fmt.Sprintf("판 %d", rev+1)
		up, err := f.svc.UpdateCrew(testCtx, f.human, w.ID, CrewUpdate{Scope: scopeA, ExpectedRevision: rev, Crew: &c})
		if err != nil {
			t.Fatal(rev, err)
		}
		w = up
	}
	page, err := f.svc.CrewVersions(testCtx, f.human, scopeA, w.ID)
	if err != nil || len(page.Items) != crewVersionsMax || page.Items[0].Revision != 26 || page.Items[crewVersionsMax-1].Revision != 7 {
		t.Fatal("window", len(page.Items), err)
	}
	if _, err := f.svc.Repo.Get(testCtx, colCrewVersions, scopeA, crewVersionID(w.ID, 6)); !errors.Is(err, ErrNotFound) {
		t.Fatal("v6 should be pruned", err)
	}
	if _, err := f.svc.Repo.Get(testCtx, colCrewVersions, scopeA, crewVersionID(w.ID, 7)); err != nil {
		t.Fatal("v7 should stay", err)
	}
}
