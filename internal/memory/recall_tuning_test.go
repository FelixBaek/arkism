package memory

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTagTreeBoundsAndSQL(t *testing.T) {
	leaf := TagTest{Tags: []string{"a"}}
	deep := leaf
	for range tagGroupDepthMax {
		deep = TagTest{And: []TagTest{deep}}
	}
	wide := TagTest{Or: []TagTest{}}
	for range tagGroupsMax {
		wide.Or = append(wide.Or, TagTest{And: slices.Repeat([]TagTest{leaf}, tagGroupsMax)})
	}
	for _, bad := range []TagTest{
		{Tags: []string{"a"}, And: []TagTest{leaf}}, // a leaf and a group at once
		{And: []TagTest{leaf}, Or: []TagTest{leaf}},
		{And: []TagTest{leaf}, Match: "all"}, // match only on a leaf
		{Tags: []string{"a"}, Resolve: "phonetic"},
		{And: []TagTest{}},
		deep, // one level too deep
		wide, // past tagGroupNodesMax tests
	} {
		if validTagGroups([][]TagTest{{bad}}) {
			t.Fatalf("%+v must be refused", bad)
		}
	}
	args := []any{}
	next := 3
	sql := pgTagTests([]TagTest{{Not: true, And: []TagTest{leaf, {Or: []TagTest{{Tags: []string{"b"}, Match: "exact"}, leaf}}}}}, " OR ", &next, &args)
	if next != 6 || len(args) != 3 || !strings.HasPrefix(sql, "(NOT (COALESCE((doc->'tags' ?| $3") || !strings.Contains(sql, " AND (COALESCE(((doc->'tags' ?& $4") || !strings.Contains(sql, " OR COALESCE((doc->'tags' ?| $5") {
		t.Fatal(sql, next, args)
	}
}

// Tag values only ever travel as bind arguments; the SQL text holds placeholders and the
// fixed mode fragments, and the placeholders count up in order through any nesting.
func TestTagTreeSQLIsParameterized(t *testing.T) {
	evil := []string{"x') OR true --", `$1`, "a\"b"}
	tree := []TagTest{
		{Tags: evil, Match: "exact"},
		{Not: true, Or: []TagTest{{Tags: evil, Match: "any"}, {And: []TagTest{{Tags: evil, Match: "all", Not: true}, {Tags: evil}}}}},
	}
	args := []any{}
	next := 7
	sql := pgTagTests(tree, " OR ", &next, &args)
	if next != 11 || len(args) != 4 {
		t.Fatal(next, args)
	}
	for _, v := range evil[:1] {
		if strings.Contains(sql, v) {
			t.Fatalf("value %q in SQL: %s", v, sql)
		}
	}
	for i, a := range args {
		if !slices.Equal(a.([]string), evil) {
			t.Fatal(i, a)
		}
		if !strings.Contains(sql, fmt.Sprintf("$%d::text[]", 7+i)) {
			t.Fatalf("$%d missing: %s", 7+i, sql)
		}
	}
	if strings.Count(sql, "COALESCE((") != 4 || strings.Count(sql, "NOT ") != 2 || strings.Count(sql, "(") != strings.Count(sql, ")") {
		t.Fatal(sql)
	}
}

// tagVocabRepo serves the tag counts the fuzzy resolver reads (the operator count query).
type tagVocabRepo struct {
	Repository
	tags []string
}

func (r tagVocabRepo) OpsRecords(context.Context, opsRecordQuery) ([]Record, error) { return nil, nil }
func (r tagVocabRepo) OpsCounts(_ context.Context, q opsCountQuery) ([]OpsCount, error) {
	if q.By != "tag" || q.Collection != colMemories {
		return nil, ErrInvalid
	}
	out := []OpsCount{}
	for _, tag := range r.tags {
		out = append(out, OpsCount{Key: tag, Count: 1})
	}
	return out, nil
}

