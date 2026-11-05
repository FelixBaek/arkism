//go:build linux

package memory

import (
	"errors"
	"testing"
	"time"
)

// Deleting a space empties every table of it (records of every sub-scope,
// graph, traces), gives back its share of a stored file another space still
// uses, and leaves the other space alone.
func TestPostgresSpaceDeleteRemovesEveryRow(t *testing.T) {
	f, _ := wikiFixture(t, wikiPrintAnswers()...)
	p := isolatedPostgres(t)
	f.svc.Repo = p
	mem := f.activeMemory("prefer tabs over spaces", "style")
	src := wikiSource(t, f, "print", wikiPrintSource)
	wikiBuild(t, f, src)
	other := Record{ID: "keep-b", Scope: scopeB, Kind: "decision", Status: statusActive, Revision: 1, Text: "other space", CreatedAt: time.Now().UTC()}
	if err := p.Insert(testCtx, colMemories, other); err != nil {
		t.Fatal(err)
	}
	if _, err := p.q().Exec(testCtx, `INSERT INTO entities (organization_id,project_id,kind,external_id) VALUES ($1,$2,'note','a'),($1,$2,'note','b'),($1,$3,'note','c')`, scopeA.WorkspaceID, scopeA.ProjectID, scopeB.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.q().Exec(testCtx, `INSERT INTO relations (organization_id,project_id,source_kind,source_id,target_kind,target_id,relation_kind) VALUES ($1,$2,'note','a','note','b','cites')`, scopeA.WorkspaceID, scopeA.ProjectID); err != nil {
		t.Fatal(err)
	}
	store := blobStore{dir: blobTestDir(t)}
	digest := ""
	if err := store.locked(testCtx, func(b *lockedBlobs) error {
		a := attachTestArtifact(t, b, p, artifactSource(t, p, scopeA), `{"shared":true}`)
		attachTestArtifact(t, b, p, artifactSource(t, p, scopeB), `{"shared":true}`)
		digest = a.Artifact.Digest
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// A running job blocks the delete and nothing changes.
	lease := Record{ID: "busy", Scope: scopeA, Kind: "embedding", Status: JobStatusRunning, Revision: 1, CreatedAt: time.Now().UTC(), LeaseUntil: time.Now().Add(time.Minute)}
	if err := p.Insert(testCtx, colJobs, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.DeleteSpace(testCtx, f.human, scopeA, scopeA.ProjectID); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete under a running job: %v", err)
	}
	if _, err := p.Get(testCtx, colMemories, scopeA, mem.ID); err != nil {
		t.Fatalf("a refused delete removed a memory: %v", err)
	}
	if _, err := p.q().Exec(testCtx, `UPDATE memory_records SET lease_until=NULL WHERE collection=$1 AND id=$2`, colJobs, lease.ID); err != nil {
		t.Fatal(err)
	}

	report, err := f.svc.DeleteSpace(testCtx, f.human, scopeA, scopeA.ProjectID)
	if err != nil || report.Attachments < 1 || report.Deleted[colMemories] < 1 || report.Deleted[colWikiPages] < 1 || report.Deleted["entities"] < 2 || report.Deleted["relations"] < 1 {
		t.Fatalf("delete %+v %v", report, err)
	}
	var left int
	if err = p.q().QueryRow(testCtx, `SELECT count(*) FROM memory_records WHERE workspace_id=$1 AND project_id=$2 AND collection<>'audit_events'`, scopeA.WorkspaceID, scopeA.ProjectID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("rows left in the deleted space: %d %v", left, err)
	}
	if err = p.q().QueryRow(testCtx, `SELECT (SELECT count(*) FROM entities WHERE project_id=$1)+(SELECT count(*) FROM relations WHERE project_id=$1)`, scopeA.ProjectID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("graph left in the deleted space: %d %v", left, err)
	}
	if n, err := p.BlobReferences(testCtx, digest); err != nil || n != 1 {
		t.Fatalf("stored file references after delete: %d %v", n, err)
	}
	if _, err = p.Get(testCtx, colMemories, scopeB, other.ID); err != nil {
		t.Fatalf("the other space lost a memory: %v", err)
	}
	if err = p.q().QueryRow(testCtx, `SELECT count(*) FROM entities WHERE project_id=$1 AND external_id='c'`, scopeB.ProjectID).Scan(&left); err != nil || left != 1 {
		t.Fatalf("the other space lost its graph: %d %v", left, err)
	}
}

// Clearing memories by kind removes that kind, its causal links and its
// queued embedding jobs, and keeps the rest of the space.
func TestPostgresSpaceClearMemories(t *testing.T) {
	f := newFixture(t)
	p := isolatedPostgres(t)
	f.svc.Repo = p
	put := func(col string, r Record) {
		r.Scope, r.Revision, r.CreatedAt = scopeA, 1, time.Now().UTC()
		if err := p.Insert(testCtx, col, r); err != nil {
			t.Fatal(err)
		}
	}
	put(colMemories, Record{ID: "lesson-1", Kind: "lesson", Status: statusActive, Text: "retry once"})
	put(colMemories, Record{ID: "decision-1", Kind: "decision", Status: statusActive, Text: "use tabs"})
	put(colObservations, Record{ID: "obs-1", Kind: "observation", Status: statusActive, Text: "likes tabs"})
	put(colMentalModels, Record{ID: "model-1", Kind: "mental_model", Status: statusActive, Text: "style"})
	put(colJobs, Record{ID: "embed-1", Kind: "embedding", Status: JobStatusPending, Target: &Ref{Collection: colMemories, ID: "lesson-1"}})
	one, err := f.svc.ClearSpaceMemories(testCtx, f.human, scopeA, scopeA.ProjectID, "lesson")
	if err != nil || one.Deleted[colMemories] != 1 || one.Deleted[colJobs] != 1 {
		t.Fatalf("clear lesson %+v %v", one, err)
	}
	if _, err = p.Get(testCtx, colMemories, scopeA, "decision-1"); err != nil {
		t.Fatalf("clear by kind removed another kind: %v", err)
	}
	all, err := f.svc.ClearSpaceMemories(testCtx, f.human, scopeA, scopeA.ProjectID, "")
	if err != nil || all.Deleted[colMemories] != 1 || all.Deleted[colObservations] != 1 {
		t.Fatalf("clear all %+v %v", all, err)
	}
	if _, err = p.Get(testCtx, colMentalModels, scopeA, "model-1"); err != nil {
		t.Fatalf("clear removed a mental model: %v", err)
	}
}
