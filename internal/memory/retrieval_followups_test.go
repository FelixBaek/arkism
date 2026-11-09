//go:build linux

package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"
)

func TestRetrievalTemporalSupersetCannotStarveActualEvent(t *testing.T) {
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	at := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	src := f.source()
	makeRow := func(id string, inside bool) Record {
		event := at.AddDate(0, -1, 0)
		if inside {
			event = at
		}
		return Record{ID: id, Scope: scopeA, Kind: "episodic", Status: statusActive, Revision: 1, Text: "inspection fact", Sources: refs(src), ApprovedBy: "human-1", CreatedAt: at, UpdatedAt: at.Add(time.Hour), Occurred: &RecallTimeWindow{From: event, To: event.Add(time.Hour)}}
	}
	for i := 0; i < 300; i++ {
		r := makeRow(fmt.Sprintf("distractor-%03d", i), false)
		insertPGRecords(t, p, colMemories, r)
	}
	answer := makeRow("answer", true)
	answer.UpdatedAt = at.Add(-time.Hour)
	insertPGRecords(t, p, colMemories, answer)
	q := SearchInput{Scope: scopeA, Limit: 3, Query: "unmatched token", TemporalWindow: &TemporalWindow{From: at, To: at.AddDate(0, 0, 1)}, Explain: true}
	direct, err := p.SearchMemoriesInWindow(testCtx, q, q.TemporalWindow.From, q.TemporalWindow.To)
	if err != nil || len(direct) != 1 || direct[0].ID != answer.ID {
		t.Fatal("event behind SQL superset lost", ids(direct), err)
	}
	out, err := f.svc.Search(testCtx, f.human, q)
	if err != nil || !slices.Contains(ids(out.Items), answer.ID) {
		t.Fatal("actual recall lost event", ids(out.Items), err)
	}
	// Malformed/free-text valid_from must safely fall back to the stored timestamp.
	fallback := makeRow("fallback", false)
	fallback.Occurred = nil
	fallback.MemoryContext = &MemoryContext{ValidFrom: "not a calendar date"}
	insertPGRecords(t, p, colMemories, fallback)
	hits, err := p.SearchMemoriesInWindow(testCtx, q, at, at.AddDate(0, 0, 1))
	if err != nil || !slices.Contains(ids(hits), fallback.ID) {
		t.Fatal("free text date changed semantics", err)
	}
	ctx, cancel := context.WithCancel(testCtx)
	cancel()
	if _, err = p.SearchMemoriesInWindow(ctx, q, at, at.AddDate(0, 0, 1)); err == nil {
		t.Fatal("cancelled scan succeeded")
	}
}

func TestRetrievalGraphHubOrderIndependentOfInsertion(t *testing.T) {
	p := isolatedPostgres(t)
	for _, branch := range []string{"outgoing", "incoming", "shared_topic", "topic_fanout"} {
		t.Run(branch, func(t *testing.T) {
			var results [][]string
			for _, reverse := range []bool{false, true} {
				results = append(results, graphHubNeighbors(t, p, branch, reverse))
			}
			if !slices.Equal(results[0], results[1]) || results[0][0] != "n001" || results[0][len(results[0])-1] != fmt.Sprintf("n%03d", len(results[0])) {
				t.Fatal("hub limit is physical-order dependent", results)
			}
		})
	}
}

// graphHubNeighbors builds a 60-edge hub around n000 for one branch, inserting
// edges in forward or reverse order, and returns its neighbors without index scans.
func graphHubNeighbors(t *testing.T, p *Postgres, branch string, reverse bool) []string {
	t.Helper()
	scope := Scope{WorkspaceID: "graph-order", ProjectID: branch + fmt.Sprint(reverse)}
	for _, kind := range []string{"memory", "topic"} {
		if _, err := p.pool.Exec(testCtx, `INSERT INTO entities(organization_id,project_id,kind,external_id) SELECT $1,$2,$3,'n'||lpad(i::text,3,'0') FROM generate_series(0,60) i`, scope.WorkspaceID, scope.ProjectID, kind); err != nil {
			t.Fatal(err)
		}
	}
	link := func(source, targetKind, target, relation string) {
		t.Helper()
		if _, err := p.pool.Exec(testCtx, `INSERT INTO relations(organization_id,project_id,source_kind,source_id,target_kind,target_id,relation_kind) VALUES($1,$2,'memory',$3,$4,$5,$6)`, scope.WorkspaceID, scope.ProjectID, source, targetKind, target, relation); err != nil {
			t.Fatal(err)
		}
	}
	if branch == "shared_topic" {
		link("n000", "topic", "n000", "mentions")
	}
	for n := 1; n <= 60; n++ {
		i := n
		if reverse {
			i = 61 - n
		}
		linkGraphHubEdge(link, branch, fmt.Sprintf("n%03d", i))
	}
	var got []string
	err := p.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		pg := tx.(*Postgres)
		if _, e := pg.q().Exec(ctx, `SET LOCAL enable_indexscan=off; SET LOCAL enable_bitmapscan=off`); e != nil {
			return e
		}
		var e error
		got, e = pg.MemoryNeighbors(ctx, scope, []string{"n000"}, 30)
		return e
	})
	expected := graphFanOut
	if branch == "shared_topic" {
		expected--
	} // The seed consumes one shared-topic slot, then is excluded.
	if err != nil || len(got) != expected {
		t.Fatal(got, err)
	}
	return got
}

