package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeRelation is one relations-table row between a memory and another node.
type fakeRelation struct{ SourceID, TargetKind, TargetID, Kind string }

func (f *fakeRepo) SearchMemoriesInWindow(_ context.Context, in SearchInput, from, to time.Time) ([]Hit, error) {
	c, err := normalizeSearchCriteria(in.SearchCriteria)
	if err != nil {
		return nil, err
	}
	rows := []Record{}
	for _, r := range f.rows(colMemories) {
		if sameScope(r.Scope, in.Scope) {
			rows = append(rows, r)
		}
	}
	return windowHits(rows, c, time.Now().UTC(), timeWindow{From: from, To: to}, from.Location(), in.Limit), nil
}

// MemoryNeighbors mirrors memoryNeighborsSQL over f.relations (scope is ignored: one scope per test).
func (f *fakeRepo) MemoryNeighbors(_ context.Context, _ Scope, seeds []string, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	count, first := map[string]int{}, map[string]int{}
	add := func(id string, pos int) {
		if slices.Contains(seeds, id) {
			return
		}
		if _, ok := first[id]; !ok || pos < first[id] {
			first[id] = pos
		}
		count[id]++
	}
	for pos, seed := range seeds {
		for _, r := range f.relations {
			switch {
			case r.SourceID == seed && r.TargetKind == "memory":
				add(r.TargetID, pos)
			case r.TargetKind == "memory" && r.TargetID == seed:
				add(r.SourceID, pos)
			case r.SourceID == seed && r.Kind == "mentions":
				for _, o := range f.relations {
					if o.Kind == "mentions" && o.TargetKind == r.TargetKind && o.TargetID == r.TargetID {
						add(o.SourceID, pos)
					}
				}
			}
		}
	}
	ids := make([]string, 0, len(count))
	for id := range count {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := ids[i], ids[j]
		if count[a] != count[b] {
			return count[a] > count[b]
		}
		if first[a] != first[b] {
			return first[a] < first[b]
		}
		return a < b
	})
	return ids[:min(limit, len(ids))], nil
}

