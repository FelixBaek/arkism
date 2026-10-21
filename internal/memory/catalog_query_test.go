package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// The fixture implements the same ID seek as Postgres, including one lookahead.
func (f *fakeRepo) ListCatalogRecords(ctx context.Context, scope Scope, col, after string, limit int) ([]Record, error) {
	rows, err := f.ListRecords(ctx, scope, col, 1<<30)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(rows, func(a, b Record) int { return strings.Compare(a.ID, b.ID) })
	out := []Record{}
	for _, r := range rows {
		if r.ID > after {
			out = append(out, r)
			if len(out) == limit+1 {
				break
			}
		}
	}
	return out, nil
}
func TestCatalogMemoryGlobalQueryPrivacyAndCursor(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	seed := f.activeMemory("seed", "fixture")
	for i := 0; i < 151; i++ {
		r := seed
		r.ID = fmt.Sprintf("catalog-%03d", i)
		r.Text = fmt.Sprintf("record %03d", i)
		r.CreatedAt = time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC)
		r.Tags = []string{"repo:fixture"}
		if i == 150 {
			r.Text = "Rare NEEDLE"
			r.Tags = []string{"repo:far"}
		}
		if e := f.repo.Insert(testCtx, colMemories, r); e != nil {
			t.Fatal(e)
		}
	}
	in := CatalogQueryInput{Scope: scopeA, Query: "needle", Repository: "far", Sort: "old", Limit: 10}
	page, e := f.svc.QueryMemoryCatalog(testCtx, f.human, in, false)
	if e != nil || page.Total != 1 || page.Items[0].ID != "catalog-150" {
		t.Fatal(page, e)
	}
	denied := map[string]bool{"catalog-149": true}
	f.svc.Auth = &memoryListAuth{Authorizer: f.auth, deny: denied}
	in = CatalogQueryInput{Scope: scopeA, Sort: "old", Limit: 50}
	page, e = f.svc.QueryMemoryCatalog(testCtx, f.human, in, false)
	if e != nil || page.Total != 151 || len(page.Items) != 50 || page.NextCursor == "" {
		t.Fatal(page, e)
	}
	next := in
	next.Cursor = page.NextCursor
	second, e := f.svc.QueryMemoryCatalog(testCtx, f.human, next, false)
	if e != nil || len(second.Items) != 50 || second.Items[0].ID == page.Items[0].ID {
		t.Fatal(second, e)
	}
	other := next
	other.Query = "different"
	if _, e = f.svc.QueryMemoryCatalog(testCtx, f.human, other, false); !errors.Is(e, ErrInvalid) {
		t.Fatal("cursor not query bound", e)
	}
	row, e := f.repo.Get(testCtx, colMemories, scopeA, "catalog-150")
	if e != nil {
		t.Fatal(e)
	}
	row.PrivacyQuarantined = true
	if e = f.repo.Replace(testCtx, colMemories, row, row.Revision); e != nil {
		t.Fatal(e)
	}
	if _, e = f.svc.QueryMemoryCatalog(testCtx, f.human, next, false); !errors.Is(e, ErrConflict) {
		t.Fatal("snapshot changed", e)
	}
	in.Query = "needle"
	page, e = f.svc.QueryMemoryCatalog(testCtx, f.human, in, false)
	if e != nil || page.Total != 0 {
		t.Fatal("quarantined result", page, e)
	}
	if _, e = f.svc.QueryMemoryCatalog(testCtx, "", in, false); !errors.Is(e, ErrDenied) {
		t.Fatal("anonymous", e)
	}
}
func TestCatalogWindowBindingCyclesAndCancellation(t *testing.T) {
	in := CatalogQueryInput{Scope: scopeA, Limit: 1}
	binding := catalogBinding(in, "fixture", "reader/human")
	_, cursor, e := catalogWindow([]string{"a", "b"}, in, binding)
	if e != nil || cursor == "" {
		t.Fatal(e)
	}
	in.Cursor = cursor
	if _, _, e = catalogWindow([]string{"a", "b"}, in, "other"); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	if _, _, e = catalogWindow([]string{"a", "changed"}, in, binding); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	calls := 0
	_, e = collectCatalogPages(testCtx, func(string) ([]int, string, error) { calls++; return []int{1}, "repeated", nil })
	if !errors.Is(e, ErrUnavailable) || calls != 2 {
		t.Fatal(calls, e)
	}
	ctx, cancel := context.WithCancel(testCtx)
	cancel()
	_, e = collectCatalogPages(ctx, func(string) ([]int, string, error) { t.Fatal("read after cancellation"); return nil, "", nil })
	if !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
}
func TestCatalogKnowledgeBeyondSourceAndSectionLimits(t *testing.T) {
	f := newFixture(t)
	f.svc.SyntheticHandoff = true
	scope := Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "fixture-user", AgentID: "fixture-agent"}
	anchor := f.source()
	anchor.Scope = scope
	put := func(col string, r Record) {
		t.Helper()
		if err := f.repo.Insert(testCtx, col, r); err != nil {
			t.Fatal(err)
		}
	}
	put("sources", anchor)
	source := Record{ID: "catalog-source", Scope: scope, Kind: colKnowledgeSources, Status: "active", Revision: 1, Sources: refs(anchor), Knowledge: &KnowledgeRecord{DisplayName: "Original", LifecycleStatus: "active", TrustLevel: "reviewed", SourceVersion: "v1", DocumentID: "catalog-document", DocumentIDs: []string{"catalog-document"}}}
	doc := Record{ID: "catalog-document", Scope: scope, Kind: colKnowledgeDocuments, Status: "active", Revision: 1, Sources: refs(anchor), Knowledge: &KnowledgeRecord{KnowledgeSourceID: source.ID, SourceVersion: "v1"}}
	// Seed canonical current sections: the query test must not depend on the
	// platform-specific ingestion/artifact writer or exercise a different gate.
	for i := 0; i < 225; i++ {
		chunk := Record{ID: fmt.Sprintf("catalog-chunk-%03d", i), Scope: scope, Kind: colKnowledgeChunks, Status: "active", Revision: 1, Sources: refs(anchor), Knowledge: &KnowledgeRecord{KnowledgeSourceID: source.ID, DocumentID: doc.ID, SectionPath: fmt.Sprintf("Heading %03d", i)}}
		doc.Knowledge.ChunkIDs = append(doc.Knowledge.ChunkIDs, chunk.ID)
		put(colKnowledgeChunks, chunk)
	}
	put(colKnowledgeSources, source)
	put(colKnowledgeDocuments, doc)
	for i := 0; i < 525; i++ {
		r := source
		r.ID = fmt.Sprintf("source-copy-%03d", i)
		k := *r.Knowledge
		k.DisplayName = fmt.Sprintf("copy %03d", i)
		k.DocumentIDs = nil
		k.DocumentID = ""
		k.ChunkIDs = nil
		r.Knowledge = &k
		if e = f.repo.Insert(testCtx, colKnowledgeSources, r); e != nil {
			t.Fatal(e)
		}
	}
	page, e := f.svc.QueryKnowledgeCatalog(testCtx, f.human, CatalogQueryInput{Scope: scope, Query: "copy 524"})
	if e != nil || page.Total != 1 {
		t.Fatal("source past 500", page, e)
	}
	page, e = f.svc.QueryKnowledgeCatalog(testCtx, f.human, CatalogQueryInput{Scope: scope, Query: "Heading 224"})
	if e != nil || page.Total < 1 {
		t.Fatal("heading past 200", page, e)
	}
}
func TestCatalogSessionsAllRowsGlobalOrderAndStableCursor(t *testing.T) {
	f := newFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	fee := 0.1
	for i := 0; i < 20005; i++ {
		r := Record{ID: fmt.Sprintf("usage-%05d", i), Scope: scopeA, Status: statusActive, Revision: 1, UsageSession: &UsageSession{Agent: "codex", SessionID: fmt.Sprint(i), Title: fmt.Sprintf("Session %d", i), StartedAt: now, EndedAt: now, Messages: i, Days: map[string]map[string]UsageTokens{"2026-01-01": {"a": {Input: int64(i), CostUSD: &fee}, "b": {Input: int64(i), CostUSD: &fee}}}}}
		if e := f.repo.Insert(testCtx, colUsageSessions, r); e != nil {
			t.Fatal(e)
		}
	}
	first, e := f.svc.UsageSessionListPage(testCtx, f.human, scopeA, "all", "", "", "tokens", "", 50)
	if e != nil || first["total"] != 20005 || first["items"].([]map[string]any)[0]["id"] != "usage-20004" {
		t.Fatal(first, e)
	}
	cursor := first["next_cursor"].(string)
	second, e := f.svc.UsageSessionListPage(testCtx, f.human, scopeA, "all", "", "", "tokens", cursor, 50)
	if e != nil || len(second["items"].([]map[string]any)) != 50 {
		t.Fatal(e)
	}
	if _, e = f.svc.UsageSessionListPage(testCtx, f.human, scopeA, "all", "", "", "messages", cursor, 50); !errors.Is(e, ErrInvalid) {
		t.Fatal("session sort binding", e)
	}
}

