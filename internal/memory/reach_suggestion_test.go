package memory

import (
	"errors"
	"testing"
	"time"
)

// ST-825 / FR-012 / TC-FUNC-034: the save-time suggestion reads kind, context, repo tag and text.
func TestSuggestReach(t *testing.T) {
	for _, c := range []struct {
		r             Record
		reach, proj   string
		minConfidence float64
	}{
		{Record{Kind: "user", Text: "보고는 짧게"}, ReachPersonal, "", 0.9},
		{Record{Kind: "lesson", Text: "x", MemoryContext: &MemoryContext{Domains: []string{"personal"}}}, ReachPersonal, "", 0.7},
		{Record{Kind: "lesson", Text: "arkism 배포는 lock 먼저", Tags: []string{"repo:FelixBaek/Arkism"}}, ReachProject, "felixbaek/arkism", 0.8},
		{Record{Kind: "procedural", Text: "internal/memory/http.go 에 라우트를 둔다", Tags: []string{"repo:o/n"}}, ReachProject, "o/n", 0.65},
		{Record{Kind: "procedural", Text: "Go로 작성한다"}, ReachProject, "", 0.45},
		{Record{Kind: "procedural", Text: "let it go and keep reports short"}, ReachGeneral, "", 0.6},
		{Record{Kind: "decision", Text: "AI는 분류만, 승인은 사람"}, ReachGeneral, "", 0.6},
	} {
		got := suggestReach(c.r)
		if got.Reach != c.reach || got.ReachProject != c.proj || got.Confidence < c.minConfidence || got.Reason == "" {
			t.Errorf("%q: got %+v", c.r.Text, got)
		}
	}
}

// ST-825 / TC-FUNC-034·TC-SEC-012: new memories carry a suggestion and keep their reach; a project
// memory used in two projects is suggested for general; only a human accepts or dismisses.
func TestReachSuggestionsHumanDecides(t *testing.T) {
	f := newFixture(t)
	f.svc.Embedder = nil
	f.auth.actors["tok-pipeline"] = actor("pipeline-1", "pipeline", true, allVerbs...)
	rule := f.activeMemory("sugneedle Go 코드는 gofmt", "sugneedle")
	plain := f.activeMemory("sugneedle 결론부터 쓴다", "sugneedle")
	if rule.ReachSuggestion == nil || rule.ReachSuggestion.Reach != ReachProject || rule.Reach != "" || memoryReach(rule) != ReachGeneral {
		t.Fatal("save-time suggestion must not change reach", rule.ReachSuggestion, rule.Reach)
	}
	list := func(token string) ReachSuggestionPage {
		t.Helper()
		page, e := f.svc.ReachSuggestions(testCtx, token, MemoryListInput{Scope: scopeA, Query: "sugneedle"})
		if e != nil {
			t.Fatal(e)
		}
		return page
	}
	find := func(page ReachSuggestionPage, id string) *ReachSuggestionItem {
		for i := range page.Items {
			if page.Items[i].MemoryID == id {
				return &page.Items[i]
			}
		}
		return nil
	}
	if got := list(f.human); find(got, rule.ID) == nil || find(got, plain.ID) != nil {
		t.Fatal("existing project-only candidate not shown, or a matching reach shown", got.Items)
	}
	// Used from two projects while general; a person then narrows it to one.
	for _, key := range []string{"owner/a", "owner/b"} {
		in := SearchInput{Scope: scopeA, Query: "sugneedle 결론부터", BudgetTokens: 4000, TokenEncoding: "cl100k_base"}
		in.ProjectKey = key
		f.search(f.human, in)
	}
	plain, e := f.svc.SetMemoryReach(testCtx, f.human, plain.ID, MemoryReachInput{Scope: scopeA, ExpectedRevision: plain.Revision, Reach: ReachProject, ReachProject: "owner/a"})
	if e != nil {
		t.Fatal(e)
	}
	item := find(list(f.human), plain.ID)
	if item == nil || item.Suggestion.Reach != ReachGeneral || item.UsedProjects != 2 || item.Revision != plain.Revision {
		t.Fatal("promotion to general not suggested", item)
	}
	dismiss := func(token string, m Record, sg ReachSuggestion) (Record, error) {
		return f.svc.DismissReachSuggestion(testCtx, token, m.ID, ReachSuggestionDismissInput{Scope: scopeA, ExpectedRevision: m.Revision, Reach: sg.Reach, ReachProject: sg.ReachProject})
	}
	for _, token := range []string{f.agent, "tok-pipeline"} {
		if _, e = dismiss(token, plain, item.Suggestion); !errors.Is(e, ErrDenied) {
			t.Fatal("non-human dismissed", token, e)
		}
		if _, e = f.svc.SetMemoryReach(testCtx, token, plain.ID, MemoryReachInput{Scope: scopeA, ExpectedRevision: plain.Revision, Reach: ReachGeneral}); !errors.Is(e, ErrDenied) {
			t.Fatal("non-human accepted", token, e)
		}
	}
	if _, e = dismiss(f.human, plain, ReachSuggestion{Reach: "everywhere"}); !errors.Is(e, ErrInvalid) {
		t.Fatal("invalid dismissal accepted", e)
	}
	if _, e = dismiss(f.human, Record{ID: plain.ID, Revision: plain.Revision - 1}, item.Suggestion); !errors.Is(e, ErrConflict) {
		t.Fatal("stale dismissal accepted", e)
	}
	dismissed, e := dismiss(f.human, plain, item.Suggestion)
	if e != nil || dismissed.Revision != plain.Revision || dismissed.Reach != ReachProject {
		t.Fatal("dismissal must keep revision and reach", dismissed, e)
	}
	if find(list(f.human), plain.ID) != nil {
		t.Fatal("dismissed suggestion still shown")
	}
	// Accepting is the ST-824 reach change: the suggestion then matches and disappears.
	sg := find(list(f.human), rule.ID).Suggestion
	if _, e = f.svc.SetMemoryReach(testCtx, f.human, rule.ID, MemoryReachInput{Scope: scopeA, ExpectedRevision: rule.Revision, Reach: sg.Reach, ReachProject: "owner/a"}); e != nil {
		t.Fatal(e)
	}
	if got := find(list(f.human), rule.ID); got != nil && got.Suggestion.Reach == ReachProject {
		t.Fatal("accepted project suggestion still shown", got)
	}
}

// ST-825: the store counts each memory's uses per project key, within the scope only.
func TestPostgresMemoryUseProjects(t *testing.T) {
	p := isolatedPostgres(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	ref := Ref{Collection: colMemories, ID: "mem-p", Revision: 1}
	for i, key := range []string{"owner/a", "owner/b", "owner/a", ""} {
		r := Record{ID: "use-" + ID(), Scope: scopeA, Kind: "context_usage", Status: "done", Revision: 1, CreatedAt: now.Add(time.Duration(i) * time.Second), UpdatedAt: now, CreatedBy: "agent-1", Usage: &ContextUsage{ProjectKey: key, Items: []MemoryUse{{ref, 1}}}}
		if e := p.Insert(testCtx, "jobs", r); e != nil {
			t.Fatal(e)
		}
	}
	counts, e := p.MemoryUseCounts(testCtx, scopeA, []string{"mem-p"})
	if e != nil || len(counts) != 1 || counts[0].Uses != 4 || len(counts[0].Projects) != 2 || counts[0].Projects[0] != "owner/a" || counts[0].Projects[1] != "owner/b" {
		t.Fatalf("projects: %+v %v", counts, e)
	}
}
