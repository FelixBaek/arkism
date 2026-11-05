package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
)

// retentionDays reads a retention env var: def when unset, 0 to keep every row.
func retentionDays(env string, def int) (int, error) {
	raw := os.Getenv(env)
	if raw == "" {
		return def, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 || days > 3650 {
		return 0, memory.ErrInvalid
	}
	return days, nil
}

type auditExpirer interface {
	ExpireAuditEvents(ctx context.Context, cutoff time.Time, execute bool) (int64, error)
	ExpireLLMRequests(ctx context.Context, cutoff time.Time, execute bool) (int64, error)
}

// runDaemonAuditExpiry counts the audit events and model call traces past retention, logs those
// counts, and with "execute" deletes them. It runs on the schema owner's connection.
func runDaemonAuditExpiry(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	return expireAuditEvents(ctx, pg, cfg.AuditExpiry, os.Stdout)
}

// expireAuditEvents: MEMORY_AUDIT_RETENTION_DAYS (default 30) for audit events, then
// ARKISM_LLM_TRACE_RETENTION_DAYS (default 30) for llm_requests.
func expireAuditEvents(ctx context.Context, store auditExpirer, mode string, out io.Writer) error {
	if mode != "preview" && mode != "execute" {
		return memory.ErrInvalid
	}
	if err := expireRows(ctx, "audit events", "MEMORY_AUDIT_RETENTION_DAYS", store.ExpireAuditEvents, mode, out); err != nil {
		return err
	}
	return expireRows(ctx, "model call traces", "ARKISM_LLM_TRACE_RETENTION_DAYS", store.ExpireLLMRequests, mode, out)
}

func expireRows(ctx context.Context, label, env string, expire func(context.Context, time.Time, bool) (int64, error), mode string, out io.Writer) error {
	days, err := retentionDays(env, 30)
	if err != nil {
		return err
	}
	if days == 0 {
		_, err = fmt.Fprintf(out, "{\"rows\":%q,\"mode\":\"disabled\",\"count\":0,\"deleted\":0}\n", label)
		return err
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	op, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	count, err := expire(op, cutoff, false)
	if err != nil {
		return err
	}
	slog.Info(label+" past retention", "mode", mode, "before", cutoff.Format(time.RFC3339), "count", count)
	var deleted int64
	if mode == "execute" {
		if deleted, err = expire(op, cutoff, true); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "{\"rows\":%q,\"mode\":%q,\"before\":%q,\"count\":%d,\"deleted\":%d}\n", label, mode, cutoff.Format(time.RFC3339), count, deleted)
	return err
}
