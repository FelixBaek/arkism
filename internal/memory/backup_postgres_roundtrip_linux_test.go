package memory

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// use the actual dump and authenticated bundle, not reconstructed
// records. No production credentials, certificate, files or database are used.
func TestPostgresBackupBundleDeletionAndExpiry(t *testing.T) {
	// pg_dump/pg_restore run inside the service container, or from a local bin directory when CI has no docker.
	container, binDir := os.Getenv("POSTGRES_TEST_CONTAINER"), os.Getenv("POSTGRES_TEST_PG_BINDIR")
	if container == "" && binDir == "" {
		t.Skip("POSTGRES_TEST_CONTAINER or POSTGRES_TEST_PG_BINDIR is required for isolated pg_dump/pg_restore")
	}
	origin := isolatedPostgres(t)
	restoredDB := isolatedPostgresSchema(t, false)
	root := t.TempDir()
	var err error
	run := func(cmd *exec.Cmd, output io.Writer) {
		t.Helper()
		var diagnostics bytes.Buffer
		cmd.Stdout, cmd.Stderr = output, &diagnostics
		if err := cmd.Run(); err != nil {
			t.Fatalf("backup subprocess failed: %v: %s", err, diagnostics.String())
		}
	}
	arkism := func(args ...string) {
		t.Helper()
		cmd := exec.Command(arkismBinary(t), args...)
		cmd.Env = arkismEnv(root, "http://127.0.0.1:9", filepath.Join(root, "no-token"))
		run(cmd, io.Discard)
	}
	databaseCommand := func(p *Postgres, program string, args ...string) *exec.Cmd {
		cfg := p.pool.Config().ConnConfig
		var cmd *exec.Cmd
		if binDir != "" {
			connection := []string{"-h", cfg.Host, "-p", strconv.Itoa(int(cfg.Port)), "-U", cfg.User, "-d", cfg.Database}
			cmd = exec.Command(filepath.Join(binDir, program), append(connection, args...)...)
		} else {
			command := []string{"exec", "-i", "-e", "PGPASSWORD", container, program,
				"-h", "127.0.0.1", "-U", cfg.User, "-d", cfg.Database}
			cmd = exec.Command("docker", append(command, args...)...)
		}
		cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.Password)
		return cmd
	}
	f := newFixture(t)
	f.svc.Repo = origin
	f.svc.BlobDirectory = blobTestDir(t)
	f.svc.SessionArchiveKey = bytes.Repeat([]byte{11}, 32)
	f.auth.actors[f.human] = actor("human-1", "human", true, append(allVerbs, "export", "expire")...)
	f.auth.actors[f.work] = actor("worker-1", "service", true, "read", "purge", "erase", "restore")
	work := workCheckpointPGInput(scopeA, "backup-work", "before-backup", 0, workCheckpointPackage())
	if _, err = f.svc.SaveWorkCheckpoint(testCtx, f.agent, work); err != nil {
		t.Fatal(err)
	}
	source := artifactSource(t, origin, scopeA)
	var artifact Record
	if err = f.svc.withBlobs(testCtx, func(b *lockedBlobs) error {
		artifact = attachTestArtifact(t, b, origin, source, "backup file bytes")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	f.svc.SessionArchiveNow = func() time.Time { return now }
	raw := []byte("{\"role\":\"user\",\"content\":\"원본 보존 검사\"}\n")
	parent := ContentHash("backup-original")
	chunk, err := f.svc.UploadSessionArchiveChunk(testCtx, f.human, SessionArchiveChunkInput{Scope: scopeA, ParentID: parent, SHA256: ContentHash(string(raw)), Data: base64.StdEncoding.EncodeToString(raw)})
	if err != nil {
		t.Fatal(err)
	}
	header, err := f.svc.CommitSessionArchive(testCtx, f.human, SessionArchiveCommitInput{Scope: scopeA, ID: parent, SessionID: "backup-session", Device: "server", Origin: "codex", Format: "jsonl", ObservedAt: now.Add(-6 * 24 * time.Hour), SHA256: ContentHash(string(raw)), Chunks: []string{chunk.ID}, Text: "원본 보존 검사"})
	if err != nil {
		t.Fatal(err)
	}
	memory := Record{ID: ID(), Scope: scopeA, Kind: "decision", Status: statusActive, Revision: 1, Text: "장기 결정은 보존한다", Tags: []string{"session:backup-session"}, CreatedAt: now, UpdatedAt: now}
	if err = origin.Insert(testCtx, "memories", memory); err != nil {
		t.Fatal(err)
	}
	snapshot := backupDatabaseSnapshot(t, origin)
	dumpPath := filepath.Join(root, "database.pgdump")
	dump, err := os.OpenFile(dumpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	run(databaseCommand(origin, "pg_dump", "-Fc"), dump)
	if err = dump.Close(); err != nil {
		t.Fatal(err)
	}
	cert, key := filepath.Join(root, "cert.pem"), filepath.Join(root, "key.pem")
	run(exec.Command("openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=isolated-backup-test", "-days", "1", "-keyout", key, "-out", cert), io.Discard)
	encrypted, authenticated := filepath.Join(root, "bundle.cms"), filepath.Join(root, "authenticated.zip")
	arkism("backup-archive", "encrypt", "--cert", cert, "--output", encrypted, "--", arkismBinary(t), "backup-bundle", "create", "--writers-stopped", "--database-archive", dumpPath, "--blob-root", f.svc.BlobDirectory)
	// The deletion occurs strictly after the dump: it is absent from the backup.
	deletion, err := f.svc.RequestPrivacyDeletion(testCtx, f.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "after-backup", TargetType: "work_checkpoint", TargetIDs: []string{work.ID}, Reason: "owner_request"})
	if err != nil {
		t.Fatal(err)
	}
	finishWorkOnlyPrivacy(t, f, scopeA, deletion.ID)
	ledger, err := f.svc.ExportDeletions(testCtx, f.human, scopeA)
	if err != nil || len(ledger.Targets) != 1 {
		t.Fatal("external ledger", err)
	}
	arkism("backup-archive", "decrypt", "--cert", cert, "--key", key, "--input", encrypted, "--output", authenticated)
	extracted := filepath.Join(root, "restore")
	arkism("backup-bundle", "extract", "--input", authenticated, "--output", extracted)
	restoredDump, err := os.Open(filepath.Join(extracted, "database.archive"))
	if err != nil {
		t.Fatal(err)
	}
	command := databaseCommand(restoredDB, "pg_restore", "--exit-on-error")
	command.Stdin = restoredDump
	run(command, io.Discard)
	restoredDump.Close()
	if !reflect.DeepEqual(snapshot, backupDatabaseSnapshot(t, restoredDB)) {
		t.Fatal("restored tables/indexes/policies differ from dump source")
	}
	if err = restoredDB.SearchReady(testCtx); err != nil {
		t.Fatal("restored schema/RLS not ready", err)
	}
	restored := newFixture(t)
	restored.svc.Repo = restoredDB
	restored.svc.BlobDirectory = filepath.Join(extracted, "files")
	restored.svc.SessionArchiveKey = f.svc.SessionArchiveKey
	restored.svc.SessionArchiveNow = func() time.Time { return now }
	restored.auth.actors[restored.human] = f.auth.actors[f.human]
	restored.auth.actors[restored.work] = f.auth.actors[f.work]
	if err = restored.svc.CheckBlobStorage(testCtx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("restore quarantine ignored", err)
	}
	if err = restored.svc.AdoptRestoredBlobs(testCtx, restored.work); err != nil {
		t.Fatal("adoption", err)
	}
	if err = restored.svc.CheckBlobStorage(testCtx); err != nil {
		t.Fatal(err)
	}
	fileBytes, err := os.ReadFile(filepath.Join(restored.svc.BlobDirectory, artifact.Artifact.Digest))
	if err != nil || string(fileBytes) != "backup file bytes" {
		t.Fatal("file roundtrip", err)
	}
	restoredRaw, err := restored.svc.ReadSessionArchiveChunk(testCtx, restored.human, SessionArchiveReadInput{Scope: scopeA, ID: parent, ChunkID: chunk.ID})
	if err != nil || restoredRaw.Data != base64.StdEncoding.EncodeToString(raw) {
		t.Fatal("restored ciphertext did not decrypt to original bytes", err)
	}
	restored.svc.SessionArchiveNow = func() time.Time { return header.ExpiresAt.Add(time.Second) }
	// Still isolated: demonstrate that an old dump requires the newer ledger.
	if _, err = restored.svc.GetWorkCheckpoint(testCtx, restored.agent, WorkCheckpointGetInput{Scope: scopeA, ID: work.ID}); err != nil {
		t.Fatal("pre-replay snapshot missing", err)
	}
	server := httptest.NewServer(RecoveryHandler(restored.svc))
	defer server.Close()
	replay := func() {
		t.Helper()
		// Re-request the exported work deletion under one stable key, as recovery does.
		if _, err := restored.svc.RequestPrivacyDeletion(testCtx, restored.human, PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "restore-work-" + work.ID, TargetType: "work_checkpoint", TargetIDs: []string{ledger.Targets[0].ID}, Reason: "owner_request"}); err != nil {
			t.Fatal("ledger replay", err)
		}
	}
	replay()
	replay()
	if _, err = restored.svc.GetWorkCheckpoint(testCtx, restored.agent, WorkCheckpointGetInput{Scope: scopeA, ID: work.ID}); err == nil {
		t.Fatal("restored deleted work readable after ledger replay")
	}
	requestID := ""
	for _, row := range pgCollection(t, restoredDB, colJobs) {
		if row.Kind == privacyDeletionKind {
			if requestID != "" {
				t.Fatal("duplicate deletion requests")
			}
			requestID = row.ID
		}
	}
	if requestID == "" {
		t.Fatal("missing deletion request")
	}
	finishRecoveryHTTPWork(t, server.URL, restored.work, scopeA, requestID)
	replay()
	erased, err := restoredDB.Get(testCtx, colWorkCheckpoints, scopeA, work.ID)
	if err != nil || !erased.ContentErased || erased.WorkCheckpoint != nil {
		t.Fatal("restored work body retained", err)
	}
	if _, err = restored.svc.SaveWorkCheckpoint(testCtx, restored.agent, work); err == nil {
		t.Fatal("replay resurrected deleted work")
	}
	view, err := restored.svc.GetSessionArchive(testCtx, restored.human, SessionArchiveGetInput{Scope: scopeA, ID: parent})
	if err != nil || view.State != "expired" || view.Text != "" {
		t.Fatal("expired restored projection readable", err)
	}
	expiredRaw, err := restored.svc.ReadSessionArchiveChunk(testCtx, restored.human, SessionArchiveReadInput{Scope: scopeA, ID: parent, ChunkID: chunk.ID})
	if err != nil || expiredRaw.State != "expired" || expiredRaw.Data != "" || expiredRaw.SHA256 != "" {
		t.Fatal("expired restored bytes readable", err)
	}
	listed, err := restored.svc.ListSessionArchives(testCtx, restored.human, SessionArchiveListInput{Scope: scopeA, Limit: 100})
	if err != nil || len(listed.Items) != 0 {
		t.Fatal("expired restored archive listed", err)
	}
	var expired SessionArchiveExpireOutput
	recoveryHTTP(t, server.URL, restored.human, "POST", "/v1/session-archives/expire", SessionArchiveExpireInput{Scope: scopeA}, &expired, 200)
	if len(expired.Expired) != 1 {
		t.Fatal("restored expiry failed")
	}
	if _, err = restoredDB.Get(testCtx, colSessionArchiveChunks, scopeA, chunk.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired encrypted chunk retained", err)
	}
	kept, err := restoredDB.Get(testCtx, "memories", scopeA, memory.ID)
	if err != nil || kept.Text != memory.Text {
		t.Fatal("original expiry removed durable memory", err)
	}
}

// Compare every application table's rows (including bytea ciphertext), indexes,
// row security flags and policies before adoption or replay adds new evidence.
func backupDatabaseSnapshot(t *testing.T, p *Postgres) map[string][]string {
	t.Helper()
	query := func(sql string) []string {
		t.Helper()
		rows, err := p.pool.Query(testCtx, sql)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := []string{}
		for rows.Next() {
			var value string
			if err = rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			result = append(result, value)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	out := map[string][]string{}
	for _, table := range query(`SELECT tablename FROM pg_tables WHERE schemaname='arkism' ORDER BY tablename`) {
		out["table:"+table] = query("SELECT to_jsonb(t)::text FROM " + pgx.Identifier{"arkism", table}.Sanitize() + " t ORDER BY 1")
	}
	out["indexes"] = query(`SELECT indexdef FROM pg_indexes WHERE schemaname='arkism' ORDER BY indexname`)
	out["policies"] = query(`SELECT to_jsonb(p)::text FROM pg_policies p WHERE schemaname='arkism' ORDER BY tablename,policyname`)
	out["rls"] = query(`SELECT c.relname||':'||c.relrowsecurity||':'||c.relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='arkism' AND c.relkind='r' ORDER BY c.relname`)
	return out
}
