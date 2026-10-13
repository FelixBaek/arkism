package memory

import (
	"context"
	"testing"
	"time"
)

func TestPostgresEnqueueReindexOnlyLiveOutOfSpaceRecords(t *testing.T) {
	p, scope := openPostgresListsTest(t)
	ctx := context.Background()
	x := scope.ProjectID + "-"
	now := time.Now().UTC().Truncate(time.Millisecond)
	insertPGRecords(t, p, colMemories,
		Record{ID: x + "live", Scope: scope, Kind: "user", Status: statusActive, Revision: 1, Text: "a", CreatedAt: now, UpdatedAt: now},
		Record{ID: x + "deleted", Scope: scope, Kind: "user", Status: "deleted", Revision: 1, Text: "b", CreatedAt: now, UpdatedAt: now})
	queued, err := p.EnqueueReindex(ctx, "gemini:gemini-embedding-2:test:1024d")
	if err != nil || queued < 1 {
		t.Fatalf("queued=%d err=%v", queued, err)
	}
	job := false
	rows, err := p.q().Query(ctx, `SELECT doc->'target'->>'id' FROM memory_records WHERE collection='jobs' AND workspace_id=$1`, scope.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		if id == x+"deleted" {
			t.Fatal("deleted memory queued for embedding")
		}
		job = job || id == x+"live"
	}
	rows.Close()
	if !job {
		t.Fatal("live memory not queued")
	}
	if _, err = p.EnqueueReindex(ctx, ""); err == nil {
		t.Fatal("empty space accepted")
	}
}
