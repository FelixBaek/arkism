package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
)

type fakeAuditStore struct {
	calls []bool
	count int64
	fail  error
}

func (f *fakeAuditStore) ExpireAuditEvents(_ context.Context, cutoff time.Time, execute bool) (int64, error) {
	f.calls = append(f.calls, execute)
	if f.fail != nil {
		return 0, f.fail
	}
	if time.Since(cutoff) < 29*24*time.Hour || time.Since(cutoff) > 31*24*time.Hour {
		return 0, errors.New("cutoff is not thirty days ago")
	}
	if execute {
		return f.count, nil
	}
	return f.count, nil
}

func TestExpireAuditEventsPreviewsCountsAndOnlyExecuteDeletes(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MEMORY_AUDIT_RETENTION_DAYS", "")
	var out bytes.Buffer
	store := &fakeAuditStore{count: 4}
	if err := expireAuditEvents(ctx, store, "preview", &out); err != nil || len(store.calls) != 1 || store.calls[0] {
		t.Fatal(err, store.calls)
	}
	if !strings.Contains(out.String(), `"mode":"preview"`) || !strings.Contains(out.String(), `"count":4,"deleted":0`) {
		t.Fatal(out.String())
	}
	out.Reset()
	store = &fakeAuditStore{count: 4}
	if err := expireAuditEvents(ctx, store, "execute", &out); err != nil || len(store.calls) != 2 || store.calls[0] || !store.calls[1] {
		t.Fatal(err, store.calls)
	}
	if !strings.Contains(out.String(), `"count":4,"deleted":4`) {
		t.Fatal(out.String())
	}
	for _, mode := range []string{"", "delete", "Execute"} {
		if err := expireAuditEvents(ctx, store, mode, &out); !errors.Is(err, memory.ErrInvalid) {
			t.Fatal(mode, err)
		}
	}
	t.Setenv("MEMORY_AUDIT_RETENTION_DAYS", "never")
	if err := expireAuditEvents(ctx, store, "preview", &out); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal(err)
	}
	out.Reset()
	t.Setenv("MEMORY_AUDIT_RETENTION_DAYS", "0")
	store = &fakeAuditStore{count: 4}
	if err := expireAuditEvents(ctx, store, "execute", &out); err != nil || len(store.calls) != 0 || !strings.Contains(out.String(), `"disabled"`) {
		t.Fatal("retention 0 must touch nothing", err, store.calls, out.String())
	}
	t.Setenv("MEMORY_AUDIT_RETENTION_DAYS", "30")
	denied := &fakeAuditStore{fail: memory.ErrDenied}
	if err := expireAuditEvents(ctx, denied, "execute", &out); !errors.Is(err, memory.ErrDenied) || len(denied.calls) != 1 {
		t.Fatal("a store error must stop before any delete", err, denied.calls)
	}
	// A nil Postgres is refused by the store itself, through the daemon entry point.
	if err := runDaemonAuditExpiry(ctx, nil, daemonOptions{AuditExpiry: "preview"}, ""); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal(err)
	}
	if handled, err := runDaemonMaintenance(ctx, nil, daemonOptions{AuditExpiry: "preview"}, ""); !handled || !errors.Is(err, memory.ErrInvalid) {
		t.Fatal(handled, err)
	}
}
