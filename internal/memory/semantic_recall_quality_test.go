package memory

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

type semanticQualityFixture struct {
	Model, Revision     string
	QueryPrefix         string `json:"query_prefix"`
	DocumentPrefix      string `json:"document_prefix"`
	DocumentInputFormat string `json:"document_input_format"`
	Space               string
	MaxDistance         float64 `json:"max_distance"`
	Documents           []struct {
		ID, Text string
		Vector   []float64
	}
	Queries []struct {
		Category, Text string
		Relevant       []string
		Vector         []float64
	}
}

type frozenSemanticEmbedder struct {
	space   string
	vectors map[string][]float64
}

func (e frozenSemanticEmbedder) Space() string { return e.space }
func (e frozenSemanticEmbedder) Embed(_ context.Context, texts []string, purpose string) ([]Embedding, error) {
	if purpose != "query" {
		return nil, ErrInvalid
	}
	var out []Embedding
	for _, text := range texts {
		vector, ok := e.vectors[text]
		if !ok {
			return nil, ErrInvalid
		}
		out = append(out, Embedding{Space: e.space, Dimension: VectorDimension, Vector: vector, ContentHash: ContentHash(text)})
	}
	return out, nil
}

// Public synthetic labels are agent-authored, not human approval. Frozen real
// local-model vectors replay through PostgreSQL, source/permission fences,
// fusion, JSON and the default host budget. No model is contacted in this test.
func TestPostgresSemanticRecallQuality(t *testing.T) {
	raw, err := os.ReadFile("testdata/semantic_recall_harrier.json")
	if err != nil {
		t.Fatal(err)
	}
	var data semanticQualityFixture
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	embedder, err := NewTEIEmbedder(TEIConfig{EndpointURL: "http://127.0.0.1:1/embed", Model: data.Model, Revision: data.Revision, Prefixes: map[string]string{"query": data.QueryPrefix, "document": data.DocumentPrefix}})
	if err != nil || embedder.Space() != data.Space || data.MaxDistance != .38 || len(data.Queries) != 30 || data.DocumentInputFormat != "recordContent: text + newline + empty summary" {
		t.Fatal("fixture identity/cutoff changed", err)
	}
	p := isolatedPostgres(t)
	p.SetVectorMaxDistance(data.MaxDistance)
	f := newFixture(t)
	f.svc.Repo = p
	ids := map[string]string{}
	for _, doc := range data.Documents {
		r := f.activeMemory(doc.Text)
		r.Embedding = &Embedding{Provider: "tei", Model: data.Model, Revision: data.Revision, Space: data.Space, Dimension: VectorDimension, Vector: doc.Vector, ContentHash: ContentHash(recordContent(r)), EmbeddedAt: time.Now().UTC()}
		if err := p.Replace(testCtx, colMemories, r, r.Revision); err != nil {
			t.Fatal(err)
		}
		ids[r.ID] = doc.ID
	}
	frozen := frozenSemanticEmbedder{space: data.Space, vectors: map[string][]float64{}}
	for _, q := range data.Queries {
		frozen.vectors[q.Text] = q.Vector
	}
	f.svc.Embedder = frozen
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "")
	t.Setenv("ARKISM_RECALL_BUDGET", "")
	budget := host.LoadRecallBudget()
	for _, mode := range []string{"vector", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			type counts struct{ Queries, ServerTarget, BeforeTarget, AfterTarget, BeforeReturned, AfterReturned, WrongReturned int }
			stats := map[string]*counts{}
			for _, q := range data.Queries {
				in := SearchInput{Scope: scopeA, Query: q.Text, Limit: 6, MaxChars: 6000}
				if mode == "vector" {
					in.Modes = []string{"vector"}
				}
				out, err := f.svc.Search(testCtx, f.human, in)
				if err != nil || len(out.Degraded) != 0 {
					t.Fatal("frozen search", err, out.Degraded)
				}
				wire, err := json.Marshal(out.Items)
				if err != nil {
					t.Fatal(err)
				}
				var items, legacy []any
				if json.Unmarshal(wire, &items) != nil || json.Unmarshal(wire, &legacy) != nil {
					t.Fatal("wire decode")
				}
				for _, i := range legacy {
					delete(i.(map[string]any), "vector_match")
				}
				before, _, _ := budget.Apply(legacy, true)
				after, _, _ := budget.Apply(items, true)
				c := stats[q.Category]
				if c == nil {
					c = &counts{}
					stats[q.Category] = c
				}
				c.Queries++
				c.BeforeReturned += len(before)
				c.AfterReturned += len(after)
				targetFound := func(rows []any) bool {
					for _, i := range rows {
						if slices.Contains(q.Relevant, ids[i.(map[string]any)["id"].(string)]) {
							return true
						}
					}
					return false
				}
				if targetFound(items) {
					c.ServerTarget++
				}
				if targetFound(before) {
					c.BeforeTarget++
				}
				if targetFound(after) {
					c.AfterTarget++
				}
				for _, i := range after {
					if !slices.Contains(q.Relevant, ids[i.(map[string]any)["id"].(string)]) {
						c.WrongReturned++
					}
				}
				if mode == "vector" {
					if len(before) != 0 {
						t.Fatal("legacy vector-only control unexpectedly passed")
					}
					if targetFound(items) != targetFound(after) {
						t.Fatal("host discarded server-qualified semantic target")
					}
					for _, hit := range out.Items {
						if hit.VectorMatch == nil || hit.VectorMatch.Space != data.Space || hit.VectorMatch.Distance > data.MaxDistance {
							t.Fatal("missing verified vector evidence")
						}
					}
				}
			}
			encoded, _ := json.Marshal(stats)
			t.Logf("fixed_cutoff=%.2f public_synthetic_quality=%s", data.MaxDistance, encoded)
			if mode == "vector" {
				if stats["paraphrase"].AfterTarget < 4 || stats["synonym"].AfterTarget < 3 {
					t.Fatal("regressed below measured 7/18 semantic recovery")
				}
				if stats["negative"].AfterReturned != 0 || stats["near_negative"].AfterReturned != 0 {
					t.Fatal("fixed-cutoff negative regression")
				}
			} else if stats["paraphrase"].AfterTarget < 5 || stats["synonym"].AfterTarget < 3 || stats["negative"].AfterReturned != 0 || stats["near_negative"].AfterReturned > 3 {
				t.Fatal("hybrid regression below 8/18 targets or above existing negative noise")
			}
		})
	}
	// NewPostgres does not configure a distance ceiling by default. Without
	// an explicit server policy, no evidence may exempt hits from the old floor.
	p.SetVectorMaxDistance(0)
	out, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: data.Queries[0].Text, Modes: []string{"vector"}, Limit: 6})
	if err != nil || len(out.Items) == 0 {
		t.Fatal("disabled gate control", err)
	}
	for _, hit := range out.Items {
		if hit.VectorMatch != nil {
			t.Fatal("implicit/disabled policy minted semantic evidence")
		}
	}
	wire, _ := json.Marshal(out.Items)
	var items []any
	if json.Unmarshal(wire, &items) != nil {
		t.Fatal("wire decode")
	}
	if kept, dropped := budget.Floor(items, true); len(kept) != 0 || dropped != len(items) {
		t.Fatal("default installation fallback opened")
	}
}

