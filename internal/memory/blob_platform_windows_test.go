package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWindowsBlobStorePrivateAndLocked(t *testing.T) {
	dir := t.TempDir()
	setWindowsBlobTestDACL(t, dir, "(A;OICI;FR;;;WD)")
	store := blobStore{dir: dir}
	if err := store.locked(t.Context(), func(*lockedBlobs) error { t.Fatal("public root accepted"); return nil }); !errors.Is(err, ErrInvalid) {
		t.Fatalf("public root: %v", err)
	}
	setWindowsBlobTestDACL(t, dir, "")
	body := "synthetic Windows attachment"
	digest := ContentHash(body)
	if err := store.locked(t.Context(), func(blob *lockedBlobs) error {
		ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
		defer cancel()
		if err := store.locked(ctx, func(*lockedBlobs) error { t.Fatal("duplicate lock"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("second lock: %v", err)
		}
		if _, _, err := blob.put(strings.NewReader(body), digest); err != nil {
			return err
		}
		return blob.verify(digest, digest, int64(len(body)))
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, digest), filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := store.locked(t.Context(), func(blob *lockedBlobs) error {
		if err := blob.verify(digest, digest, int64(len(body))); !errors.Is(err, ErrInvalid) {
			t.Fatalf("hard-linked file: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func blobTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	setWindowsBlobTestDACL(t, dir, "")
	return dir
}

func setWindowsBlobTestDACL(t *testing.T, dir, extra string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")" + extra)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsAttachmentPostgresRoundTrip(t *testing.T) {
	bin := os.Getenv("POSTGRES_TEST_PG_BINDIR")
	if bin == "" {
		t.Skip("requires native PostgreSQL dump and restore executables")
	}
	ctx := t.Context()
	source, restored := isolatedPostgres(t), isolatedPostgresSchema(t, false)
	originDir, restoredDir := t.TempDir(), t.TempDir()
	setWindowsBlobTestDACL(t, originDir, "")
	setWindowsBlobTestDACL(t, restoredDir, "")
	scope := Scope{WorkspaceID: "windows-test", ProjectID: "attachments", UserID: "owner"}
	owner, err := NewPrincipalToken()
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewPrincipalToken()
	if err != nil {
		t.Fatal(err)
	}
	for _, grant := range []PrincipalGrant{
		{PrincipalID: "owner", Role: roleHuman, Token: owner, OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"*"}},
		{PrincipalID: "worker", Role: roleService, Token: worker, OrganizationID: scope.WorkspaceID, ProjectID: scope.ProjectID, Actions: []string{"*"}},
		{PrincipalID: "worker", Role: roleService, Token: worker, OrganizationID: blobScope().WorkspaceID, ProjectID: blobScope().ProjectID, Actions: []string{"*"}},
	} {
		if err := source.ProvisionPrincipal(ctx, grant); err != nil {
			t.Fatal(err)
		}
	}
	newService := func(repo *Postgres, dir string) *Service {
		auth, err := NewPostgresAuthorizer(repo)
		if err != nil {
			t.Fatal(err)
		}
		return &Service{Repo: repo, Auth: auth, BlobDirectory: dir}
	}
	svc := newService(source, originDir)
	body := []byte("synthetic FelixPC attachment")
	now := time.Now().UTC()
	in := FileIngestInput{
		Source: CreateInput{Scope: scope, Kind: "document", OriginProduct: "brain", SourceRef: "fixture/windows-attachment",
			CollectedAt: &now, Sensitivity: "internal", IdempotencyKey: "synthetic-file"},
		Artifact: FileArtifactSpec{Kind: "tool_output", MIME: "text/plain"},
	}
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	metadata, err := form.CreateFormField("metadata")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(metadata).Encode(in); err != nil {
		t.Fatal(err)
	}
	content, err := form.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="fixture.txt"`}, "Content-Type": {"text/plain"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = content.Write(body); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/sources/ingest", &upload)
	req.Header.Set("Authorization", "Bearer "+owner)
	req.Header.Set("Content-Type", form.FormDataContentType())
	out := httptest.NewRecorder()
	Handler(svc).ServeHTTP(out, req)
	var item FileIngestResult
	if out.Code != http.StatusCreated || json.Unmarshal(out.Body.Bytes(), &item) != nil || item.Artifact.Digest != ContentHash(string(body)) {
		t.Fatalf("upload: %d %s", out.Code, out.Body)
	}
	path := "/v1/sources/" + item.Source.ID + "/artifacts/" + item.Artifact.ID +
		"?workspace_id=windows-test&project_id=attachments&user_id=owner"
	readHTTP := func(s *Service) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+owner)
		out := httptest.NewRecorder()
		Handler(s).ServeHTTP(out, req)
		return out
	}
	if out = readHTTP(svc); out.Code != http.StatusOK || !bytes.Equal(out.Body.Bytes(), body) {
		t.Fatalf("original download: %d", out.Code)
	}
	dump := filepath.Join(t.TempDir(), "database.pgdump")
	runPG := func(repo *Postgres, program string, args ...string) {
		t.Helper()
		cfg := repo.pool.Config().ConnConfig
		cmd := exec.CommandContext(ctx, filepath.Join(bin, program+".exe"),
			append([]string{"-h", cfg.Host, "-p", strconv.Itoa(int(cfg.Port)), "-U", cfg.User, "-d", cfg.Database}, args...)...)
		cmd.Env = append(os.Environ(), "PGPASSWORD="+cfg.Password)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", program, err, output)
		}
	}
	runPG(source, "pg_dump", "-Fc", "-f", dump)
	runPG(restored, "pg_restore", "--exit-on-error", dump)
	database, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := os.ReadFile(filepath.Join(originDir, ".database"))
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Split(strings.TrimSpace(string(binding)), "\n")
	if len(ids) != 2 {
		t.Fatal("invalid original binding")
	}
	manifest, err := json.Marshal(blobBackupManifest{
		Version: 1, SourceIdentity: ids[0], SourceRootID: ids[1],
		Database: backupFile{Digest: ContentHash(string(database)), Bytes: int64(len(database))},
		Files:    []backupFile{{Digest: item.Artifact.Digest, Bytes: int64(len(body))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(restoredDir, item.Artifact.Digest)
	if err = os.WriteFile(blob, body, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(restoredDir, ".restore-pending"), manifest, 0600); err != nil {
		t.Fatal(err)
	}
	clone := newService(restored, restoredDir)
	if !errors.Is(clone.CheckBlobStorage(ctx), ErrUnavailable) {
		t.Fatal("unadopted files were available")
	}
	if err = clone.AdoptRestoredBlobs(ContextWithPostgresToken(ctx, worker), worker); err != nil {
		t.Fatal(err)
	}
	if err = clone.CheckBlobStorage(ctx); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(restored.CheckRestorePublication(ctx), ErrUnavailable) {
		t.Fatal("blob adoption skipped the restore review")
	}
	if actual, err := clone.ReadArtifact(ctx, owner, scope, item.Source.ID, item.Artifact.ID); err != nil || !bytes.Equal(actual, body) {
		t.Fatalf("restored attachment: %v", err)
	}
	// Erase the original through the privacy workflow. The clone remains isolated.
	if _, err = svc.RequestPrivacyDeletion(ctx, owner, PrivacyDeletionRequest{
		Scope: scope, IdempotencyKey: "synthetic-delete", TargetType: "source_event", TargetIDs: []string{item.Source.ID}, Reason: "owner_request",
	}); err != nil {
		t.Fatal(err)
	}
	if out = readHTTP(svc); out.Code != http.StatusNotFound && out.Code != http.StatusForbidden {
		t.Fatalf("attachment visible during deletion: %d", out.Code)
	}
	workerCtx := ContextWithPostgresToken(ctx, worker)
	if _, err = svc.RunPrivacyRuntimeStep(workerCtx, worker); err != nil {
		t.Fatal(err)
	}
	originalBlob := filepath.Join(originDir, item.Artifact.Digest)
	for tick := range 64 {
		if err = svc.WorkOnce(workerCtx, worker); err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatalf("worker iteration %d: %v", tick, err)
		}
		if _, err = os.Stat(originalBlob); errors.Is(err, os.ErrNotExist) {
			break
		}
	}
	if _, err = os.Stat(originalBlob); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worker did not remove the source blob: %v", err)
	}
	if _, err = os.Stat(blob); err != nil {
		t.Fatalf("deletion touched the isolated clone: %v", err)
	}
}
