package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func erasureWorker(f *fixture) string {
	f.auth.actors["eraser"] = actor("eraser-1", "service", false, "erase")
	return "eraser"
}
func TestSourceMemoryErasureBatches(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	token := erasureWorker(f)
	for i := 0; i < sourceMemoryErasureBatch+2; i++ {
		col := "memories"
		if i%2 == 0 {
			col = "memory_candidates"
		}
		r := Record{ID: fmt.Sprintf("child-%03d", i), Scope: scopeA, Revision: 1, Status: "expired", Text: "private original copy", Sources: refs(src), Embedding: &Embedding{Vector: []float64{1}}}
		if e := f.repo.Insert(testCtx, col, r); e != nil {
			t.Fatal(e)
		}
	}
	for _, col := range []string{"skill_versions", "reflections", "memories"} {
		r := Record{ID: "preserved-" + col, Scope: scopeA, Revision: 1, Sources: refs(src)}
		if col == "memories" {
			r.Scope = scopeB
		}
		if e := f.repo.Insert(testCtx, col, r); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	// Preserve the former direct-record contract as an actual legacy job.
	job, _ := f.repo.Get(testCtx, "jobs", scopeA, sourceMemoryErasureID(scopeA, src.ID))
	job.Kind = sourceRecordErasureKind
	if e := f.repo.Replace(testCtx, "jobs", job, job.Revision); e != nil {
		t.Fatal(e)
	}
	for i, want := range []int64{100, 102, 102} {
		if e := f.svc.WorkOnce(testCtx, token); e != nil {
			t.Fatal(e)
		}
		status, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
		if e != nil || status.DeletedMemories+status.DeletedCandidates != want || status.Attempts != 0 || status.FullErasureVerified {
			t.Fatalf("batch %d: %+v %v", i, status, e)
		}
		expected := JobStatusPending
		if i == 2 {
			expected = JobStatusDone
		}
		if status.Status != expected {
			t.Fatalf("status %s want %s", status.Status, expected)
		}
	}
	if e := f.svc.WorkOnce(testCtx, token); !errors.Is(e, ErrNotFound) {
		t.Fatalf("reprocessed done: %v", e)
	}
	if _, e := f.repo.Get(testCtx, "reflections", scopeA, "preserved-reflections"); !errors.Is(e, ErrNotFound) {
		t.Fatal("reflection not erased", e)
	}
	for _, col := range []string{"skill_versions", "memories"} {
		scope := scopeA
		if col == "memories" {
			scope = scopeB
		}
		if _, e := f.repo.Get(testCtx, col, scope, "preserved-"+col); e != nil {
			t.Fatal("outside phase deleted", e)
		}
	}
	req := httptest.NewRequest("GET", "/v1/sources/"+src.ID+"/deletion?workspace_id=ws-a&project_id=proj-a", nil)
	req.Header.Set("Authorization", "Bearer "+f.human)
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "child-") || !strings.Contains(w.Body.String(), `"full_erasure_verified":false`) {
		t.Fatalf("status response: %d %s", w.Code, w.Body.String())
	}
	if len(f.emb.calls) != 0 {
		t.Fatal("erasure called embedding provider")
	}
}

func TestSourceMemoryErasureFailureAndLease(t *testing.T) {
	for _, name := range []string{"row_denied", "principal", "agent", "pipeline", "human", "audit", "expired_lease", "bad_receipt"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			token := erasureWorker(f)
			child := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "private copy", Sources: refs(src)}))
			if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
				t.Fatal(e)
			}
			switch name {
			case "row_denied":
				f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: child.ID}
			case "principal":
				f.svc.Auth = usagePrincipalAuth{Authorizer: f.auth, action: "erase:memory_candidates"}
			case "agent", "pipeline", "human":
				f.auth.actors[token] = actor("eraser-1", name, false, "erase")
			case "audit":
				f.repo.failAudit = true
			case "bad_receipt":
				delete(f.repo.store["jobs"], recKey(scopeA, sourceDeletionID(scopeA, src.ID)))
			}
			if name == "expired_lease" {
				job, e := f.repo.ClaimJob(testCtx, time.Now(), time.Nanosecond)
				if e != nil {
					t.Fatal(e)
				}
				if e = f.svc.eraseSourceMemories(testCtx, token, job); !errors.Is(e, ErrConflict) {
					t.Fatalf("expired lease: %v", e)
				}
			} else {
				for i := 0; i < maxJobAttempts; i++ {
					if e := f.svc.WorkOnce(testCtx, token); e == nil {
						t.Fatal("guard accepted")
					}
				}
				job, e := f.repo.Get(testCtx, "jobs", scopeA, sourceMemoryErasureID(scopeA, src.ID))
				if e != nil || job.Status != JobStatusFailed {
					t.Fatalf("retry bound: %+v %v", job, e)
				}
			}
			if _, e := f.repo.Get(testCtx, "memory_candidates", scopeA, child.ID); e != nil {
				t.Fatal("failure deleted child", e)
			}
			job, e := f.repo.Get(testCtx, "jobs", scopeA, sourceMemoryErasureID(scopeA, src.ID))
			if e != nil || job.SourceMemoryErasure.DeletedCandidates != 0 {
				t.Fatal("failure changed progress", e)
			}
		})
	}
}

