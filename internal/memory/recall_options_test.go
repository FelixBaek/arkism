package memory

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestParseTimeIntentLanguages(t *testing.T) {
	seoul := recallLocation("")
	nov5 := time.Date(2026, 11, 5, 10, 0, 0, 0, seoul) // Thursday
	day := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, seoul) }
	today, yesterday, before := [2]time.Time{day(2026, 11, 5), day(2026, 11, 6)}, [2]time.Time{day(2026, 11, 4), day(2026, 11, 5)}, [2]time.Time{day(2026, 11, 3), day(2026, 11, 4)}
	threeAgo := [2]time.Time{day(2026, 11, 2), day(2026, 11, 3)}
	lastWeek, thisWeek := [2]time.Time{day(2026, 10, 26), day(2026, 11, 2)}, [2]time.Time{day(2026, 11, 2), day(2026, 11, 9)}
	lastMonth, thisMonth := [2]time.Time{day(2026, 10, 1), day(2026, 11, 1)}, [2]time.Time{day(2026, 11, 1), day(2026, 12, 1)}
	lastYear, thisYear := [2]time.Time{day(2025, 1, 1), day(2026, 1, 1)}, [2]time.Time{day(2026, 1, 1), day(2027, 1, 1)}
	oct31, october, lateOct := [2]time.Time{day(2026, 10, 31), day(2026, 11, 1)}, lastMonth, [2]time.Time{day(2026, 10, 21), day(2026, 11, 1)}
	for lang, cases := range map[string]map[string][2]time.Time{
		"ja":      {"今日の会議": today, "昨日の決定": yesterday, "一昨日": before, "3日前": threeAgo, "先週": lastWeek, "今週": thisWeek, "先月": lastMonth, "今月": thisMonth, "去年": lastYear, "昨年": lastYear, "今年": thisYear, "2026年10月31日": oct31, "10月31日": oct31, "10月": october, "10月下旬": lateOct, "10月末": lateOct, "一昨年": {day(2024, 1, 1), day(2025, 1, 1)}},
		"zh-Hans": {"今天": today, "昨天的部署": yesterday, "前天": before, "3天前": threeAgo, "三天前": threeAgo, "上周": lastWeek, "上个星期": lastWeek, "这周": thisWeek, "上个月": lastMonth, "这个月": thisMonth, "去年": lastYear, "10月31号": oct31, "10月底": lateOct},
		"zh-Hant": {"上週": lastWeek, "這週": thisWeek, "上個月": lastMonth, "這個月": thisMonth, "10月31號": oct31},
		"es":      {"hoy": today, "ayer": yesterday, "anteayer": before, "hace 3 días": threeAgo, "la semana pasada": lastWeek, "esta semana": thisWeek, "el mes pasado": lastMonth, "este mes": thisMonth, "el año pasado": lastYear, "este año": thisYear, "31 de octubre de 2026": oct31, "31 de octubre": oct31, "en octubre": october, "octubre de 2026": october, "finales de octubre": lateOct},
		"fr":      {"aujourd'hui": today, "hier soir": yesterday, "qu'avons-nous décidé hier": yesterday, "avant-hier": before, "il y a 3 jours": threeAgo, "la semaine dernière": lastWeek, "cette semaine": thisWeek, "le mois dernier": lastMonth, "ce mois-ci": thisMonth, "l'année dernière": lastYear, "cette année": thisYear, "31 octobre 2026": oct31, "le 31 octobre": oct31, "en octobre": october, "fin octobre": lateOct},
		"de":      {"heute": today, "gestern": yesterday, "was haben wir hier gestern gemacht": yesterday, "vorgestern": before, "vor 3 Tagen": threeAgo, "letzte Woche": lastWeek, "diese Woche": thisWeek, "letzten Monat": lastMonth, "diesen Monat": thisMonth, "letztes Jahr": lastYear, "dieses Jahr": thisYear, "31. Oktober 2026": oct31, "31.10.2026": oct31, "im Oktober": october, "Ende Oktober": lateOct, "im März": {day(2026, 3, 1), day(2026, 4, 1)}},
	} {
		for query, want := range cases {
			w, ok := parseTimeIntent(query, nov5)
			if !ok || !w.From.Equal(want[0]) || !w.To.Equal(want[1]) {
				t.Errorf("%s %q: got %v..%v ok=%v, want %v..%v", lang, query, w.From, w.To, ok, want[0], want[1])
			}
		}
	}
	// A bare month name that is also an ordinary word, and impossible dates, name no span.
	for _, q := range []string{"mars rover", "31.02.2026", "3个月计划", "31 février", "was haben wir hier entschieden", "die Notizen von hier"} {
		if w, ok := parseTimeIntent(q, nov5); ok {
			t.Errorf("%q: unexpected window %v", q, w)
		}
	}
	if cjkInt("二十一") != 21 || cjkInt("十二") != 12 || cjkInt("两") != 2 || cjkInt("7") != 7 {
		t.Fatal("cjkInt")
	}
}

