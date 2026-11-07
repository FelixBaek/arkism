package memory

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRetentionBatchResumeAndScopeIsolation(t *testing.T) {
	p := isolatedPostgres(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-40 * 24 * time.Hour)
	cutoff := old.Add(24 * time.Hour)
	for i := 0; i < 1003; i++ {
		r := Record{ID: fmt.Sprintf("old-%04d", i), Scope: Scope{WorkspaceID: "w", ProjectID: fmt.Sprintf("p-%d", i%2)}, Kind: "allow:read", Status: "active", CreatedAt: old, UpdatedAt: old}
		if err := p.Insert(ctx, "audit_events", r); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Insert(ctx, colLLMRequests, Record{ID: "other", Scope: Scope{WorkspaceID: "w", ProjectID: "p"}, Kind: "request", Status: "active", CreatedAt: old, UpdatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if n, e := p.ExpireAuditEventsBatch(ctx, cutoff, 3); e != nil || n != 3 {
		t.Fatal(n, e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if n, e := p.ExpireAuditEventsBatch(cancelled, cutoff, 3); e == nil || n != 0 {
		t.Fatal(n, e)
	}
	// A separate call resumes from committed progress; the wrapper crosses batch boundaries.
	if n, e := p.ExpireAuditEvents(ctx, cutoff, true); e != nil || n != 1000 {
		t.Fatal(n, e)
	}
	if n, e := p.ExpireAuditEventsBatch(ctx, cutoff, 3); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	if n, e := p.ExpireLLMRequestsBatch(ctx, cutoff, 1); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	for _, limit := range []int{0, 1001} {
		if _, e := p.ExpireAuditEventsBatch(ctx, cutoff, limit); !errors.Is(e, ErrInvalid) {
			t.Fatal(e)
		}
	}
	if _, e := p.ExpireAuditEventsBatch(ContextWithPostgresToken(ctx, "token"), cutoff, 1); !errors.Is(e, ErrDenied) {
		t.Fatal(e)
	}
}
