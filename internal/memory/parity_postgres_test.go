//go:build linux

package memory

// Real-PostgreSQL checks for the entity, recall, ops, space, webhook, causal and file-delta SQL.
// They skip when POSTGRES_TEST_DSN is not configured (see tools/check_postgres_local.sh).
import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func pgFixture(t *testing.T) (*fixture, *Postgres) {
	t.Helper()
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	f.auth.actors[f.human].verbs["ops"] = true
	return f, p
}

func TestPostgresEntityMentionsFeedRecallAndLeaveWithTheMemory(t *testing.T) {
	f, p := pgFixture(t)
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	entityMemory(t, f, p, "m1", "Arkism stores memories in Postgres.", day)
	entityMemory(t, f, p, "m2", "We tuned Postgres for Arkism.", day.AddDate(0, 1, 0))
	m3 := entityMemory(t, f, p, "m3", "Postgres vacuum runs nightly; see `vacuum.sh`.", day.AddDate(0, 2, 0))
	out, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA})
	if err != nil || out.NewEntities != 3 || out.AddedMentions != 6 {
		t.Fatal(out, err)
	}
	if again, err := f.svc.ExtractEntities(testCtx, f.human, EntityExtractInput{Scope: scopeA}); err != nil || again.NewEntities != 0 || again.AddedMentions != 0 {
		t.Fatal("rerun not idempotent", again, err)
	}
	list, err := f.svc.ListEntities(testCtx, f.agent, EntityListInput{Scope: scopeA, Limit: 10})
	if err != nil || list.Total != 3 || list.Items[0].Name != "Postgres" || list.Items[0].Mentions != 3 {
		t.Fatal(list, err)
	}
	graph, err := f.svc.EntityGraphView(testCtx, f.agent, EntityGraphInput{Scope: scopeA})
	if err != nil || len(graph.Nodes) != 3 || len(graph.Edges) != 2 || graph.Edges[0].Weight != 2 {
		t.Fatal("co-occurrence", graph, err)
	}
	// The recall graph lane reads the same mention rows: m2 shares two entities with m1, m3 one.
	near, err := p.MemoryNeighbors(testCtx, scopeA, []string{"m1"}, 10)
	if err != nil || !slices.Equal(near, []string{"m2", "m3"}) {
		t.Fatal("neighbors", near, err)
	}
	hits, err := p.SearchMemoriesInWindow(testCtx, SearchInput{Scope: scopeA, Limit: 10}, day.AddDate(0, 1, -1), day.AddDate(0, 1, 1))
	if err != nil || len(hits) != 1 || hits[0].ID != "m2" {
		t.Fatal("window", hits, err)
	}
	// Deleting m3 drops its mentions and the entity only it named (migration 0006 trigger).
	if err := p.Delete(testCtx, colMemories, scopeA, m3.ID, m3.Revision); err != nil {
		t.Fatal(err)
	}
	mentions, err := p.EntityMentions(testCtx, scopeA, 100)
	if err != nil || len(mentions) != 4 {
		t.Fatal("mentions after delete", mentions, err)
	}
	ents, err := p.NamedEntities(testCtx, scopeA, 100)
	if err != nil || len(ents) != 2 {
		t.Fatal("entities after delete", ents, err)
	}
}