func TestCatalogSkillsGlobalNameCategoryAndModelFilters(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	seed, e := f.svc.ImportSkill(testCtx, f.human, ImportSkillInput{Scope: scopeA, SkillID: "design-review", Path: "/skills/design-review/SKILL.md", Content: "# Review\nA synthetic fixture.", Analysis: detailedSkillAnalysis()})
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 515; i++ {
		id := fmt.Sprintf("skill-%03d", i)
		parent := seed.Skill
		parent.ID = id
		parent.ActiveVersion = "version-" + id
		version := seed.Record
		version.ID = parent.ActiveVersion
		version.SkillID = id
		analysis := *version.Analysis
		analysis.SkillID = id
		analysis.Name = fmt.Sprintf("Fixture %03d", i)
		if i == 514 {
			analysis.Name = "A rare skill"
			version.SkillCompatibility = map[string]string{"codex": "unsupported"}
		}
		version.Analysis = &analysis
		if e = f.repo.Insert(testCtx, colSkillVersions, version); e != nil {
			t.Fatal(e)
		}
		if e = f.repo.Insert(testCtx, colSkills, parent); e != nil {
			t.Fatal(e)
		}
	}
	in := CatalogQueryInput{Scope: scopeA, Sort: "name", Limit: 50}
	page, e := f.svc.QuerySkillCatalog(testCtx, f.human, in)
	if e != nil || page.Total != 516 || page.Items[0].ID != "skill-514" {
		t.Fatal(page, e)
	}
	in.Query = "rare"
	in.Model = "!codex"
	in.Category = "custom"
	in.Categories = []CatalogCategory{{ID: "custom", Rules: [][]string{{"name", "*rare*"}}}}
	page, e = f.svc.QuerySkillCatalog(testCtx, f.human, in)
	if e != nil || page.Total != 1 || page.Facets.Categories["custom"] != 1 {
		t.Fatal(page, e)
	}
	in.Model = "codex"
	page, e = f.svc.QuerySkillCatalog(testCtx, f.human, in)
	if e != nil || page.Total != 0 {
		t.Fatal(page, e)
	}
}
