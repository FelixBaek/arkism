package memory

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// failingEmbedder fails its first n calls with err, then delegates.
type failingEmbedder struct {
	inner Embedder
	n     int
	err   error
	calls int
}

func (e *failingEmbedder) Embed(ctx context.Context, texts []string, purpose string) ([]Embedding, error) {
	e.calls++
	if e.calls <= e.n {
		return nil, e.err
	}
	return e.inner.Embed(ctx, texts, purpose)
}

func (e *failingEmbedder) Space() string { return e.inner.Space() }

// outageFixture creates one conversation source and one pending embedding job
// for it, queued at created; any other job is finished so only this one runs.
func outageFixture(t *testing.T, created time.Time) (*fixture, *Postgres, Record) {
	t.Helper()
	p := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = p
	src := f.create(f.agent, "sources", sourceInput(CreateInput{Scope: scopeA, Kind: conversationSourceKind, Text: "We moved the planning meeting to Thursday."}))
	for _, j := range pgCollection(t, p, colJobs) {
		j.Status = JobStatusDone
		j.Revision++
		if e := p.Replace(testCtx, colJobs, j, j.Revision-1); e != nil {
			t.Fatal(e)
		}
	}
	job := Record{ID: ID(), Scope: scopeA, Kind: "embedding", Status: JobStatusPending, Revision: 1, CreatedAt: created, UpdatedAt: created, Target: &Ref{Collection: "sources", ID: src.ID, Revision: src.Revision}}
	if e := p.Insert(testCtx, colJobs, job); e != nil {
		t.Fatal(e)
	}
	return f, p, job
}

// makeDue clears the backoff so the next WorkOnce can claim the job again.
func makeDue(t *testing.T, p *Postgres, id string) Record {
	t.Helper()
	j, e := p.Get(testCtx, colJobs, scopeA, id)
	if e != nil {
		t.Fatal(e)
	}
	if j.Status == JobStatusRetry || j.Status == JobStatusPending {
		past := time.Now().Add(-time.Second)
		j.RunAt = &past
		j.Revision++
		if e = p.Replace(testCtx, colJobs, j, j.Revision-1); e != nil {
			t.Fatal(e)
		}
	}
	return j
}

func TestEmbedderOutageDoesNotExhaustAttempts(t *testing.T) {
	f, p, job := outageFixture(t, time.Now().UTC())
	down := &failingEmbedder{inner: f.emb, n: maxJobAttempts + 3, err: fmt.Errorf("call dependency: connection refused: %w", ErrUnavailable)}
	f.svc.Embedder = down
	for i := 0; i <= down.n; i++ {
		makeDue(t, p, job.ID)
		if e := f.svc.WorkOnce(testCtx, f.work); e != nil {
			t.Fatalf("round %d: %v", i, e)
		}
	}
	got, e := p.Get(testCtx, colJobs, scopeA, job.ID)
	if e != nil || got.Status != JobStatusDone || down.calls != down.n+1 {
		t.Fatal("outage job did not finish once the embedder came back", got.Status, got.Attempts, down.calls, e)
	}
	src, e := p.Get(testCtx, "sources", scopeA, job.Target.ID)
	if e != nil || src.Embedding == nil || src.Embedding.Space != f.emb.Space() {
		t.Fatal("source not embedded after recovery", e)
	}
}

func TestEmbedderOutagePastWindowAndBadInputStillFail(t *testing.T) {
	cases := map[string]struct {
		created time.Time
		err     error
	}{
		"rejected_input": {time.Now().UTC(), fmt.Errorf("embed: dependency status 400: %w", ErrInvalid)},
		"outage_too_old": {time.Now().UTC().Add(-embedOutageWindow - time.Hour), fmt.Errorf("call dependency: %w", ErrUnavailable)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f, p, job := outageFixture(t, c.created)
			bad := &failingEmbedder{inner: f.emb, n: 1 << 20, err: c.err}
			f.svc.Embedder = bad
			for i := 0; i < maxJobAttempts; i++ {
				makeDue(t, p, job.ID)
				if e := f.svc.WorkOnce(testCtx, f.work); e == nil {
					t.Fatalf("round %d: failure reported as success", i)
				}
			}
			got, e := p.Get(testCtx, colJobs, scopeA, job.ID)
			if e != nil || got.Status != JobStatusFailed || got.Attempts != maxJobAttempts || bad.calls != maxJobAttempts {
				t.Fatal("bad input must fail after the normal attempts", got.Status, got.Attempts, bad.calls, e)
			}
		})
	}
}

func TestPostgresReindexRequeuesFailedEmbeddingsOnce(t *testing.T) {
	p, scope := openPostgresListsTest(t)
	ctx := context.Background()
	x := scope.ProjectID + "-"
	now := time.Now().UTC().Truncate(time.Millisecond)
	insertPGRecords(t, p, colMemories,
		Record{ID: x + "mem", Scope: scope, Kind: "user", Status: statusActive, Revision: 1, Text: "a", CreatedAt: now, UpdatedAt: now},
		Record{ID: x + "gone", Scope: scope, Kind: "user", Status: "deleted", Revision: 1, Text: "b", CreatedAt: now, UpdatedAt: now})
	insertPGRecords(t, p, "sources",
		Record{ID: x + "src", Scope: scope, Kind: conversationSourceKind, Status: statusActive, Revision: 1, Text: "c", CreatedAt: now, UpdatedAt: now})
	failed := func(id, col string) Record {
		return Record{ID: id + "-job", Scope: scope, Kind: "embedding", Status: JobStatusFailed, Attempts: maxJobAttempts, Revision: 3, CreatedAt: now, UpdatedAt: now, Target: &Ref{Collection: col, ID: id, Revision: 1}}
	}
	insertPGRecords(t, p, colJobs, failed(x+"mem", colMemories), failed(x+"gone", colMemories), failed(x+"src", "sources"))
	if _, e := p.EnqueueReindex(ctx, "test:other-space:1024d"); e != nil {
		t.Fatal(e)
	}
	count := map[string]int{}
	rows, e := p.q().Query(ctx, `SELECT id, status, doc->'target'->>'id' FROM memory_records WHERE collection='jobs' AND workspace_id=$1`, scope.WorkspaceID)
	if e != nil {
		t.Fatal(e)
	}
	for rows.Next() {
		var id, status, target string
		_ = rows.Scan(&id, &status, &target)
		count[target]++
		if target != x+"gone" && (id != target+"-job" || status != JobStatusPending) {
			t.Fatal("failed job not requeued in place", id, status)
		}
	}
	rows.Close()
	if count[x+"mem"] != 1 || count[x+"src"] != 1 || count[x+"gone"] != 1 {
		t.Fatal("expected one job per target", count)
	}
	for _, id := range []string{x + "mem", x + "src"} {
		j, e := p.Get(ctx, colJobs, scope, id+"-job")
		if e != nil || j.Attempts != 0 || j.Revision != 4 {
			t.Fatal("requeue must reset attempts and bump revision", j.Attempts, j.Revision, e)
		}
	}
	if gone, e := p.Get(ctx, colJobs, scope, x+"gone-job"); e != nil || gone.Status != JobStatusFailed {
		t.Fatal("deleted target was requeued", gone.Status, e)
	}
	again, e := p.EnqueueReindex(ctx, "test:other-space:1024d")
	if e != nil || again != 0 {
		t.Fatal("second run queued again", again, e)
	}
}
