package memory

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/jackc/pgx/v5/pgconn"
)

func recoveryGateDSN(p *Postgres) string {
	c := p.pool.Config().ConnConfig
	u := url.URL{Scheme: "postgres", User: url.UserPassword(c.User, c.Password), Host: net.JoinHostPort(c.Host, strconv.Itoa(int(c.Port))), Path: "/" + c.Database, RawQuery: "sslmode=disable"}
	return u.String()
}

func gateStatus(t *testing.T, p *Postgres) RecoveryWriteGateStatus {
	t.Helper()
	status, err := p.recoveryWriteGate(testCtx, "")
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func gateChange(t *testing.T, p *Postgres, action string, previous RecoveryWriteGateStatus) RecoveryWriteGateStatus {
	t.Helper()
	status, err := p.ManageRecoveryWrites(testCtx, action, previous.StorageIdentity, previous.Gate.Generation)
	if err != nil {
		t.Fatal(action, err)
	}
	return status
}

func requireGateSQLCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != code {
		t.Fatalf("want SQLSTATE %s, got %v", code, err)
	}
}

func TestPostgresRecoveryWriteGateLifecycle(t *testing.T) {
	p := isolatedPostgres(t)
	snapshotReceipt(t, p, scopeA, "gate-source")
	app := restrictedPostgres(t, p)
	initial := gateStatus(t, p)
	if initial.Gate.State != "open" {
		t.Fatal("initially paused")
	}
	for _, action := range []string{"status", "pause", "resume"} {
		generation := initial.Gate.Generation
		if action == "status" {
			generation = ""
		}
		if _, err := app.ManageRecoveryWrites(testCtx, action, initial.StorageIdentity, generation); !errors.Is(err, ErrDenied) {
			t.Fatal("restricted role managed gate", action, err)
		}
	}
	if _, err := p.ManageRecoveryWrites(testCtx, "pause", strings.Repeat("f", 64), initial.Gate.Generation); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong identity", err)
	}
	if _, err := p.ManageRecoveryWrites(testCtx, "pause", initial.StorageIdentity, strings.Repeat("f", 64)); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong generation", err)
	}
	if raw, err := p.RecoverySnapshotAtWriteGate(testCtx, false, initial.Gate.Generation); !errors.Is(err, ErrConflict) || raw != nil {
		t.Fatal("open gate exported as paused", err)
	}
	paused := gateChange(t, p, "pause", initial)
	if paused.Gate.State != "paused" || paused.Gate.Generation == initial.Gate.Generation {
		t.Fatal("transition lost generation")
	}
	if gateChange(t, p, "pause", paused) != paused {
		t.Fatal("same transition not idempotent")
	}
	if _, err := p.ManageRecoveryWrites(testCtx, "resume", paused.StorageIdentity, initial.Gate.Generation); !errors.Is(err, ErrConflict) {
		t.Fatal("stale resume", err)
	}
	columns := []string{"collection", "id", "id", "organization_id", "organization_id", "id", "organization_id", "organization_id", "organization_id", "day"}
	for i, table := range recoveryWriteTables {
		name := pgx.Identifier{"arkism", table}.Sanitize()
		column := pgx.Identifier{columns[i]}.Sanitize()
		for _, sql := range []string{"INSERT INTO " + name + " SELECT * FROM " + name + " WHERE false", "UPDATE " + name + " SET " + column + "=" + column + " WHERE false", "DELETE FROM " + name + " WHERE false", "TRUNCATE " + name + " CASCADE"} {
			tx, err := p.pool.Begin(testCtx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(testCtx, sql)
			tx.Rollback(testCtx)
			requireGateSQLCode(t, err, "55000")
		}
	}
	// Shared row locks run as the trigger owner, including restricted app writes.
	if _, err := app.q().Exec(testCtx, `DELETE FROM memory_records WHERE false`); err == nil {
		t.Fatal("restricted application bypassed pause")
	}
	if !errors.Is(p.CheckRestorePublication(testCtx), ErrUnavailable) {
		t.Fatal("paused API published")
	}
	raw, err := p.RecoverySnapshotAtWriteGate(testCtx, false, paused.Gate.Generation)
	out := decodeSnapshot(t, raw, err)
	if len(out.Ledgers) != 1 || len(out.Ledgers[0].Targets) != 1 {
		t.Fatal("paused snapshot lost committed deletion")
	}
	if _, err = p.RecoverySnapshotAtWriteGate(testCtx, false, initial.Gate.Generation); !errors.Is(err, ErrConflict) {
		t.Fatal("old generation exported")
	}
	if err = p.Init(testCtx); err != nil {
		t.Fatal("idempotent init", err)
	}
	reconnected, err := NewPostgres(testCtx, recoveryGateDSN(p))
	if err != nil {
		t.Fatal(err)
	}
	defer reconnected.Close()
	if gateStatus(t, reconnected) != paused {
		t.Fatal("init/reconnect released gate")
	}
	if err = p.RequireRestoreReview(testCtx, ContentHash("gate restore")); err != nil {
		t.Fatal(err)
	}
	if raw, err = p.RecoverySnapshotAtWriteGate(testCtx, true, paused.Gate.Generation); err != nil || !strings.Contains(string(raw), `"version":4`) {
		t.Fatal("paused restore inspection", err)
	}
	opened := gateChange(t, p, "resume", paused)
	if opened.Gate.State != "open" || opened.Gate.Generation == paused.Gate.Generation {
		t.Fatal("resume generation")
	}
	if !errors.Is(p.CheckRestorePublication(testCtx), ErrUnavailable) {
		t.Fatal("resume released publication")
	}
	snapshotReceipt(t, p, scopeA, "after-resume")
	// Missing/disabled enforcement may not be reported as a usable write gate.
	if _, err = p.q().Exec(testCtx, `ALTER TABLE entities DISABLE TRIGGER recovery_write_gate`); err != nil {
		t.Fatal(err)
	}
	if _, err = p.ManageRecoveryWrites(testCtx, "status", opened.StorageIdentity, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatal("disabled trigger reported ready", err)
	}
	if err = p.SearchReady(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("disabled trigger readiness", err)
	}
}

