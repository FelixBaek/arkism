//go:build integration && linux

package memory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestIntegrationBlobGCCLI(t *testing.T) {
	repo := isolatedPostgres(t)
	f := blobGCFixture(t, repo)
	name := ContentHash("cli orphan")
	path := filepath.Join(f.svc.BlobDirectory, name)
	if e := os.WriteFile(path, []byte("cli orphan"), 0600); e != nil {
		t.Fatal(e)
	}
	// Postgres is the canonical authorizer: the CLI's worker token needs a grant.
	grant := PrincipalGrant{PrincipalID: "cli-maintainer", Role: "service", Token: testPrincipalToken("test-maintainer"), OrganizationID: blobScope().WorkspaceID, ProjectID: blobScope().ProjectID, Actions: []string{"maintain:blobs"}}
	if e := repo.ProvisionPrincipal(testCtx, grant); e != nil {
		t.Fatal(e)
	}
	bin := filepath.Join(t.TempDir(), "memoryd")
	build := exec.Command("go", "build", "-o", bin, "../../cmd/memoryd")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatal("build", e, string(out))
	}
	env := []string{"MEMORY_STORE=postgres", "MEMORY_DATABASE_URL=" + itDSN(repo), "MEMORY_BLOB_DIRECTORY=" + f.svc.BlobDirectory, "MEMORY_WORKER_TOKEN=" + testPrincipalToken("test-maintainer")}
	for _, args := range [][]string{{"--collect-blobs", "--recovery"}, {"--collect-blobs", "--worker"}, {"--collect-blobs", "--init-db"}} {
		cmd := exec.Command(bin, args...)
		cmd.Env = env
		if cmd.Run() == nil {
			t.Fatal("mixed command modes accepted", args)
		}
	}
	ctx, cancel := context.WithTimeout(testCtx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--collect-blobs")
	cmd.Env = env
	out, e := cmd.Output()
	if e != nil {
		t.Fatal("maintenance command", e, string(e.(*exec.ExitError).Stderr))
	}
	var result BlobGCResult
	if e = json.Unmarshal(out, &result); e != nil || result.CollectedBlobs != 1 {
		t.Fatal("command result", string(out), e)
	}
	if _, e = os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("command did not collect orphan", e)
	}
}
