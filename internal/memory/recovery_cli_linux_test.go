package memory

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Exercise the actual --recovery process with the PostgreSQL authorizer, not
// just httptest or fixture authorization. Credentials exist only in this DB.
func TestPostgresRecoveryBinaryWorkDeletion(t *testing.T) {
	repo := isolatedPostgres(t)
	f := newFixture(t)
	f.svc.Repo = repo
	in := workCheckpointPGInput(scopeA, "recovery-binary-work", "before-recovery", 0, workCheckpointPackage())
	if _, err := f.svc.SaveWorkCheckpoint(testCtx, f.agent, in); err != nil {
		t.Fatal(err)
	}
	owner, worker := testPrincipalToken("synthetic-recovery-owner"), testPrincipalToken("synthetic-recovery-worker")
	for _, grant := range []PrincipalGrant{
		{PrincipalID: "restore-owner", Role: "human", Token: owner, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"delete:work_checkpoints", "read:privacy_deletion", "expire:session_archives"}},
		{PrincipalID: "restore-worker", Role: "service", Token: worker, OrganizationID: scopeA.WorkspaceID, ProjectID: scopeA.ProjectID, Actions: []string{"purge:runtime", "purge:canonical", "read:privacy_deletion"}},
	} {
		if err := repo.ProvisionPrincipal(testCtx, grant); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(t.TempDir(), "memoryd")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/memoryd")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v %s", err, out)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg := repo.pool.Config().ConnConfig
	dsn := url.URL{Scheme: "postgres", User: url.UserPassword(cfg.User, cfg.Password), Host: net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))), Path: "/" + cfg.Database, RawQuery: "sslmode=disable"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, bin, "--recovery", "--listen", address)
	command.Env = []string{"MEMORY_STORE=postgres", "MEMORY_DATABASE_URL=" + dsn.String(), "MEMORY_WORKER_TOKEN=" + worker}
	// The same environment starts normally before the persistent restore gate.
	func() {
		controlCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		control := exec.CommandContext(controlCtx, bin, "--listen", address)
		control.Env = command.Env
		if err := control.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = control.Process.Signal(os.Interrupt); _ = control.Wait() }()
		client := http.Client{Timeout: time.Second}
		for n := 0; n < 50; n++ {
			response, err := client.Get("http://" + address + "/healthz")
			if err == nil {
				good := response.StatusCode == 200
				response.Body.Close()
				if good {
					return
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("ungated control API did not start")
	}()
	if err = repo.RequireRestoreReview(testCtx, ContentHash("verified file manifest")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--listen", address}, {"--worker"}, {"--reindex"}} {
		blockedCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		blocked := exec.CommandContext(blockedCtx, bin, args...)
		blocked.Env = command.Env // Deliberately no MEMORY_BLOB_DIRECTORY.
		err := blocked.Run()
		timedOut := blockedCtx.Err() != nil
		stop()
		if err == nil || timedOut {
			t.Fatal("normal mode did not refuse restored database", args)
		}
	}
	if err = command.Start(); err != nil {
		t.Fatal("recovery process start", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cancel()
			<-done
		}
	})
	base := "http://" + address
	client := http.Client{Timeout: time.Second}
	ready := false
	for n := 0; n < 100; n++ {
		res, err := client.Get(base + "/healthz")
		if err == nil {
			ready = res.StatusCode == 200
			res.Body.Close()
			if ready {
				break
			}
		}
		select {
		case err := <-done:
			done <- err
			t.Fatal("recovery process exited before health", err)
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("recovery process did not start")
	}
	recoveryHTTP(t, base, "", "GET", "/readyz", nil, nil, 503)
	request := PrivacyDeletionRequest{Scope: scopeA, IdempotencyKey: "binary-delete", TargetType: "work_checkpoint", TargetIDs: []string{in.ID}, Reason: "owner_request"}
	recoveryHTTP(t, base, "", "POST", "/v1/privacy/deletions", request, nil, 401)
	recoveryHTTP(t, base, worker, "POST", "/v1/privacy/deletions", request, nil, 403)
	var deletion PrivacyDeletionStatus
	recoveryHTTP(t, base, owner, "POST", "/v1/privacy/deletions", request, &deletion, 202)
	finishRecoveryHTTPWork(t, base, worker, scopeA, deletion.ID)
	row, err := repo.Get(testCtx, colWorkCheckpoints, scopeA, in.ID)
	if err != nil || !row.ContentErased || row.WorkCheckpoint != nil {
		t.Fatal("binary recovery retained work body", err)
	}
	recoveryHTTP(t, base, owner, "POST", "/v1/session-archives/expire", SessionArchiveExpireInput{Scope: scopeA}, nil, 200)
	recoveryHTTP(t, base, owner, "POST", "/v1/work-checkpoints/get", WorkCheckpointGetInput{Scope: scopeA, ID: in.ID}, nil, 404)
	recoveryHTTP(t, base, owner, "POST", "/v1/work-checkpoints/save", in, nil, 404)
	recoveryHTTP(t, base, owner, "GET", "/readyz", nil, nil, 503)
}