func TestRecallQueryTimeAnchorsTimeWordsAndRecency(t *testing.T) {
	f, now := recallFixture(t)
	// "yday" happened on Nov 4: asked on Nov 5 it is yesterday, asked as of Nov 7 it is not.
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "어제 뭐 바꿨지", Explain: true})
	if err != nil || !slices.Contains(ids(out.Items), "yday") {
		t.Fatal(ids(out.Items), err)
	}
	later := now.AddDate(0, 0, 2)
	out, err = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "어제 뭐 바꿨지", Explain: true, QueryTime: &later})
	if err != nil || slices.Contains(ids(out.Items), "yday") || out.Explain.TimeWindow == nil || !out.Explain.TimeWindow.From.Equal(time.Date(2026, 11, 6, 0, 0, 0, 0, recallLocation(""))) {
		t.Fatal(ids(out.Items), out.Explain, err)
	}
	if f.svc.recallClock(SearchInput{QueryTime: &later}) != later || !f.svc.recallClock(SearchInput{}).Equal(now) {
		t.Fatal("recallClock")
	}
	bad := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "q", QueryTime: &bad}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	pack, err := f.svc.BuildContext(testCtx, f.human, ContextBuildInput{Scope: scopeA, Task: "어제 뭐 바꿨지", TokenEncoding: "cl100k_base", Explain: true, QueryTime: &later})
	if err != nil || pack.Explain == nil || pack.Explain.TimeWindow == nil || !pack.Explain.TimeWindow.From.Equal(time.Date(2026, 11, 6, 0, 0, 0, 0, recallLocation(""))) {
		t.Fatalf("%+v %v", pack.Explain, err)
	}
}

func TestTagsMatchModes(t *testing.T) {
	want := []string{"a", "b"}
	for _, tc := range []struct {
		mode string
		have []string
		ok   bool
	}{
		{"", []string{"a"}, true}, {"", nil, false},
		{"any_strict", []string{"b", "z"}, true}, {"any_strict", nil, false}, {"any_strict", []string{"z"}, false},
		{"any", nil, true}, {"any", []string{"z"}, false}, {"any", []string{"a"}, true},
		{"all", nil, true}, {"all", []string{"a"}, false}, {"all", []string{"a", "b", "z"}, true},
		{"all_strict", nil, false}, {"all_strict", []string{"b", "a"}, true}, {"all_strict", []string{"a"}, false},
		{"exact", []string{"b", "a"}, true}, {"exact", []string{"a", "b", "z"}, false}, {"exact", nil, false},
	} {
		if got := tagsMatch(tc.mode, want, tc.have); got != tc.ok {
			t.Errorf("%q %v: got %v", tc.mode, tc.have, got)
		}
	}
	for _, mode := range append(slices.Clone(tagsMatchModes), "") {
		if pgTagsMatch[mode] == "" {
			t.Fatalf("no SQL for %q", mode)
		}
	}
	for _, f := range []SearchFilters{{TagsMatch: "all"}, {Tags: []string{"a"}, TagsMatch: "some"}} {
		if _, err := normalizeSearchCriteria(SearchCriteria{Filters: &f}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", f, err)
		}
	}
	// End to end: one tagged and one untagged match.
	f, _ := recallFixture(t)
	if _, err := f.svc.SetMemoryTags(testCtx, f.human, "alpha", MemoryTagsInput{Scope: scopeA, Add: []string{"team"}}); err != nil {
		t.Fatal(err)
	}
	for mode, wantIDs := range map[string][]string{"": {"alpha"}, "any": {"alpha", "delta"}, "all_strict": {"alpha"}} {
		out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", SearchCriteria: SearchCriteria{Filters: &SearchFilters{Tags: []string{"team"}, TagsMatch: mode}}})
		got := ids(out.Items)
		slices.Sort(got)
		if err != nil || !slices.Equal(got, wantIDs) {
			t.Fatalf("%q: %v %v", mode, got, err)
		}
	}
}