func TestFuzzyTagLeaf(t *testing.T) {
	if sim := trigramSimilarity(trigramSet("deployment"), trigramSet("deploymnt")); sim < tagFuzzySimilarity {
		t.Fatal(sim)
	}
	if sim := trigramSimilarity(trigramSet("mango"), trigramSet("mongo")); sim >= tagFuzzySimilarity {
		t.Fatal(sim)
	}
	vocab := tagVocab([]string{"deployment", "deploy", "lang:golang", "golang", "billing"})
	// A namespace must match; only the value is compared. The token itself always stays.
	if got := similarTags("lang:golan", vocab); !slices.Equal(got, []string{"lang:golan", "lang:golang"}) {
		t.Fatal(got)
	}
	if got := fuzzyLeaf(TagTest{Tags: []string{"deploymnt", "biling"}, Match: "all", Not: true}, vocab); !got.Not || len(got.And) != 2 || got.And[0].Match != "any" || !slices.Contains(got.And[1].Tags, "billing") {
		t.Fatalf("%+v", got)
	}
	if got := fuzzyLeaf(TagTest{Tags: []string{"deploymnt", "biling"}, Match: "exact"}, vocab); len(got.Or) == 0 || !slices.ContainsFunc(got.Or, func(x TagTest) bool { return slices.Equal(x.Tags, []string{"deployment", "billing"}) }) {
		t.Fatalf("%+v", got)
	}

	f, _ := recallFixture(t)
	f.editRecord(colMemories, "alpha", func(r *Record) { r.Tags = []string{"deployment"} })
	f.editRecord(colMemories, "delta", func(r *Record) { r.Tags = []string{"rollback"} })
	f.svc.Repo = tagVocabRepo{Repository: f.repo, tags: []string{"deployment", "rollback"}}
	search := func(test TagTest) ([]string, error) {
		out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", SearchCriteria: SearchCriteria{Filters: &SearchFilters{TagGroups: [][]TagTest{{test}}}}})
		return ids(out.Items), err
	}
	if got, err := search(TagTest{Tags: []string{"deploymnt"}, Resolve: tagResolveFuzzy}); err != nil || !slices.Equal(got, []string{"alpha"}) {
		t.Fatal(got, err)
	}
	// The literal leaf finds nothing; the fuzzy one never widens past similar spellings.
	if got, err := search(TagTest{Tags: []string{"deploymnt"}}); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	if got, err := search(TagTest{Tags: []string{"zebra"}, Resolve: tagResolveFuzzy}); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}

func TestLaneWeights(t *testing.T) {
	t.Setenv("ARKISM_RECALL_LANE_WEIGHTS", "graph=2, temporal=4,bogus=3,vector=99,keyword=x")
	if got := serverLaneWeights(); len(got) != 2 || got["graph"] != 2 || got["temporal"] != 4 {
		t.Fatal(got)
	}
	in := SearchInput{LaneWeights: map[string]float64{"temporal": 1, "exact": 0.5}, space: &SpaceConfig{RecallLaneWeights: map[string]float64{"temporal": 4, "graph": 2}}}
	if got := in.laneWeights(); len(got) != 2 || got["graph"] != 2 || got["exact"] != 0.5 {
		t.Fatal(got) // the request's temporal=1 resets the space's 4
	}
	a := Hit{ID: "a", Origin: "memory", Collection: colMemories, Reasons: []string{reasonTextSearch}}
	b := Hit{ID: "b", Origin: "memory", Collection: colMemories, Reasons: []string{reasonTemporal}}
	lists := [][]Hit{{a}, {Hit{ID: "x", Origin: "memory", Collection: colMemories, Reasons: []string{reasonTemporal}}, b}}
	if got := fuseWeighted(lists, 3, nil); got[0].ID != "a" {
		t.Fatal(got)
	}
	// Weight 2 makes the temporal lane's rank 2 tie the keyword lane's rank 1; above 2 it wins.
	if got := fuseWeighted(lists, 3, map[string]float64{"temporal": 2.5}); got[0].ID != "x" || got[1].ID != "b" {
		t.Fatal(got)
	}
	for _, bad := range []map[string]float64{{"temporal": 9}, {"temporal": 0.1}, {"chunks": 1}} {
		if validLaneWeights(bad) {
			t.Fatal(bad)
		}
		if validSpaceSettings(SpaceConfig{ExtractionMode: spaceExtractConcise, RecallLaneWeights: bad}) == nil {
			t.Fatal("space", bad)
		}
	}
	f, _ := recallFixture(t)
	// The server's env weights are the last layer; a space's own weights replace them whole.
	if c, src := f.svc.resolveSpaceSettings(SpaceConfig{}); c.RecallLaneWeights["temporal"] != 4 || src["recall_lane_weights"] != spaceSourceServer {
		t.Fatal(c.RecallLaneWeights, src)
	}
	if c, src := f.svc.resolveSpaceSettings(SpaceConfig{RecallLaneWeights: map[string]float64{"vector": 2}}); len(c.RecallLaneWeights) != 1 || src["recall_lane_weights"] != spaceSourceSpace {
		t.Fatal(c.RecallLaneWeights, src)
	}
	if _, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha", LaneWeights: map[string]float64{"exact": 20}}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha", Explain: true, LaneWeights: map[string]float64{"keyword": 3}})
	if err != nil || out.Explain.LaneWeights["keyword"] != 3 {
		t.Fatal(out.Explain, err)
	}
}

