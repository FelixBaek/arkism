package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func artifactReadRequest(source, artifact string, scope Scope, token string) *http.Request {
	q := url.Values{"workspace_id": {scope.WorkspaceID}, "project_id": {scope.ProjectID}, "user_id": {scope.UserID}, "agent_id": {scope.AgentID}}
	r := httptest.NewRequest("GET", "/v1/sources/"+source+"/artifacts/"+artifact+"?"+q.Encode(), nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func exerciseArtifactRead(t *testing.T, repo Repository) {
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.BlobDirectory = blobTestDir(t)
	body := []byte("shared original download")
	upload := func(scope Scope) FileIngestResult {
		out, err := f.svc.IngestFile(testCtx, f.human, FileIngestInput{Source: sourceInput(CreateInput{Scope: scope}), Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}, body)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first, second := upload(scopeA), upload(scopeB)
	h := Handler(f.svc)
	query := url.Values{"workspace_id": {scopeA.WorkspaceID}, "project_id": {scopeA.ProjectID}}
	listRequest := httptest.NewRequest("GET", "/v1/sources/"+first.Source.ID+"/artifacts?"+query.Encode(), nil)
	listRequest.Header.Set("Authorization", "Bearer "+f.human)
	listResponse := httptest.NewRecorder()
	h.ServeHTTP(listResponse, listRequest)
	var listed SourceArtifacts
	if listResponse.Code != 200 || json.Unmarshal(listResponse.Body.Bytes(), &listed) != nil || len(listed.Items) != 1 || listed.Items[0] != first.Artifact {
		t.Fatal("discover uploaded file", listResponse.Code)
	}
	r := artifactReadRequest(first.Source.ID, listed.Items[0].ID, scopeA, f.human)
	r.Header.Set("Range", "bytes=0-1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("download contract", w.Code, w.Header())
	}
	for _, r := range []*http.Request{
		artifactReadRequest(first.Source.ID, first.Artifact.ID, scopeB, f.human),
		artifactReadRequest(second.Source.ID, first.Artifact.ID, scopeA, f.human),
	} {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 || bytes.Equal(w.Body.Bytes(), body) {
			t.Fatal("wrong scope/source returned file", w.Code)
		}
	}
	f.svc.Repo = artifactAuditFailure{repo}
	if content, err := f.svc.ReadArtifact(testCtx, f.human, scopeA, first.Source.ID, first.Artifact.ID); !errors.Is(err, ErrUnavailable) || content != nil {
		t.Fatal("read ignored audit failure", err)
	}
	f.svc.Repo = repo
	if err := f.svc.Delete(testCtx, f.human, "sources", first.Source.ID, ChangeInput{Scope: scopeA, ExpectedRevision: first.Source.Revision}); err != nil {
		t.Fatal(err)
	}
	if content, err := f.svc.ReadArtifact(testCtx, f.human, scopeA, first.Source.ID, first.Artifact.ID); !errors.Is(err, ErrNotFound) || content != nil {
		t.Fatal("deleted source returned bytes", err)
	}
	if content, err := f.svc.ReadArtifact(testCtx, f.human, scopeB, second.Source.ID, second.Artifact.ID); err != nil || !bytes.Equal(content, body) {
		t.Fatal("shared surviving source unavailable", err)
	}
	w = httptest.NewRecorder()
	RecoveryHandler(f.svc).ServeHTTP(w, artifactReadRequest(second.Source.ID, second.Artifact.ID, scopeB, f.human))
	if w.Code == 200 {
		t.Fatal("recovery exposed file download")
	}
}

func TestArtifactRead(t *testing.T) { exerciseArtifactRead(t, newFakeRepo()) }

type artifactReadPolicy struct {
	Authorizer
	artifactCalls                                 int
	denySource, denyArtifact, revoke, changeActor bool
}

func (p *artifactReadPolicy) Authorize(ctx context.Context, token string, in Authorization) (Decision, error) {
	d, err := p.Authorizer.Authorize(ctx, token, in)
	if in.Action == "read:sources" && p.denySource {
		d.Allowed = false
	}
	if in.Action == "read:artifacts" {
		p.artifactCalls++
		if p.denyArtifact || p.revoke && p.artifactCalls > 1 {
			d.Allowed = false
		}
		if p.changeActor {
			d.PrincipalID = "different-reader"
		}
	}
	return d, err
}

func TestArtifactReadRefuses(t *testing.T) {
	for _, name := range []string{"deny_source", "deny_artifact", "revoked", "changed_actor", "expired", "revision", "corrupt", "missing", "permissions", "hardlink", "symlink", "symlink_inside", "count", "wrong_root", "wrong_query"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.svc.BlobDirectory = blobTestDir(t)
			body := []byte("original private download")
			out, err := f.svc.IngestFile(testCtx, f.human, FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA}), Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}, body)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.svc.BlobDirectory, out.Artifact.Digest)
			policy := &artifactReadPolicy{Authorizer: f.auth}
			f.svc.Auth = policy
			status := 503
			switch name {
			case "deny_source":
				policy.denySource = true
				status = 403
			case "deny_artifact":
				policy.denyArtifact = true
				status = 403
			case "revoked":
				policy.revoke = true
				status = 403
			case "changed_actor":
				policy.changeActor = true
				status = 403
			case "expired", "revision":
				r, e := f.repo.Get(testCtx, "sources", scopeA, out.Source.ID)
				if e != nil {
					t.Fatal(e)
				}
				previous := r.Revision
				r.Revision++
				status = 409
				if name == "expired" {
					at := time.Now().Add(-time.Minute)
					r.ExpiresAt = &at
					status = 404
				}
				if e = f.repo.Replace(testCtx, "sources", r, previous); e != nil {
					t.Fatal(e)
				}
			case "corrupt":
				err = os.WriteFile(path, bytes.Repeat([]byte("x"), len(body)), 0600)
			case "missing":
				err = os.Remove(path)
			case "permissions":
				err = os.Chmod(path, 0644)
			case "hardlink":
				err = os.Link(path, filepath.Join(t.TempDir(), "outside"))
			case "symlink", "symlink_inside":
				external := filepath.Join(t.TempDir(), "outside")
				if name == "symlink_inside" {
					external = filepath.Join(f.svc.BlobDirectory, "alias")
				}
				if err = os.Rename(path, external); err == nil {
					err = os.Symlink(external, path)
				}
			case "count":
				r, e := f.repo.Get(testCtx, colBlobs, blobScope(), out.Artifact.Digest)
				if e != nil {
					t.Fatal(e)
				}
				previous := r.Revision
				r.Revision++
				r.Blob.References++
				err = f.repo.Replace(testCtx, colBlobs, r, previous)
			case "wrong_root":
				f.svc.BlobDirectory = blobTestDir(t)
			case "wrong_query":
				status = 400
			}
			if err != nil {
				t.Fatal(err)
			}
			r := artifactReadRequest(out.Source.ID, out.Artifact.ID, scopeA, f.human)
			if name == "wrong_query" {
				r.URL.RawQuery += "&project_id=duplicate"
			}
			w := httptest.NewRecorder()
			Handler(f.svc).ServeHTTP(w, r)
			if w.Code != status || bytes.Contains(w.Body.Bytes(), body) {
				t.Fatal("unsafe download", w.Code, status, w.Body.String())
			}
		})
	}
}

