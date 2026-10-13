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
)

func snapshotReceipt(t *testing.T, p *Postgres, scope Scope, id string) {
	t.Helper()
	row := Record{ID: sourceDeletionID(scope, id), Scope: scope, Kind: "source_deletion", Status: "done", Revision: 1, CreatedAt: time.Now().Add(-time.Second), Target: &Ref{Collection: "sources", ID: id, Revision: 1}}
	if err := p.Insert(testCtx, colJobs, row); err != nil {
		t.Fatal(err)
	}
}

func decodeSnapshot(t *testing.T, raw []byte, err error) RecoverySnapshot {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var out RecoverySnapshot
	if err = json.Unmarshal(raw, &out); err != nil || out.Version != 3 || len(out.SourceIdentity) != 64 || out.CapturedAt.IsZero() || out.Coverage != "arkism_canonical_tables_v1" {
		t.Fatal("invalid snapshot", err)
	}
	return out
}

func TestPostgresRecoverySnapshotCoverageAndCLI(t *testing.T) {
	p := isolatedPostgres(t)
	f := deletionReplayFixture(t, p)
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, workCheckpointPGInput(scopeA, "snapshot-work", "initial", 0, workCheckpointPackage())); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "snapshot-delete", TargetType: "work_checkpoint", TargetIDs: []string{"snapshot-work"}, Reason: "private-reason-not-exported"}); err != nil {
		t.Fatal(err)
	}
	scopes := []Scope{scopeA, scopeA, scopeA, scopeA}
	scopes[0].WorkspaceID = "other-org"
	scopes[1].ProjectID = "other-project"
	scopes[2].UserID = "other-user"
	scopes[3].AgentID = "other-agent"
	for i, s := range scopes {
		snapshotReceipt(t, p, s, string(rune('a'+i)))
	}
	// Canonical graph-only and internal storage scopes must not disappear simply
	// because they have no ordinary memories or deletion receipts.
	for _, s := range []Scope{{WorkspaceID: "canonical-only", ProjectID: "graph", UserID: "owner"}, {WorkspaceID: "__memory_blobs__", ProjectID: "__memory_blobs__"}} {
		if _, err := p.q().Exec(testCtx, `INSERT INTO entities(organization_id,project_id,user_id,agent_id,kind,external_id) VALUES($1,$2,$3,$4,'test','only')`, s.WorkspaceID, s.ProjectID, s.UserID, s.AgentID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.q().Exec(testCtx, `INSERT INTO execution_traces(id,organization_id,project_id,user_id,agent_id,thread_id,agent_run_id,status,started_at,finished_at,payload_raw,sensitivity,inspection,request_hash,created_by,created_at)
VALUES('trace-only','canonical-trace','p','u','a','thread','run','completed',now(),now(),'private-trace-not-exported','private','{}','hash','actor',now())`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := p.q().QueryRow(testCtx, `SELECT count(*) FROM memory_records`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	raw, err := p.ExportRecoverySnapshot(testCtx)
	out := decodeSnapshot(t, raw, err)
	if len(out.Ledgers) != 8 {
		t.Fatalf("scope coverage=%d", len(out.Ledgers))
	}
	requests, receipts, empty := 0, 0, 0
	for _, ledger := range out.Ledgers {
		if ledger.Version != 2 || ledger.PrivacyRequests == nil {
			t.Fatal("missing v2 requests")
		}
		requests += len(*ledger.PrivacyRequests)
		receipts += len(ledger.Targets)
		if len(ledger.Targets) == 0 && len(*ledger.PrivacyRequests) == 0 {
			empty++
		}
	}
	if requests != 1 || receipts != 4 || empty != 3 || strings.Contains(string(raw), "private-reason-not-exported") || strings.Contains(string(raw), "private-trace-not-exported") {
		t.Fatal("missing or excessive contents", requests, receipts, empty)
	}
	var after int
	if err = p.q().QueryRow(testCtx, `SELECT count(*) FROM memory_records`).Scan(&after); err != nil || after != count {
		t.Fatal("export wrote records", err)
	}
	if data, err := p.InspectRestoredRecovery(testCtx); err == nil || data != nil {
		t.Fatal("normal database inspected as restore")
	}
	app := restrictedPostgres(t, p)
	if data, err := app.ExportRecoverySnapshot(testCtx); !errors.Is(err, ErrDenied) || data != nil {
		t.Fatal("scoped role exported partial data", err)
	}
	root := t.TempDir()
	binary := filepath.Join(root, "memoryd")
	build := exec.Command("go", "build", "-o", binary, "./cmd/memoryd")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	cfg := p.pool.Config().ConnConfig
	dsn := url.URL{Scheme: "postgres", User: url.UserPassword(cfg.User, cfg.Password), Host: net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))), Path: "/" + cfg.Database, RawQuery: "sslmode=disable"}
	for _, args := range [][]string{{"--export-recovery-snapshot"}, {"--export-recovery-snapshot", "--worker"}, {"--export-recovery-snapshot", "--init-db"}} {
		command := exec.Command(binary, args...)
		command.Env = append(os.Environ(), "MEMORY_DATABASE_URL="+dsn.String(), "MEMORY_STORE=postgres", "MEMORY_LOCAL_STANDALONE=true")
		output, err := command.Output()
		if len(args) == 1 {
			result := decodeSnapshot(t, output, err)
			if len(result.Ledgers) != 8 || result.SourceIdentity != out.SourceIdentity {
				t.Fatal("CLI lost scopes")
			}
		} else if err == nil || len(output) != 0 {
			t.Fatal("mixed command published output")
		}
	}
	manifest := ContentHash("inspection-cli")
	if err := p.RequireRestoreReview(testCtx, manifest); err != nil {
		t.Fatal(err)
	}
	if data, err := app.InspectRestoredRecovery(testCtx); !errors.Is(err, ErrDenied) || data != nil {
		t.Fatal("restricted restore inspection", err)
	}
	command := exec.Command(binary, "--inspect-restored-recovery")
	command.Env = append(os.Environ(), "MEMORY_DATABASE_URL="+dsn.String(), "MEMORY_STORE=postgres", "MEMORY_LOCAL_STANDALONE=true")
	output, err := command.Output()
	var inspected RestoredRecoverySnapshot
	if err != nil || json.Unmarshal(output, &inspected) != nil || inspected.Version != 4 || inspected.RestoreManifest != manifest || inspected.RestoredIdentity != out.SourceIdentity || len(inspected.Ledgers) != 8 {
		t.Fatal("restore inspection CLI", err)
	}

}

