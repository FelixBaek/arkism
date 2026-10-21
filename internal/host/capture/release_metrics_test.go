package capture

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/tiktoken-go/tokenizer"
)

// Release metrics (evaluation/release_metrics.py): compression, recall hook latency and injected
// tokens on the synthetic fixture only. Skipped unless ARKISM_METRICS_OUT names the raw output file.

type metricsFact struct {
	ID       string   `json:"id"`
	Fact     string   `json:"fact"`
	Keywords []string `json:"keywords"`
}

type metricsTask struct {
	metricsFact
	Task       string           `json:"task"`
	Transcript []map[string]any `json:"transcript"`
}

type metricsFixture struct {
	Tasks       []metricsTask `json:"tasks"`
	Distractors []metricsFact `json:"distractors"`
}

var metricsWord = regexp.MustCompile(`[a-z]+`)

// metricsStore is the local test store behind /v1/search, served over loopback HTTP: keyword
// match, best first. GETs (standing rules, skills, digests) answer empty.
// shortcut: lexical keywords only, so the hit is right by construction; retrieval quality is
// measured by tools/evaluate_semantic_recall.py, not here.
func metricsStore(facts []metricsFact) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		items := []any{}
		var body struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/search" && json.NewDecoder(r.Body).Decode(&body) == nil {
			words := map[string]bool{}
			for _, w := range metricsWord.FindAllString(strings.ToLower(body.Query), -1) {
				words[w] = true
			}
			type hit struct {
				item  map[string]any
				score int
			}
			var hits []hit
			for _, f := range facts {
				score := 0
				for _, k := range f.Keywords {
					if words[k] {
						score++
					}
				}
				if score > 0 {
					hits = append(hits, hit{map[string]any{"collection": "memories", "id": f.ID, "kind": "decision",
						"revision": 1, "text": f.Fact, "lanes": []any{"fts"},
						"scope": map[string]any{"workspace_id": "w", "project_id": "p", "user_id": "u"}}, score})
				}
			}
			sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
			for i, h := range hits {
				if i < body.Limit {
					items = append(items, h.item)
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"items": items})
	}
}

func TestReleaseMetrics(t *testing.T) {
	out := os.Getenv("ARKISM_METRICS_OUT")
	if out == "" {
		t.Skip("set ARKISM_METRICS_OUT to measure release metrics")
	}
	rounds, err := strconv.Atoi(os.Getenv("ARKISM_METRICS_ROUNDS"))
	if err != nil || rounds < 1 {
		rounds = 40
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "evaluation", "metrics", "fixture.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx metricsFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	codec, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		t.Fatal(err)
	}
	tokens := func(s string) int { n, _ := codec.Count(s); return n }

	t.Chdir(t.TempDir())
	capFake(t)
	hookEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ARKISM_CAPTURE_SETTINGS", filepath.Join(t.TempDir(), "capture.json"))
	t.Setenv("ARKISM_REPO", "")
	t.Setenv("ARKISM_WORK_ID", "")
	t.Setenv("ARKISM_WORK_AGENT", "")
	t.Setenv("ARKISM_SESSION_PROCESSOR", "")
	scope := capScope
	t.Cleanup(func() { capScope = scope })
	capScope = func() map[string]any {
		return map[string]any{"workspace_id": "w", "project_id": "p", "user_id": "u", "agent_id": ""}
	}
	store := append([]metricsFact{}, fx.Distractors...)
	for _, task := range fx.Tasks {
		store = append(store, task.metricsFact)
	}
	server := httptest.NewServer(metricsStore(store))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("metrics-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	url, tokenFile := host.BaseURL, host.TokenFile
	t.Cleanup(func() { host.BaseURL, host.TokenFile = url, tokenFile })
	host.BaseURL, host.TokenFile = server.URL, token
	capPost = host.Post // the real client, so latency includes JSON over loopback HTTP
	capGet = host.Get

	// Static baseline: every project rule written into the instruction file, sent every turn.
	var static strings.Builder
	static.WriteString("# Project rules\n")
	for _, f := range store {
		static.WriteString("- " + f.Fact + "\n")
	}

	type compression struct {
		Task             string `json:"task"`
		TranscriptBytes  int    `json:"transcript_bytes"`
		TranscriptTokens int    `json:"transcript_tokens"`
		UserTextBytes    int    `json:"user_text_bytes"`
		MemoryBytes      int    `json:"memory_bytes"`
		MemoryTokens     int    `json:"memory_tokens"`
	}
	type injection struct {
		Task         string `json:"task"`
		TaskTokens   int    `json:"task_tokens"`
		RecallTokens int    `json:"recall_tokens"`
		StaticTokens int    `json:"static_tokens"`
		RelevantHit  bool   `json:"relevant_hit"`
		Distractors  int    `json:"distractor_hits"`
	}
	result := struct {
		Encoding    string        `json:"token_encoding"`
		Rounds      int           `json:"rounds"`
		Compression []compression `json:"compression"`
		Injection   []injection   `json:"injection"`
		LatencyMS   []float64     `json:"latency_ms"`
	}{Encoding: string(tokenizer.Cl100kBase), Rounds: rounds}

	for _, task := range fx.Tasks {
		// Capture: transcript -> typed user text -> extraction (model output frozen) -> claim checks.
		var lines []string
		for _, row := range task.Transcript {
			b, _ := json.Marshal(row)
			lines = append(lines, string(b))
		}
		transcript := strings.Join(lines, "\n") + "\n"
		path := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
			t.Fatal(err)
		}
		users, _, _, _, err := readNew(path, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		var typed []string
		for _, u := range users {
			typed = append(typed, str(u.(map[string]any)["text"]))
		}
		userText := strings.Join(typed, "\n")
		llm = func(string, string, string, time.Duration, string, string) (any, error) {
			return []any{map[string]any{"subject": task.ID, "kind": "decision", "text": task.Fact, "quote": task.Fact, "risk": "low"}}, nil
		}
		extracted, err := ExtractClaims(userText, "2026-01-01", nil, "", "release-metrics")
		if err != nil || len(extracted.Claims) != 1 {
			t.Fatal(task.ID, err, extracted.Claims)
		}
		memory := str(extracted.Claims[0].(map[string]any)["text"])
		result.Compression = append(result.Compression, compression{task.ID, len(transcript), tokens(transcript),
			len(userText), len(memory), tokens(memory)})

		// Recall: the UserPromptSubmit hook's injected context for the task prompt.
		event, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "inject-" + task.ID, "prompt": task.Task})
		ctx := hookText(t, string(event))
		hits := 0
		for _, d := range fx.Distractors {
			if strings.Contains(ctx, d.Fact) {
				hits++
			}
		}
		result.Injection = append(result.Injection, injection{task.ID, tokens(task.Task), tokens(ctx), tokens(static.String()),
			strings.Contains(ctx, task.Fact), hits})
	}

	for r := 0; r < rounds; r++ {
		for _, task := range fx.Tasks {
			// A fresh session each call, so earlier deliveries never shorten the work.
			event, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit",
				"session_id": "latency-" + strconv.Itoa(r) + "-" + task.ID, "prompt": task.Task})
			var buf bytes.Buffer
			start := time.Now()
			code := hookMain(bytes.NewReader(event), &buf)
			elapsed := time.Since(start)
			if code != 0 || !strings.Contains(buf.String(), "[decision]") {
				t.Fatal(code, buf.String())
			}
			result.LatencyMS = append(result.LatencyMS, float64(elapsed.Microseconds())/1000)
		}
	}
	encoded, _ := json.MarshalIndent(result, "", " ")
	if err := os.WriteFile(out, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
