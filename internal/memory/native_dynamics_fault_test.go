package memory

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

type nativeFaultTx struct {
	Tx
	failKind string
	failure  error
}

func (x nativeFaultTx) Insert(ctx context.Context, col string, r Record) error {
	if col == colJobs && r.Kind == nativeDynamicsPrefix+x.failKind {
		return x.failure
	}
	return x.Tx.Insert(ctx, col, r)
}
func (x nativeFaultTx) Replace(ctx context.Context, col string, r Record, rev int64) error {
	if col == colJobs && r.Kind == nativeDynamicsPrefix+x.failKind {
		return x.failure
	}
	return x.Tx.Replace(ctx, col, r, rev)
}
func (x nativeFaultTx) NativeDynamicsRows(ctx context.Context, s Scope, role, dep, after string, n int) ([]Record, error) {
	return x.Tx.(nativeDynamicsTxQueries).NativeDynamicsRows(ctx, s, role, dep, after, n)
}
func (x nativeFaultTx) NativeDynamicsPendingCaptures(ctx context.Context, s Scope, n int) ([]Record, error) {
	return x.Tx.(nativeDynamicsTxQueries).NativeDynamicsPendingCaptures(ctx, s, n)
}

func (x nativeFaultTx) NativeDynamicsPendingPurges(ctx context.Context, s Scope, n int) ([]Record, error) {
	return x.Tx.(nativeDynamicsTxQueries).NativeDynamicsPendingPurges(ctx, s, n)
}

type nativeFaultRepo struct {
	Repository
	kind    string
	failure error
}

func (x nativeFaultRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return x.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error { return fn(ctx, nativeFaultTx{tx, x.kind, x.failure}) })
}

func TestNativeDynamicsAtomicFaultLedger(t *testing.T) {
	for _, kind := range []string{"event", "seen", "consumption", "state", "checkpoint", "capture"} {
		t.Run(kind, func(t *testing.T) {
			checkNativeDynamicsAtomicFaultKind(t, kind)
		})
	}
}
func TestNativeDynamicsCaptureFailureRollsBackSource(t *testing.T) {
	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	failure := errors.New("isolated capture insert failure")
	f.svc.Repo = nativeFaultRepo{f.repo, "capture", failure}
	ref, e := f.svc.RecordMemoryOutcome(testCtx, "checker", in)
	if !errors.Is(e, failure) || ref != (Ref{}) {
		t.Fatal("failed capture acknowledged source", e)
	}
	for _, r := range f.repo.rows("evaluations") {
		if r.Kind == "memory_outcome" {
			t.Fatal("source committed without capture")
		}
	}
	f.svc.Repo = f.repo
	nativeAccept(t, f, in)
	if len(nativeKindRows(f, "capture")) != 1 {
		t.Fatal("recovery missing")
	}
}
func TestNativeDynamicsRecoverySkipsCompletedPrefix(t *testing.T) {
	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	in.Passed = true
	_, captureRef := nativeAccept(t, f, in)
	for i := 0; i < 600; i++ {
		r := nativeJob(in.Scope, "capture", fmt.Sprintf("a_done_%04d", i), NativeDynamicsPayload{Protocol: nativeDynamicsProtocol})
		r.Status = JobStatusDone
		if e := f.repo.Transaction(testCtx, func(ctx context.Context, tx Tx) error { return nativeInsertTx(ctx, tx, r) }); e != nil {
			t.Fatal(e)
		}
	}
	results, e := f.svc.DrainNativeDynamicsCaptures(testCtx, "native", in.Scope, 1)
	if e != nil || len(results) != 1 || results[0].Capture.ID != captureRef.ID || !results[0].AppliedNow {
		t.Fatal("completed queue prefix starved pending", e)
	}
}

