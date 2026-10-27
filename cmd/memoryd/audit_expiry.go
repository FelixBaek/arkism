package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
)

// auditRetentionDays reads MEMORY_AUDIT_RETENTION_DAYS: 30 when unset, 0 to keep every row.
func auditRetentionDays() (int, error) {
	raw := os.Getenv("MEMORY_AUDIT_RETENTION_DAYS")
	if raw == "" {
		return 30, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 0 || days > 3650 {
		return 0, memory.ErrInvalid
	}
	return days, nil
}

// runDaemonAuditExpiry counts the audit events past retention, logs that count,
// and with "execute" deletes them. It runs on the schema owner's connection.
func runDaemonAuditExpiry(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	if cfg.AuditExpiry != "preview" && cfg.AuditExpiry != "execute" {
		return memory.ErrInvalid
	}
	days, err := auditRetentionDays()
	if err != nil {
		return err
	}
	if days == 0 {
		fmt.Println(`{"mode":"disabled","count":0,"deleted":0}`)
		return nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	op, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	count, err := pg.ExpireAuditEvents(op, cutoff, false)
	if err != nil {
		return err
	}
	slog.Info("audit events past retention", "mode", cfg.AuditExpiry, "before", cutoff.Format(time.RFC3339), "count", count)
	var deleted int64
	if cfg.AuditExpiry == "execute" {
		if deleted, err = pg.ExpireAuditEvents(op, cutoff, true); err != nil {
			return err
		}
	}
	fmt.Printf("{\"mode\":%q,\"before\":%q,\"count\":%d,\"deleted\":%d}\n", cfg.AuditExpiry, cutoff.Format(time.RFC3339), count, deleted)
	return nil
}