// tagFilterCases cross every mode and a few tag groups with stored tag sets; the Go
// rule decides, and TestPostgresTagFiltersAgreeWithGo checks the SQL says the same.
func tagFilterCases() (filters []SearchFilters, stored [][]string) {
	for _, mode := range append(slices.Clone(tagsMatchModes), "") {
		filters = append(filters, SearchFilters{Tags: []string{"a", "b"}, TagsMatch: mode})
	}
	filters = append(filters,
		// (a AND b) OR c
		SearchFilters{TagGroups: [][]TagTest{{{Tags: []string{"a", "b"}, Match: "all_strict"}, {Tags: []string{"c"}}}}},
		// any of a, and not c (untagged rows pass the negation)
		SearchFilters{TagGroups: [][]TagTest{{{Tags: []string{"a"}, Match: "any"}}, {{Tags: []string{"c"}, Not: true}}}},
		// tags plus a group: has a, and exactly {a, b} or not b
		SearchFilters{Tags: []string{"a"}, TagGroups: [][]TagTest{{{Tags: []string{"a", "b"}, Match: "exact"}, {Tags: []string{"b"}, Not: true}}}},
	)
	return filters, [][]string{nil, {}, {"a"}, {"b"}, {"c"}, {"a", "b"}, {"b", "a"}, {"a", "c"}, {"a", "b", "c"}, {"z"}}
}

func TestTagGroups(t *testing.T) {
	filters, _ := tagFilterCases()
	for _, tc := range []struct {
		f    SearchFilters
		have []string
		ok   bool
	}{
		{filters[6], []string{"b", "a"}, true}, {filters[6], []string{"c"}, true}, {filters[6], []string{"a"}, false}, {filters[6], nil, false},
		{filters[7], nil, true}, {filters[7], []string{"a"}, true}, {filters[7], []string{"a", "c"}, false}, {filters[7], []string{"z"}, false},
		{filters[8], []string{"a", "b"}, true}, {filters[8], []string{"a", "c"}, true}, {filters[8], []string{"a", "b", "c"}, false}, {filters[8], nil, false},
	} {
		if got := (SearchCriteria{Filters: &tc.f}).matches("", tc.have, time.Time{}, time.Time{}); got != tc.ok {
			t.Errorf("%+v %v: got %v", tc.f.TagGroups, tc.have, got)
		}
	}
	for _, groups := range [][][]TagTest{{{}}, {{{Tags: nil}}}, {{{Tags: []string{"a"}, Match: "some"}}}, {{{Tags: []string{"a", "a"}}}}, make([][]TagTest, tagGroupsMax+1)} {
		if _, err := normalizeSearchCriteria(SearchCriteria{Filters: &SearchFilters{TagGroups: groups}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", groups, err)
		}
	}
	if c, err := normalizeSearchCriteria(SearchCriteria{Filters: &SearchFilters{TagGroups: filters[6].TagGroups}}); err != nil || c.Filters == nil {
		t.Fatal("tag groups alone must keep the filter", err)
	}
}

func TestRecallMinScoreAndRerankToggle(t *testing.T) {
	f, _ := recallFixture(t)
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true})
	if err != nil || len(out.Items) < 2 {
		t.Fatal(ids(out.Items), err)
	}
	floor := (out.Items[0].Score + out.Items[len(out.Items)-1].Score) / 2
	kept, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true, MinScore: floor})
	if err != nil || len(kept.Items) == 0 || len(kept.Items) >= len(out.Items) || kept.Explain.MinScoreDropped != len(out.Items)-len(kept.Items) {
		t.Fatal(ids(kept.Items), kept.Explain, err)
	}
	for _, h := range kept.Items {
		if h.Score < floor {
			t.Fatal(h.ID, h.Score)
		}
	}
	for _, bad := range []float64{-0.1, 1.5} {
		if _, err = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "q", MinScore: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatal(bad, err)
		}
	}
	// rerank=false skips the reranker before any call or usage row.
	f.svc.Reranker, _ = NewTEIReranker(TEIRerankConfig{URL: "http://127.0.0.1:1"})
	usage := len(f.repo.rows(colProviderUsage))
	rows, err := f.svc.rerankRows(testCtx, SearchInput{Scope: scopeA, Query: "q", Limit: 3, Rerank: rerankOff}, Decision{ExternalTransfer: true, ContentSafe: true}, rerankRowsFixture(), &recallTrace{rerank: map[string]float64{}})
	if err != nil || rows[0].ID != "a" || f.svc.Reranker.Metrics().Calls != 0 || len(f.repo.rows(colProviderUsage)) != usage {
		t.Fatal(rows, err)
	}
}

