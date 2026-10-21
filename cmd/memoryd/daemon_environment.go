package main

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/FelixBaek/arkism/internal/memory"
	"github.com/jackc/pgx/v5"
)

func validateSyntheticDaemon(cfg daemonOptions, store string) (string, error) {
	syntheticHandoff := os.Getenv("MEMORY_SYNTHETIC_HANDOFF")
	if syntheticHandoff != "" && syntheticHandoff != "1" {
		return "", memory.ErrInvalid
	}
	if syntheticHandoff == "1" {
		host, _, err := net.SplitHostPort(cfg.Listen)
		if (!cfg.Worker && (err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback())) || store != "postgres" {
			return "", memory.ErrInvalid
		}
		if err := syntheticEmbedderAllowed(); err != nil {
			return "", err
		}
		if err := validateSyntheticDatabase(); err != nil {
			return "", err
		}
		if err := validateSyntheticSecrets(); err != nil {
			return "", err
		}
	}
	return syntheticHandoff, nil
}

func configureDaemonPostgres(pg *memory.Postgres, cfg daemonOptions) error {
	pg.SetRecoveryMode(cfg.Recovery)
	if raw := os.Getenv("MEMORY_VECTOR_MAX_DISTANCE"); raw != "" {
		d, err := strconv.ParseFloat(raw, 64)
		if err != nil || d <= 0 || d >= 2 {
			return fmt.Errorf("MEMORY_VECTOR_MAX_DISTANCE must be a cosine distance in (0,2)")
		}
		pg.SetVectorMaxDistance(d)
	}
	if raw := os.Getenv("MEMORY_TOPIC_DIGEST_MAX"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			return fmt.Errorf("MEMORY_TOPIC_DIGEST_MAX must be 1-100")
		}
		memory.TopicDigestCap = n
	}
	return nil
}

func validateSyntheticDatabase() error {
	databaseConfig, err := pgx.ParseConfig(os.Getenv("MEMORY_DATABASE_URL"))
	if err != nil {
		return memory.ErrInvalid
	}
	dbhost := databaseConfig.Host
	if dbhost != "localhost" && (net.ParseIP(dbhost) == nil || !net.ParseIP(dbhost).IsLoopback()) {
		return memory.ErrInvalid
	}
	for _, fallback := range databaseConfig.Fallbacks {
		host := fallback.Host
		if host != "localhost" && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback()) {
			return memory.ErrInvalid
		}
	}

	return nil
}
func validateSyntheticSecrets() error {
	for _, key := range []string{"SKILL_LLM_API_KEY", "TYPESAFE_API_KEY", "LANGSMITH_API_KEY", "OTEL_EXPORTER_OTLP_ENDPOINT"} {
		if os.Getenv(key) != "" {
			return memory.ErrInvalid
		}
	}
	return nil
}