func TestNativeDynamicsHiddenProvenanceMutationWithholds(t *testing.T) {
	for _, field := range []string{"request_hash", "capture_stamp"} {
		t.Run(field, func(t *testing.T) {
			f, in := nativeFixture(t)
			nativeActivate(t, f, in, "v1")
			in.Passed = true
			eval, captureRef := nativeAccept(t, f, in)
			if _, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef); e != nil {
				t.Fatal(e)
			}
			r, _ := f.repo.Get(testCtx, "evaluations", in.Scope, eval.ID)
			if field == "request_hash" {
				r.RequestHash = "forged_hidden_hash"
			} else {
				s := *r.NativeOutcomeCaptureStamp
				s.Mode = "off"
				r.NativeOutcomeCaptureStamp = &s
			}
			f.repo.mu.Lock()
			f.repo.store["evaluations"][recKey(in.Scope, eval.ID)] = r
			f.repo.mu.Unlock()
			if v, e := f.svc.ReadNativeDynamicsSnapshot(testCtx, "native", NativeDynamicsQuery{in.Scope, in.Target, in.Dimension, "v1", time.Now().UnixNano()}); e == nil && v.Value != nil {
				t.Fatal("hidden accepted-source provenance mutation published known value")
			}
		})
	}
}

type nativeTerminateBeforeCommitRepo struct {
	Repository
	admin   *Postgres
	witness func(int, string)
}

func (x nativeTerminateBeforeCommitRepo) Transaction(ctx context.Context, fn func(context.Context, Tx) error) error {
	return x.Repository.Transaction(ctx, func(ctx context.Context, tx Tx) error {
		if e := fn(ctx, tx); e != nil {
			return e
		}
		return x.terminateOwnedBackend(ctx, tx.(*Postgres))
	})
}
func (x nativeTerminateBeforeCommitRepo) terminateOwnedBackend(ctx context.Context, child *Postgres) error {
	if x.admin == nil || x.admin.pool == nil || x.witness == nil {
		return errors.New("own termination fixture requires explicit admin and witness")
	}
	cfg := x.admin.pool.Config().ConnConfig
	ip := net.ParseIP(cfg.Host)
	if ip == nil || !ip.IsLoopback() || !strings.HasPrefix(cfg.Database, "arkism_test_") {
		return errors.New("own termination fixture requires an isolated child database")
	}
	var pid int
	var database string
	if e := child.q().QueryRow(ctx, "SELECT pg_backend_pid(),current_database()").Scan(&pid, &database); e != nil {
		return e
	}
	if pid <= 0 || database != cfg.Database {
		return errors.New("termination target is not the current owned transaction")
	}
	var killed bool
	if e := x.admin.q().QueryRow(ctx, "SELECT pg_terminate_backend($1) WHERE EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND datname=$2 AND backend_type='client backend')", pid, database).Scan(&killed); e != nil {
		return e
	}
	if !killed {
		return errors.New("own transaction backend was not terminated")
	}
	_, e := child.q().Exec(ctx, "SELECT 1")
	if e == nil || !child.tx.Conn().IsClosed() {
		return errors.New("terminated transaction connection was not lost")
	}
	x.witness(pid, database)
	return e
}
func TestDynamicsStoreNativeBackendTerminationPG(t *testing.T) {
	for _, phase := range []string{"source_capture", "state_apply"} {
		t.Run(phase, func(t *testing.T) {
			checkNativeActualBackendTerminationPhase(t, phase)
		})
	}
}

func TestDynamicsStoreNativeBackendDenialControlPG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "v1")
	_, captureRef := p.accept(t)
	// A separate restricted pool avoids holding/reentering the worker's one-connection pool.
	// It receives no signal/backend privileges and must never produce a termination witness.
	deniedAdmin := restrictedPostgres(t, p.admin)
	witnessed := false
	p.worker.Repo = nativeTerminateBeforeCommitRepo{Repository: p.workerRepo, admin: deniedAdmin, witness: func(int, string) { witnessed = true }}
	result, e := p.worker.ProcessNativeDynamicsCapture(testCtx, p.workerToken, p.in.Scope, captureRef)
	if e == nil || !reflect.DeepEqual(result, NativeDynamicsReceipt{}) {
		t.Fatal("denied termination exposed success", e)
	}
	if witnessed {
		t.Fatal("a statement error became a termination witness")
	}
	p.worker.Repo = p.workerRepo
	for _, role := range []string{"event", "seen", "consumption", "checkpoint"} {
		if len(p.rows(t, role)) != 0 {
			t.Fatal("denied termination partially committed", role)
		}
	}
	p.process(t, captureRef)
	t.Log("native_denied_termination_control witnessed=false; rollback and original-capture retry verified")
}

