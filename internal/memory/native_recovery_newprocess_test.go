package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func sys04DatabaseCommand(t *testing.T, p *Postgres, program string, args ...string) *exec.Cmd {
	t.Helper()
	cfg := p.pool.Config().ConnConfig
	binDir := os.Getenv("POSTGRES_TEST_PG_BINDIR")
	if binDir == "" {
		t.Fatal("explicit owned pg_dump/restore binary directory required; cannot skip")
	}
	if !strings.HasPrefix(cfg.Database, "arkism_test_") || net.ParseIP(cfg.Host) == nil || !net.ParseIP(cfg.Host).IsLoopback() {
		t.Fatal("requires owned synthetic child database")
	}
	command := exec.Command(filepath.Join(binDir, program), append([]string{"-h", cfg.Host, "-p", strconv.Itoa(int(cfg.Port)), "-U", cfg.User, "-d", cfg.Database}, args...)...)
	command.Env = append(os.Environ(), "PGPASSWORD="+cfg.Password)
	return command
}

func sys04RunDatabaseCommand(t *testing.T, command *exec.Cmd, input io.Reader, output io.Writer) {
	t.Helper()
	var diagnostics bytes.Buffer
	command.Stdin = input
	command.Stdout = output
	command.Stderr = &diagnostics
	if err := command.Run(); err != nil {
		t.Fatalf("owned database subprocess failed: %v %s", err, diagnostics.String())
	}
}

func sys04DumpAndRestore(t *testing.T, source, target *Postgres) {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "native-synthetic.archive")
	sys04RunDatabaseCommand(t, sys04DatabaseCommand(t, source, "pg_dump", "-Fc", "-f", archive), nil, io.Discard)
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	sys04RunDatabaseCommand(t, sys04DatabaseCommand(t, target, "pg_restore", "--exit-on-error"), file, io.Discard)
	if err = target.Init(testCtx); err != nil {
		t.Fatal("existing restored schema initialization", err)
	}
}

type sys04RecoveryHelperInput struct {
	Mode               string
	SourceDSN          string
	RestoredDSN        string
	ExpectedPort       uint16
	AppRole            string
	WorkerToken        string
	Manifest           string
	SourceGeneration   string
	RestoredGeneration string
	Snapshot           NativeRecoverySnapshot
	Query              NativeDynamicsQuery
	Capture            Ref
}

func sys04ReadHelperInput(t *testing.T, path string) sys04RecoveryHelperInput {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input sys04RecoveryHelperInput
	if json.Unmarshal(raw, &input) != nil {
		t.Fatal("invalid owned helper input")
	}
	return input
}

func sys04OpenHelperDatabase(t *testing.T, dsn string, port uint16, role string) *Postgres {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("owned helper connection config")
	}
	if cfg.ConnConfig.Port != port || !strings.HasPrefix(cfg.ConnConfig.Database, "arkism_test_") || net.ParseIP(cfg.ConnConfig.Host) == nil || !net.ParseIP(cfg.ConnConfig.Host).IsLoopback() {
		t.Fatal("helper refused a non-owned database")
	}
	cfg.MaxConns = 1
	if role != "" {
		if !strings.HasPrefix(role, "arkism_rls_test_") {
			t.Fatal("helper refused an unexpected SQL role")
		}
		cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
			return err
		}
	}
	pool, err := pgxpool.NewWithConfig(testCtx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := &Postgres{pool: pool, ownsPool: true}
	t.Cleanup(p.Close)
	return p
}

func sys04HelperWorker(t *testing.T, input sys04RecoveryHelperInput) *Service {
	t.Helper()
	app := sys04OpenHelperDatabase(t, input.RestoredDSN, input.ExpectedPort, input.AppRole)
	repo, err := app.WithToken(input.WorkerToken)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(app)
	if err != nil {
		t.Fatal(err)
	}
	return &Service{Repo: repo, Auth: auth, NativeDynamics: &NativeDynamicsRuntime{Enabled: true}}
}

