package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ST-824 / FR-012 / TC-FUNC-032: personal and general recall in every project, project reach
// only for its own key; only a human changes reach.
func TestMemoryReachRecallAndHumanOnlyChange(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	f.auth.actors["tok-pipeline"] = actor("pipeline-1", "pipeline", true, allVerbs...)
	general := f.activeMemory("reachneedle work rule", "reachneedle")
	mine := f.activeMemory("reachneedle Go로 작성", "reachneedle")
	other := f.activeMemory("reachneedle Rust로 작성", "reachneedle")
	if memoryReach(general) != ReachGeneral {
		t.Fatal("unmigrated non-user memory is not general", memoryReach(general))
	}
	if memoryReach(Record{Kind: "user"}) != ReachPersonal {
		t.Fatal("unmigrated user memory is not personal")
	}
	set := func(token string, m Record, reach, project string) (Record, error) {
		return f.svc.SetMemoryReach(testCtx, token, m.ID, MemoryReachInput{Scope: scopeA, ExpectedRevision: m.Revision, Reach: reach, ReachProject: project})
	}
	for _, token := range []string{f.agent, "tok-pipeline"} {
		if _, e := set(token, mine, ReachProject, "owner/mine"); !errors.Is(e, ErrDenied) {
			t.Fatal("non-human changed reach", token, e)
		}
	}
	for _, bad := range [][2]string{{"everywhere", ""}, {ReachProject, ""}, {ReachGeneral, "owner/mine"}} {
		if _, e := set(f.human, mine, bad[0], bad[1]); !errors.Is(e, ErrInvalid) {
			t.Fatal("invalid reach accepted", bad, e)
		}
	}
	mine, e := set(f.human, mine, ReachProject, " Owner/Mine ")
	if e != nil || mine.Reach != ReachProject || mine.ReachProject != "owner/mine" || mine.ReachOrigin != reachOriginHuman {
		t.Fatal("set reach", mine, e)
	}
	if _, e = f.svc.SetMemoryReach(testCtx, f.human, mine.ID, MemoryReachInput{Scope: scopeA, ExpectedRevision: mine.Revision - 1, Reach: ReachGeneral}); !errors.Is(e, ErrConflict) {
		t.Fatal("stale revision accepted", e)
	}
	if other, e = set(f.human, other, ReachProject, "owner/other"); e != nil {
		t.Fatal(e)
	}
	ids := func(key string) map[string]bool {
		got := map[string]bool{}
		in := SearchInput{Scope: scopeA, Query: "reachneedle", BudgetTokens: 4000, TokenEncoding: "cl100k_base"}
		in.ProjectKey = key
		for _, h := range f.search(f.human, in).Items {
			got[h.ID] = true
		}
		return got
	}
	if got := ids("owner/mine"); !got[general.ID] || !got[mine.ID] || got[other.ID] {
		t.Fatal("project owner/mine recall", got)
	}
	if got := ids("owner/other"); !got[general.ID] || got[mine.ID] || !got[other.ID] {
		t.Fatal("project owner/other recall", got)
	}
	if got := ids(""); !got[general.ID] || got[mine.ID] || got[other.ID] {
		t.Fatal("outside any project", got)
	}
	key := "owner/mine"
	page, e := f.svc.ListMemories(testCtx, f.human, MemoryListInput{Scope: scopeA, ProjectKey: &key})
	if e != nil || len(page.Items) != 2 {
		t.Fatal("list for a project", page.Items, e)
	}
	if page, e = f.svc.ListMemories(testCtx, f.human, MemoryListInput{Scope: scopeA}); e != nil || len(page.Items) != 3 {
		t.Fatal("management list must show every reach", page.Items, e)
	}
	if _, e := parseMemoryListQuery("workspace_id=w&project_id=p&project_key="); e != nil {
		t.Fatal("empty project key must be accepted", e)
	}
}

// ST-824 / TC-SEC-011: the store's SQL drops other projects' memories before LIMIT, the
// migration keeps revisions and is idempotent and reversible.
func TestPostgresMemoryReachFilterAndMigration(t *testing.T) {
	ctx := context.Background()
	p := isolatedPostgres(t)
	scope := Scope{WorkspaceID: "reach-" + ID(), ProjectID: "storage"}
	now := time.Now().UTC()
	marker := "pgreach-" + ID()
	put := func(id, kind, reach, project string) Record {
		r := Record{ID: id, Scope: scope, Kind: kind, Status: "active", Revision: 3, Text: "verified " + marker, Keywords: []string{marker}, ApprovedBy: "reviewer", CreatedAt: now, UpdatedAt: now, Reach: reach, ReachProject: project}
		if reach != "" {
			r.ReachOrigin = reachOriginHuman
		}
		if err := p.Insert(ctx, colMemories, r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	put("user", "user", "", "")
	put("rule", "procedural", "", "")
	put("mine", "procedural", ReachProject, "owner/mine")
	put("other", "procedural", ReachProject, "owner/other")
	found := func(key string) map[string]bool {
		in := SearchInput{Scope: scope, Query: marker, Limit: 10}
		in.ProjectKey = key
		lanes, err := p.SearchCollection(ctx, colMemories, in, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, h := range append(lanes[0], lanes[1]...) {
			got[h.ID] = true
		}
		listed, err := p.ListMemories(ctx, MemoryListInput{Scope: scope, Limit: 10, ProjectKey: &key})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range listed {
			if !got[r.ID] {
				t.Fatal("list and search disagree", key, r.ID)
			}
		}
		return got
	}
	if got := found("owner/mine"); len(got) != 3 || got["other"] {
		t.Fatal("owner/mine", got)
	}
	if got := found(""); len(got) != 2 || got["mine"] || got["other"] {
		t.Fatal("no project", got)
	}
	before := found("owner/mine")
	for i, want := range []int{2, 0} {
		n, err := p.MigrateMemoryReach(ctx, false)
		if err != nil || n != want {
			t.Fatal("apply", i, n, err)
		}
	}
	user, _ := p.Get(ctx, colMemories, scope, "user")
	rule, _ := p.Get(ctx, colMemories, scope, "rule")
	if user.Reach != ReachPersonal || rule.Reach != ReachGeneral || user.Revision != 3 || rule.Revision != 3 {
		t.Fatal("migration result", user.Reach, rule.Reach, user.Revision, rule.Revision)
	}
	if after := found("owner/mine"); len(after) != len(before) {
		t.Fatal("migration changed recall", before, after)
	}
	for i, want := range []int{2, 0} {
		n, err := p.MigrateMemoryReach(ctx, true)
		if err != nil || n != want {
			t.Fatal("revert", i, n, err)
		}
	}
	rule, _ = p.Get(ctx, colMemories, scope, "rule")
	mine, _ := p.Get(ctx, colMemories, scope, "mine")
	if rule.Reach != "" || mine.Reach != ReachProject || mine.ReachProject != "owner/mine" {
		t.Fatal("revert touched a human reach or kept a migrated one", rule.Reach, mine.Reach)
	}
}
