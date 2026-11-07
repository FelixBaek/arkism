package main

import (
	"context"
	"github.com/FelixBaek/arkism/internal/memory"
)

type postgresBackend struct{ store *memory.Postgres }

func openBackend(ctx context.Context, dsn string) (backend, error) {
	p, e := memory.NewPostgres(ctx, dsn)
	if e != nil {
		return nil, e
	}
	return &postgresBackend{p}, nil
}
func (b *postgresBackend) Close() { b.store.Close() }
func (b *postgresBackend) Execute(ctx context.Context, o options, in maintenanceInput) (any, error) {
	switch o.command {
	case "status":
		return b.store.MemoryMaintenanceStatus(ctx)
	case "advance":
		return b.store.AdvanceMemoryMaintenance(ctx, o.revision)
	case "cancel":
		return b.store.CancelMemoryMaintenance(ctx, o.revision)
	case "hold", "release":
		return b.store.HoldMemoryMaintenance(ctx, o.revision, o.command == "hold")
	case "begin-privacy", "begin-supersede":
		auth, e := memory.NewPostgresAuthorizer(b.store)
		if e != nil {
			return nil, e
		}
		service := &memory.Service{Repo: b.store, Auth: auth}
		if o.command == "begin-privacy" {
			return service.BeginPrivacyMaintenance(ctx, in.token, *in.privacy)
		}
		return service.BeginSupersedeMaintenance(ctx, in.token, o.memoryID, *in.supersede)
	default:
		return nil, errInput
	}
}