func TestSourceMemoryErasureAtomicRequestAndFence(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	before := src.Revision
	f.repo.failAudit = true
	if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	if _, e := f.repo.Get(testCtx, "sources", scopeA, src.ID); e != nil {
		t.Fatal("request rollback lost source", e)
	}
	if _, e := f.repo.Get(testCtx, "jobs", scopeA, sourceMemoryErasureID(scopeA, src.ID)); !errors.Is(e, ErrNotFound) {
		t.Fatal("request rollback left job", e)
	}
	_, e := f.svc.Create(testCtx, f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "candidate", Sources: refs(src)}))
	if !errors.Is(e, ErrUnavailable) {
		t.Fatal(e)
	}
	saved, e := f.repo.Get(testCtx, "sources", scopeA, src.ID)
	if e != nil || saved.MemoryReferenceFence != 0 || saved.Revision != before {
		t.Fatalf("fence rollback %+v %v", saved, e)
	}
	f.repo.failAudit = false
	f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "candidate", Sources: refs(src)}))
	saved, e = f.repo.Get(testCtx, "sources", scopeA, src.ID)
	if e != nil || saved.MemoryReferenceFence != 1 || saved.Revision != before {
		t.Fatalf("fence %+v %v", saved, e)
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), "reference_fence") {
		t.Fatal("internal fence exposed")
	}
}

type erasurePolicyRepo struct {
	Repository
	active *atomic.Bool
}

func (r erasurePolicyRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return r.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		r.active.Store(true)
		defer r.active.Store(false)
		return fn(ctx, tx)
	})
}

type erasurePolicyGate struct {
	Authorizer
	active         *atomic.Bool
	ready, release chan struct{}
}

func (a erasurePolicyGate) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	if a.active.Load() {
		return Decision{}, ErrUnavailable
	}
	if in.Action == "erase:memory_candidates" {
		select {
		case a.ready <- struct{}{}:
		case <-ctx.Done():
			return Decision{}, ctx.Err()
		}
		select {
		case <-a.release:
		case <-ctx.Done():
			return Decision{}, ctx.Err()
		}
	}
	return a.Authorizer.Authorize(ctx, token, in)
}
func TestSourceErasurePolicyOutsideTransaction(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	token := erasureWorker(f)
	for i := 0; i < 8; i++ {
		f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: fmt.Sprintf("candidate %d", i), Sources: refs(src)}))
	}
	if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	active := &atomic.Bool{}
	gate := erasurePolicyGate{Authorizer: f.auth, active: active, ready: make(chan struct{}, 8), release: make(chan struct{})}
	f.svc.Auth = gate
	f.svc.Repo = erasurePolicyRepo{Repository: f.repo, active: active}
	ctx, cancel := context.WithTimeout(testCtx, 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- f.svc.WorkOnce(ctx, token) }()
	for i := 0; i < 8; i++ {
		select {
		case <-gate.ready:
		case <-ctx.Done():
			t.Fatal("policy did not run concurrently outside transaction")
		}
	}
	if active.Load() {
		t.Fatal("database transaction held during policy wait")
	}
	close(gate.release)
	if e := <-result; e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 10; i++ {
		if e := f.svc.WorkOnce(ctx, token); errors.Is(e, ErrNotFound) {
			break
		} else if e != nil {
			t.Fatal(e)
		}
	}
	status, e := f.svc.SourceDeletionStatus(ctx, f.human, scopeA, src.ID)
	if e != nil || status.Status != JobStatusDone || status.DeletedCandidates != 8 || status.QueuedCandidateJobs != 8 {
		t.Fatalf("progress %+v %v", status, e)
	}
}
func TestSourceErasureChangedAuthorizedBatch(t *testing.T) {
	f := newFixture(t)
	src := f.source()
	token := erasureWorker(f)
	child := f.create(f.human, "memory_candidates", candidateInput(CreateInput{Scope: scopeA, Text: "candidate", Sources: refs(src)}))
	if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	f.svc.Repo = evaluationChangeRepo{Repository: f.repo, change: func() {
		updated := child
		updated.Revision++
		if e := f.repo.Replace(testCtx, "memory_candidates", updated, child.Revision); e != nil {
			t.Fatal(e)
		}
	}}
	if e := f.svc.WorkOnce(testCtx, token); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale policy batch: %v", e)
	}
	if _, e := f.repo.Get(testCtx, "memory_candidates", scopeA, child.ID); e != nil {
		t.Fatal("changed row deleted", e)
	}
}

