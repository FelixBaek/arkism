package memory

import (
	"context"
	"testing"
	"time"
)

func (f *fakeRepo) ListSkillRecords(_ context.Context, scope Scope, limit int, after string) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Record
	for _, row := range f.rowsLocked(colSkills) {
		if row.Scope == scope && row.Status == statusActive && row.ActiveVersion != "" && row.ID > after {
			out = append(out, row)
			if len(out) == limit+1 {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeRepo) ListDeprecatedSkillRecords(_ context.Context, scope Scope, limit int, after string) ([]Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Record
	for _, row := range f.rowsLocked(colSkills) {
		if row.Scope == scope && row.Status == skillStatusDeprecated && row.ActiveVersion != "" && row.ID > after && len(out) <= limit {
			out = append(out, row)
		}
	}
	return out, nil
}

func TestSkillListCursorReadsEveryPage(t *testing.T) { checkSkillListPages(t, newFixture(t), nil) }
func TestPostgresSkillListCursorReadsEveryPage(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkSkillListPages(t, f, f.svc.Repo)
}
func checkSkillListPages(t *testing.T, f *fixture, store Repository) {
	t.Helper()
	if store == nil {
		store = f.repo
	}
	now := time.Now().UTC()
	for _, id := range []string{"alpha", "beta", "broken", "gamma", "retired", "held"} {
		source := Record{ID: "source-" + id, Scope: scopeA, Kind: "skill_file", Status: statusActive, Revision: 1, Text: id, CreatedAt: now, UpdatedAt: now}
		versionID := "version-" + id
		analysis := &SkillAnalysis{SkillID: id, Version: "1", ContentHash: ContentHash(id), Name: id, Description: id, Problem: id, UseWhen: []string{id}, InputFormat: id, OutputFormat: id, SuccessCriteria: []string{id}, Verification: []string{id}, SourcePath: "/skills/" + id + "/SKILL.md", SourceFile: "SKILL.md"}
		version := Record{ID: versionID, Scope: scopeA, Kind: importedSkillBaselineKind, Status: statusActive, Revision: 1, SkillID: id, Analysis: analysis, Sources: refs(source), CreatedAt: now, UpdatedAt: now, CreatedByType: roleHuman, ApprovedBy: "human-1"}
		parent := Record{ID: id, Scope: scopeA, Kind: "skill", Status: statusActive, Revision: 1, ActiveVersion: versionID, Sources: refs(source), CreatedAt: now, UpdatedAt: now}
		if id == "broken" { // legacy rollback: re-activated baseline without a stored validation
			version.Revision = 3
		}
		if id == "retired" || id == "held" {
			parent.Status = skillStatusDeprecated
		}
		if id == "held" { // waiting for erasure: never listed
			source.PrivacyQuarantined = true
		}
		for collection, record := range map[string]Record{"sources": source, colSkillVersions: version, colSkills: parent} {
			if err := store.Insert(testCtx, collection, record); err != nil {
				t.Fatal(err)
			}
		}
	}

	first, err := f.svc.ListSkills(testCtx, f.human, scopeA, 2, "")
	if err != nil || len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatal("first page", err, first)
	}
	after, err := decodeSkillCursor(scopeA, first.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.svc.ListSkills(testCtx, f.human, scopeA, 2, after)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != "gamma" || second.NextCursor != "" || len(second.Withheld) != 1 || second.Withheld[0] != "broken" {
		t.Fatal("second page", err, second)
	}
	if _, err = decodeSkillCursor(scopeB, first.NextCursor); err == nil {
		t.Fatal("cursor was not bound to its scope")
	}
	trash, err := f.svc.ListTrashedSkills(testCtx, f.human, scopeA, 500, "")
	if err != nil || len(trash.Items) != 1 || trash.Items[0].Name != "retired" {
		t.Fatal("trash", err, trash)
	}
	// One per page: the held skill fills page one (and is left out), the cursor reaches the retired one.
	page, err := f.svc.ListTrashedSkills(testCtx, f.human, scopeA, 1, "")
	if err != nil || page.NextCursor == "" {
		t.Fatal("first trash page", err, page)
	}
	next, _ := decodeSkillCursor(scopeA, page.NextCursor)
	rest, err := f.svc.ListTrashedSkills(testCtx, f.human, scopeA, 1, next)
	if err != nil || len(page.Items)+len(rest.Items) != 1 || rest.NextCursor != "" {
		t.Fatal("trash pages", err, page, rest)
	}
}
