//go:build integration

package memory

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// Recovery mode claims only erasure jobs and never touches embedding jobs.
func TestIntegrationRecoveryClaims(t *testing.T) {
	repo := isolatedPostgres(t)
	ctx := itContext(t)
	scope := itScope("recovery-claims")
	embedding := itRecord(scope, "")
	embedding.Kind = "embedding"
	embedding.Status = JobStatusPending
	if e := repo.Insert(ctx, colJobs, embedding); e != nil {
		t.Fatal(e)
	}
	embedding = itJob(t, repo, ctx, scope, embedding.ID)
	recovery := &Postgres{pool: repo.pool, erasureOnly: true}
	for _, kind := range erasureJobKinds {
		job := itRecord(scope, "")
		job.Kind = kind
		job.Status = JobStatusPending
		if e := repo.Insert(ctx, colJobs, job); e != nil {
			t.Fatal(e)
		}
		claimed, e := recovery.ClaimJob(ctx, time.Now().UTC(), time.Minute)
		if e != nil || claimed.ID != job.ID {
			t.Fatalf("claim %s: %+v %v", kind, claimed, e)
		}
	}
	if _, e := recovery.ClaimJob(ctx, time.Now().UTC(), time.Minute); !errors.Is(e, ErrNotFound) {
		t.Fatalf("claimed embedding: %v", e)
	}
	after := itJob(t, repo, ctx, scope, embedding.ID)
	if !reflect.DeepEqual(after, embedding) {
		t.Fatal("recovery changed embedding")
	}
}
