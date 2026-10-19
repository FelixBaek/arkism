package memory

import "testing"

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
