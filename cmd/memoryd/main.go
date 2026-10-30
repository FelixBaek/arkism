// memoryd runs the API and the leased embedding worker against a dedicated
// PostgreSQL database. It never connects to Design databases or Qdrant.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("memoryd stopped: configuration or dependency failure (raw errors suppressed)")
		os.Exit(1)
	}
}
func run() error {
	cfg := daemonFlags()
	if err := configureLogging(cfg.LogLevel); err != nil {
		return err
	}
	standalone, err := validateDaemonOptions(cfg)
	if err != nil {
		return err
	}
	cfg, err = prepareDaemonDynamics(cfg)
	if err != nil {
		return err
	}
	store, err := configuredStore()
	if err != nil {
		return err
	}
	synthetic, err := validateSyntheticDaemon(cfg, store)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	closeDiagnostics, err := startDiagnostics(ctx, cfg.Pprof, os.Getenv("MEMORY_DIAGNOSTICS_TOKEN"))
	if err != nil {
		return err
	}
	defer closeDiagnostics()
	op, cancel := context.WithTimeout(ctx, 15*time.Second)
	pg, err := memory.NewPostgres(op, os.Getenv("MEMORY_DATABASE_URL"))
	cancel()
	if err != nil {
		return err
	}
	defer pg.Close()
	if cfg.usesWorkerToken() {
		ctx = memory.ContextWithPostgresToken(ctx, os.Getenv("MEMORY_WORKER_TOKEN"))
	}
	mode, _ := daemonMode(cfg)
	if snapshotDaemonMode(mode) {
		_, err := runDaemonMaintenance(ctx, pg, cfg, standalone)
		return err
	}
	if err := configureDaemonPostgres(pg, cfg); err != nil {
		return err
	}
	if handled, err := runDaemonMaintenance(ctx, pg, cfg, standalone); handled {
		return err
	}
	if !cfg.Recovery {
		if err := checkDaemonReadiness(ctx, pg); err != nil {
			return err
		}
	}
	auth, err := configuredAuthorizer(pg, standalone)
	if err != nil {
		return err
	}
	embed, err := configuredEmbedder(cfg.Worker, cfg.Recovery)
	if err != nil {
		return err
	}
	if cfg.Reindex {
		return runDaemonReindex(ctx, pg, embed)
	}
	svc, err := newDaemonService(ctx, pg, cfg, store, standalone, synthetic, auth, embed)
	if err != nil {
		return err
	}
	return serveDaemon(ctx, svc, cfg)
}
func checkDaemonReadiness(ctx context.Context, pg *memory.Postgres) error {

	if e := pg.CheckRestorePublication(ctx); e != nil {
		return e
	}
	ready, cancelReady := context.WithTimeout(ctx, 30*time.Second)
	e := pg.SearchReady(ready)
	cancelReady()
	if e != nil {
		return e
	}
	return nil
}
func runDaemonReindex(ctx context.Context, pg *memory.Postgres, embed memory.Embedder) error {
	if embed == nil {
		return memory.ErrInvalid
	}
	queued, err := pg.EnqueueReindex(ctx, embed.Space())
	if err != nil {
		return err
	}
	fmt.Printf("{\"queued\":%d,\"space\":%q}\n", queued, embed.Space())
	return nil
}

func costRatesFromEnv() (memory.CostRates, error) {
	perMillion := func(name string) (int64, error) {
		raw := os.Getenv(name)
		if raw == "" {
			return 0, nil
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value <= 0 || value > 1000 {
			return 0, memory.ErrInvalid
		}
		return int64(math.Round(value * 1_000_000)), nil
	}
	perRequest := func(name string) (int64, error) {
		raw := os.Getenv(name)
		if raw == "" {
			return 0, nil
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value <= 0 || value > 1000 {
			return 0, memory.ErrInvalid
		}
		return int64(math.Round(value * 1_000_000_000_000)), nil
	}
	var rates memory.CostRates
	var err error
	rates.Revision = os.Getenv("MEMORY_PRICING_REVISION")
	if rates.SkillLLMInputTokenPicoUSD, err = perMillion("SKILL_LLM_INPUT_USD_PER_MILLION_TOKENS"); err != nil {
		return rates, err
	}
	if rates.SkillLLMOutputTokenPicoUSD, err = perMillion("SKILL_LLM_OUTPUT_USD_PER_MILLION_TOKENS"); err != nil {
		return rates, err
	}
	if rates.JEVRequestPicoUSD, err = perRequest("JEV_USD_PER_REQUEST"); err != nil {
		return rates, err
	}
	if rates.Revision == "" && (rates.SkillLLMInputTokenPicoUSD > 0 || rates.SkillLLMOutputTokenPicoUSD > 0 || rates.JEVRequestPicoUSD > 0) {
		return rates, memory.ErrInvalid
	}
	return rates, nil
}

// googleLogin turns on desktop Google sign-in when all four values are set; a
// partial configuration is refused at start rather than half working.
func googleLogin() (*memory.GoogleLogin, error) {
	id, secret := os.Getenv("MEMORY_GOOGLE_CLIENT_ID"), os.Getenv("MEMORY_GOOGLE_CLIENT_SECRET")
	emails, token := os.Getenv("MEMORY_GOOGLE_ALLOWED_EMAILS"), os.Getenv("MEMORY_DESKTOP_TOKEN")
	if id == "" && secret == "" && emails == "" && token == "" {
		return nil, nil
	}
	if id == "" || secret == "" || emails == "" || len(token) < 32 {
		return nil, fmt.Errorf("google login needs MEMORY_GOOGLE_CLIENT_ID, MEMORY_GOOGLE_CLIENT_SECRET, MEMORY_GOOGLE_ALLOWED_EMAILS and MEMORY_DESKTOP_TOKEN together")
	}
	allowed := map[string]bool{}
	for _, e := range strings.Split(emails, ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			allowed[e] = true
		}
	}
	return &memory.GoogleLogin{ClientID: id, ClientSecret: secret, Allowed: allowed, DesktopToken: token}, nil
}
