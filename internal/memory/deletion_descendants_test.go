package memory

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDeletionDescendantSummary(t *testing.T) { checkDeletionDescendants(t, newFixture(t)) }
func TestPostgresDeletionDescendantSummary(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	checkDeletionDescendants(t, f)
}
func TestPostgresDeletionChildSummary(t *testing.T) {
	f := newFixture(t)
	f.svc.Repo = isolatedPostgres(t)
	if e := enqueueSourceMemoryErasure(testCtx, f.svc.Repo, scopeA, "root", 1, "human-1"); e != nil {
		t.Fatal(e)
	}
	root, e := f.svc.Repo.Get(testCtx, colJobs, scopeA, sourceMemoryErasureID(scopeA, "root"))
	if e != nil {
		t.Fatal(e)
	}
	if e = enqueueMemoryErasureAs(testCtx, f.svc.Repo, colMemories, scopeA, "child", 1, "human-1", roleHuman, nil, root.ID); e != nil {
		t.Fatal(e)
	}
	root.Status = JobStatusDone
	root.SourceMemoryErasure.DeletedMemories = 1
	root.SourceMemoryErasure.QueuedMemoryJobs = 1
	root.Revision++
	if e = f.svc.Repo.Replace(testCtx, colJobs, root, root.Revision-1); e != nil {
		t.Fatal(e)
	}
	status, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, "root")
	if e != nil || status.DependentCleanup == nil || status.DependentCleanup.Complete || !status.DependentCleanup.TrackingComplete || status.DependentCleanup.Statuses[JobStatusPending] != 1 {
		t.Fatal("pending memory child", status, e)
	}
	child, e := f.svc.Repo.Get(testCtx, colJobs, scopeA, deletionJobID(colMemories, scopeA, "child"))
	if e != nil {
		t.Fatal(e)
	}
	child.Status = JobStatusDone
	child.Revision++
	if e = f.svc.Repo.Replace(testCtx, colJobs, child, child.Revision-1); e != nil {
		t.Fatal(e)
	}
	status, e = f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, "root")
	if e != nil || !status.DependentCleanup.Complete {
		t.Fatal("completed memory child", status, e)
	}
}

func checkDeletionDescendants(t *testing.T, f *fixture) {
	t.Helper()
	repo := f.svc.Repo
	makeJob := func(name string) Record {
		r := Record{ID: sourceMemoryErasureID(scopeA, name), Scope: scopeA, Kind: sourceMemoryDependentsErasureKind, Status: JobStatusDone, Revision: 1, CreatedBy: "human-1", CreatedByType: roleHuman, Target: &Ref{Collection: "sources", ID: name, Revision: 1}, SourceMemoryErasure: &SourceMemoryErasure{DeletedRecords: map[string]int64{}}}
		return r
	}
	root, a, b, shared := makeJob("root"), makeJob("a"), makeJob("b"), makeJob("shared")
	link := func(r *Record, children ...Record) {
		r.SourceMemoryErasure.QueuedSourceJobs = int64(len(children))
		r.SourceMemoryErasure.DeletedRecords["sources"] = int64(len(children))
		for _, child := range children {
			r.SourceMemoryErasure.DerivedSourceJobs = append(r.SourceMemoryErasure.DerivedSourceJobs, child.ID)
		}
	}
	link(&root, a, b)
	link(&a, shared)
	link(&b, shared)
	shared.Status = JobStatusFailed
	shared.CreatedByType = "service"
	shared.CreatedBy = "privacy-worker"
	shared.ErasureParentID = "privacy-deletion-" + ContentHash("privacy-request")
	for _, r := range []Record{root, a, b, shared} {
		if e := repo.Insert(testCtx, colJobs, r); e != nil {
			t.Fatal(e)
		}
	}
	status, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, "root")
	if e != nil || status.DependentCleanup == nil {
		t.Fatal("summary", e)
	}
	s := status.DependentCleanup
	if s.Complete || !s.TrackingComplete || s.ExpectedJobs != 3 || s.ObservedJobs != 3 || s.Statuses[JobStatusFailed] != 1 {
		t.Fatal("failed/shared descendants", s)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), shared.ID) || strings.Contains(string(raw), "derived_source_jobs") || strings.Contains(string(raw), shared.ErasureParentID) {
		t.Fatal("private linkage leaked")
	}
	current, e := repo.Get(testCtx, colJobs, scopeA, shared.ID)
	if e != nil {
		t.Fatal(e)
	}
	current.Status = JobStatusDone
	current.Revision++
	if e = repo.Replace(testCtx, colJobs, current, current.Revision-1); e != nil {
		t.Fatal(e)
	}
	status, e = f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, "root")
	if e != nil || !status.DependentCleanup.Complete || status.FullErasureVerified {
		t.Fatal("descendant completion", e, status)
	}
	current, e = repo.Get(testCtx, colJobs, scopeA, shared.ID)
	if e != nil || current.ErasureParentID != shared.ErasureParentID {
		t.Fatal("privacy parent changed", e)
	}
	if e = repo.Delete(testCtx, colJobs, scopeA, shared.ID, current.Revision); e != nil {
		t.Fatal(e)
	}
	status, e = f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, "root")
	if e != nil || status.DependentCleanup.Complete || status.DependentCleanup.TrackingComplete {
		t.Fatal("missing shared child accepted", e)
	}
}

func TestDeletionDescendantInvalidTracking(t *testing.T) {
	for _, mode := range []string{"cycle", "foreign_scope", "legacy", "overflow", "limit"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			r := Record{ID: sourceMemoryErasureID(scopeA, "root"), Scope: scopeA, Kind: sourceMemoryDependentsErasureKind, Status: JobStatusDone, Revision: 1, CreatedBy: "human-1", CreatedByType: roleHuman, Target: &Ref{Collection: "sources", ID: "root", Revision: 1}, SourceMemoryErasure: &SourceMemoryErasure{DeletedRecords: map[string]int64{"sources": 1}, QueuedSourceJobs: 1}}
			switch mode {
			case "cycle":
				r.SourceMemoryErasure.DerivedSourceJobs = []string{r.ID}
			case "foreign_scope":
				other := r
				other.Scope = scopeFar
				other.ID = sourceMemoryErasureID(scopeFar, "other")
				other.Target = &Ref{Collection: "sources", ID: "other", Revision: 1}
				other.SourceMemoryErasure = &SourceMemoryErasure{}
				if e := f.repo.Insert(testCtx, colJobs, other); e != nil {
					t.Fatal(e)
				}
				r.SourceMemoryErasure.DerivedSourceJobs = []string{other.ID}
			case "legacy":
				r.SourceMemoryErasure.QueuedSourceJobs = 0
			case "overflow":
				r.SourceMemoryErasure.QueuedMemoryJobs = 1<<63 - 1
			case "limit":
				r.SourceMemoryErasure.DerivedSourceJobs = make([]string, 4097)
			}
			if e := f.repo.Insert(testCtx, colJobs, r); e != nil {
				t.Fatal(e)
			}
			status, e := f.svc.SourceDeletionStatus(testCtx, f.human, scopeA, "root")
			if mode == "legacy" || mode == "foreign_scope" {
				if e != nil || status.DependentCleanup.TrackingComplete || status.DependentCleanup.Complete {
					t.Fatal("untracked deletion completed", e)
				}
			} else if !errors.Is(e, ErrUnavailable) || status.DependentCleanup != nil {
				t.Fatal("invalid graph returned partial status", e)
			}
		})
	}
}