func TestParseTimeIntent(t *testing.T) {
	seoul := recallLocation("")
	nov5 := time.Date(2026, 11, 5, 10, 0, 0, 0, seoul)  // Thursday
	jan10 := time.Date(2027, 1, 10, 10, 0, 0, 0, seoul) // Sunday
	day := func(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, seoul) }
	for _, tc := range []struct {
		query    string
		now      time.Time
		from, to time.Time
	}{
		{"어제 뭐 했지", nov5, day(2026, 11, 4), day(2026, 11, 5)},
		{"what did I do yesterday?", nov5, day(2026, 11, 4), day(2026, 11, 5)},
		{"오늘 회의", nov5, day(2026, 11, 5), day(2026, 11, 6)},
		{"그저께 배포", nov5, day(2026, 11, 3), day(2026, 11, 4)},
		{"3일 전 오류", nov5, day(2026, 11, 2), day(2026, 11, 3)},
		{"2 days ago", nov5, day(2026, 11, 3), day(2026, 11, 4)},
		{"지난주 결정", nov5, day(2026, 10, 26), day(2026, 11, 2)},
		{"last week", nov5, day(2026, 10, 26), day(2026, 11, 2)},
		{"이번 주", nov5, day(2026, 11, 2), day(2026, 11, 9)},
		{"이번 달 작업", nov5, day(2026, 11, 1), day(2026, 12, 1)},
		{"last month", nov5, day(2026, 10, 1), day(2026, 11, 1)},
		{"10월 말 장애", nov5, day(2026, 10, 21), day(2026, 11, 1)},
		{"10월말", nov5, day(2026, 10, 21), day(2026, 11, 1)},
		{"10월 초", nov5, day(2026, 10, 1), day(2026, 10, 11)},
		{"end of October", nov5, day(2026, 10, 21), day(2026, 11, 1)},
		{"10월 31일", nov5, day(2026, 10, 31), day(2026, 11, 1)},
		{"October 31st", nov5, day(2026, 10, 31), day(2026, 11, 1)},
		{"31 Oct", nov5, day(2026, 10, 31), day(2026, 11, 1)},
		{"2026-10-31 release", nov5, day(2026, 10, 31), day(2026, 11, 1)},
		{"in October", nov5, day(2026, 10, 1), day(2026, 11, 1)},
		{"in May", nov5, day(2026, 5, 1), day(2026, 6, 1)},
		{"작년", nov5, day(2025, 1, 1), day(2026, 1, 1)},
		{"last year", nov5, day(2025, 1, 1), day(2026, 1, 1)},
		{"올해", nov5, day(2026, 1, 1), day(2027, 1, 1)},
		{"2025년 3월", nov5, day(2025, 3, 1), day(2025, 4, 1)},
		// A month after now without a year is last year's.
		{"12월", nov5, day(2025, 12, 1), day(2026, 1, 1)},
		{"12월 31일", nov5, day(2025, 12, 31), day(2026, 1, 1)},
		// Year rollover backwards from January.
		{"지난달", jan10, day(2026, 12, 1), day(2027, 1, 1)},
		{"last week", jan10, day(2026, 12, 28), day(2027, 1, 4)},
		{"어제", time.Date(2027, 1, 1, 9, 0, 0, 0, seoul), day(2026, 12, 31), day(2027, 1, 1)},
		{"in December", jan10, day(2026, 12, 1), day(2027, 1, 1)},
	} {
		w, ok := parseTimeIntent(tc.query, tc.now)
		if !ok || !w.From.Equal(tc.from) || !w.To.Equal(tc.to) {
			t.Errorf("%q: got %v..%v ok=%v, want %v..%v", tc.query, w.From, w.To, ok, tc.from, tc.to)
		}
	}
	for _, q := range []string{"deploy the build", "may I ask about march forward", "3개월 계획", "version 12", "2026-02-30 배포", "2월 30일", "february 30", "2026-13 plan"} {
		if w, ok := parseTimeIntent(q, nov5); ok {
			t.Errorf("%q: unexpected window %v", q, w)
		}
	}
	// The reader's zone moves the day boundary.
	utcNow := time.Date(2026, 11, 4, 20, 0, 0, 0, time.UTC) // already Nov 5 in Seoul
	if w, _ := parseTimeIntent("yesterday", utcNow); !w.From.Equal(time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(w)
	}
}

