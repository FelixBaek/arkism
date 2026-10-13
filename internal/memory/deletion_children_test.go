package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDeletionChildSummary(t *testing.T) { testDeletionChildSummary(t, nil) }
func testDeletionChildSummary(t *testing.T, repo Repository) {
	f := newFixture(t)
	if repo != nil {
		f.svc.Repo = repo
	}
	repo = f.svc.Repo
	src := f.source()
	for _, col := range []string{"memories", "memory_candidates"} {
		if e := repo.Insert(testCtx, col, Record{ID: "erased-" + col, Scope: scopeA, Revision: 1, Sources: refs(src)}); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
		t.Fatal(e)
	}
	token := erasureWorker(f)
	job, e := repo.ClaimJob(testCtx, time.Now(), time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.svc.eraseSourceMemories(testCtx, token, job); e != nil {
		t.Fatal(e)
	}
	status, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(status)
	var body map[string]any
	if e = json.Unmarshal(raw, &body); e != nil {
		t.Fatal(e)
	}
	child, ok := body["dependent_cleanup"].(map[string]any)
	if !ok || child["expected_jobs"] != float64(2) || child["observed_jobs"] != float64(2) || child["tracking_complete"] != true || child["complete"] != false {
		t.Fatal("missing or false child progress", string(raw))
	}

	if status.DependentCleanup.Statuses[JobStatusPending] != 2 {
		t.Fatal("pending count", status)
	}
	scopes := []Scope{scopeB, {WorkspaceID: "other", ProjectID: scopeA.ProjectID}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, UserID: "other"}, {WorkspaceID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, AgentID: "other"}}
	for i, scope := range scopes {
		row := Record{ID: fmt.Sprintf("foreign-child-%d", i), Scope: scope, Revision: 1, Kind: candidateEvaluationErasureKind, Status: JobStatusFailed, ErasureParentID: job.ID}
		if e = repo.Insert(testCtx, "jobs", row); e != nil {
			t.Fatal(e)
		}
	}
	for _, col := range []string{"memories", "memory_candidates"} {
		row, e := repo.Get(testCtx, "jobs", scopeA, deletionJobID(col, scopeA, "erased-"+col))
		if e != nil || row.ErasureParentID != job.ID {
			t.Fatal("missing parent", row, e)
		}
		encoded, _ := json.Marshal(row)
		if strings.Contains(string(encoded), "erasure_parent_id") {
			t.Fatal("private linkage leaked")
		}
		changed := row
		changed.ErasureParentID = "wrong-parent"
		if e = repo.Replace(testCtx, "jobs", changed, row.Revision); !errors.Is(e, ErrConflict) {
			t.Fatal("parent linkage mutable", e)
		}
		prev := row.Revision
		row.Revision++
		row.Status = JobStatusFailed
		if e = repo.Replace(testCtx, "jobs", row, prev); e != nil {
			t.Fatal(e)
		}
	}
	if e = f.svc.WorkOnce(testCtx, token); e != nil {
		t.Fatal(e)
	} // only the source phase is runnable
	status, e = f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
	if e != nil || status.Status != JobStatusDone || status.DependentCleanup.Complete || status.DependentCleanup.Statuses[JobStatusFailed] != 2 || status.DependentCleanup.ObservedJobs != 2 {
		t.Fatal("root done hid failed children", status, e)
	}
	f.auth.actors["retry-human"] = actor("reviewer", "human", false, "retry")
	for i, col := range []string{"memories", "memory_candidates"} {
		id := "erased-" + col
		row, e := repo.Get(testCtx, "jobs", scopeA, deletionJobID(col, scopeA, id))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.svc.retryDeletion(testCtx, "retry-human", col, id, RetrySourceDeletionInput{Scope: scopeA, ExpectedRevision: row.Revision}); e != nil {
			t.Fatal(e)
		}
		if e = f.svc.WorkOnce(testCtx, token); e != nil {
			t.Fatal(e)
		}
		status, e = f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
		if e != nil || status.DependentCleanup.Statuses[JobStatusDone] != int64(i+1) || status.DependentCleanup.Complete != (i == 1) {
			t.Fatal("child completion summary", status, e)
		}
	}
	if status.FullErasureVerified {
		t.Fatal("full erasure overstated")
	}
	f.svc.Repo = relationAuditFailureRepo{Repository: repo, action: "read:source_deletion"}
	failed, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
	if !errors.Is(e, ErrUnavailable) || failed.DependentCleanup != nil {
		t.Fatal("audit failure returned partial progress", failed, e)
	}
}

func TestDeletionChildTrackingUnavailable(t *testing.T) {
	for _, mode := range []string{"unlinked", "legacy", "negative", "unknown_status", "missing_registration"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			src := f.source()
			if e := f.svc.Delete(testCtx, f.human, "sources", src.ID, ChangeInput{Scope: scopeA, ExpectedRevision: src.Revision}); e != nil {
				t.Fatal(e)
			}
			row, e := f.repo.Get(testCtx, "jobs", scopeA, sourceMemoryErasureID(scopeA, src.ID))
			if e != nil {
				t.Fatal(e)
			}
			row.Status = JobStatusDone
			row.SourceMemoryErasure = &SourceMemoryErasure{QueuedMemoryJobs: 1}
			switch mode {
			case "missing_registration":
				row.SourceMemoryErasure = &SourceMemoryErasure{DeletedCandidates: 1}
			case "legacy":
				row.Kind = sourceRelationErasureKind
			case "negative":
				row.SourceMemoryErasure.QueuedMemoryJobs = -1
			case "unknown_status":
				if e = f.repo.Insert(testCtx, "jobs", Record{ID: "bad-child", Scope: scopeA, Revision: 1, Kind: memoryEvaluationErasureKind, Status: "unknown", ErasureParentID: row.ID}); e != nil {
					t.Fatal(e)
				}
			}
			if e = f.repo.Replace(testCtx, "jobs", row, row.Revision); e != nil {
				t.Fatal(e)
			}
			got, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, src.ID)
			if mode == "negative" || mode == "unknown_status" {
				if !errors.Is(e, ErrUnavailable) || got.DependentCleanup != nil {
					t.Fatal("invalid tracking accepted", got, e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if mode == "legacy" {
				if got.DependentCleanup != nil {
					t.Fatal("legacy completion inferred")
				}
				return
			}
			if got.DependentCleanup == nil || got.DependentCleanup.TrackingComplete || got.DependentCleanup.Complete || got.DependentCleanup.ObservedJobs != 0 {
				t.Fatal("unlinked completion inferred", got)
			}
		})
	}
}