func TestSourceErasureRetry(t *testing.T) {
	for _, name := range []string{"success", "stale", "pending", "done", "running", "denied", "agent", "pipeline", "service", "audit", "receipt", "scope", "source_exists"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
				t.Fatal(e)
			}
			job, e := f.repo.Get(testCtx, "jobs", scopeA, sourceMemoryErasureID(scopeA, src.ID))
			if e != nil {
				t.Fatal(e)
			}
			job.Status = JobStatusFailed
			job.Attempts = 5
			job.SourceMemoryErasure = &SourceMemoryErasure{DeletedMemories: 7}
			job.Revision++
			if e = f.repo.Replace(testCtx, "jobs", job, job.Revision-1); e != nil {
				t.Fatal(e)
			}
			job, _ = f.repo.Get(testCtx, "jobs", scopeA, job.ID)
			f.auth.actors["retry-human"] = actor("retry-reviewer", "human", false, "retry")
			token := "retry-human"
			in := RetrySourceDeletionInput{Scope: scopeA, ExpectedRevision: job.Revision}
			want := error(nil)
			switch name {
			case "stale":
				in.ExpectedRevision--
				want = ErrConflict
			case "pending", "running", "done":
				job.Status = name
				if e = f.repo.Replace(testCtx, "jobs", job, job.Revision); e != nil {
					t.Fatal(e)
				}
				job, _ = f.repo.Get(testCtx, "jobs", scopeA, job.ID)
				in.ExpectedRevision = job.Revision
				want = ErrConflict
			case "denied":
				f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: src.ID}
				want = ErrDenied
			case "agent", "pipeline", "service":
				f.auth.actors[token] = actor("retry-reviewer", name, false, "retry")
				want = ErrDenied
			case "audit":
				f.repo.failAudit = true
				want = ErrUnavailable
			case "receipt":
				delete(f.repo.store["jobs"], recKey(scopeA, sourceDeletionID(scopeA, src.ID)))
				want = ErrNotFound
			case "scope":
				in.Scope = scopeB
				want = ErrNotFound
			case "source_exists":
				if e = f.repo.Insert(testCtx, "sources", src); e != nil {
					t.Fatal(e)
				}
				want = ErrConflict
			}
			var out Ref
			if name == "success" {
				body, err := json.Marshal(in)
				if err != nil {
					t.Fatal(err)
				}
				req := httptest.NewRequest("POST", "/v1/sources/"+src.ID+"/deletion/retry", strings.NewReader(string(body)))
				req.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				Handler(f.svc).ServeHTTP(w, req)
				if w.Code != 200 {
					t.Fatalf("retry HTTP: %d %s", w.Code, w.Body.String())
				}
				if err = json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
			} else {
				out, e = f.svc.RetrySourceDeletion(testCtx, token, src.ID, in)
			}
			if !errors.Is(e, want) {
				t.Fatalf("got %v want %v", e, want)
			}
			saved, _ := f.repo.Get(testCtx, "jobs", scopeA, job.ID)
			if want != nil {
				if saved.Revision != job.Revision || saved.Status != job.Status || saved.Attempts != 5 || out.ID != "" {
					t.Fatalf("failure mutated job: %+v", saved)
				}
				return
			}
			if saved.Status != JobStatusPending || saved.Attempts != 0 || saved.SourceMemoryErasure.DeletedMemories != 7 || saved.CreatedBy != job.CreatedBy || out.Revision != job.Revision+1 {
				t.Fatalf("bad retry: %+v %+v", saved, out)
			}
			if _, e = f.svc.RetrySourceDeletion(testCtx, token, src.ID, in); !errors.Is(e, ErrConflict) {
				t.Fatalf("duplicate retry: %v", e)
			}
			if e = f.svc.WorkOnce(testCtx, erasureWorker(f)); e != nil {
				t.Fatal(e)
			}
			status, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
			if e != nil || status.Status != JobStatusDone || status.Revision <= out.Revision || status.DeletedMemories != 7 {
				t.Fatalf("resumed: %+v %v", status, e)
			}
		})
	}
}