func TestRecallTEIRerankClient(t *testing.T) {
	var got teiRerankRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rerank" || json.NewDecoder(r.Body).Decode(&got) != nil {
			w.WriteHeader(400)
			return
		}
		_, _ = w.Write([]byte(`[{"index":1,"score":0.9},{"index":0,"score":0.1}]`))
	}))
	defer srv.Close()
	rr, err := NewTEIReranker(TEIRerankConfig{URL: srv.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	scores, err := rr.Rerank(testCtx, "q", []string{"a", "b"})
	if err != nil || !slices.Equal(scores, []float64{0.1, 0.9}) || got.Query != "q" || got.RawScores || !slices.Equal(got.Texts, []string{"a", "b"}) {
		t.Fatal(scores, err, got)
	}
	if _, err := NewTEIReranker(TEIRerankConfig{URL: "http://example.com"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("plain http to a remote host must be refused", err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"index":0,"score":0.5},{"index":0,"score":0.4}]`))
	}))
	defer bad.Close()
	rb, _ := NewTEIReranker(TEIRerankConfig{URL: bad.URL})
	if _, err := rb.Rerank(testCtx, "q", []string{"a", "b"}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("duplicate index must be refused", err)
	}
}

func rerankRowsFixture() []ScoredHit {
	return []ScoredHit{
		{Hit: Hit{ID: "a", Collection: colMemories, Text: "first"}, Score: 0.03},
		{Hit: Hit{ID: "b", Collection: colMemories, Text: "second"}, Score: 0.02},
		{Hit: Hit{ID: "c", Collection: colMemories, Text: "third"}, Score: 0.01},
	}
}

func TestRecallRerankBlendAndTimeoutFallback(t *testing.T) {
	d := Decision{ExternalTransfer: true, ContentSafe: true}
	in := SearchInput{Scope: scopeA, Query: "q", Limit: 3}
	var sent teiRerankRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`[{"index":0,"score":0.0},{"index":1,"score":0.1},{"index":2,"score":1.0}]`))
	}))
	defer srv.Close()
	f := newFixture(t)
	s := f.svc
	s.Reranker, _ = NewTEIReranker(TEIRerankConfig{URL: srv.URL})
	trace := &recallTrace{rerank: map[string]float64{}}
	rows, err := s.rerankRows(testCtx, in, d, rerankRowsFixture(), trace)
	// c: 0.7*1 + 0.3*(1/3) = 0.8 beats a: 0.7*0 + 0.3*1 = 0.3.
	if err != nil || rows[0].ID != "c" || rows[1].ID != "a" || !trace.reranked || trace.rerank[hitKey(rows[0].Hit)] != 1 {
		t.Fatal(rows, trace, err)
	}
	// Every rerank call leaves a provider usage row, like the query embedding.
	if usage := f.repo.rows(colProviderUsage); len(usage) != 1 || usage[0].Cost.Operation != "rerank" || usage[0].Status != JobStatusDone {
		t.Fatalf("%+v", usage)
	}
	// A restricted memory's text never leaves the host; its row keeps the fused score.
	restricted := rerankRowsFixture()
	restricted[1].Sensitivity = "restricted"
	if _, err = s.rerankRows(testCtx, in, d, restricted, &recallTrace{rerank: map[string]float64{}}); err != nil || slices.Contains(sent.Texts, "second") || len(sent.Texts) != 2 {
		t.Fatal(sent.Texts, err)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer slow.Close()
	s.Reranker, _ = NewTEIReranker(TEIRerankConfig{URL: slow.URL, Timeout: 50 * time.Millisecond})
	trace = &recallTrace{rerank: map[string]float64{}}
	rows, err = s.rerankRows(testCtx, in, d, rerankRowsFixture(), trace)
	m := s.Reranker.Metrics()
	if err != nil || rows[0].ID != "a" || rows[0].Score != 0.03 || trace.reranked || m.Calls != 1 || m.Fallbacks != 1 || m.Timeouts != 1 {
		t.Fatal(rows, m, err)
	}
	// No transfer permission: the reranker is never called.
	_, _ = s.rerankRows(testCtx, in, Decision{}, rerankRowsFixture(), trace)
	if s.Reranker.Metrics().Calls != 1 {
		t.Fatal("rerank without transfer permission")
	}
}

// recallFixture holds approved memories; "yday" was observed yesterday (Seoul).
func recallFixture(t *testing.T) (*fixture, time.Time) {
	f := newFixture(t)
	src := f.source()
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, recallLocation(""))
	f.svc.RecallNow = func() time.Time { return now }
	old := now.AddDate(0, -2, 0).UTC()
	observed := now.AddDate(0, 0, -1).UTC()
	for _, m := range []struct{ id, text string }{{"alpha", "alpha deploy note"}, {"beta", "beta rollback"}, {"gamma", "gamma incident"}, {"yday", "quiet change"}, {"delta", "alpha deploy follow up"}} {
		r := Record{ID: m.id, Scope: scopeA, Kind: "decision", Text: m.text, Keywords: []string{m.id}, Status: "active", ApprovedBy: "human", Revision: 1, Sources: refs(src), CreatedAt: old, UpdatedAt: old}
		if m.id == "yday" {
			r.MemoryContext = &MemoryContext{Domains: []string{"work"}, EvidenceKind: "user_statement", SemanticKind: "fact", ObservedAt: &observed}
		}
		if err := f.repo.Insert(testCtx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	return f, now
}

func ids(items []Hit) []string {
	out := make([]string, len(items))
	for i, h := range items {
		out[i] = h.ID
	}
	return out
}

func TestRecallTemporalLaneWithExplain(t *testing.T) {
	f, _ := recallFixture(t)
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "어제 뭐 바꿨지", Explain: true})
	if err != nil || !slices.Equal(ids(out.Items), []string{"yday"}) {
		t.Fatal(ids(out.Items), err)
	}
	h := out.Items[0]
	if h.Explain == nil || h.Explain.LaneRanks["temporal"] != 1 || h.Explain.FusedScore <= 0 || h.Explain.RerankScore != nil || !slices.Equal(h.Lanes, []string{"temporal"}) {
		t.Fatalf("%+v", h.Explain)
	}
	if out.Explain == nil || out.Explain.TimeWindow == nil || out.Explain.TimeWindow.Phrase != "어제" || !slices.Contains(out.Explain.Lanes, "temporal") {
		t.Fatalf("%+v", out.Explain)
	}
	// The temporal lane never filters: a plain match still comes back beside it.
	out, err = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "gamma 어제"})
	if err != nil || !slices.Contains(ids(out.Items), "yday") || out.Explain != nil || out.Items[0].Explain != nil {
		t.Fatal(ids(out.Items), err)
	}
	if _, err = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "어제", Timezone: "Mars/Base"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown timezone must be rejected", err)
	}
	// Explicit lanes keep exactly those lanes.
	out, err = f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "어제", Modes: []string{"exact", "fts"}})
	if err != nil || len(out.Items) != 0 {
		t.Fatal(ids(out.Items), err)
	}
}

func TestRecallGraphLaneWalksRelationsAndMentions(t *testing.T) {
	f, _ := recallFixture(t)
	f.svc.RecallGraph = true
	f.repo.relations = []fakeRelation{
		{SourceID: "gamma", TargetKind: "memory", TargetID: "beta", Kind: "references"},
		{SourceID: "gamma", TargetKind: "entity", TargetID: "e-db", Kind: "mentions"},
		{SourceID: "yday", TargetKind: "entity", TargetID: "e-db", Kind: "mentions"},
		{SourceID: "alpha", TargetKind: "memory", TargetID: "missing", Kind: "references"},
		{SourceID: "gamma", TargetKind: "memory", TargetID: "retired", Kind: "supersedes"},
	}
	// A neighbour that is no longer active is dropped by the same recheck as every lane.
	at := time.Now().UTC()
	if err := f.repo.Insert(testCtx, colMemories, Record{ID: "retired", Scope: scopeA, Kind: "decision", Text: "retired note", Status: "superseded", ApprovedBy: "human", Revision: 1, Sources: refs(f.source()), CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: "gamma", Explain: true})
	got := ids(out.Items)
	if err != nil || len(got) != 3 || got[0] != "gamma" || !slices.Contains(got, "beta") || !slices.Contains(got, "yday") {
		t.Fatal(got, err)
	}
	for _, h := range out.Items[1:] {
		if h.Explain.LaneRanks["graph"] == 0 || !slices.Equal(h.Lanes, []string{"graph"}) || h.Score >= out.Items[0].Score {
			t.Fatalf("%s %+v %v", h.ID, h.Explain, h.Lanes)
		}
	}
	if !slices.Contains(out.Explain.Lanes, "graph") {
		t.Fatal(out.Explain)
	}
}

// With every new feature off and no time words, results equal the plain fused,
// recency-weighted order, and turning the graph lane on without edges changes nothing.
func TestRecallRegressionGuardNewLanesOff(t *testing.T) {
	f, now := recallFixture(t)
	in := SearchInput{Scope: scopeA, Query: "alpha deploy", Limit: 8}
	lanes, err := f.repo.SearchCollection(testCtx, colMemories, in, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	want := decayByRecency(FuseScored(lanes, 24), now, nil, 8)
	got, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || len(got.Items) != len(want) || len(want) == 0 || got.Explain != nil {
		t.Fatal(ids(got.Items), err)
	}
	for i := range want {
		if got.Items[i].ID != want[i].ID || got.Items[i].Score != want[i].Score {
			t.Fatalf("item %d: got %s %v want %s %v", i, got.Items[i].ID, got.Items[i].Score, want[i].ID, want[i].Score)
		}
	}
	f.svc.RecallGraph = true
	again, err := f.svc.Search(testCtx, f.human, in)
	if err != nil || !slices.Equal(ids(again.Items), ids(got.Items)) {
		t.Fatal(ids(again.Items), err)
	}
}

// The window SQL must read stored times through ->>'$date': doc holds them as
// {"$date": ...} objects, and casting the object text to timestamptz errors.
func TestRecallWindowSQLReadsStoredDates(t *testing.T) {
	at := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	_, doc, err := encodeRecord(Record{ID: "m", Text: "x", CreatedAt: at, EffectiveAt: &at, MemoryContext: &MemoryContext{ObservedAt: &at}})
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]any
	if err := json.Unmarshal(doc, &d); err != nil {
		t.Fatal(err)
	}
	stored := map[string]any{"created_at": d["created_at"], "effective_at": d["effective_at"], "memory_context'->'observed_at": d["memory_context"].(map[string]any)["observed_at"]}
	for path, v := range stored {
		if obj, ok := v.(map[string]any); !ok || obj["$date"] == nil {
			t.Fatalf("%s stored as %v", path, v)
		}
		if !strings.Contains(recallWindowSQL, "doc->'"+path+"'->>'$date'") {
			t.Fatalf("recallWindowSQL does not read %s through $date", path)
		}
	}
}

func TestRecallContextBuildPassesExplainAndTimezone(t *testing.T) {
	f, _ := recallFixture(t)
	pack, err := f.svc.BuildContext(testCtx, f.human, ContextBuildInput{Scope: scopeA, Task: "어제 뭐 바꿨지", TokenEncoding: "cl100k_base", Explain: true})
	if err != nil || pack.Explain == nil || pack.Explain.TimeWindow == nil || len(pack.Items) == 0 || pack.Items[0].Explain == nil {
		t.Fatalf("%+v %v", pack.Explain, err)
	}
	if _, err = f.svc.BuildContext(testCtx, f.human, ContextBuildInput{Scope: scopeA, Task: "어제", TokenEncoding: "cl100k_base", Timezone: "Mars/Base"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown timezone must be rejected", err)
	}
}

func TestLocalLexicalRerank(t *testing.T) {
	f := newFixture(t)
	f.svc.Reranker = NewLocalReranker()
	in := SearchInput{Scope: scopeA, Query: "staging flag", Limit: 3}
	rows := rerankRowsFixture()
	rows[2].Text = "deploys to staging need the staging flag"
	trace := &recallTrace{rerank: map[string]float64{}}
	// Runs in process even without an external transfer decision.
	got, err := f.svc.rerankRows(testCtx, in, Decision{}, rows, trace)
	if err != nil || got[0].ID != "c" || trace.rerank[hitKey(got[0].Hit)] != 1 || !trace.reranked {
		t.Fatal(got, trace, err)
	}
	if usage := f.repo.rows(colProviderUsage); len(usage) != 1 || usage[0].Cost.Provider != "local" || usage[0].Cost.Model != "lexical" {
		t.Fatalf("%+v", usage)
	}
	if scores := localRerank("absent", []string{"a", "b"}); !slices.Equal(scores, []float64{0, 0}) {
		t.Fatal(scores)
	}
}
