package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeObjects is a minimal in-memory object storage server.
type fakeObjects struct {
	mu         sync.Mutex
	objects    map[string][]byte
	failDelete bool
}

func (f *fakeObjects) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch {
	case r.Method == http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != r.Header.Get("X-Amz-Content-Sha256") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.objects[r.URL.Path] = body
	case r.Method == http.MethodDelete && f.failDelete:
		w.WriteHeader(http.StatusServiceUnavailable)
	case r.Method == http.MethodDelete:
		delete(f.objects, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Query().Get("list-type") == "2":
		keys := []string{}
		for k := range f.objects {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprint(w, "<ListBucketResult>")
		for _, k := range keys {
			fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", strings.TrimPrefix(k, "/bucket/"), len(f.objects[k]))
		}
		fmt.Fprint(w, "<IsTruncated>false</IsTruncated></ListBucketResult>")
	default:
		body, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	}
}

func (f *fakeObjects) setFailDelete(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failDelete = v
}

func (f *fakeObjects) has(digest string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects["/bucket/blobs/"+digest]
	return ok
}

// TestObjectBlobStorage stores, reads, erases and collects blobs in object storage.
func TestObjectBlobStorage(t *testing.T) {
	fake := &fakeObjects{objects: map[string][]byte{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	objects, err := NewS3BlobStore(server.URL, "bucket", "us-east-1", "blobs/", "AK", "SK", "")
	if err != nil {
		t.Fatal(err)
	}
	f := blobGCFixture(t, newFakeRepo())
	f.svc.BlobObjects = objects
	token := erasureWorker(f)
	source := artifactSource(t, f.svc.Repo, scopeA)
	var art Record
	if err = f.svc.withBlobs(testCtx, func(b *lockedBlobs) error {
		art = attachTestArtifact(t, b, f.svc.Repo, source, "object stored file")
		got, e := b.readArtifactBytes(*art.Artifact)
		if e != nil || string(got) != "object stored file" {
			t.Fatal("read back", string(got), e)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	digest := art.Artifact.Digest
	if !fake.has(digest) {
		t.Fatal("object not stored")
	}
	if _, err = os.Stat(filepath.Join(f.svc.BlobDirectory, digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("bytes also kept locally", err)
	}
	if err = f.svc.Delete(testCtx, f.human, "sources", source.ID, ChangeInput{Scope: source.Scope, ExpectedRevision: source.Revision}); err != nil {
		t.Fatal(err)
	}
	job, err := f.svc.Repo.ClaimJob(testCtx, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.eraseSourceMemories(testCtx, token, job); err != nil {
		t.Fatal(err)
	}
	// A failed object delete is an error, not a silent success; the job is retried.
	fake.setFailDelete(true)
	job, err = f.svc.Repo.ClaimJob(testCtx, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.eraseSourceMemories(testCtx, token, job); err == nil || !fake.has(digest) {
		t.Fatal("failed object delete was not reported", err)
	}
	fake.setFailDelete(false)
	if err = f.svc.eraseSourceMemories(testCtx, token, job); err != nil {
		t.Fatal("retry", err)
	}
	if fake.has(digest) {
		t.Fatal("erasure kept the object")
	}
	if _, err = f.svc.Repo.Get(testCtx, colBlobs, blobScope(), digest); !errors.Is(err, ErrNotFound) {
		t.Fatal("blob record kept", err)
	}
	orphan := ContentHash("orphan bytes")
	fake.objects["/bucket/blobs/"+orphan] = []byte("orphan bytes")
	result, err := f.svc.CollectBlobGarbage(testCtx, f.work)
	if err != nil || result.CollectedBlobs != 1 || fake.has(orphan) {
		t.Fatalf("orphan GC %+v %v", result, err)
	}
	if err = os.WriteFile(filepath.Join(f.svc.BlobDirectory, orphan), []byte("orphan bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.CollectBlobGarbage(testCtx, f.work); !errors.Is(err, ErrUnavailable) {
		t.Fatal("local digest file ignored while object storage is on", err)
	}
	f.auth.actors[f.work] = actor("restorer", "service", false, "restore")
	if err = f.svc.AdoptRestoredBlobs(testCtx, f.work); !errors.Is(err, ErrUnavailable) {
		t.Fatal("local restore accepted with object storage")
	}
}