// This helper is executed only by the current Go test binary, in a fresh
// process. It never invokes memoryd/arkism or their standalone build helpers.
func TestSYS04OwnedRecoveryProcessHelper(t *testing.T) {
	path := os.Getenv("SYS04_OWNED_RECOVERY_HELPER_INPUT")
	if path == "" {
		return
	}
	input := sys04ReadHelperInput(t, path)
	target := sys04OpenHelperDatabase(t, input.RestoredDSN, input.ExpectedPort, "")
	switch input.Mode {
	case "readback":
		worker := sys04HelperWorker(t, input)
		if _, err := worker.ReadNativeDynamicsSnapshot(testCtx, input.WorkerToken, input.Query); !errors.Is(err, ErrUnavailable) {
			t.Fatal("fresh process bypassed pending restore gate", err)
		}
		if err := target.VerifyNativeRecoveryReadback(testCtx, input.Manifest, input.RestoredGeneration, input.Snapshot); err != nil {
			t.Fatal("fresh process actual six-field readback", err)
		}
	case "reconcile":
		source := sys04OpenHelperDatabase(t, input.SourceDSN, input.ExpectedPort, "")
		if err := target.ReconcileNativeRecovery(testCtx, source, input.Manifest, input.SourceGeneration, input.RestoredGeneration); err != nil {
			t.Fatal("fresh process authoritative restore reconciliation", err)
		}
		var genericPending bool
		if err := target.q().QueryRow(testCtx, `SELECT EXISTS(SELECT 1 FROM memory_storage_metadata WHERE key='restore_review_pending')`).Scan(&genericPending); err != nil || !genericPending {
			t.Fatal("native review cleared general publication approval", err)
		}
	case "active":
		sys04HelperAssertActive(t, input)
	case "withheld":
		sys04HelperAssertWithheld(t, input)
	default:
		t.Fatal("unknown owned helper mode")
	}
}

func sys04RunHelper(t *testing.T, input sys04RecoveryHelperInput) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "helper.json")
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "tool", "test2json", "-t", "-p", "sys04-owned-recovery-helper", executable, "-test.run", "^TestSYS04OwnedRecoveryProcessHelper$", "-test.v=test2json", "-test.timeout=45s")
	command.Env = append(os.Environ(), "SYS04_OWNED_RECOVERY_HELPER_INPUT="+path)
	out, err := command.CombinedOutput()
	if evidence := os.Getenv("SYS04_TEST_EVIDENCE_DIR"); evidence != "" {
		name := ContentHash(t.Name()+"\x00"+input.Mode+"\x00"+input.Capture.ID) + ".child.jsonl"
		if writeErr := os.WriteFile(filepath.Join(evidence, name), out, 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err != nil {
		t.Fatalf("fresh owned helper process failed: %v %s", err, out)
	}
}

func sys04DatabaseDSN(p *Postgres) string {
	cfg := p.pool.Config().ConnConfig
	endpoint := url.URL{Scheme: "postgresql", User: url.UserPassword(cfg.User, cfg.Password), Host: net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))), Path: "/" + cfg.Database}
	values := url.Values{"sslmode": []string{"disable"}}
	if path := cfg.RuntimeParams["search_path"]; path != "" {
		values.Set("search_path", path)
	}
	endpoint.RawQuery = values.Encode()
	return endpoint.String()
}

