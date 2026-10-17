package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
)

// runRecallBaseline prints the ST-810 recall baseline as JSON numbers. It reads one
// READ ONLY snapshot; the only outside call is the configured embedder for each question.
func runRecallBaseline(ctx context.Context, pg *memory.Postgres, spec, rawScope string) error {
	parts := strings.Split(rawScope, "/")
	if len(parts) != 4 {
		return memory.ErrInvalid
	}
	scope := memory.Scope{WorkspaceID: parts[0], ProjectID: parts[1], UserID: parts[2], AgentID: parts[3]}
	queries, self, err := baselineQueries(spec)
	if err != nil {
		return err
	}
	embedder, err := configuredEmbedder(false, false)
	if err != nil {
		return err
	}
	if token := os.Getenv("MEMORY_WORKER_TOKEN"); token != "" {
		ctx = memory.ContextWithPostgresToken(ctx, token)
	}
	op, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	report, err := pg.RecallBaseline(op, scope, queries, self, embedder)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

// baselineQueries reads "self" or a JSONL file of {"query","relevant","negative"} lines.
func baselineQueries(spec string) ([]memory.BaselineQuery, bool, error) {
	if spec == "self" {
		return nil, true, nil
	}
	f, err := os.Open(spec)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	var out []memory.BaselineQuery
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 1<<20)
	for scan.Scan() {
		if strings.TrimSpace(scan.Text()) == "" {
			continue
		}
		var q memory.BaselineQuery
		dec := json.NewDecoder(strings.NewReader(scan.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&q); err != nil || strings.TrimSpace(q.Query) == "" || len([]rune(q.Query)) > 1000 {
			return nil, false, memory.ErrInvalid
		}
		out = append(out, q)
	}
	return out, false, scan.Err()
}
