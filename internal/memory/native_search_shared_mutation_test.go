package memory

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// Preserve the actual Postgres capabilities while holding a request before BEGIN.
type nativeSearchSharedMutationRepo struct {
	*Postgres
	before func()
	calls  int
}

func (r *nativeSearchSharedMutationRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	r.calls++
	if r.calls == 1 {
		r.before()
	}
	return r.Postgres.Transaction(ctx, fn)
}

// Include every physical memory_records column, all payload bytes, and their identities.
// No purge/time/fence fields are filtered or normalized.
func nativeSearchRawDatabaseSnapshot(t *testing.T, p *Postgres) []string {
	t.Helper()
	rows, e := p.q().Query(testCtx, `SELECT to_jsonb(r)::text FROM memory_records AS r ORDER BY collection,workspace_id,project_id,user_id,agent_id,id`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var raw string
		if e = rows.Scan(&raw); e != nil {
			t.Fatal(e)
		}
		out = append(out, raw)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestNativeSearchShadowRevisionMutationPreservesFullDatabase(t *testing.T) {
	p, r, in := nativeSearchPGFixture(t)
	initial := nativeSearchRawDatabaseSnapshot(t, p.admin)
	beforeJobs := pgCollection(t, p.admin, colJobs)
	beforeState := structuredText(p.rows(t, "state"))
	beforeConsumption := structuredText(p.rows(t, "consumption"))
	offReady, onReady := make(chan struct{}), make(chan struct{})
	offRelease, onRelease := make(chan struct{}), make(chan struct{})
	offClosed, onClosed := false, false
	defer func() {
		if !offClosed {
			close(offRelease)
		}
		if !onClosed {
			close(onRelease)
		}
	}()
	offRepo := &nativeSearchSharedMutationRepo{Postgres: p.workerRepo, before: func() {
		close(offReady)
		select {
		case <-offRelease:
		case <-time.After(10 * time.Second):
		}
	}}
	onRepo := &nativeSearchSharedMutationRepo{Postgres: p.workerRepo, before: func() {
		close(onReady)
		select {
		case <-onRelease:
		case <-time.After(10 * time.Second):
		}
	}}
	offService := &Service{Repo: offRepo, Auth: p.worker.Auth, NativeDynamics: p.worker.NativeDynamics}
	onService := &Service{Repo: onRepo, Auth: p.worker.Auth, NativeDynamics: p.worker.NativeDynamics, NativeSearchShadow: r}
	type response struct {
		out ContextBundle
		err error
	}
	offDone, onDone := make(chan response, 1), make(chan response, 1)
	go func() { out, e := offService.Search(testCtx, p.workerToken, in); offDone <- response{out, e} }()
	go func() { out, e := onService.Search(testCtx, p.workerToken, in); onDone <- response{out, e} }()
	for _, ready := range []chan struct{}{offReady, onReady} {
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			t.Fatal("same original cleaned candidates did not reach first actual transaction boundary")
		}
	}
	if !reflect.DeepEqual(initial, nativeSearchRawDatabaseSnapshot(t, p.admin)) {
		t.Fatal("candidate preparation changed canonical database")
	}
	// One real external change is shared by both requests: no timestamp normalization and no replayed mutation.
	nativeSearchInjectPGFault(t, p, in, "revision")
	postMutation := nativeSearchRawDatabaseSnapshot(t, p.admin)
	close(offRelease)
	offClosed = true
	var off, on response
	select {
	case off = <-offDone:
	case <-time.After(5 * time.Second):
		t.Fatal("off request did not end")
	}
	if !reflect.DeepEqual(postMutation, nativeSearchRawDatabaseSnapshot(t, p.admin)) {
		t.Fatal("off baseline wrote after failed final fence")
	}
	close(onRelease)
	onClosed = true
	select {
	case on = <-onDone:
	case <-time.After(5 * time.Second):
		t.Fatal("shadow request did not end")
	}
	if !errors.Is(off.err, ErrConflict) || !errors.Is(on.err, ErrConflict) || !reflect.DeepEqual(off.out, on.out) || off.out.ContextData != "" || len(off.out.Items) != 0 {
		t.Fatal("same external revision final fence off/shadow mismatch", off.err, on.err)
	}
	if !reflect.DeepEqual(postMutation, nativeSearchRawDatabaseSnapshot(t, p.admin)) {
		t.Fatal("shadow added any canonical/job payload/fence side effect after mutation")
	}
	afterJobs, purge := nativeSearchAssertExternalMutationJobs(t, p, in, beforeJobs)
	if beforeState != structuredText(p.rows(t, "state")) || beforeConsumption != structuredText(p.rows(t, "consumption")) {
		t.Fatal("native state/consumption changed")
	}
	t.Logf("same original candidates/shared actual external revision mutation1; entire memory_records %d->%d postmutation/off/shadow exact including all payload times/fences; jobs%d->%d exact purge %s rev%d; usage0; errors revision_conflict", len(initial), len(postMutation), len(beforeJobs), len(afterJobs), purge.ID, purge.NativeDynamics.Purge.ThroughRevision)
}

func nativeSearchAssertExternalMutationJobs(t *testing.T, p *nativePGFixture, in SearchInput, beforeJobs []Record) ([]Record, Record) {
	t.Helper()
	afterJobs := pgCollection(t, p.admin, colJobs)
	if len(afterJobs) != len(beforeJobs)+1 {
		t.Fatal("whole job count does not equal exactly one external purge", len(beforeJobs), len(afterJobs))
	}
	prior := map[string]Record{}
	for _, row := range beforeJobs {
		prior[row.ID] = row
	}
	extra := []Record{}
	for _, row := range afterJobs {
		if old, ok := prior[row.ID]; ok {
			if !reflect.DeepEqual(old, row) {
				t.Fatal("existing job mutated", row.ID)
			}
		} else {
			extra = append(extra, row)
		}
	}
	if len(extra) != 1 || extra[0].Kind != nativeDynamicsPrefix+"purge" || extra[0].ID != nativePurgeID(in.Scope, p.in.Target) || extra[0].NativeDynamics == nil || extra[0].NativeDynamics.Purge == nil || extra[0].NativeDynamics.Purge.DependencyKey != nativeDependencyKey(in.Scope, p.in.Target) || extra[0].NativeDynamics.Purge.ThroughRevision != p.in.Target.Revision || extra[0].NativeDynamics.Reason != "dependency_revision_changed" {
		t.Fatal("external mutation purge does not match original ref/rev", extra)
	}
	return afterJobs, extra[0]
}
