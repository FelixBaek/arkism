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

type auditExpirer interface {
	ExpireAuditEvents(ctx context.Context, cutoff time.Time, execute bool) (int64, error)
}

// runDaemonAuditExpiry counts the audit events past retention, logs that count,
// and with "execute" deletes them. It runs on the schema owner's connection.
func runDaemonAuditExpiry(ctx context.Context, pg *memory.Postgres, cfg daemonOptions, standalone string) error {
	return expireAuditEvents(ctx, pg, cfg.AuditExpiry, os.Stdout)
}

func expireAuditEvents(ctx context.Context, store auditExpirer, mode string, out io.Writer) error {
	if mode != "preview" && mode != "execute" {
		return memory.ErrInvalid
	}
	days, err := auditRetentionDays()
	if err != nil {
		return err
	}
	if days == 0 {
		_, err = fmt.Fprintln(out, `{"mode":"disabled","count":0,"deleted":0}`)
		return err
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	op, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	count, err := store.ExpireAuditEvents(op, cutoff, false)
	if err != nil {
		return err
	}
	slog.Info("audit events past retention", "mode", mode, "before", cutoff.Format(time.RFC3339), "count", count)
	var deleted int64
	if mode == "execute" {
		if deleted, err = store.ExpireAuditEvents(op, cutoff, true); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "{\"mode\":%q,\"before\":%q,\"count\":%d,\"deleted\":%d}\n", mode, cutoff.Format(time.RFC3339), count, deleted)
	return err
}
