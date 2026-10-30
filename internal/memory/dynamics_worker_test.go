package memory

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func dynamicsFixtureConfig() DynamicsLoadConfig {
	return DynamicsLoadConfig{Protocol: dynamicsLoadProtocol, Profile: "isolated-fixture-v1", Mode: "async", LoadContractSHA256: dynamicsLoadContractSHA256, LoadConfigSHA256: strings.Repeat("b", 64), Scopes: []Scope{scopeA}, Limits: DynamicsLoadLimits{
		QueueCountLimit: 4, QueueActiveBytesLimit: 65536, MaxWaitNS: 5000000000, BatchLimit: 2, ConcurrencyLimit: 1, RetryLimit: 2, RetryBackoffNS: 1000000, DeadlineNS: 5000000000, WorkerSubstepTimeoutNS: 1000000000, LeaseNS: 4000000000, FinishTimeoutNS: 2000000000, PollIntervalNS: 5000000, PublishIntervalNS: 5000000, AllowedFreshnessNS: 1000000000, SnapshotMaxItems: 16, SnapshotMaxBytes: 65536, MaxConfiguredScopes: 2, TelemetryMaxBytes: 262144, TerminalLedgerRetentionBudget: 1048576}}
}
func dynamicsFixtureRuntime(t *testing.T, c DynamicsLoadConfig) *DynamicsLoadRuntime {
	t.Helper()
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewDynamicsLoadRuntime(raw)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func dynamicsPG(t *testing.T) *nativePGFixture {
	t.Helper()
	p := nativePGSetup(t)
	p.activate(t, "load_v1")
	r := dynamicsFixtureRuntime(t, dynamicsFixtureConfig())
	p.checker.DynamicsLoad = r
	p.worker.DynamicsLoad = r
	return p
}
func TestDynamicsWorkerConfig(t *testing.T) {
	if err := (&Service{DynamicsLoad: &DynamicsLoadRuntime{}}).WorkDynamicsOnce(testCtx, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero runtime not rejected", err)
	}
	raw, _ := json.Marshal(dynamicsFixtureConfig())
	if _, err := NewDynamicsLoadRuntime(raw); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"null", "{}", string(raw) + "{}", strings.Replace(string(raw), `"profile":`, `"profile":"duplicate","profile":`, 1), strings.Replace(string(raw), `"queue_count_limit":4`, `"queue_count_limit":0`, 1), strings.Replace(string(raw), `"mode":"async"`, `"mode":"unsupported"`, 1), strings.Replace(string(raw), `"lease_ns":4000000000`, `"lease_ns":5000000000`, 1), strings.Replace(string(raw), `"profile":`, `"unknown":1,"profile":`, 1)} {
		if _, err := NewDynamicsLoadRuntime([]byte(value)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid config accepted: %s", value)
		}
	}
}
func TestDynamicsWorkerAtomicAdmissionRetryAndDrainPG(t *testing.T) {
	p := dynamicsPG(t)
	first := p.in
	ref, _ := p.accept(t)
	for i := 0; i < 3; i++ {
		p.in.EventID = "load-event-" + string(rune('a'+i))
		p.accept(t)
	}
	before := len(pgCollection(t, p.admin, "evaluations"))
	captures := len(p.rows(t, "capture"))
	p.in.EventID = "load-overflow"
	if _, err := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, p.in); !errors.Is(err, ErrDynamicsQueueFull) {
		t.Fatalf("overflow %v", err)
	}
	if len(pgCollection(t, p.admin, "evaluations")) != before || len(p.rows(t, "capture")) != captures {
		t.Fatal("full queue committed canonical source/capture")
	}
	retry, err := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, first)
	if err != nil || retry != ref {
		t.Fatalf("free retry %v %v", retry, err)
	}
	first.Passed = false
	if _, err = p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, first); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting retry %v", err)
	}
	observation, err := p.worker.ObserveDynamicsLoad(testCtx, p.workerToken, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if observation.QueueCount != 4 || observation.OldestAgeNS == nil || observation.ActiveQueueBytes <= 0 {
		t.Fatalf("observation %+v", observation)
	}
	for i := 0; i < 2; i++ {
		if err = p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
			t.Fatal("drain", err)
		}
	}
	observation, err = p.worker.ObserveDynamicsLoad(testCtx, p.workerToken, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if observation.QueueCount != 0 || observation.Head != 4 || observation.ActiveQueueBytes != 0 {
		t.Fatalf("not drained %+v", observation)
	}
	snapshot, err := p.worker.DynamicsLoad.DynamicsSnapshot(scopeA, dynamicsNowNS())
	if err != nil {
		t.Fatal("snapshot", err)
	}
	if snapshot.Head != 4 || snapshot.Tail != 4 || snapshot.AsOfNS <= 0 || snapshot.PublishedNS < snapshot.AsOfNS {
		t.Fatalf("snapshot %+v", snapshot)
	}
	if p.view(t, "load_v1").LastSeq < 1 {
		t.Fatal("native state not actually applied")
	}
}
func TestDynamicsWorkerRestartAndStaleLeasePG(t *testing.T) {
	p := dynamicsPG(t)
	_, capture := p.accept(t)
	old, err := p.worker.claimDynamics(testCtx, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.worker.ProcessNativeDynamicsCapture(testCtx, p.workerToken, scopeA, capture); !errors.Is(err, ErrDynamicsRuntimeBinding) {
		t.Fatalf("manual bypass %v", err)
	}
	if err = p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		c, e := dynamicsLockCoordinator(ctx, tx, scopeA)
		if e != nil {
			return e
		}
		c.DynamicsLoad.LeaseUntilNS = dynamicsNowNS() - 1
		return dynamicsReplace(ctx, tx, c)
	}); err != nil {
		t.Fatal(err)
	}
	restart := dynamicsFixtureRuntime(t, dynamicsFixtureConfig())
	p.worker.DynamicsLoad = restart
	current, err := p.worker.claimDynamics(testCtx, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.worker.applyDynamicsClaim(testCtx, p.workerToken, scopeA, old); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale lease applied: %v", err)
	}
	if len(p.rows(t, "event")) != 0 {
		t.Fatal("stale writer mutated native state")
	}
	if err = p.worker.applyDynamicsClaim(testCtx, p.workerToken, scopeA, current); err != nil {
		t.Fatal(err)
	}
	if len(p.rows(t, "event")) != 1 {
		t.Fatal("restart lost or duplicated event")
	}
	if _, err = restart.DynamicsSnapshot(scopeA, dynamicsNowNS()); !errors.Is(err, ErrDynamicsSnapshotUnavailable) {
		t.Fatal("restart inherited cache")
	}
}
func TestDynamicsWorkerDurableBindingAndUnsequencedPG(t *testing.T) {
	t.Run("nil_and_mismatch", func(t *testing.T) {
		p := dynamicsPG(t)
		p.accept(t)
		p.in.EventID = "after-restart"
		p.checker.DynamicsLoad = nil
		if _, err := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, p.in); !errors.Is(err, ErrDynamicsRuntimeBinding) {
			t.Fatalf("nil bypass %v", err)
		}
		cfg := dynamicsFixtureConfig()
		cfg.Profile = "other-profile"
		p.checker.DynamicsLoad = dynamicsFixtureRuntime(t, cfg)
		if _, err := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, p.in); !errors.Is(err, ErrDynamicsRuntimeBinding) {
			t.Fatalf("mismatch bypass %v", err)
		}
	})
	t.Run("unknown_order", func(t *testing.T) {
		p := nativePGSetup(t)
		p.activate(t, "legacy_pending")
		p.accept(t)
		p.checker.DynamicsLoad = dynamicsFixtureRuntime(t, dynamicsFixtureConfig())
		p.in.EventID = "after-enable"
		if _, err := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, p.in); !errors.Is(err, ErrDynamicsUnsequenced) {
			t.Fatalf("guessed old order %v", err)
		}
	})
}
func TestDynamicsWorkerSnapshotImmutabilityMonotone(t *testing.T) {
	r := dynamicsFixtureRuntime(t, dynamicsFixtureConfig())
	now := dynamicsNowNS()
	row := Record{ID: "state", Revision: 1, NativeDynamics: &NativeDynamicsPayload{State: &NativeDynamicsState{LastSeq: 1}}}
	original := &DynamicsLoadSnapshot{Scope: scopeA, ConfigSHA256: r.hash, Generation: 2, AsOfNS: now, PublishedNS: now, States: map[string]Record{"state": row}}
	if !r.installDynamicsSnapshot(original) {
		t.Fatal("initial install")
	}
	original.States["state"].NativeDynamics.State.LastSeq = 99
	stale := *original
	stale.Generation = 1
	if r.installDynamicsSnapshot(&stale) {
		t.Fatal("stale writer overwrote")
	}
	got, err := r.DynamicsSnapshot(scopeA, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.States["state"].NativeDynamics.State.LastSeq != 1 {
		t.Fatal("writer mutated published state")
	}
	got.States["state"].NativeDynamics.State.LastSeq = 7
	again, _ := r.DynamicsSnapshot(scopeA, now)
	if again.States["state"].NativeDynamics.State.LastSeq != 1 {
		t.Fatal("reader mutated published state")
	}
	if _, err = r.DynamicsSnapshot(scopeA, now+int64(2*time.Second)); !errors.Is(err, ErrDynamicsSnapshotUnavailable) {
		t.Fatal("republish time camouflaged freshness")
	}
}

func TestDynamicsWorkerSynchronousSameTransactionPG(t *testing.T) {
	p := nativePGSetup(t)
	p.activate(t, "sync_v1")
	cfg := dynamicsFixtureConfig()
	cfg.Mode = "sync"
	runtime := dynamicsFixtureRuntime(t, cfg)
	p.checker.DynamicsLoad = runtime
	p.worker.DynamicsLoad = runtime
	// The existing evaluator grant intentionally lacks job authority. Sync must
	// reject the entire new source, rather than derive authority from its config.
	if _, err := p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, p.in); !errors.Is(err, ErrDenied) {
		t.Fatalf("sync bypassed explicit job permissions: %v", err)
	}
	if len(p.rows(t, "capture")) != 0 {
		t.Fatal("unauthorized sync committed source/capture")
	}
	actions := []string{"db:read", "db:write", "read:evaluations", "write:evaluations", "read:agent_runs", "read:execution_traces", "read:sources", "read:memories", "read:jobs", "write:jobs"}
	if err := p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "native-pg-checker", Role: roleService, Token: p.checkerToken, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: actions}); err != nil {
		t.Fatal(err)
	}
	_, capture := p.accept(t)
	row, err := p.workerRepo.Get(testCtx, colJobs, scopeA, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.NativeDynamics.Receipt == nil || row.NativeDynamics.Receipt.ProcessingSeq == nil {
		t.Fatal("sync response preceded native apply")
	}
	observation, err := p.worker.ObserveDynamicsLoad(testCtx, p.workerToken, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Head != 1 || observation.QueueCount != 0 {
		t.Fatal("sync queue not finalized atomically", observation)
	}
	if len(p.rows(t, "event")) != 1 {
		t.Fatal("sync event missing")
	}
	// A real native capture receipt write failure rolls back canonical outcome, capture,
	// queue reservation, event, and consumption together.
	if _, err = p.admin.q().Exec(testCtx, `CREATE FUNCTION sys05_fail_sync() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='native_dynamics_capture' THEN RAISE EXCEPTION 'own SYS05 sync fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER sys05_fail_sync BEFORE UPDATE ON memory_records FOR EACH ROW EXECUTE FUNCTION sys05_fail_sync()`); err != nil {
		t.Fatal(err)
	}
	p.in.EventID = "sync-rollback"
	before := nativeSearchRawDatabaseSnapshot(t, p.admin)
	if _, err = p.checker.RecordMemoryOutcome(testCtx, p.checkerToken, p.in); err == nil {
		t.Fatal("storage fault accepted")
	}
	after := nativeSearchRawDatabaseSnapshot(t, p.admin)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("sync storage failure partially committed")
	}
}