func linkGraphHubEdge(link func(source, targetKind, target, relation string), branch, id string) {
	switch branch {
	case "outgoing":
		link("n000", "memory", id, "relates")
	case "incoming":
		link(id, "memory", "n000", "relates")
	case "shared_topic":
		link(id, "topic", "n000", "mentions")
	case "topic_fanout":
		link("n000", "topic", id, "mentions")
		link(id, "topic", id, "mentions")
	}
}

func assertSearchTokensNFC(t *testing.T) {
	t.Helper()
	for _, text := range []string{"café résumé", "서버를 배포한다", "किताब समीक्षा"} {
		if !slices.Equal(SearchTokens(text), SearchTokens(norm.NFD.String(text))) {
			t.Fatal("canonically equal text differs", text)
		}
	}
	if slices.Equal(SearchTokens("resume"), SearchTokens("résumé")) {
		t.Fatal("distinct accents collapsed")
	}
}

func TestRetrievalNFCQueryDocumentAndResumableBackfill(t *testing.T) {
	assertSearchTokensNFC(t)
	p, scope := openPostgresListsTest(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for i := 0; i < 510; i++ {
		r := Record{ID: fmt.Sprintf("nfc-%03d", i), Scope: scope, Kind: "fact", Status: statusActive, Revision: 1, ApprovedBy: "human", Text: norm.NFD.String("café 서버"), CreatedAt: now, UpdatedAt: now}
		insertPGRecords(t, p, colMemories, r)
	}
	knowledge := Record{ID: "nfc-knowledge", Scope: scope, Kind: "chunk", Status: statusActive, Revision: 1, Text: norm.NFD.String("café 서버"), CreatedAt: now, UpdatedAt: now}
	insertPGRecords(t, p, colKnowledgeChunks, knowledge)
	before, err := p.Get(testCtx, colMemories, scope, "nfc-000")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.pool.Exec(testCtx, `UPDATE memory_records SET search_tokens=to_tsvector('simple',doc->>'text'),search_tokens_version=1 WHERE workspace_id=$1`, scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if n, more, err := p.BackfillSearchTokens(testCtx, time.Nanosecond); err != nil || n != 0 || !more {
		t.Fatal("budget cannot resume", n, more, err)
	}
	if n, more, err := p.BackfillSearchTokens(testCtx, time.Minute); err != nil || n != 511 || more {
		t.Fatal("version backfill", n, more, err)
	}
	for _, query := range []string{"café", "cafe\u0301", "서버"} {
		hits, err := p.Search(testCtx, scope, query, nil, "", 5, "fts")
		if err != nil || len(hits[1]) != 5 {
			t.Fatal(query, hits, err)
		}
	}
	ranked, err := p.RankKnowledgeText(testCtx, scope, []string{knowledge.ID}, knowledgeQueryTerms("café"))
	if err != nil || len(ranked) != 1 {
		t.Fatal("knowledge was not upgraded", ranked, err)
	}
	after, err := p.Get(testCtx, colMemories, scope, before.ID)
	if err != nil || structuredText(before) != structuredText(after) {
		t.Fatal("backfill changed canonical source", err)
	}
	if n, more, err := p.BackfillSearchTokens(testCtx, time.Minute); err != nil || n != 0 || more {
		t.Fatal("backfill replay", n, more, err)
	}
}

func TestRetrievalExplicitWindowsReachHTTPAndDeduplicate(t *testing.T) {
	f := newFixture(t)
	a := f.activeMemory("copper first delivery")
	b := f.activeMemory("copper second delivery")
	first := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	second := first.AddDate(0, 0, 7)
	for id, at := range map[string]time.Time{a.ID: first, b.ID: second} {
		f.editRecord(colMemories, id, func(r *Record) { r.Occurred = &RecallTimeWindow{From: at, To: at.Add(time.Hour)} })
	}
	windows := []TemporalWindow{{From: first, To: first.AddDate(0, 0, 1)}, {From: second, To: second.AddDate(0, 0, 1)}}
	body := map[string]any{"scope": scopeA, "task": "unmatched tokens", "modes": []string{"fts"}, "temporal_windows": windows, "explain": true, "token_encoding": "cl100k_base", "budget_tokens": 4000}
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/v1/context/build", strings.NewReader(structuredText(body)))
	req.Header.Set("Authorization", "Bearer "+f.human)
	Handler(f.svc).ServeHTTP(w, req)
	var out ContextPack
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || !slices.Contains(ids(out.Items), a.ID) || !slices.Contains(ids(out.Items), b.ID) || len(out.Explain.TimeWindows) != 2 || !strings.Contains(out.ContextData, "first delivery") || !strings.Contains(out.ContextData, "second delivery") {
		t.Fatal(w.Code, w.Body.String())
	}
	if dest := os.Getenv("ARKISM_TEMPORAL_DELIVERY_CASES"); dest != "" {
		cases := []apiContractCase{{Operation: "POST /v1/context/build", Status: w.Code, Body: append(json.RawMessage(nil), w.Body.Bytes()...), Request: json.RawMessage(structuredText(body))}}
		data, e := json.Marshal(cases)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(dest, data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	clock := time.Now().UTC().Truncate(time.Second)
	q := SearchInput{Scope: scopeA, Query: "unmatched tokens", Modes: []string{"fts"}, TemporalWindows: windows, Explain: true, QueryTime: &clock}
	base := f.search(f.human, q)
	q.TemporalWindows = append(slices.Clone(windows), windows[0])
	dupes := f.search(f.human, q)
	if len(base.Items) != len(dupes.Items) {
		t.Fatal("duplicate interval duplicated hits")
	}
	for i := range base.Items {
		if base.Items[i].ID != dupes.Items[i].ID || base.Items[i].Score != dupes.Items[i].Score {
			t.Fatal("duplicate interval inflated rank")
		}
	}
	q.TemporalWindow = &windows[0]
	if _, err := f.svc.Search(testCtx, f.human, q); !errors.Is(err, ErrInvalid) {
		t.Fatal("ambiguous options accepted", err)
	}
	q.TemporalWindow = nil
	q.TemporalWindows = []TemporalWindow{}
	if _, err := f.svc.Search(testCtx, f.human, q); !errors.Is(err, ErrInvalid) {
		t.Fatal("empty explicit list accepted", err)
	}
	q.TemporalWindows = append(windows, windows...)
	q.TemporalWindows = append(q.TemporalWindows, windows[0])
	if _, err := f.svc.Search(testCtx, f.human, q); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded intervals accepted", err)
	}
}

func TestRetrievalLaneWeightRejectsNonfiniteEnvironment(t *testing.T) {
	t.Setenv("ARKISM_RECALL_LANE_WEIGHTS", "graph=NaN,vector=+Inf,keyword=-Inf,exact=0.25,temporal=8")
	got := serverLaneWeights()
	if len(got) != 2 || got["exact"] != 0.25 || got["temporal"] != 8 {
		t.Fatal(got)
	}
	if validLaneWeights(map[string]float64{"graph": math.NaN()}) || validLaneWeights(map[string]float64{"graph": math.Inf(1)}) {
		t.Fatal("nonfinite internal policy accepted")
	}
}

func TestRetrievalExplicitWindowsNativeFallbackAndPinnedRejection(t *testing.T) {
	runtime := nativeFSRuntime(t, "on", "V3-P1-NOTIME")
	f, q, _ := nativeFSSearchFixture(t, runtime)
	q.IncludeInactive = false
	clock := time.Now().UTC().Truncate(time.Second)
	q.QueryTime = &clock
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	q.TemporalWindows = []TemporalWindow{{From: at, To: at.AddDate(0, 0, 1)}}
	baseline, err := f.svc.Search(testCtx, "native", q)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.NativeSearchShadow = runtime
	out, err := f.svc.Search(testCtx, "native", q)
	if err != nil {
		t.Fatal(err)
	}
	observations := runtime.Observations()
	if nativeFSStrip(out) != nativeFSStrip(baseline) || len(observations) != 1 || observations[0].Reason != "unsupported_context" || !observations[0].BaselineReturned || observations[0].Served {
		t.Fatal("explicit windows overridden", out, observations)
	}
	f = newFixture(t)
	_, err = f.svc.BuildContext(testCtx, f.human, ContextBuildInput{Scope: scopeA, Task: "copper", Agent: "test-agent", TemporalWindows: q.TemporalWindows, TokenEncoding: "cl100k_base"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("pinned windows silently ignored", err)
	}
}