func TestPostgresOpsAuditStatsAndTags(t *testing.T) {
	f, p := pgFixture(t)
	base := time.Now().UTC().Truncate(time.Millisecond)
	put := func(col string, r Record) {
		r.Scope, r.Revision, r.UpdatedAt = scopeA, 1, r.CreatedAt
		if err := p.Insert(testCtx, col, r); err != nil {
			t.Fatal(err)
		}
	}
	put("audit_events", Record{ID: "a1", Kind: "set_reach:memories", CreatedBy: "human-1", CreatedAt: base.Add(-2 * time.Second), Target: &Ref{ID: "m1"}})
	put("audit_events", Record{ID: "a2", Kind: "wiki:job_created", CreatedBy: "human-2", CreatedAt: base.Add(-time.Second)})
	page, err := f.svc.ListAuditEvents(testCtx, f.human, OpsListInput{Scope: scopeA, Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "a2" || page.NextCursor == "" {
		t.Fatalf("audit page = %+v, %v", page, err)
	}
	next, err := f.svc.ListAuditEvents(testCtx, f.human, OpsListInput{Scope: scopeA, Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != "a1" {
		t.Fatalf("audit next = %+v, %v", next, err)
	}
	if filtered, err := f.svc.ListAuditEvents(testCtx, f.human, OpsListInput{Scope: scopeA, Action: "wiki:"}); err != nil || len(filtered.Items) != 1 {
		t.Fatalf("action filter = %+v, %v", filtered, err)
	}
	if stats, err := f.svc.AuditEventStats(testCtx, f.human, scopeA, base.Add(-time.Hour), base.Add(time.Hour)); err != nil || stats.Total != 2 || len(stats.ByDay) == 0 {
		t.Fatalf("audit stats = %+v, %v", stats, err)
	}
	if ops, err := f.svc.ListOperations(testCtx, f.human, OpsListInput{Scope: scopeA}); err != nil {
		t.Fatalf("operations = %+v, %v", ops, err)
	}
	mem := f.activeMemory("prefer tabs over spaces", "style")
	put(colMemories, Record{ID: "old", Kind: "lesson", Status: "superseded", CreatedAt: base})
	st, err := f.svc.Stats(testCtx, f.human, scopeA)
	if err != nil || st.Memories.Total != 2 || st.Memories.ByStatus["active"] != 1 || st.Memories.ByKind["lesson"] != 1 {
		t.Fatalf("stats = %+v, %v", st, err)
	}
	ts, err := f.svc.StatsTimeseries(testCtx, f.human, StatsTimeseriesInput{Scope: scopeA, From: base.Add(-time.Hour), To: base.Add(time.Hour), By: "status"})
	if err != nil || len(ts.Points) != 2 {
		t.Fatalf("timeseries = %+v, %v", ts, err)
	}
	if _, err := f.svc.SetMemoryTags(testCtx, f.human, mem.ID, MemoryTagsInput{Scope: scopeA, Add: []string{"team"}}); err != nil {
		t.Fatal(err)
	}
	tags, err := f.svc.ListTags(testCtx, f.human, scopeA)
	if err != nil || !slices.ContainsFunc(tags.Items, func(c TagCount) bool { return c.Tag == "team" }) {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
	if res, err := f.svc.RenameTag(testCtx, f.human, TagRenameInput{Scope: scopeA, From: "team", To: "crew"}); err != nil || res.Renamed != 1 {
		t.Fatalf("rename = %+v, %v", res, err)
	}
}

func TestPostgresSpacesListAliasAndBundle(t *testing.T) {
	f, p := pgFixture(t)
	mem := f.activeMemory("prefer tabs over spaces", "style")
	if _, err := f.svc.SetSpaceAlias(testCtx, f.human, scopeA, "front", true); err != nil {
		t.Fatal(err)
	}
	if got, err := f.svc.resolveSpace(testCtx, Scope{WorkspaceID: scopeA.WorkspaceID, ProjectID: "@front"}); err != nil || got != scopeA {
		t.Fatalf("resolve = %+v %v", got, err)
	}
	if _, err := f.svc.SetSpaceAlias(testCtx, f.human, scopeB, "front", true); err == nil {
		t.Fatal("duplicate alias accepted")
	}
	page, err := f.svc.ListSpaces(testCtx, f.human, scopeA, "", 10)
	if err != nil || len(page.Items) != 1 || !slices.Contains(page.Items[0].Config.Aliases, "front") {
		t.Fatalf("list = %+v %v", page, err)
	}
	report := importBundle(t, f, scopeB, exportBundle(t, f, scopeA, SpaceExportInput{}), "")
	cands, err := p.ListRecords(testCtx, scopeB, "memory_candidates", 10)
	if err != nil || report.Imported != 1 || len(cands) != 1 || cands[0].Text != mem.Text {
		t.Fatalf("import = %+v %+v %v", report, cands, err)
	}
}

func TestPostgresWebhookDeliveriesPage(t *testing.T) {
	f, _ := pgFixture(t)
	f.svc.WebhookKey = []byte("kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk")
	f.svc.WebhookAllowPrivate = true
	v := f.hook(newHookServer(t, http.StatusOK).URL, false, EventMemoryDeleted)
	for range 3 {
		if _, err := f.svc.TestWebhook(testCtx, f.human, scopeA, v.ID); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	page, err := f.svc.ListWebhookDeliveries(testCtx, f.human, scopeA, v.ID, "", 2)
	if err != nil || len(page.Items) != 2 || page.NextBefore == "" {
		t.Fatal("first page", err, page)
	}
	rest, err := f.svc.ListWebhookDeliveries(testCtx, f.human, scopeA, v.ID, page.NextBefore, 2)
	if err != nil || len(rest.Items) != 1 || rest.NextBefore != "" {
		t.Fatal("second page", err, rest)
	}
}

// Phase 2 SQL: a model-proposed causal link waits for a person, then feeds lineage and the
// causal recall lane; a re-uploaded document only sends its changed chunks to the model.
func TestPostgresCausalReviewAndDocumentDelta(t *testing.T) {
	f, llm := wikiFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	a := f.activeMemory("the cache server ran out of memory overnight")
	b := f.activeMemory("search answered slowly all morning")
	llm.answers = []string{causalAnswerText(t, causalDraft{CauseID: a.ID, EffectID: b.ID, Reason: "no cache, slow search", Confidence: 0.7})}
	out, err := f.svc.ProposeCausalLinks(testCtx, f.human, CausalProposeInput{Scope: scopeA})
	if err != nil || len(out.Proposed) != 1 || out.Proposed[0].Status != "candidate" {
		t.Fatalf("%+v %v", out, err)
	}
	if ids := causalHitIDs(t, f, "overnight"); slices.Contains(ids, b.ID) {
		t.Fatal("a proposal must not expand recall")
	}
	p := out.Proposed[0]
	if _, err := f.svc.ReviewCausalLink(testCtx, f.human, p.ID, CausalReviewInput{Scope: scopeA, ExpectedRevision: p.Revision, Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.ListCausalLinks(testCtx, f.agent, scopeA, statusActive, "", 0)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != p.ID || page.Items[0].ApprovedBy == "" {
		t.Fatalf("%+v %v", page, err)
	}
	if got := lineageRelations(t, f, b.ID); !slices.Equal(got, []string{"caused_by:" + a.ID}) {
		t.Fatalf("lineage %v", got)
	}
	if ids := causalHitIDs(t, f, "overnight"); !slices.Contains(ids, b.ID) {
		t.Fatalf("approved link expands recall: %v", ids)
	}

	llm.answers, llm.prompts = []string{extractAnswer(extractRuleClaim), extractAnswer()}, nil
	filler := strings.Repeat(" 배포 전에 테스트를 실행한다.", 200)
	p1, p2 := "선택 상태에는 주황 강조선을 쓰지 않는다."+filler, "둘째 문단."+filler
	first := extractUpload(t, f, p1, p2)
	if first.CandidatesCreated != 1 || len(llm.prompts) != 2 || first.Delta.ChunksChanged != 2 {
		t.Fatalf("first %+v %+v", first, first.Delta)
	}
	same := extractUpload(t, f, p1, p2)
	if len(llm.prompts) != 2 || same.JobID == first.JobID || same.Delta.ChunksUnchanged != 2 || same.Delta.ChunksChanged != 0 {
		t.Fatalf("unchanged re-upload called the model: %d %+v", len(llm.prompts), same.Delta)
	}
}

// The window query reads the stored event span on overlap, half-open, and still finds
// older file-extract memories whose event date sits in memory_context.valid_from.
func TestPostgresRecallWindowReadsTheEventSpan(t *testing.T) {
	f, p := pgFixture(t)
	saved := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	put := func(id string, o *RecallTimeWindow, mc *MemoryContext) {
		src := f.create(f.human, "sources", sourceInput(CreateInput{Scope: scopeA, Text: "source for " + id}))
		r := Record{ID: id, Scope: scopeA, Kind: "episodic", Text: id, Status: statusActive, ApprovedBy: "human-1", Revision: 1, Sources: refs(src), CreatedAt: saved, UpdatedAt: saved, Occurred: o, MemoryContext: mc}
		if err := p.Insert(testCtx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	put("span", &RecallTimeWindow{From: seoulDay(2026, 10, 25), To: seoulDay(2026, 10, 28)}, nil)
	put("before", &RecallTimeWindow{From: seoulDay(2026, 10, 25), To: seoulDay(2026, 10, 26)}, nil)
	put("outside", &RecallTimeWindow{From: seoulDay(2026, 9, 1), To: seoulDay(2026, 9, 3)}, nil)
	put("legacy", nil, &MemoryContext{Domains: []string{"work"}, EvidenceKind: "inference", SemanticKind: "fact", ValidFrom: "2026-10-27"})
	hits, err := p.SearchMemoriesInWindow(testCtx, SearchInput{Scope: scopeA, Limit: 10}, seoulDay(2026, 10, 26), seoulDay(2026, 11, 2))
	got := []string{}
	for _, h := range hits {
		got = append(got, h.ID)
	}
	slices.Sort(got)
	if err != nil || !slices.Equal(got, []string{"legacy", "span"}) {
		t.Fatal(got, err)
	}
}

// tags_match modes run in SQL (pgTagsMatch), so untagged rows must not spend the LIMIT or vanish.
func TestPostgresSearchTagsMatchModes(t *testing.T) {
	f, _ := pgFixture(t)
	both := f.activeMemory("deploy checklist alpha", "deploy")
	one := f.activeMemory("deploy checklist beta", "deploy")
	none := f.activeMemory("deploy checklist gamma", "deploy")
	for id, tags := range map[string][]string{both.ID: {"team", "ops"}, one.ID: {"team"}} {
		if _, err := f.svc.SetMemoryTags(testCtx, f.human, id, MemoryTagsInput{Scope: scopeA, Add: tags}); err != nil {
			t.Fatal(err)
		}
	}
	for mode, want := range map[string][]string{
		"": {both.ID, one.ID}, "any": {both.ID, one.ID, none.ID}, "all": {both.ID, none.ID},
		"all_strict": {both.ID}, "exact": {both.ID},
	} {
		out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "deploy checklist", Modes: []string{"exact", "fts"}, SearchCriteria: SearchCriteria{Filters: &SearchFilters{Tags: []string{"team", "ops"}, TagsMatch: mode}}})
		got := ids(out.Items)
		slices.Sort(got)
		slices.Sort(want)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("%q: %v %v want %v", mode, got, err, want)
		}
	}
}
