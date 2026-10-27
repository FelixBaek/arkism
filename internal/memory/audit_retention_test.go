package memory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExpireAuditEventsPreviewsCountsAndDeletesOnlyOldRows(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	scope := Scope{WorkspaceID: "w", ProjectID: "p"}
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	for _, r := range []Record{
		{ID: "old-1", Scope: scope, Kind: "allow:read", Status: "active", CreatedAt: old, UpdatedAt: old},
		{ID: "old-2", Scope: scope, Kind: "policy:deny:read", Status: "active", CreatedAt: old, UpdatedAt: old},
		{ID: "new-1", Scope: scope, Kind: "allow:read", Status: "active"},
	} {
		if err := p.Insert(ctx, "audit_events", r); err != nil {
			t.Fatal(err)
		}
	}
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if n, err := p.ExpireAuditEvents(ctx, cutoff, false); err != nil || n != 2 {
		t.Fatal("preview", n, err)
	}
	if rows := pgCollection(t, p, "audit_events"); len(rows) != 3 {
		t.Fatal("preview deleted rows", len(rows))
	}
	if n, err := p.ExpireAuditEvents(ctx, cutoff, true); err != nil || n != 2 {
		t.Fatal("execute", n, err)
	}
	rows := pgCollection(t, p, "audit_events")
	if len(rows) != 1 || rows[0].ID != "new-1" {
		t.Fatal("wrong rows left", rows)
	}
	if n, err := p.ExpireAuditEvents(ctx, cutoff, true); err != nil || n != 0 {
		t.Fatal("second execute", n, err)
	}
}

func TestExpireAuditEventsRefusesRecentCutoffsAndTokenBoundCallers(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	for _, cutoff := range []time.Time{{}, time.Now(), time.Now().Add(-time.Hour), time.Now().Add(time.Hour)} {
		if _, err := p.ExpireAuditEvents(ctx, cutoff, true); !errors.Is(err, ErrInvalid) {
			t.Fatal(cutoff, err)
		}
	}
	bound := ContextWithPostgresToken(ctx, "worker-token")
	if _, err := p.ExpireAuditEvents(bound, time.Now().Add(-48*time.Hour), false); !errors.Is(err, ErrDenied) {
		t.Fatal("a token-bound caller sees only its scopes and must not run retention", err)
	}
}