func TestTemporalWindowBoosts(t *testing.T) {
	f, now := recallFixture(t)
	day := now.AddDate(0, 0, -1)
	w := &TemporalWindow{From: time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location()), To: time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())}
	// No time words in the query: the window alone opens the temporal lane, and other matches stay.
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true, TemporalWindow: w})
	if err != nil || out.Explain.TimeWindow == nil || out.Explain.TimeWindow.Phrase != "temporal_window" || !slices.Contains(ids(out.Items), "yday") || !slices.Contains(ids(out.Items), "alpha") {
		t.Fatal(ids(out.Items), out.Explain, err)
	}
	for _, bad := range []*TemporalWindow{{From: w.To, To: w.From}, {From: time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC), To: w.To}} {
		if _, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha", TemporalWindow: bad}); !errors.Is(err, ErrInvalid) {
			t.Fatal(bad, err)
		}
	}
}

func TestMinScoreFloors(t *testing.T) {
	trace := &recallTrace{rerank: map[string]float64{}}
	near := Hit{ID: "near", Origin: "memory", Collection: colMemories, Reasons: []string{reasonVector}, VectorMatch: &VectorMatch{Distance: 0.2}}
	far := Hit{ID: "far", Origin: "memory", Collection: colMemories, Reasons: []string{reasonVector}, VectorMatch: &VectorMatch{Distance: 0.6}}
	both := Hit{ID: "both", Origin: "memory", Collection: colMemories, Reasons: []string{reasonTextSearch}, Text: "alpha deploy note"}
	half := Hit{ID: "half", Origin: "memory", Collection: colMemories, Reasons: []string{reasonTextSearch}, Text: "alpha only"}
	in := SearchInput{Query: "alpha deploy", MinScores: &MinScores{Semantic: 0.7, Keyword: 1}}
	got := laneFloors(in, [][]Hit{{near, far}, {both, half}, {far}}, trace)
	if !slices.Equal(ids(got[0]), []string{"near"}) || !slices.Equal(ids(got[1]), []string{"both"}) || len(got[2]) != 0 || trace.dropStages["semantic"] != 2 || trace.dropStages["keyword"] != 1 {
		t.Fatal(got, trace.dropStages)
	}
	// The reranker floor acts only after a rerank and drops what it did not score.
	rows := []ScoredHit{{Hit: near}, {Hit: far}, {Hit: both}}
	trace.rerank = map[string]float64{hitKey(near): 0.9, hitKey(far): 0.1}
	in.MinScores.Reranker = 0.5
	if got := rerankFloor(in, slices.Clone(rows), trace); len(got) != 3 {
		t.Fatal("no rerank, no floor", got)
	}
	trace.reranked = true
	if got := rerankFloor(in, slices.Clone(rows), trace); len(got) != 1 || got[0].ID != "near" || trace.dropStages["reranker"] != 2 {
		t.Fatal(got, trace.dropStages)
	}
	// The final floor is the higher of min_score and min_scores.final; bad floors are refused.
	f, _ := recallFixture(t)
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy", Explain: true, MinScores: &MinScores{Final: 1, Keyword: 1}})
	if err != nil || len(out.Items) != 0 || out.Explain.MinScoreDropped == 0 {
		t.Fatal(ids(out.Items), out.Explain, err)
	}
	if _, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha", MinScores: &MinScores{Semantic: 1.5}}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestSemanticNeighborLane(t *testing.T) {
	f, _ := recallFixture(t)
	vec := func(x float64) *Embedding {
		v := make([]float64, VectorDimension)
		v[0], v[1] = x, 1-x
		norm := 0.0
		for _, n := range v {
			norm += n * n
		}
		for i := range v {
			v[i] /= math.Sqrt(norm)
		}
		return &Embedding{Space: "test", Dimension: VectorDimension, Vector: v}
	}
	f.editRecord(colMemories, "alpha", func(r *Record) { r.Embedding = vec(1) })
	f.editRecord(colMemories, "gamma", func(r *Record) { r.Embedding = vec(0.95) }) // close to alpha, shares no words
	f.editRecord(colMemories, "beta", func(r *Record) { r.Embedding = vec(0) })
	got, err := f.svc.semanticNeighbors(testCtx, scopeA, []string{"alpha"}, 5)
	if err != nil || !slices.Equal(got, []string{"gamma"}) {
		t.Fatal(got, err)
	}
	on := true
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy note", Explain: true, space: &SpaceConfig{Lanes: &SpaceLanes{Semantic: &on}}})
	if err != nil || !slices.Contains(ids(out.Items), "gamma") || !slices.Contains(out.Explain.Lanes, "semantic") {
		t.Fatal(ids(out.Items), out.Explain, err)
	}
	// Off by default.
	out, err = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "alpha deploy note", Explain: true})
	if err != nil || slices.Contains(out.Explain.Lanes, "semantic") {
		t.Fatal(out.Explain, err)
	}
}

