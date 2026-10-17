package memory

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

// TC-BASE-001: FuseScored keeps Fuse's order and exposes the RRF score and lanes.
func TestFuseScoredMatchesFuse(t *testing.T) {
	a := Hit{ID: "a", Collection: colMemories, Origin: "memory", Reasons: []string{"exact_keyword"}}
	b := Hit{ID: "b", Collection: colMemories, Origin: "memory", Reasons: []string{"text_search"}}
	lists := [][]Hit{{a}, {b, a}, {}}
	scored, plain := FuseScored(lists, 6), Fuse(lists, 6)
	if len(scored) != 2 || len(plain) != 2 || scored[0].ID != "a" || plain[0].ID != "a" || scored[1].ID != "b" {
		t.Fatalf("order scored=%v plain=%v", scored, plain)
	}
	// a: exact rank 1 (weight 2) + fts rank 2, which also carries the exact reason.
	if want := 2.0/61 + 2.0/62; math.Abs(scored[0].Score-want) > 1e-12 || len(scored[0].Lanes) != 2 {
		t.Fatalf("a score=%v lanes=%v want %v", scored[0].Score, scored[0].Lanes, want)
	}
	if want := 1.0 / 61; math.Abs(scored[1].Score-want) > 1e-12 || scored[1].Lanes[0] != 1 {
		t.Fatalf("b score=%v lanes=%v", scored[1].Score, scored[1].Lanes)
	}
}

// TC-BASE-002: relevance, lanes and cut candidates are computed from labels only.
func TestSummarizeBaselineRelevanceAndCuts(t *testing.T) {
	hit := func(score float64, label int, dist float64, lanes ...int) baselineHit {
		return baselineHit{score: score, label: label, distance: dist, hasDist: true, lanes: lanes, chars: 100}
	}
	results := []baselineResult{
		{labeled: true, hits: []baselineHit{hit(0.05, 1, 0.2, 0, 1, 2), hit(0.02, 0, 0.5, 2)}},
		{labeled: true, hits: []baselineHit{hit(0.03, 0, 0.4, 1), hit(0.04, 1, 0.3, 1, 2)}},
		{labeled: true},
		{negative: true, hits: []baselineHit{hit(0.016, -1, 0.6, 2), hit(0.015, -1, 0.7, 2)}},
		{negative: true},
	}
	rep := summarizeBaseline(results, true)
	if rep.Queries["total"] != 5 || rep.Queries["labeled"] != 3 || rep.Queries["negative"] != 2 || rep.Queries["empty"] != 2 || rep.Queries["negative_empty"] != 1 {
		t.Fatalf("queries=%v", rep.Queries)
	}
	if rep.Relevance["hit_at_6"] != round4(2.0/3) || rep.Relevance["mrr"] != round4((1+0.5)/3) {
		t.Fatalf("relevance=%v", rep.Relevance)
	}
	if rep.Lanes["exact+fts+vector"] != 1 || rep.Lanes["vector"] != 3 || rep.Lanes["fts+vector"] != 1 {
		t.Fatalf("lanes=%v", rep.Lanes)
	}
	if rep.RecallChars.Max != 200 || rep.RelevantScore.N != 2 || rep.NegativeScore.N != 2 || rep.NegativeTop1.N != 1 {
		t.Fatalf("distributions %+v", rep)
	}
	var score, dist *BaselineCut
	for i, c := range rep.Cuts {
		if c.Rule == "best_separation" && c.Signal == "rrf_score" {
			score = &rep.Cuts[i]
		}
		if c.Rule == "best_separation" && c.Signal == "vector_distance" {
			dist = &rep.Cuts[i]
		}
	}
	if score == nil || score.Value != 0.04 || score.KeepRelevant != 1 || score.DropNegative != 1 {
		t.Fatalf("score cut=%+v", score)
	}
	if dist == nil || dist.Value != 0.3 || dist.KeepRelevant != 1 || dist.DropNegative != 1 {
		t.Fatalf("distance cut=%+v", dist)
	}
	if cutCandidates("rrf_score", nil, []float64{1}, true) != nil {
		t.Fatal("a cut needs both relevant and negative values")
	}
}

// TC-BASE-003: outcome pass rates per dimension, ignoring inactive or untargeted rows.
func TestSummarizeOutcomes(t *testing.T) {
	now := time.Now().UTC()
	ev := func(target string, dim string, passed bool, age time.Duration) Record {
		return Record{Kind: "memory_outcome", Status: "active", Target: &Ref{Collection: colMemories, ID: target, Revision: 1}, Tags: []string{"dimension:" + dim}, Passed: passed, CreatedAt: now.Add(-age)}
	}
	rows := []Record{
		ev("m1", "personal_compliance", true, time.Hour), ev("m1", "personal_compliance", true, time.Hour),
		ev("m1", "personal_compliance", false, 40*24*time.Hour), ev("m2", "personal_compliance", false, time.Hour),
		ev("m2", "work_task", true, time.Hour),
		{Kind: "memory_outcome", Status: "superseded", Target: &Ref{ID: "m3"}, Tags: []string{"dimension:personal_compliance"}},
	}
	out := summarizeOutcomes(rows, now)
	pc := out["personal_compliance"]
	if pc.Results != 4 || pc.Passed != 2 || pc.PassRate != 0.5 || pc.Memories != 2 || pc.Last30Results != 3 || pc.Last30PassRate != round4(2.0/3) {
		t.Fatalf("personal_compliance=%+v", pc)
	}
	if pc.PerMemoryRate.N != 1 || pc.PerMemoryRate.P50 != round6(2.0/3) {
		t.Fatalf("per memory=%+v", pc.PerMemoryRate)
	}
	if out["work_task"].Results != 1 || out["work_task"].PassRate != 1 {
		t.Fatalf("work_task=%+v", out["work_task"])
	}
}