func TestSourceErasureExecutionRecords(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		f := newFixture(t)
		src := f.source()
		token := erasureWorker(f)
		for _, col := range []string{"agent_runs", "tool_calls", "evaluations", "reflections"} {
			r := Record{ID: "derived-" + col, Scope: scopeA, Revision: 1, Status: "expired", Text: "private retained body", Sources: refs(src), Embedding: &Embedding{Vector: []float64{1}}}
			if e := f.repo.Insert(testCtx, col, r); e != nil {
				t.Fatal(e)
			}
			r.ID = "other-" + col
			r.Scope = scopeB
			if e := f.repo.Insert(testCtx, col, r); e != nil {
				t.Fatal(e)
			}
		}
		if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
			t.Fatal(e)
		}
		if legacy {
			job, _ := f.repo.Get(testCtx, "jobs", scopeA, sourceMemoryErasureID(scopeA, src.ID))
			job.Kind = sourceMemoryErasureKind
			job.SourceMemoryErasure = &SourceMemoryErasure{}
			if e := f.repo.Replace(testCtx, "jobs", job, job.Revision); e != nil {
				t.Fatal(e)
			}
		}
		if e := f.svc.WorkOnce(testCtx, token); e != nil {
			t.Fatal(e)
		}
		if !legacy {
			if e := f.svc.WorkOnce(testCtx, token); e != nil {
				t.Fatal(e)
			}
		}
		status, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
		if e != nil || status.Status != JobStatusDone || status.FullErasureVerified {
			t.Fatal(status, e)
		}
		expectedCoverage := sourceRecordsMemoryDependents
		if legacy {
			expectedCoverage = "source_memories"
		}
		if status.Coverage != expectedCoverage {
			t.Fatal("coverage", status.Coverage)
		}
		for _, col := range []string{"agent_runs", "tool_calls", "evaluations", "reflections"} {
			expectedCount := int64(1)
			if legacy {
				expectedCount = 0
			}
			if status.DeletedRecords[col] != expectedCount {
				t.Fatal("counter", col, status.DeletedRecords)
			}
			_, e := f.repo.Get(testCtx, col, scopeA, "derived-"+col)
			if legacy {
				if e != nil {
					t.Fatal("legacy scope expanded", e)
				}
			} else if !errors.Is(e, ErrNotFound) {
				t.Fatal("derived body remained", col, e)
			}
			if _, e = f.repo.Get(testCtx, col, scopeB, "other-"+col); e != nil {
				t.Fatal("foreign document deleted", e)
			}
		}
		if len(f.emb.calls) != 0 {
			t.Fatal("erasure called provider")
		}
	}
}

func TestSourceRecordErasureGuards(t *testing.T) {
	for _, name := range []string{"row_denied", "principal", "audit"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			token := erasureWorker(f)
			r := Record{ID: "private-reflection", Scope: scopeA, Revision: 1, Sources: refs(src), Text: "private summary"}
			if e := f.repo.Insert(testCtx, "reflections", r); e != nil {
				t.Fatal(e)
			}
			if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
				t.Fatal(e)
			}
			job, _ := f.repo.Get(testCtx, "jobs", scopeA, sourceMemoryErasureID(scopeA, src.ID))
			job.SourceMemoryErasure.DeletedRecords = map[string]int64{"agent_runs": 7}
			if e := f.repo.Replace(testCtx, "jobs", job, job.Revision); e != nil {
				t.Fatal(e)
			}
			switch name {
			case "row_denied":
				f.svc.Auth = reviewDenyAuth{Authorizer: f.auth, denyID: r.ID}
			case "principal":
				f.svc.Auth = usagePrincipalAuth{Authorizer: f.auth, action: "erase:reflections"}
			case "audit":
				f.repo.failAudit = true
			}
			if e := f.svc.WorkOnce(testCtx, token); e == nil {
				t.Fatal("guard bypassed")
			}
			if _, e := f.repo.Get(testCtx, "reflections", scopeA, r.ID); e != nil {
				t.Fatal("failure lost record", e)
			}
			job, _ = f.repo.Get(testCtx, "jobs", scopeA, job.ID)
			if job.SourceMemoryErasure.DeletedRecords["agent_runs"] != 7 || job.SourceMemoryErasure.DeletedRecords["reflections"] != 0 {
				t.Fatal("failure changed counters", job.SourceMemoryErasure.DeletedRecords)
			}
		})
	}
}