func TestIncludeBudgetsAndOptions(t *testing.T) {
	for _, tc := range []struct {
		include []string
		opts    *IncludeOptions
		ok      bool
	}{
		{[]string{"sources", "entities", "source_facts"}, nil, true},
		{[]string{"sources"}, &IncludeOptions{Sources: &IncludeBudget{MaxTokens: 100}}, true},
		{[]string{"source_facts"}, &IncludeOptions{SourceFacts: &IncludeBudget{MaxTokens: 100, MaxTokensPerObservation: 50}}, true},
		{[]string{"entities"}, &IncludeOptions{Sources: &IncludeBudget{MaxTokens: 100}}, false},                             // part not included
		{[]string{"sources"}, &IncludeOptions{Sources: &IncludeBudget{MaxTokens: 100, MaxTokensPerObservation: 10}}, false}, // per-observation only on source_facts
		{[]string{"source_facts"}, &IncludeOptions{SourceFacts: &IncludeBudget{MaxTokens: 10, MaxTokensPerObservation: 50}}, false},
		{[]string{"sources"}, &IncludeOptions{Sources: &IncludeBudget{MaxTokens: includeTokensMax + 1}}, false},
		{[]string{"chunks"}, nil, false},
	} {
		if validInclude(tc.include, tc.opts) != tc.ok {
			t.Fatalf("%v %+v", tc.include, tc.opts)
		}
	}
	p, err := newTokenPacker(5, 0)
	if err != nil || !p.fits("one two") || p.fits(strings.Repeat("word ", 20)) || p.used == 0 {
		t.Fatal(p, err)
	}
}