func TestRecallGraphWalksBoundedDepthWithDecay(t *testing.T) {
	f, _ := recallFixture(t)
	f.svc.RecallGraph = true
	at := time.Now().UTC()
	for id, status := range map[string]string{"retired": "superseded", "behind": "active"} {
		if err := f.repo.Insert(testCtx, colMemories, Record{ID: id, Scope: scopeA, Kind: "decision", Text: id + " note", Status: status, ApprovedBy: "human", Revision: 1, Sources: refs(f.source()), CreatedAt: at, UpdatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	// gamma - beta - delta - yday is a chain; "behind" is only reachable through a retired memory.
	f.repo.relations = []fakeRelation{
		{SourceID: "gamma", TargetKind: "memory", TargetID: "beta", Kind: "references"},
		{SourceID: "beta", TargetKind: "memory", TargetID: "delta", Kind: "references"},
		{SourceID: "delta", TargetKind: "memory", TargetID: "yday", Kind: "references"},
		{SourceID: "gamma", TargetKind: "memory", TargetID: "retired", Kind: "supersedes"},
		{SourceID: "retired", TargetKind: "memory", TargetID: "behind", Kind: "references"},
	}
	walk := func(depth int) ContextBundle {
		out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "gamma", Explain: true, GraphDepth: depth})
		if err != nil {
			t.Fatal(depth, err)
		}
		return out
	}
	hops := func(out ContextBundle) map[string]int {
		m := map[string]int{}
		for _, h := range out.Items {
			m[h.ID] = h.Explain.GraphHops
		}
		return m
	}
	if got := hops(walk(1)); len(got) != 2 || got["beta"] != 1 {
		t.Fatal(got)
	}
	two := walk(0) // default depth 2
	if got := hops(two); len(got) != 3 || got["beta"] != 1 || got["delta"] != 2 {
		t.Fatal(got)
	}
	if two.Items[1].ID != "beta" || two.Items[2].ID != "delta" || two.Items[2].Score >= two.Items[1].Score {
		t.Fatalf("farther neighbour must weigh less: %v", ids(two.Items))
	}
	if got := hops(walk(3)); len(got) != 4 || got["yday"] != 3 || got["behind"] != 0 {
		t.Fatal(got)
	}
	if _, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "gamma", GraphDepth: 4}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

// A step asks past the ids it has already seen, so back-edges to the seeds cannot fill it.
func TestNeighborStepSkipsBackEdges(t *testing.T) {
	f, _ := recallFixture(t)
	next := func(_ context.Context, _ Scope, frontier []string, limit int) ([]string, error) {
		if slices.Equal(frontier, []string{"beta"}) {
			return []string{"gamma", "yday", "alpha"}[:min(3, limit)], nil
		}
		return []string{"beta"}, nil
	}
	in := SearchInput{Scope: scopeA, Limit: 1, GraphDepth: 2}
	top := []ScoredHit{{Hit: Hit{ID: "gamma"}}, {Hit: Hit{ID: "yday"}}}
	lanes, err := f.svc.neighborLanes(testCtx, in, top, []neighborSource{{reasonGraph, next}}, func(h []Hit) ([]Hit, error) { return h, nil })
	if err != nil || len(lanes) != 1 || !slices.Equal(ids(lanes[0]), []string{"beta", "alpha"}) {
		t.Fatal(lanes, err)
	}
}

// The walk never steps through a memory the caller cannot read: neighbours reachable
// only through another scope's memory or another project's memory stay out.
func TestRecallGraphDoesNotWalkThroughUnreadable(t *testing.T) {
	f, _ := recallFixture(t)
	f.svc.RecallGraph = true
	at := time.Now().UTC()
	for _, r := range []Record{
		{ID: "foreign", Scope: scopeB},
		{ID: "elsewhere", Scope: scopeA, Reach: ReachProject, ReachProject: "other-repo"},
		{ID: "hidden1", Scope: scopeA}, {ID: "hidden2", Scope: scopeA},
	} {
		r.Kind, r.Text, r.Status, r.ApprovedBy, r.Revision, r.Sources, r.CreatedAt, r.UpdatedAt = "decision", r.ID+" note", "active", "human", 1, refs(f.source()), at, at
		if err := f.repo.Insert(testCtx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	f.repo.relations = []fakeRelation{
		{SourceID: "gamma", TargetKind: "memory", TargetID: "foreign", Kind: "references"},
		{SourceID: "foreign", TargetKind: "memory", TargetID: "hidden1", Kind: "references"},
		{SourceID: "gamma", TargetKind: "memory", TargetID: "elsewhere", Kind: "references"},
		{SourceID: "elsewhere", TargetKind: "memory", TargetID: "hidden2", Kind: "references"},
	}
	out, err := f.svc.Search(testCtx, f.agent, SearchInput{Scope: scopeA, Query: "gamma", GraphDepth: 3, SearchCriteria: SearchCriteria{ProjectKey: "this-repo"}})
	if err != nil || !slices.Equal(ids(out.Items), []string{"gamma"}) {
		t.Fatal(ids(out.Items), err)
	}
	// The same walk from the other project reaches its memory and the one behind it.
	out, err = f.svc.Search(testCtx, f.agent, SearchInput{Scope: scopeA, Query: "gamma", GraphDepth: 3, SearchCriteria: SearchCriteria{ProjectKey: "other-repo"}})
	if got := ids(out.Items); err != nil || !slices.Contains(got, "elsewhere") || !slices.Contains(got, "hidden2") || slices.Contains(got, "hidden1") {
		t.Fatal(got, err)
	}
}

func TestRecallIncludeSourcesAndEntities(t *testing.T) {
	f, repo := entityFixture(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	m := entityMemory(t, f, repo, "m-include", "the billing service moved to the new queue", at)
	repo.ents["e-bill"] = NamedEntity{ID: "e-bill", Name: "billing service", Type: "service"}
	repo.ents["e-prop"] = NamedEntity{ID: "e-prop", Name: "proposed thing", Type: "service", Proposed: true}
	repo.edges = []EntityMention{{MemoryID: m.ID, EntityID: "e-bill", Relation: relMentions}, {MemoryID: m.ID, EntityID: "e-prop", Relation: relMentions}}
	plain, err := f.svc.Search(testCtx, f.agent, SearchInput{Scope: scopeA, Query: "billing service"})
	if err != nil || len(plain.Items) != 1 || plain.Items[0].SourceTraces != nil || plain.Items[0].Entities != nil {
		t.Fatalf("%+v %v", plain.Items, err)
	}
	out, err := f.svc.Search(testCtx, f.agent, SearchInput{Scope: scopeA, Query: "billing service", Include: []string{"sources", "entities"}})
	if err != nil || len(out.Items) != 1 {
		t.Fatal(err)
	}
	h := out.Items[0]
	if len(h.SourceTraces) != 1 || h.SourceTraces[0].State != "available" || h.SourceTraces[0].Excerpt != "source for m-include" {
		t.Fatalf("%+v", h.SourceTraces)
	}
	// Agents see confirmed entities only, as on the entity reads.
	if len(h.Entities) != 1 || h.Entities[0].Name != "billing service" {
		t.Fatalf("%+v", h.Entities)
	}
	// A restricted memory keeps its source reference but not the evidence text.
	stored := repo.Repository.(*fakeRepo).store[colMemories][recKey(scopeA, m.ID)]
	stored.Sensitivity = "restricted"
	repo.Repository.(*fakeRepo).store[colMemories][recKey(scopeA, m.ID)] = stored
	out, err = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "billing service", Include: []string{"sources"}})
	if err != nil || len(out.Items) != 1 || (len(out.Items[0].SourceTraces) != 1 || out.Items[0].SourceTraces[0].Excerpt != "") {
		t.Fatalf("%+v", out.Items[0].SourceTraces)
	}
	for _, bad := range [][]string{{"chunks"}, {"sources", "sources"}} {
		if _, err = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "q", Include: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatal(bad, err)
		}
	}
}
