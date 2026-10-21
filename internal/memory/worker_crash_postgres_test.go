package memory

import (
	"bufio"
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Kill a real worker after its claim committed but while its completion write
// is still uncommitted. PostgreSQL must roll back the partial write and another
// worker must be able to reclaim the durable lease. The DB server stays alive.
func TestPostgresWorkerCrashRollsBackPartialFinishAndReclaimsLease(t *testing.T) {
	if database := os.Getenv("ARKISM_TEST_CRASH_DATABASE"); database != "" {
		if !strings.HasPrefix(database, "arkism_test_") {
			os.Exit(2)
		}
		cfg, err := pgxpool.ParseConfig(os.Getenv("POSTGRES_TEST_DSN"))
		if err != nil {
			os.Exit(2)
		}
		cfg.ConnConfig.Database = database
		cfg.ConnConfig.RuntimeParams["search_path"] = "arkism,extensions,pg_catalog"
		pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
		if err != nil {
			os.Exit(2)
		}
		p := &Postgres{pool: pool, ownsPool: true}
		defer p.Close()
		job, err := p.ClaimJob(context.Background(), time.Now().UTC(), time.Minute)
		if err != nil {
			os.Exit(2)
		}
		err = p.Transaction(context.Background(), func(ctx context.Context, tx Tx) error {
			job.Status = JobStatusDone
			job.Revision++
			if err := tx.Replace(ctx, colJobs, job, job.Revision-1); err != nil {
				return err
			}
			fmt.Println("partial-finish-written")
			for {
				time.Sleep(time.Hour)
			}
		})
		if err != nil {
			os.Exit(2)
		}
		return
	}
	p := isolatedPostgres(t)
	now := time.Now().UTC()
	if err := p.Insert(testCtx, colJobs, Record{ID: "crash-job", Scope: scopeA, Kind: "embedding", Status: JobStatusPending, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPostgresWorkerCrashRollsBackPartialFinishAndReclaimsLease$")
	cmd.Env = append(os.Environ(), "ARKISM_TEST_CRASH_DATABASE="+p.pool.Config().ConnConfig.Database)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(pipe)
		ready <- scanner.Scan() && scanner.Text() == "partial-finish-written"
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("crash worker did not write")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("crash worker timeout")
	}
	committed, err := p.Get(testCtx, colJobs, scopeA, "crash-job")
	if err != nil || committed.Status != JobStatusRunning || committed.Attempts != 1 {
		t.Fatal("uncommitted finish visible", err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	deadline := time.Now().Add(3 * time.Second)
	var reclaimed Record
	for {
		reclaimed, err = p.ClaimJob(testCtx, now.Add(3*time.Minute), time.Minute)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("killed worker retained its lock", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reclaimed.ID != "crash-job" || reclaimed.Attempts != 2 || reclaimed.LeaseToken == committed.LeaseToken {
		t.Fatal("durable claim did not recover")
	}
	if err = p.FinishJob(testCtx, committed, true); err != ErrConflict {
		t.Fatal("killed worker's lease still valid", err)
	}
	if err = p.FinishJob(testCtx, reclaimed, true); err != nil {
		t.Fatal("new worker cannot complete recovered job", err)
	}
}