func TestDynamicsWorkerStorageRetryAndExpiredWaitPG(t *testing.T) {
	t.Run("storage_retry", dynamicsStorageRetryPG)
	t.Run("expired_wait", dynamicsExpiredWaitPG)
}
func dynamicsStorageRetryPG(t *testing.T) {
	t.Helper()
	p := dynamicsPG(t)
	_, capture := p.accept(t)
	if _, err := p.admin.q().Exec(testCtx, `CREATE FUNCTION sys05_fail_apply() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='native_dynamics_capture' THEN RAISE EXCEPTION 'own SYS05 apply fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER sys05_fail_apply BEFORE UPDATE ON memory_records FOR EACH ROW EXECUTE FUNCTION sys05_fail_apply()`); err != nil {
		t.Fatal(err)
	}
	if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err == nil {
		t.Fatal("storage fault hidden")
	}
	if len(p.rows(t, "event")) != 0 {
		t.Fatal("failed transaction changed native event")
	}
	row, err := p.workerRepo.Get(testCtx, colJobs, scopeA, capture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.NativeDynamics.Receipt != nil {
		t.Fatal("failed transaction left successful receipt")
	}
	if _, err = p.admin.q().Exec(testCtx, `DROP TRIGGER sys05_fail_apply ON memory_records; DROP FUNCTION sys05_fail_apply()`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err = p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
		t.Fatal("retry", err)
	}
	if len(p.rows(t, "event")) != 1 {
		t.Fatal("retry lost/duplicated native event")
	}
}
func dynamicsExpiredWaitPG(t *testing.T) {
	t.Helper()
	p := dynamicsPG(t)
	p.accept(t)
	if err := p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		row, e := tx.Get(ctx, colJobs, scopeA, dynamicsEntryID(scopeA, 1))
		if e != nil {
			return e
		}
		row.DynamicsLoad.AcceptedNS = dynamicsNowNS() - p.worker.DynamicsLoad.config.Limits.MaxWaitNS - 1
		return dynamicsReplace(ctx, tx, row)
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); !errors.Is(err, ErrDynamicsRuntimeBinding) {
		t.Fatalf("expired wait hidden: %v", err)
	}
	observation, err := p.worker.ObserveDynamicsLoad(testCtx, p.workerToken, scopeA)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Withheld || observation.QueueCount != 0 || observation.Head != 1 {
		t.Fatal("expired accepted event not accounted", observation)
	}
	if err = p.worker.PublishDynamicsSnapshot(testCtx, p.workerToken, scopeA); !errors.Is(err, ErrDynamicsSnapshotUnavailable) {
		t.Fatal("published through failed prefix", err)
	}
}

