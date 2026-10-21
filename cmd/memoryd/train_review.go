package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/memory"
)

// Training leaves candidate decisions unchanged; scoped audit and provider usage
// records are still written, and the private model is replaced atomically.
func runTrainReviewClassifier(ctx context.Context, pg *memory.Postgres, path, rawScope, exclude string) error {
	if err := pg.CheckRestorePublication(ctx); err != nil {
		return err
	}
	parts := strings.Split(rawScope, "/")
	if len(parts) != 4 {
		return memory.ErrInvalid
	}
	scope := memory.Scope{WorkspaceID: parts[0], ProjectID: parts[1], UserID: parts[2], AgentID: parts[3]}
	embedder, err := configuredEmbedder(false, false)
	if err != nil {
		return err
	}
	token := os.Getenv("MEMORY_WORKER_TOKEN")
	if token == "" {
		return memory.ErrDenied
	}
	ctx = memory.ContextWithPostgresToken(ctx, token)
	auth, err := configuredAuthorizer(pg, os.Getenv("MEMORY_LOCAL_STANDALONE"))
	if err != nil {
		return err
	}
	svc := &memory.Service{Repo: pg, Auth: auth, Embedder: embedder}
	svc.CostRates, err = costRatesFromEnv()
	if err != nil {
		return err
	}
	op, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	var excluded []string
	for _, p := range strings.Split(exclude, ",") {
		if p = strings.TrimSpace(p); p != "" {
			excluded = append(excluded, p)
		}
	}
	model, report, err := svc.TrainReviewClassifier(op, token, scope, excluded)
	if err != nil {
		return err
	}
	// Without both outcomes nothing was learned, and the server refuses to load an empty model.
	if model.Rejected == 0 || model.Rejected == model.Examples {
		_ = json.NewEncoder(os.Stderr).Encode(report)
		return memory.ErrUnavailable
	}
	data, err := json.Marshal(model)
	if err != nil {
		return err
	}
	if err = host.AtomicWrite(path, string(data)); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
