package memory

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func exerciseFileBoundActor(t *testing.T, repo Repository, field string) {
	t.Helper()
	f := newFixture(t)
	f.svc.Repo = repo
	f.svc.BlobDirectory = blobTestDir(t)
	in := FileIngestInput{Source: sourceInput(CreateInput{Scope: scopeA, Sensitivity: "public"}), Artifact: FileArtifactSpec{Kind: "tool_output", MIME: "text/plain"}}
	call := func(body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		Handler(f.svc).ServeHTTP(w, fileRequest(t, in, body, f.human))
		return w
	}
	if w := call([]byte("baseline file")); w.Code != 201 {
		t.Fatal("positive control", w.Code, w.Body.String())
	}
	preflight, changed := false, false
	f.svc.Auth = requestActorAuth{Authorizer: f.auth, change: func(in Authorization, d *Decision) error {
		if in.Action == "write:sources" && in.Content == "" {
			preflight = true
		}
		if in.Content != "" && (in.Action == "write:sources" || in.Action == "write:artifacts") {
			changed = true
			if field == "principal" {
				d.PrincipalID = "other-human"
			} else {
				d.Role = "service"
			}
		}
		return nil
	}}
	body := []byte("blocked file")
	w := call(body)
	if w.Code != 403 || !preflight || !changed {
		t.Fatal("changed preflight actor", w.Code, preflight, changed, w.Body.String())
	}
	digest := ContentHash(string(body))
	if _, e := repo.Get(testCtx, colBlobs, blobScope(), digest); !errors.Is(e, ErrNotFound) {
		t.Fatal("blocked blob registered", e)
	}
	pointer := "source-digest-" + ContentHash(structuredText(scopeA)+"\nsource-file-v1\n"+ContentHash("")+"\n"+digest)
	if _, e := repo.Get(testCtx, "jobs", scopeA, pointer); !errors.Is(e, ErrNotFound) {
		t.Fatal("blocked source registered", e)
	}
	if _, e := os.Stat(filepath.Join(f.svc.BlobDirectory, digest)); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("blocked bytes published", e)
	}
	// Binding belongs only to the nested request, not the Service or the token.
	f.svc.Auth = f.auth
	if w := call(body); w.Code != 201 {
		t.Fatal("next request inherited old binding", w.Code, w.Body.String())
	}
}
func TestFileBoundActor(t *testing.T) {
	for _, field := range []string{"principal", "role"} {
		t.Run(field, func(t *testing.T) { exerciseFileBoundActor(t, newFakeRepo(), field) })
	}
}