func TestDynamicsWorkerPayloadCodecAndJSONSilence(t *testing.T) {
	now := dynamicsNowNS()
	row := dynamicsRow(scopeA, "entry", "codec", DynamicsLoadPayload{AcceptedNS: now, StartedNS: now + 1, AppliedNS: now + 2, Ordinal: 9, LeaseGeneration: 7})
	raw, err := storeMarshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var restored Record
	if err = storeUnmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(row.DynamicsLoad, restored.DynamicsLoad) {
		t.Fatal("int64 nanosecond/order metadata rounded")
	}
	wire, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "sys05_load\":") || strings.Contains(string(wire), "accepted_ns") || strings.Contains(string(wire), "DynamicsLoad") {
		t.Fatal("private queue payload leaked to Record JSON")
	}
}

// Regressions adapted from the independent three counterexamples. Locked
// original independent inputs/expectations are replayed separately unchanged.
func TestDynamicsWorkerPublicationRequiresWriteAuthorityPG(t *testing.T) {
	p := dynamicsPG(t)
	dynamicsUseLogin(t, p)
	p.accept(t)
	if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
		t.Fatal(err)
	}
	token := testPrincipalToken("independent-snapshot-reader-" + ID())
	if err := p.admin.ProvisionPrincipal(testCtx, PrincipalGrant{PrincipalID: "independent-snapshot-reader", Role: roleService, Token: token, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"db:read", "db:write", "read:jobs"}}); err != nil {
		t.Fatal(err)
	}
	repo, err := p.app.WithToken(token)
	if err != nil {
		t.Fatal(err)
	}
	reader := &Service{Repo: repo, Auth: p.worker.Auth, NativeDynamics: p.worker.NativeDynamics, DynamicsLoad: p.worker.DynamicsLoad}
	before, err := p.admin.Get(testCtx, colJobs, scopeA, dynamicsCoordinatorID(scopeA))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	err = reader.PublishDynamicsSnapshot(testCtx, token, scopeA)
	after, getErr := p.admin.Get(testCtx, colJobs, scopeA, dynamicsCoordinatorID(scopeA))
	if getErr != nil {
		t.Fatal(getErr)
	}
	t.Logf("read:jobs only publication error=%v before_revision=%d after_revision=%d generation=%d->%d", err, before.Revision, after.Revision, before.DynamicsLoad.PublicationGeneration, after.DynamicsLoad.PublicationGeneration)
	if !errors.Is(err, ErrDenied) || after.Revision != before.Revision {
		t.Errorf("publisher without write:jobs mutated authoritative coordinator: err=%v revisions %d->%d", err, before.Revision, after.Revision)
	}
}