func TestIncludeSourceFacts(t *testing.T) {
	f, llm, ms := obsFixture(t)
	llm.answers = []string{obsAnswer(t, obsDraft("Staging deploys need the staging flag.", ms[0].ID, ms[1].ID))}
	out, err := f.consolidate(f.human, false)
	if err != nil || len(out.Proposed) != 1 {
		t.Fatal(err)
	}
	p := out.Proposed[0]
	if _, err := f.svc.ReviewObservation(testCtx, f.human, p.ID, ObservationReviewInput{Scope: scopeA, ExpectedRevision: p.Revision, Action: "approve"}); err != nil {
		t.Fatal(err)
	}
	obsHit := func(in SearchInput) Hit {
		res, err := f.svc.Search(testCtx, f.agent, in)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range res.Items {
			if h.ID == p.ID {
				return h
			}
		}
		t.Fatal("no observation hit", ids(res.Items))
		return Hit{}
	}
	h := obsHit(SearchInput{Scope: scopeA, Query: "staging flag", Limit: 20, Include: []string{"source_facts"}, PreferObservations: new(true)})
	if len(h.SourceFacts) != 2 || h.SourceFacts[0].Text == "" || !slices.ContainsFunc(h.SourceFacts, func(x IncludedFact) bool { return x.ID == ms[0].ID }) {
		t.Fatalf("%+v", h.SourceFacts)
	}
	// A per-observation budget too small for one fact leaves none; without include there are none.
	h = obsHit(SearchInput{Scope: scopeA, Query: "staging flag", Limit: 20, Include: []string{"source_facts"}, IncludeOptions: &IncludeOptions{SourceFacts: &IncludeBudget{MaxTokens: 100, MaxTokensPerObservation: 1}}})
	if len(h.SourceFacts) != 0 {
		t.Fatalf("%+v", h.SourceFacts)
	}
	if h = obsHit(SearchInput{Scope: scopeA, Query: "staging flag", Limit: 20}); h.SourceFacts != nil {
		t.Fatalf("%+v", h.SourceFacts)
	}
}

func TestIncludeEntityObservationsAndSourceBudget(t *testing.T) {
	f, repo := entityFixture(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	m := entityMemory(t, f, repo, "m-include", "the billing service moved to the new queue", at)
	repo.ents["e-bill"] = NamedEntity{ID: "e-bill", Name: "billing service", Type: "service", Summary: &DerivedRevision{Status: "approved", Text: "Bills customers monthly."}}
	repo.edges = []EntityMention{{MemoryID: m.ID, EntityID: "e-bill", Relation: relMentions}}
	obs := Record{ID: "obs-bill", Scope: scopeA, Kind: observationKind, Text: "Billing runs on the new queue.", Status: obsActive, ApprovedBy: "human-1", Revision: 1, Sources: m.Sources,
		Observation: &ObservationData{SourceMemories: []Ref{{Collection: colMemories, ID: m.ID, Revision: m.Revision}}, Durability: "durable"}, CreatedAt: at, UpdatedAt: at}
	if err := repo.Insert(testCtx, colObservations, obs); err != nil {
		t.Fatal(err)
	}
	search := func(opts *IncludeOptions) HitEntity {
		out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "billing service", Include: []string{"sources", "entities"}, IncludeOptions: opts})
		if err != nil || len(out.Items) != 1 || len(out.Items[0].Entities) != 1 {
			t.Fatalf("%+v %v", out.Items, err)
		}
		return out.Items[0].Entities[0]
	}
	// Names only without a budget, as before.
	if e := search(nil); e.Summary != "" || e.Observations != nil {
		t.Fatalf("%+v", e)
	}
	e := search(&IncludeOptions{Entities: &IncludeBudget{MaxTokens: 500}})
	if e.Summary != "Bills customers monthly." || len(e.Observations) != 1 || e.Observations[0].ID != "obs-bill" {
		t.Fatalf("%+v", e)
	}
	// A budget the summary uses up leaves the observation out.
	if e = search(&IncludeOptions{Entities: &IncludeBudget{MaxTokens: 8}}); e.Summary == "" || e.Observations != nil {
		t.Fatalf("%+v", e)
	}
	// A source budget too small for the excerpt blanks it but keeps the trace.
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "billing service", Include: []string{"sources"}, IncludeOptions: &IncludeOptions{Sources: &IncludeBudget{MaxTokens: 1}}})
	if err != nil || len(out.Items[0].SourceTraces) != 1 || out.Items[0].SourceTraces[0].Excerpt != "" {
		t.Fatalf("%+v %v", out.Items, err)
	}
}