func sys04ProcessInput(t *testing.T, p *nativePGFixture, target *Postgres, snapshot NativeRecoverySnapshot, sourceGate, targetGate RecoveryWriteGateStatus, manifest string) sys04RecoveryHelperInput {
	t.Helper()
	var role string
	if err := p.app.q().QueryRow(testCtx, `SELECT current_user`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	return sys04RecoveryHelperInput{SourceDSN: sys04DatabaseDSN(p.admin), RestoredDSN: sys04DatabaseDSN(target), ExpectedPort: p.admin.pool.Config().ConnConfig.Port, AppRole: role, WorkerToken: p.workerToken, Manifest: manifest, SourceGeneration: sourceGate.Gate.Generation, RestoredGeneration: targetGate.Gate.Generation, Snapshot: snapshot, Query: NativeDynamicsQuery{p.in.Scope, p.in.Target, p.in.Dimension, "roundtrip", time.Now().UnixNano()}}
}

func TestDynamicsRecoveryActualDumpNewProcessAndPublicationPG(t *testing.T) {
	source := nativePGSetup(t)
	source.activate(t, "roundtrip")
	_, capture := source.accept(t)
	source.process(t, capture)
	sourceGate := sys04Pause(t, source.admin)
	snapshot := sys04Snapshot(t, source.admin, false, sourceGate.Gate.Generation)
	restored := isolatedPostgresSchema(t, false)
	sys04DumpAndRestore(t, source.admin, restored)
	restoredGate := gateStatus(t, restored)
	if restoredGate.Gate.State != "paused" {
		t.Fatal("actual dump lost paused generation")
	}
	manifest := ContentHash("actual native dump/restore/newprocess")
	if err := restored.RequireRestoreReview(testCtx, manifest); err != nil {
		t.Fatal(err)
	}
	input := sys04ProcessInput(t, source, restored, *snapshot.NativeDynamics, sourceGate, restoredGate, manifest)
	input.Capture = capture
	input.Mode = "readback"
	sys04RunHelper(t, input)
	input.Mode = "reconcile"
	sys04RunHelper(t, input)
	if err := restored.CheckRestorePublication(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("general review disappeared", err)
	}
	if _, err := restored.q().Exec(testCtx, `DELETE FROM memory_storage_metadata WHERE key='restore_review_pending'`); err != nil {
		t.Fatal(err)
	}
	sys04Resume(t, restored)
	input.Mode = "active"
	input.Query.CutoffNS = time.Now().UnixNano()
	sys04RunHelper(t, input)
}

func sys04DeleteActualMemory(t *testing.T, p *Postgres, scope Scope, target Ref) {
	t.Helper()
	fixture := newFixture(t)
	fixture.svc.Repo = p
	if err := fixture.svc.Delete(testCtx, fixture.human, colMemories, target.ID, ChangeInput{Scope: scope, ExpectedRevision: target.Revision}); err != nil {
		t.Fatal("actual service memory deletion", err)
	}
}
func sys04DrainNativePurges(t *testing.T, p *nativePGFixture) {
	t.Helper()
	for _, purge := range p.rows(t, "purge") {
		done := false
		for step := 0; step < 200 && !done; step++ {
			var err error
			done, err = p.worker.ProcessNativeDynamicsPurge(testCtx, p.workerToken, p.in.Scope, Ref{Collection: colJobs, ID: purge.ID, Revision: purge.Revision}, 1)
			if err != nil {
				t.Fatal("bounded native cleanup", err)
			}
		}
		if !done {
			t.Fatal("bounded cleanup did not finish")
		}
	}
}
func TestDynamicsRecoveryOldBackupDeletionReplayAndNewProcessPG(t *testing.T) {
	source := nativePGSetup(t)
	source.activate(t, "roundtrip")
	_, capture := source.accept(t)
	source.process(t, capture)
	source.in.EventID = "unapplied-old-capture"
	_, unapplied := source.accept(t)
	sourceGate := sys04Pause(t, source.admin)
	snapshot := sys04Snapshot(t, source.admin, false, sourceGate.Gate.Generation)
	restored := isolatedPostgresSchema(t, false)
	sys04DumpAndRestore(t, source.admin, restored)
	targetGate := gateStatus(t, restored)
	manifest := ContentHash("old backup followed by authoritative deletion")
	if err := restored.RequireRestoreReview(testCtx, manifest); err != nil {
		t.Fatal(err)
	}
	input := sys04ProcessInput(t, source, restored, *snapshot.NativeDynamics, sourceGate, targetGate, manifest)
	input.Capture = unapplied
	input.Mode = "readback"
	sys04RunHelper(t, input)
	sys04Resume(t, source.admin)
	sys04DeleteActualMemory(t, source.admin, source.in.Scope, source.in.Target)
	sys04ExpectWithheld(t, source, "roundtrip", unapplied)
	sys04DrainNativePurges(t, source)
	sourceGate = sys04Pause(t, source.admin)
	// A valid old readback cannot override the CURRENT original ledger.
	err := restored.ReconcileNativeRecovery(testCtx, source.admin, manifest, sourceGate.Gate.Generation, targetGate.Gate.Generation)
	if err == nil {
		t.Fatal("old checkpoint/readback resurrected a subsequently deleted target")
	}
	if err = restored.checkNativeRestorePublication(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("failed reconcile removed native hold", err)
	}
	// Explicit recovery maintenance replays the existing canonical deletion API.
	sys04Resume(t, restored)
	sys04DeleteActualMemory(t, restored, source.in.Scope, source.in.Target)
	targetApp := sys04OpenHelperDatabase(t, sys04DatabaseDSN(restored), input.ExpectedPort, input.AppRole)
	targetRepo, err := targetApp.WithToken(source.workerToken)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := NewPostgresAuthorizer(targetApp)
	if err != nil {
		t.Fatal(err)
	}
	targetFixture := &nativePGFixture{admin: restored, app: targetApp, workerRepo: targetRepo, worker: &Service{Repo: targetRepo, Auth: auth, NativeDynamics: &NativeDynamicsRuntime{Enabled: true}}, in: source.in, workerToken: source.workerToken}
	// Maintenance cleanup is allowed while publication is held. Normal calls remain denied.
	if _, err = targetFixture.worker.ReadNativeDynamicsSnapshot(testCtx, source.workerToken, input.Query); !errors.Is(err, ErrUnavailable) {
		t.Fatal("restore hold bypass", err)
	}
	sys04DrainNativePurges(t, targetFixture)
	targetGate = sys04Pause(t, restored)
	input.SourceGeneration = sourceGate.Gate.Generation
	input.RestoredGeneration = targetGate.Gate.Generation
	input.Mode = "reconcile"
	sys04RunHelper(t, input)
	if _, err = restored.q().Exec(testCtx, `DELETE FROM memory_storage_metadata WHERE key='restore_review_pending'`); err != nil {
		t.Fatal(err)
	}
	sys04Resume(t, restored)
	input.Mode = "withheld"
	input.Query.CutoffNS = time.Now().UnixNano()
	sys04RunHelper(t, input)
	input.Capture = capture
	sys04RunHelper(t, input)
}

func sys04HelperAssertActive(t *testing.T, input sys04RecoveryHelperInput) {
	t.Helper()
	worker := sys04HelperWorker(t, input)
	if err := worker.CheckRestorePublication(testCtx); err != nil {
		t.Fatal("reviewed fresh process publication", err)
	}
	out, err := worker.ReadNativeDynamicsSnapshot(testCtx, input.WorkerToken, input.Query)
	if err != nil || out.Status != "known" || out.Value == nil || *out.Value != 0.55 {
		t.Fatal("fresh process did not read exact restored state", err)
	}
}
func sys04HelperAssertWithheld(t *testing.T, input sys04RecoveryHelperInput) {
	t.Helper()
	worker := sys04HelperWorker(t, input)
	out, err := worker.ReadNativeDynamicsSnapshot(testCtx, input.WorkerToken, input.Query)
	if err == nil && out.Value != nil {
		t.Fatal("fresh process revived invalidated state")
	}
	receipt, err := worker.ProcessNativeDynamicsCapture(testCtx, input.WorkerToken, input.Query.Scope, input.Capture)
	if err == nil || receipt.AppliedNow || receipt.LearningConsumed {
		t.Fatal("fresh process replay revived deleted capture", err)
	}

}