func TestDynamicsWorkerClosedJSONCaseAliasRegression(t *testing.T) {
	raw, _ := json.Marshal(dynamicsFixtureConfig())
	duplicate := strings.Replace(string(raw), `"mode":"async"`, `"mode":"async","Mode":"sync"`, 1)
	runtime, err := NewDynamicsLoadRuntime([]byte(duplicate))
	if runtime != nil {
		t.Logf("conflicting semantic mode alias decoded as %q", runtime.config.Mode)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("closed config accepted duplicate semantic key alias: %v", err)
	}
}

func TestDynamicsWorkerRecoveryMaximumEmptyOrdinalPG(t *testing.T) {
	p := dynamicsPG(t)
	p.accept(t)
	if err := p.worker.WorkDynamicsOnce(testCtx, p.workerToken); err != nil {
		t.Fatal(err)
	}
	// A valid empty queue can eventually reach maximum accepted ordinal. No entry
	// should be read: bounded zero count means zero iterations independent of Head.
	if err := p.admin.Transaction(testCtx, func(ctx context.Context, tx Tx) error {
		c, err := dynamicsLockCoordinator(ctx, tx, scopeA)
		if err != nil {
			return err
		}
		c.DynamicsLoad.Head = math.MaxInt64
		c.DynamicsLoad.Tail = math.MaxInt64
		return dynamicsReplace(ctx, tx, c)
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(testCtx, time.Second)
	defer cancel()
	view, err := p.worker.ReadDynamicsLoadRecovery(ctx, p.workerToken, scopeA)
	t.Logf("empty maximum ordinal recovery err=%v head=%d count=%d", err, view.Head, view.Count)
	if err != nil || view.Head != math.MaxInt64 || view.Count != 0 {
		t.Errorf("valid terminal empty queue recovery failed: %v", err)
	}
}