func waitGateLock(t *testing.T, p *Postgres, pattern string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := p.q().QueryRow(testCtx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE $1`, pattern).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected database lock wait was not observed", pattern)
}

func TestPostgresRecoveryWriteGateDrainsAndRejectsOldSnapshots(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.Serializable, pgx.RepeatableRead, pgx.ReadCommitted} {
		t.Run(string(isolation), func(t *testing.T) {
			p := isolatedPostgres(t)
			initial := gateStatus(t, p)
			writer, err := p.pool.Begin(testCtx)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Rollback(testCtx)
			if _, err = writer.Exec(testCtx, `INSERT INTO entities(organization_id,project_id,kind,external_id) VALUES('drain','p','test','committed')`); err != nil {
				t.Fatal(err)
			}
			// An interrupted pause may not leave a partially changed persistent gate.
			timeout, cancel := context.WithTimeout(testCtx, 100*time.Millisecond)
			_, err = p.ManageRecoveryWrites(timeout, "pause", initial.StorageIdentity, initial.Gate.Generation)
			cancel()
			if err == nil || gateStatus(t, p) != initial {
				t.Fatal("timed-out pause changed gate", err)
			}
			old, err := p.pool.BeginTx(testCtx, pgx.TxOptions{IsoLevel: isolation})
			if err != nil {
				t.Fatal(err)
			}
			defer old.Rollback(testCtx)
			if _, err = old.Exec(testCtx, `SELECT count(*) FROM entities`); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := p.ManageRecoveryWrites(testCtx, "pause", initial.StorageIdentity, initial.Gate.Generation)
				done <- err
			}()
			waitGateLock(t, p, "%FOR UPDATE OF g%")
			select {
			case err := <-done:
				t.Fatal("pause did not drain writer", err)
			default:
			}
			// A queued pause must let a writer already holding the shared gate finish
			// its other statements, including graph-trigger writes in that transaction.
			if _, err = writer.Exec(testCtx, `UPDATE entities SET external_id=external_id WHERE organization_id='drain'`); err != nil {
				t.Fatal("queued pause interrupted existing writer", err)
			}
			if err = writer.Commit(testCtx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			if gateStatus(t, p).Gate.State != "paused" {
				t.Fatal("pause not persisted")
			}
			_, err = old.Exec(testCtx, `INSERT INTO entities(organization_id,project_id,kind,external_id) VALUES('drain','p','test','stale')`)
			code := "40001"
			if isolation == pgx.ReadCommitted {
				code = "55000"
			}
			requireGateSQLCode(t, err, code)
			old.Rollback(testCtx)
			var n int
			if err = p.q().QueryRow(testCtx, `SELECT count(*) FROM entities WHERE organization_id='drain'`).Scan(&n); err != nil || n != 1 {
				t.Fatal("drained writer missing or stale writer committed", n, err)
			}
		})
	}
}

func TestPostgresRecoveryWriteGateSnapshotHoldsResume(t *testing.T) {
	p := isolatedPostgres(t)
	paused := gateChange(t, p, "pause", gateStatus(t, p))
	blocker, err := p.pool.Begin(testCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(testCtx)
	if _, err = blocker.Exec(testCtx, `LOCK TABLE memory_records IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	exported := make(chan error, 1)
	go func() {
		_, err := p.RecoverySnapshotAtWriteGate(testCtx, false, paused.Gate.Generation)
		exported <- err
	}()
	waitGateLock(t, p, "%SELECT workspace_id,project_id,user_id,agent_id FROM (%")
	resumed := make(chan error, 1)
	go func() {
		_, err := p.ManageRecoveryWrites(testCtx, "resume", paused.StorageIdentity, paused.Gate.Generation)
		resumed <- err
	}()
	waitGateLock(t, p, "%FOR UPDATE OF g%")
	select {
	case err := <-resumed:
		t.Fatal("resumed during snapshot", err)
	default:
	}
	if err = blocker.Commit(testCtx); err != nil {
		t.Fatal(err)
	}
	if err = <-exported; err != nil {
		t.Fatal("snapshot", err)
	}
	if err = <-resumed; err != nil {
		t.Fatal("resume", err)
	}
}

func TestPostgresRecoveryWriteGateMissingOrMalformed(t *testing.T) {
	p := isolatedPostgres(t)
	valid, err := json.Marshal(gateStatus(t, p).Gate)
	if err != nil {
		t.Fatal(err)
	}
	var infinity map[string]any
	if err = json.Unmarshal(valid, &infinity); err != nil {
		t.Fatal(err)
	}
	infinity["changed_at"] = "-infinity"
	invalidTime, err := json.Marshal(infinity)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{`{}`, `null`, `{"version":1,"state":"open","generation":"x","changed_at":"2026-10-05T00:00:00Z"}`, string(invalidTime), strings.Replace(string(valid), `"version":1`, `"version":1.0`, 1)} {
		if _, err := p.q().Exec(testCtx, `UPDATE memory_storage_metadata SET value=$1 WHERE key='recovery_write_gate'`, []byte(data)); err != nil {
			t.Fatal(err)
		}
		if _, err := p.q().Exec(testCtx, `DELETE FROM entities WHERE false`); err == nil {
			t.Fatal("malformed gate allowed writes")
		}
		if err := p.CheckRestorePublication(testCtx); !errors.Is(err, ErrUnavailable) {
			t.Fatal("malformed gate published")
		}
	}
	if _, err := p.q().Exec(testCtx, `DELETE FROM memory_storage_metadata WHERE key='recovery_write_gate'`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.q().Exec(testCtx, `DELETE FROM entities WHERE false`); err == nil {
		t.Fatal("missing gate allowed writes")
	}
}

func TestPostgresRecoveryWriteGateCLI(t *testing.T) {
	p := isolatedPostgres(t)
	initial := gateStatus(t, p)
	binary := filepath.Join(t.TempDir(), "memoryd")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/memoryd")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %v: %s", err, output)
	}
	invoke := func(args ...string) ([]byte, error) {
		command := exec.Command(binary, args...)
		command.Env = append(os.Environ(), "MEMORY_STORE=postgres", "MEMORY_DATABASE_URL="+recoveryGateDSN(p), "MEMORY_LOCAL_STANDALONE=true")
		return command.Output()
	}
	base := []string{"--recovery-write-gate=pause", "--expected-storage-identity=" + initial.StorageIdentity, "--expected-write-generation=" + initial.Gate.Generation}
	for _, extra := range []string{"--worker", "--init-db", "--recovery", "--export-recovery-snapshot", "unexpected"} {
		if output, err := invoke(append(append([]string{}, base...), extra)...); err == nil || len(output) != 0 {
			t.Fatal("mixed mode executed")
		}
	}
	output, err := invoke(base...)
	if err != nil {
		t.Fatal("CLI pause", err)
	}
	var paused RecoveryWriteGateStatus
	if err = json.Unmarshal(output, &paused); err != nil || paused.Gate.State != "paused" {
		t.Fatal("CLI pause output", err)
	}
	if output, err = invoke("--export-recovery-snapshot", "--expected-write-generation="+paused.Gate.Generation); err != nil {
		t.Fatal("CLI fenced snapshot", err)
	}
	decodeSnapshot(t, output, err)
	if output, err = invoke("--export-recovery-snapshot", "--expected-write-generation="+initial.Gate.Generation); err == nil || len(output) != 0 {
		t.Fatal("stale CLI snapshot")
	}
	if output, err = invoke("--recovery-write-gate=status", "--expected-storage-identity="+paused.StorageIdentity); err != nil {
		t.Fatal("CLI status", err)
	}
	var status RecoveryWriteGateStatus
	if err = json.Unmarshal(output, &status); err != nil || status != paused {
		t.Fatal("CLI status changed gate", err)
	}
	if output, err = invoke("--recovery-write-gate=resume", "--expected-storage-identity="+paused.StorageIdentity, "--expected-write-generation="+paused.Gate.Generation); err != nil {
		t.Fatal("CLI resume", err)
	}
	if gateStatus(t, p).Gate.State != "open" {
		t.Fatal("CLI resume lost")
	}
}