func checkNativeDynamicsAtomicFaultKind(t *testing.T, kind string) {
	t.Helper()

	f, in := nativeFixture(t)
	nativeActivate(t, f, in, "v1")
	in.Passed = true
	_, captureRef := nativeAccept(t, f, in)
	before := nativeView(t, f, in, "v1")
	failure := errors.New("isolated native write fault")
	f.svc.Repo = nativeFaultRepo{f.repo, kind, failure}
	r, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef)
	if !errors.Is(e, failure) || !reflect.DeepEqual(r, NativeDynamicsReceipt{}) {
		t.Fatal("write failure leaked result", e)
	}
	f.svc.Repo = f.repo
	after := nativeView(t, f, in, "v1")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("partial state committed")
	}
	for _, role := range []string{"event", "seen", "consumption", "checkpoint"} {
		if len(nativeKindRows(f, role)) != 0 {
			t.Fatal("partial durable write", role)
		}
	}
	row, _ := f.repo.Get(testCtx, colJobs, in.Scope, captureRef.ID)
	if row.Status != JobStatusPending || row.NativeDynamics.Receipt != nil {
		t.Fatal("capture advanced without state")
	}
	if _, e := f.svc.ProcessNativeDynamicsCapture(testCtx, "native", in.Scope, captureRef); e != nil {
		t.Fatal("retry failed", e)
	}
	if len(nativeKindRows(f, "consumption")) != 1 {
		t.Fatal("retry not exactly once")
	}
}

func checkNativeActualBackendTerminationPhase(t *testing.T, phase string) {
	t.Helper()

	p := nativePGSetup(t)
	terminated := false
	witness := func(pid int, database string) {
		terminated = true
		t.Logf("native_actual_backend_termination phase=%s pid=%d child_database=%s bool=true closed=true", phase, pid, database)
	}

	p.activate(t, "v1")
	var captureRef Ref
	if phase == "source_capture" {
		p.checker.Repo = nativeTerminateBeforeCommitRepo{Repository: p.checkerRepo, admin: p.admin, witness: witness}
		ref, e := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, p.in)
		if e == nil || ref != (Ref{}) {
			t.Fatal("terminated source transaction returned success")
		}
		p.checker.Repo = p.checkerRepo
		if len(p.rows(t, "capture")) != 0 {
			t.Fatal("terminated source committed capture")
		}
		_, captureRef = p.accept(t)
	} else {
		_, captureRef = p.accept(t)
		p.worker.Repo = nativeTerminateBeforeCommitRepo{Repository: p.workerRepo, admin: p.admin, witness: witness}
		receipt, e := p.worker.ProcessNativeDynamicsCapture(testCtx, p.workerToken, p.in.Scope, captureRef)
		if e == nil || !reflect.DeepEqual(receipt, NativeDynamicsReceipt{}) {
			t.Fatal("terminated apply returned success")
		}
		p.worker.Repo = p.workerRepo
		if len(p.rows(t, "event")) != 0 || len(p.rows(t, "seen")) != 0 || len(p.rows(t, "consumption")) != 0 {
			t.Fatal("terminated connection left partial ledger")
		}
	}
	if !terminated {
		t.Fatal("backend termination was not witnessed; a statement error is insufficient")
	}

	p.process(t, captureRef)
	if len(p.rows(t, "event")) != 1 || len(p.rows(t, "consumption")) != 1 {
		t.Fatal("terminated connection retry not exactly once")
	}
}