func TestReflectTagFilters(t *testing.T) {
	groups := [][]TagTest{{{Not: true, Or: []TagTest{{Tags: []string{"draft"}}, {Tags: []string{"old"}}}}}}
	in := ReflectInput{Scope: scopeA, Query: "q", Tags: []string{"ops"}, TagsMatch: "all_strict", TagGroups: groups}
	if !validReflectInput(&in) {
		t.Fatal("valid")
	}
	if s := reflectSearch(in, "q"); s.Filters == nil || s.Filters.TagsMatch != "all_strict" || len(s.Filters.TagGroups) != 1 {
		t.Fatalf("%+v", s.Filters)
	}
	for _, bad := range []ReflectInput{{Scope: scopeA, Query: "q", Tags: []string{"a"}, TagsMatch: "most"}, {Scope: scopeA, Query: "q", TagGroups: [][]TagTest{{{}}}}} {
		if validReflectInput(&bad) {
			t.Fatalf("%+v", bad)
		}
	}
}

type denyObservationReads struct{ Authorizer }

func (a denyObservationReads) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if in.Action == "read:"+colObservations {
		return Decision{}, ErrDenied
	}
	return a.Authorizer.Authorize(ctx, token, in)
}

// Entity observations are only approved, unrestricted, same-scope ones, and none at all
// for a caller without the observation read grant.
func TestIncludeEntityObservationsStayInBounds(t *testing.T) {
	f, repo := entityFixture(t)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	m := entityMemory(t, f, repo, "m-bounds", "the billing service moved to the new queue", at)
	repo.ents["e-bill"] = NamedEntity{ID: "e-bill", Name: "billing service", Type: "service"}
	repo.edges = []EntityMention{{MemoryID: m.ID, EntityID: "e-bill", Relation: relMentions}}
	obs := func(id, approvedBy, sensitivity string, scope Scope) Record {
		return Record{ID: id, Scope: scope, Kind: observationKind, Text: "Billing note " + id, Status: obsActive, ApprovedBy: approvedBy, Sensitivity: sensitivity, Revision: 1, Sources: m.Sources,
			Observation: &ObservationData{SourceMemories: []Ref{{Collection: colMemories, ID: m.ID, Revision: m.Revision}}, Durability: "durable"}, CreatedAt: at, UpdatedAt: at}
	}
	for _, r := range []Record{obs("obs-ok", "human-1", "", scopeA), obs("obs-draft", "", "", scopeA), obs("obs-restricted", "human-1", "restricted", scopeA), obs("obs-far", "human-1", "", scopeFar)} {
		if err := repo.Insert(testCtx, colObservations, r); err != nil {
			t.Fatal(err)
		}
	}
	search := func() []IncludedFact {
		out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "billing service", Include: []string{"entities"}, IncludeOptions: &IncludeOptions{Entities: &IncludeBudget{MaxTokens: 2000}}})
		if err != nil || len(out.Items) != 1 || len(out.Items[0].Entities) != 1 {
			t.Fatalf("%+v %v", out.Items, err)
		}
		return out.Items[0].Entities[0].Observations
	}
	if got := search(); len(got) != 1 || got[0].ID != "obs-ok" {
		t.Fatalf("%+v", got)
	}
	f.svc.Auth = denyObservationReads{f.auth}
	if got := search(); got != nil {
		t.Fatalf("observations without the read grant: %+v", got)
	}
}