// TC-BASE-007: hits past the character budget are dropped, as Service.Search drops them.
func TestBaselineHitsStopAtCharBudget(t *testing.T) {
	long := strings.Repeat("x", baselineMaxChars)
	fused := []ScoredHit{
		{Hit: Hit{ID: "a", Text: long}, Score: 0.03},
		{Hit: Hit{ID: "b", Text: "short"}, Score: 0.02},
	}
	out := baselineHits(BaselineQuery{Relevant: []string{"b"}}, fused, nil)
	if len(out.hits) != 1 || out.hits[0].chars != baselineMaxChars || out.hits[0].label != -1 {
		t.Fatalf("hits=%+v", out.hits)
	}
	if rep := summarizeBaseline([]baselineResult{out}, false); rep.Relevance["hit_at_6"] != 0 {
		t.Fatalf("a dropped hit counted as found: %v", rep.Relevance)
	}
}

type baselineEmbedder struct {
	marker string
	seen   *[]string
}

func (e baselineEmbedder) Space() string { return "baseline-test" }
func (e baselineEmbedder) Embed(_ context.Context, texts []string, _ string) ([]Embedding, error) {
	if e.seen != nil {
		*e.seen = append(*e.seen, texts...)
	}
	out := make([]Embedding, len(texts))
	for i, text := range texts {
		v := make([]float64, VectorDimension)
		if strings.Contains(text, e.marker) {
			v[0] = 1
		} else {
			v[1] = 1
		}
		out[i] = Embedding{Space: e.Space(), Dimension: VectorDimension, Vector: v}
	}
	return out, nil
}

// TC-BASE-004: the Postgres baseline runs read only, finds known items and prints no
// memory text, question text or id.
func TestRecallBaselinePostgresReadOnly(t *testing.T) {
	ctx := context.Background()
	p := isolatedPostgres(t)
	scope := Scope{WorkspaceID: "baseline-" + ID(), ProjectID: "p", UserID: "u", AgentID: ""}
	now := time.Now().UTC()
	marker := "baselinemarker" + strings.ToLower(ID()[:8])
	if err := p.Insert(ctx, "sources", Record{ID: "src-1", Scope: scope, Kind: "source", Status: "active", Revision: 1, Text: "source", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var seen []string
	emb := baselineEmbedder{marker: marker, seen: &seen}
	for i, text := range []string{marker + " worktree rule", marker + " report in Korean", "unrelated garden note"} {
		r := Record{ID: "mem-" + string(rune('a'+i)), Scope: scope, Kind: "procedural", Status: "active", Revision: 1, Text: text, Summary: text, Keywords: []string{marker}, ApprovedBy: "human", Sources: []SourceRef{{SourceID: "src-1", Locator: "test"}}, CreatedAt: now, UpdatedAt: now}
		vec, _ := emb.Embed(ctx, []string{text}, "document")
		r.Embedding = &Embedding{Space: emb.Space(), Dimension: VectorDimension, Vector: vec[0].Vector, ContentHash: ContentHash(recordContent(r)), EmbeddedAt: now}
		if err := p.Insert(ctx, colMemories, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Insert(ctx, "evaluations", Record{ID: "ev-1", Scope: scope, Kind: "memory_outcome", Status: "active", Revision: 1, Text: "checker", Target: &Ref{Collection: colMemories, ID: "mem-a", Revision: 1}, Tags: []string{"dimension:personal_compliance"}, Passed: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// TC-BASE-008: a query failing safeTransfer is searched without being embedded.
	unsafe := marker + " mail owner@example.com"
	rep, err := p.RecallBaseline(ctx, scope, []BaselineQuery{{Query: marker + " worktree", Relevant: []string{"mem-a"}}, {Query: unsafe, Relevant: []string{"mem-b"}}}, true, emb)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(seen, unsafe) || rep.Queries["vector_disabled"] != 1 {
		t.Fatalf("unsafe query embedded: vector_disabled=%d", rep.Queries["vector_disabled"])
	}
	if !rep.ReadOnlyTxHeld || !rep.VectorEnabled || rep.Queries["labeled"] != 5 || rep.Queries["negative"] != len(baselineNegatives) {
		t.Fatalf("report=%+v", rep)
	}
	if rep.Relevance["hit_at_6"] != 1 || rep.RelevantDist.N == 0 || rep.Outcomes["personal_compliance"].Results != 1 {
		t.Fatalf("relevance=%v dist=%+v outcomes=%+v", rep.Relevance, rep.RelevantDist, rep.Outcomes)
	}
	raw, _ := json.Marshal(rep)
	for _, secret := range []string{marker, "worktree", "garden", "mem-a", "src-1", scope.WorkspaceID} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("report leaks %q", secret)
		}
	}
}