func TestArtifactReadLargerThanPublicIngest(t *testing.T) {
	f := newFixture(t)
	f.svc.BlobDirectory = blobTestDir(t)
	source := artifactSource(t, f.repo, scopeA)
	body := bytes.Repeat([]byte{0, 1, 2, 3}, (2<<20)/4)
	data := artifactMetadata{Digest: ContentHash(string(body)), MIME: "audio/wav", Bytes: int64(len(body))}
	var artifact Record
	err := f.svc.withBlobs(testCtx, func(b *lockedBlobs) error {
		var e error
		artifact, e = b.attach(f.repo, scopeA, Ref{Collection: "sources", ID: source.ID, Revision: source.Revision}, "audio", data, artifactActor(scopeA), bytes.NewReader(body))
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := f.svc.ListSourceArtifacts(testCtx, f.human, source.ID, ArtifactListInput{Scope: scopeA})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].Bytes != int64(len(body)) || listed.Items[0].MIME != "audio/wav" {
		t.Fatal("list inherited small text ingest limit", err)
	}
	w := httptest.NewRecorder()
	Handler(f.svc).ServeHTTP(w, artifactReadRequest(source.ID, artifact.ID, scopeA, f.human))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), body) {
		t.Fatal("download inherited small text ingest limit", w.Code)
	}
	if err = f.svc.withBlobs(testCtx, func(b *lockedBlobs) error {
		oversized := data
		oversized.Bytes = (200 << 20) + 1
		if result, err := b.readArtifactBytes(oversized); !errors.Is(err, ErrInvalid) || result != nil {
			t.Fatal("oversized metadata accepted", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Blocks the real response flush, after the buffered handler has finished.
type artifactSlowWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
}

func (w *artifactSlowWriter) Write(p []byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-w.release
	return w.ResponseRecorder.Write(p)
}
func TestArtifactReadSlowResponse(t *testing.T) {
	f := newFixture(t)
	f.svc.BlobDirectory = blobTestDir(t)
	out, err := f.svc.IngestFile(testCtx, f.human, FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA}), Artifact: FileArtifactSpec{Kind: "code", MIME: "text/plain"}}, []byte("original download"))
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(f.svc)
	w := &artifactSlowWriter{httptest.NewRecorder(), make(chan struct{}, 1), make(chan struct{})}
	done := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(w.release)
		}
		<-done
	}()
	go func() {
		handler.ServeHTTP(w, artifactReadRequest(out.Source.ID, out.Artifact.ID, scopeA, f.human))
		close(done)
	}()
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("response did not reach flush")
	}
	ctx, cancel := context.WithTimeout(testCtx, time.Second)
	defer cancel()
	if err := f.svc.CheckBlobStorage(ctx); err != nil {
		t.Fatal("slow response holds storage lock", err)
	}
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, artifactReadRequest(out.Source.ID, out.Artifact.ID, scopeA, f.human))
	if second.Code != 503 {
		t.Fatal("download admitted during flush", second.Code)
	}
	close(w.release)
	released = true
	<-done
	third := httptest.NewRecorder()
	handler.ServeHTTP(third, artifactReadRequest(out.Source.ID, out.Artifact.ID, scopeA, f.human))
	if third.Code != 200 {
		t.Fatal("download slot was not released", third.Code)
	}
}
