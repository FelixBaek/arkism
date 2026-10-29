package memory

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

type recallExperimentVector struct {
	ID          string    `json:"id"`
	ContentHash string    `json:"content_sha256"`
	InputHash   string    `json:"input_sha256"`
	Vector      []float64 `json:"vector"`
}

type recallExperimentStage struct {
	MaxDistance    float64          `json:"max_distance"`
	ServerLimit    int              `json:"server_limit"`
	ServerMaxChars int              `json:"server_max_chars"`
	FinalLimit     int              `json:"final_limit"`
	Candidates     []map[string]any `json:"server_candidates"`
	FinalIDs       []string         `json:"host_final_ids"`
	DroppedScore   int              `json:"host_dropped_score"`
	DroppedBudget  int              `json:"host_dropped_budget"`
	SearchMS       float64          `json:"search_ms"`
	JSONMS         float64          `json:"json_ms"`
	HostMS         float64          `json:"host_ms"`
	TotalMS        float64          `json:"total_ms"`
}

// This opt-in experiment exports observations, never labels or quality assertions.
// Its synthetic human fixture exercises the ordinary approval/live/source fences;
// frozen vectors mean no model or external provider is contacted by this test.
func TestPostgresRecallQualityExperimentExport(t *testing.T) {
	outputPath := os.Getenv("ARKISM_RECALL_EXPERIMENT_EXPORT")
	if outputPath == "" {
		t.Skip("ARKISM_RECALL_EXPERIMENT_EXPORT is not set")
	}
	if os.Getenv("POSTGRES_TEST_DSN") == "" {
		t.Fatal("explicit disposable POSTGRES_TEST_DSN is required for an export")
	}
	dir := filepath.Join("..", "..", "evaluation", "recall-quality-v1")
	datasetBytes, err := os.ReadFile(filepath.Join(dir, "dataset.json"))
	if err != nil {
		t.Fatal(err)
	}
	vectorBytes, err := os.ReadFile(filepath.Join(dir, "model_vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Intentionally do not decode relevant labels, categories, or split assignments.
	var dataset struct {
		ID        string `json:"dataset_id"`
		Documents []struct{ ID, Text, Summary, Kind string }
		Queries   []struct{ ID, Text string }
	}
	var vectors struct {
		Model, Revision, Space string
		Dimension              int
		QueryPrefix            string `json:"query_prefix"`
		DocumentPrefix         string `json:"document_prefix"`
		InputFormat            string `json:"document_input_format"`
		DatasetHash            string `json:"dataset_sha256"`
		Documents, Queries     []recallExperimentVector
	}
	if err = json.Unmarshal(datasetBytes, &dataset); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(vectorBytes, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(dataset.Documents) != 36 || len(dataset.Queries) != 144 || vectors.DatasetHash != ContentHash(string(datasetBytes)) || vectors.Dimension != VectorDimension || vectors.InputFormat != "recordContent: text + newline + summary" {
		t.Fatal("dataset/vector identity mismatch")
	}
	identity, err := NewTEIEmbedder(TEIConfig{EndpointURL: "http://127.0.0.1:1/embed", Model: vectors.Model, Revision: vectors.Revision, Prefixes: map[string]string{"query": vectors.QueryPrefix, "document": vectors.DocumentPrefix}})
	if err != nil || identity.Space() != vectors.Space {
		t.Fatal("embedding identity mismatch", err)
	}
	index := func(rows []recallExperimentVector, want int) map[string]recallExperimentVector {
		t.Helper()
		if len(rows) != want {
			t.Fatal("vector count mismatch")
		}
		out := make(map[string]recallExperimentVector, want)
		for _, row := range rows {
			if _, duplicate := out[row.ID]; duplicate || row.ID == "" || len(row.Vector) != VectorDimension {
				t.Fatal("duplicate/malformed vector", row.ID)
			}
			norm := 0.0
			for _, v := range row.Vector {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					t.Fatal("nonfinite vector", row.ID)
				}
				norm += v * v
			}
			if norm <= 0 || math.IsInf(norm, 0) {
				t.Fatal("invalid vector norm", row.ID)
			}
			out[row.ID] = row
		}
		return out
	}
	docs, queries := index(vectors.Documents, 36), index(vectors.Queries, 144)
	checkInput := func(row recallExperimentVector, content, prefix string) {
		t.Helper()
		if row.ContentHash != ContentHash(content) || row.InputHash != ContentHash(prefix+content) {
			t.Fatal("embedding input hash mismatch", row.ID)
		}
	}
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	canonicalIDs := map[string]string{}
	seenDocuments := map[string]bool{}
	for _, doc := range dataset.Documents {
		row, ok := docs[doc.ID]
		if !ok || seenDocuments[doc.ID] || doc.Summary != "" || doc.Kind == "" {
			t.Fatal("invalid document", doc.ID)
		}
		seenDocuments[doc.ID] = true
		checkInput(row, doc.Text+"\n"+doc.Summary, vectors.DocumentPrefix)
		src := f.source()
		candidate := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: doc.Text, Summary: doc.Summary, Kind: doc.Kind, Sources: refs(src)}))
		approved := f.transition(f.human, "memory_candidates", candidate.ID, ChangeInput{Scope: scopeA, ExpectedRevision: candidate.Revision, Status: "approved"})
		active := f.transition(f.human, colMemories, approved.ID, ChangeInput{Scope: scopeA, ExpectedRevision: approved.Revision, Status: statusActive})
		if active.Kind != doc.Kind || active.ApprovedBy == "" || !live(active) {
			t.Fatal("fixture approval/kind mismatch", doc.ID)
		}
		active.Embedding = &Embedding{Provider: "tei", Model: vectors.Model, Revision: vectors.Revision, Space: vectors.Space, Dimension: VectorDimension, Vector: row.Vector, ContentHash: ContentHash(recordContent(active)), EmbeddedAt: time.Now().UTC()}
		if err = p.Replace(testCtx, colMemories, active, active.Revision); err != nil {
			t.Fatal(err)
		}
		canonicalIDs[active.ID] = doc.ID
	}
	frozen := frozenSemanticEmbedder{space: vectors.Space, vectors: map[string][]float64{}}
	seenQueries := map[string]bool{}
	for _, q := range dataset.Queries {
		row, ok := queries[q.ID]
		if !ok || seenQueries[q.ID] {
			t.Fatal("missing/duplicate query", q.ID)
		}
		seenQueries[q.ID] = true
		checkInput(row, q.Text, vectors.QueryPrefix)
		frozen.vectors[q.Text] = row.Vector
	}
	f.svc.Embedder = frozen
	t.Setenv("ARKISM_RECALL_MIN_SCORE", "")
	t.Setenv("ARKISM_RECALL_BUDGET", "")
	budget := host.LoadRecallBudget()
	stage := func(query string, distance float64, limit, maxChars int) recallExperimentStage {
		t.Helper()
		start := time.Now()
		p.SetVectorMaxDistance(distance) // Isolated test repository only; relative cut is unchanged.
		result, err := f.svc.Search(testCtx, f.human, SearchInput{Scope: scopeA, Query: query, Limit: limit, MaxChars: maxChars})
		if err != nil || len(result.Degraded) > 0 {
			t.Fatal("experiment search failed/degraded", err, result.Degraded)
		}
		searchEnd := time.Now()
		wire, err := json.Marshal(result.Items)
		if err != nil {
			t.Fatal(err)
		}
		var items []any
		if err = json.Unmarshal(wire, &items); err != nil {
			t.Fatal(err)
		}
		jsonEnd := time.Now()
		finalInput := items
		if len(finalInput) > 6 {
			finalInput = finalInput[:6]
		}
		// The frozen corpus is short enough that the existing 6000-rune final
		// boundary cannot truncate any top-six list. Fail if that ceases to hold.
		chars := 0
		for _, item := range finalInput {
			hit := item.(map[string]any)
			text, _ := hit["text"].(string)
			summary, _ := hit["summary"].(string)
			chars += utf8.RuneCountInString(text) + utf8.RuneCountInString(summary)
		}
		if chars > 6000 {
			t.Fatal("fixture requires final max_chars truncation")
		}
		kept, low, overBudget := budget.Apply(finalInput, true)
		hostEnd := time.Now()
		out := recallExperimentStage{MaxDistance: distance, ServerLimit: limit, ServerMaxChars: maxChars, FinalLimit: 6, Candidates: []map[string]any{}, FinalIDs: []string{}, DroppedScore: low, DroppedBudget: overBudget, SearchMS: float64(searchEnd.Sub(start)) / 1e6, JSONMS: float64(jsonEnd.Sub(searchEnd)) / 1e6, HostMS: float64(hostEnd.Sub(jsonEnd)) / 1e6, TotalMS: float64(hostEnd.Sub(start)) / 1e6}
		for _, item := range items {
			hit, ok := item.(map[string]any)
			if !ok {
				t.Fatal("invalid wire item")
			}
			id, ok := canonicalIDs[hit["id"].(string)]
			if !ok {
				t.Fatal("unexpected document in search")
			}
			text, _ := hit["text"].(string)
			entry := map[string]any{"id": id, "score": hit["score"], "lanes": hit["lanes"], "kind": hit["kind"], "text_runes": utf8.RuneCountInString(text)}
			if match, ok := hit["vector_match"]; ok {
				entry["vector_match"] = match
			}
			out.Candidates = append(out.Candidates, entry)
		}
		for _, item := range kept {
			out.FinalIDs = append(out.FinalIDs, canonicalIDs[item.(map[string]any)["id"].(string)])
		}
		return out
	}
	observations := make([]map[string]any, 0, len(dataset.Queries))
	elapsed := func(s recallExperimentStage) map[string]float64 {
		return map[string]float64{"search_ms": s.SearchMS, "json_ms": s.JSONMS, "host_ms": s.HostMS, "total_ms": s.TotalMS}
	}
	for _, q := range dataset.Queries {
		baseline := stage(q.Text, .38, 6, 6000)
		expanded := stage(q.Text, .55, 16, 16000)
		observations = append(observations, map[string]any{
			"id": q.ID, "baseline": baseline.FinalIDs, "baseline_candidates": baseline.Candidates, "expanded_candidates": expanded.Candidates, "expanded": expanded.FinalIDs,
			"baseline_elapsed_ms": baseline.TotalMS, "expanded_elapsed_ms": expanded.TotalMS, "baseline_stages": elapsed(baseline), "expanded_stages": elapsed(expanded),
			"baseline_dropped_score": baseline.DroppedScore, "baseline_dropped_budget": baseline.DroppedBudget, "expanded_dropped_score": expanded.DroppedScore, "expanded_dropped_budget": expanded.DroppedBudget,
		})
	}
	testBytes, err := os.ReadFile("recall_quality_experiment_test.go")
	if err != nil {
		t.Fatal(err)
	}
	export := map[string]any{
		"test_sha256":    ContentHash(string(testBytes)),
		"schema_version": 1, "dataset_id": dataset.ID, "dataset_sha256": ContentHash(string(datasetBytes)), "vectors_sha256": ContentHash(string(vectorBytes)),
		"model": vectors.Model, "revision": vectors.Revision, "space": vectors.Space, "host_budget": budget, "queries": observations,
		"baseline_config": map[string]any{"max_distance": .38, "limit": 6, "max_chars": 6000, "mode": "hybrid"},
		"expanded_config": map[string]any{"max_distance": .55, "limit": 16, "max_chars": 16000, "final_limit": 6, "final_max_chars": 6000, "mode": "hybrid"},
		"notes":           "Isolated PostgreSQL; ordinary Service.Search approval/live/source/hash fences and usage receipts; fake human policy authorizer, not a live DOLHARU authorization or RLS-role test; frozen embedding lookup excludes live model latency; no labels decoded/exported; relative vector cut unchanged; host final truncates server order to 6 then default Apply; top-six text fits 6000 runes; candidates preserve order for independent rerank before final limits.",
	}
	raw, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(outputPath, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("exported %d queries and %d documents through baseline and expanded Service.Search", len(observations), len(dataset.Documents))
}
