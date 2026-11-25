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

func TestPostgresEnqueueSingleMemoryReindex(t *testing.T) {
	p, scope := openPostgresListsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	src := Record{ID: "source", Scope: scope, Kind: "user", Status: statusActive, Revision: 1, Text: "Synthetic source", CreatedAt: now, UpdatedAt: now}
	insertPGRecords(t, p, "sources", src)
	m := Record{ID: "target", Scope: scope, Kind: "user", Status: statusActive, Revision: 6, Text: "Synthetic lesson", Sources: []SourceRef{{SourceID: src.ID, Locator: "original"}}, CreatedAt: now, UpdatedAt: now}
	other := m
	other.ID = "untargeted"
	restricted := m
	restricted.ID = "restricted"
	restricted.Sensitivity = "restricted"
	insertPGRecords(t, p, colMemories, m, other, restricted)
	insertPGRecords(t, p, colJobs, Record{ID: "obsolete-job", Scope: scope, Kind: "embedding", Status: "done", Revision: 1, Target: &Ref{Collection: colMemories, ID: m.ID, Revision: 2}, CreatedAt: now, UpdatedAt: now})
	// A stale leased job queues the current revision once, even if retried.
	svc := &Service{Repo: p, Embedder: &fakeEmbedder{space: "local-test"}}
	stale := Record{Scope: scope, Target: &Ref{Collection: colMemories, ID: m.ID, Revision: 2}}
	for range 2 {
		done, err := svc.embedJob(ctx, "", &stale)
		if !done || err != nil {
			t.Fatalf("stale job did not finish: done=%v err=%v", done, err)
		}
	}
	if n, err := p.EnqueueMemoryReindex(ctx, "local-test", scope, m.ID); n != 0 || err != nil {
		t.Fatalf("repeat single-memory repair: queued=%d err=%v", n, err)
	}
	if n, err := p.EnqueueMemoryReindex(ctx, "local-test", scope, restricted.ID); n != 0 || err != ErrDenied {
		t.Fatalf("restricted: %d %v", n, err)
	}
	var count, selected int
	if err := p.q().QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE doc->'target'->>'id'=$1 AND doc->'target'->>'revision'='6') FROM memory_records WHERE collection='jobs' AND workspace_id=$2 AND status='pending'`, m.ID, scope.WorkspaceID).Scan(&count, &selected); err != nil || count != 1 || selected != 1 {
		t.Fatalf("jobs=%d selected=%d err=%v", count, selected, err)
	}
	r, err := p.Get(ctx, colMemories, scope, m.ID)
	if err != nil || r.Revision != 6 || r.Text != m.Text {
		t.Fatalf("target changed rev=%d err=%v", r.Revision, err)
	}
}

func TestPostgresEnqueueSingleMemoryReindexConcurrentRetry(t *testing.T) {
	pg, scope := openPostgresListsTest(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	src := Record{ID: "synthetic-source", Scope: scope, Status: statusActive, Revision: 1, Text: "Test source", CreatedAt: now, UpdatedAt: now}
	r := Record{ID: "synthetic-memory", Scope: scope, Status: statusActive, Revision: 2, Text: "Test memory", Sources: []SourceRef{{SourceID: src.ID, Locator: "original"}}, CreatedAt: now, UpdatedAt: now}
	insertPGRecords(t, pg, "sources", src)
	insertPGRecords(t, pg, colMemories, r)
	failed := Record{ID: "failed-embedding", Scope: scope, Kind: "embedding", Status: JobStatusFailed, Revision: 3, Target: &Ref{Collection: colMemories, ID: r.ID, Revision: r.Revision}, Attempts: 3, CreatedAt: now, UpdatedAt: now}
	insertPGRecords(t, pg, colJobs, failed)
	type outcome struct {
		count int
		err   error
	}
	results := make(chan outcome, 4)
	ready := make(chan struct{})
	for range cap(results) {
		go func() {
			<-ready
			n, err := pg.EnqueueMemoryReindex(ctx, "test-space", scope, r.ID)
			results <- outcome{n, err}
		}()
	}
	close(ready)
	queued := 0
	for range cap(results) {
		res := <-results
		if res.err != nil {
			t.Fatal(res.err)
		}
		queued += res.count
	}
	if queued != 1 {
		t.Fatalf("queued %d repairs for one revision", queued)
	}
	job, err := pg.Get(ctx, colJobs, scope, failed.ID)
	if err != nil || job.Status != JobStatusPending || job.Revision != 4 || job.Attempts != 0 {
		t.Fatalf("failed job was not reused: status=%s revision=%d attempts=%d err=%v", job.Status, job.Revision, job.Attempts, err)
	}
	different := scope
	different.ProjectID = "unrelated"
	if n, err := pg.EnqueueMemoryReindex(ctx, "test-space", different, r.ID); n != 0 || err != ErrNotFound {
		t.Fatalf("cross-scope repair n=%d err=%v", n, err)
	}
}