func TestPostgresRecoverySnapshotConcurrentDeletion(t *testing.T) {
	p := isolatedPostgres(t)
	snapshotReceipt(t, p, scopeA, "before")
	holder, err := p.pool.Begin(testCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(testCtx)
	if _, err = holder.Exec(testCtx, `LOCK TABLE entities IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(testCtx, 15*time.Second)
	defer cancel()
	go func() { data, err := p.ExportRecoverySnapshot(ctx); done <- result{data, err} }()
	// The exporter establishes its snapshot when it checks the current DB role,
	// then blocks when the all-table scope query reaches the locked graph table.
	waiting := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err = p.pool.QueryRow(testCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT workspace_id,project_id,user_id,agent_id FROM (%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("export did not reach scope snapshot")
	}
	snapshotReceipt(t, p, scopeA, "after-same-scope")
	snapshotReceipt(t, p, scopeB, "after-new-scope")
	if err = holder.Commit(testCtx); err != nil {
		t.Fatal(err)
	}
	finished := <-done
	out := decodeSnapshot(t, finished.data, finished.err)
	if len(out.Ledgers) != 1 || len(out.Ledgers[0].Targets) != 1 || out.Ledgers[0].Targets[0].ID != "before" {
		t.Fatal("mixed snapshots")
	}
	data, err := p.ExportRecoverySnapshot(testCtx)
	next := decodeSnapshot(t, data, err)
	count := 0
	for _, ledger := range next.Ledgers {
		count += len(ledger.Targets)
	}
	if len(next.Ledgers) != 2 || count != 3 {
		t.Fatal("later export lost committed deletions")
	}
}

func TestPostgresRecoverySnapshotFailClosed(t *testing.T) {
	for _, kind := range []string{"pending", "identity", "corrupt", "scope", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			p := isolatedPostgres(t)
			switch kind {
			case "pending":
				if err := p.RequireRestoreReview(testCtx, ContentHash("restore")); err != nil {
					t.Fatal(err)
				}
			case "identity":
				if _, err := p.q().Exec(testCtx, `DELETE FROM memory_storage_metadata WHERE key='storage_identity'`); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := p.Insert(testCtx, colJobs, Record{ID: "bad", Scope: scopeA, Kind: "source_deletion", Revision: 1}); err != nil {
					t.Fatal(err)
				}
			case "scope":
				if _, err := p.q().Exec(testCtx, `INSERT INTO entities(organization_id,project_id,kind,external_id) VALUES('','p','test','x')`); err != nil {
					t.Fatal(err)
				}
			case "overflow":
				if _, err := p.q().Exec(testCtx, `INSERT INTO entities(organization_id,project_id,kind,external_id) SELECT 'org',i::text,'test','x' FROM generate_series(1,$1) i`, recoverySnapshotScopeLimit+1); err != nil {
					t.Fatal(err)
				}
			}
			if data, err := p.ExportRecoverySnapshot(testCtx); err == nil || data != nil {
				t.Fatal("partial snapshot escaped", kind)
			}
		})
	}
	p := isolatedPostgres(t)
	data, err := p.ExportRecoverySnapshot(testCtx)
	out := decodeSnapshot(t, data, err)
	if len(out.Ledgers) != 0 {
		t.Fatal("empty DB invented scopes")
	}
}