func TestVerifiedSemanticMatchRejectsStaleOrInvalidEvidence(t *testing.T) {
	v := make([]float64, VectorDimension)
	v[0] = 1
	base := Record{Text: "public synthetic memory"}
	base.Embedding = &Embedding{Space: "one-space", Dimension: VectorDimension, Vector: v, ContentHash: ContentHash(recordContent(base))}
	for _, mode := range []string{"valid", "wrong-space", "stale-content", "disabled", "nan-limit", "infinite-limit", "zero-query", "short-query", "nan-query", "infinite-query", "overflow-query", "far"} {
		t.Run(mode, func(t *testing.T) {
			r, query, space, limit := base, slices.Clone(v), "one-space", .38
			switch mode {
			case "wrong-space":
				space = "other-space"
			case "stale-content":
				r.Text = "changed canonical content"
			case "disabled":
				limit = 0
			case "nan-limit":
				limit = math.NaN()
			case "infinite-limit":
				limit = math.Inf(1)
			case "zero-query":
				query[0] = 0
			case "short-query":
				query = []float64{1}
			case "nan-query":
				query[0] = math.NaN()
			case "infinite-query":
				query[0] = math.Inf(1)
			case "overflow-query":
				query[0] = math.MaxFloat64
			case "far":
				query[0] = -1
			}
			if match := verifiedSemanticMatch(r, query, space, limit); (match != nil) != (mode == "valid") {
				t.Fatal("invalid semantic evidence", mode, match)
			}
		})
	}
}
