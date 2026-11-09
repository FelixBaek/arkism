package memory

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testSharedKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestSharedKeySignature pins the canonical string the protocol signs: the
// twelve standard header lines, the x-ms- headers, then account, path and
// sorted query parameters.
func TestSharedKeySignature(t *testing.T) {
	s, err := NewSharedKeyBlobStore("https://storage.example/acct", "acct", "blobs", "p/", testSharedKey)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, s.blobURL("", map[string][]string{"comp": {"list"}, "restype": {"container"}, "prefix": {"p/"}}).String(), nil)
	req.Header.Set("X-Ms-Date", "Wed, 05 Nov 2026 10:00:00 GMT")
	req.Header.Set("X-Ms-Version", sharedKeyVersion)
	want := "GET\n\n\n\n\n\n\n\n\n\n\n\nx-ms-date:Wed, 05 Nov 2026 10:00:00 GMT\nx-ms-version:2021-08-06\n/acct/acct/blobs\ncomp:list\nprefix:p/\nrestype:container"
	m := hmac.New(sha256.New, mustB64(t, testSharedKey))
	m.Write([]byte(want))
	if got := signSharedKey(req, "acct", mustB64(t, testSharedKey)); got != base64.StdEncoding.EncodeToString(m.Sum(nil)) {
		t.Fatalf("signature %s", got)
	}
}

// fakeSharedKeyHandler serves an in-memory container that requires SharedKey auth.
func fakeSharedKeyHandler(objects map[string][]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "SharedKey acct:") || r.Header.Get("X-Ms-Version") != sharedKeyVersion {
			w.WriteHeader(403)
			return
		}
		switch {
		case r.Method == http.MethodPut && r.Header.Get("X-Ms-Blob-Type") == "BlockBlob":
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
			w.WriteHeader(201)
		case r.Method == http.MethodGet && r.URL.Query().Get("comp") == "list":
			var b strings.Builder
			b.WriteString("\xef\xbb\xbf<EnumerationResults><Blobs>")
			for k, v := range objects {
				fmt.Fprintf(&b, "<Blob><Name>%s</Name><Properties><Content-Length>%d</Content-Length></Properties></Blob>", strings.TrimPrefix(k, "/acct/blobs/"), len(v))
			}
			b.WriteString("</Blobs><NextMarker/></EnumerationResults>")
			_, _ = io.WriteString(w, b.String())
		case r.Method == http.MethodGet:
			if v, ok := objects[r.URL.Path]; ok {
				_, _ = w.Write(v)
				return
			}
			w.WriteHeader(404)
		case r.Method == http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(202)
		}
	}
}

func TestSharedKeyStoreRoundTrip(t *testing.T) {
	objects := map[string][]byte{}
	srv := httptest.NewServer(fakeSharedKeyHandler(objects))
	defer srv.Close()
	s, err := NewSharedKeyBlobStore(srv.URL+"/acct", "acct", "blobs", "p/", testSharedKey)
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.Unix(0, 0) }
	digest, ctx := ContentHash("x"), context.Background()
	if err = s.Put(ctx, digest, strings.NewReader("x"), 1); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, digest, 10); err != nil || string(got) != "x" {
		t.Fatal(got, err)
	}
	seen := map[string]int64{}
	if err = s.List(ctx, func(d string, n int64) error { seen[d] = n; return nil }); err != nil || seen[digest] != 1 {
		t.Fatal(seen, err)
	}
	if err = s.Delete(ctx, digest); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(ctx, digest, 10); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestBlobObjectStoreSettings(t *testing.T) {
	for _, c := range [][]string{
		{"https://storage.example", "acct", "blobs", "", "not base64!"},
		{"https://storage.example", "acct", "Blobs", "", testSharedKey}, // container names are lowercase
		{"http://storage.example", "acct", "blobs", "", testSharedKey},
		{"https://storage.example", "acct", "blobs", "../", testSharedKey},
	} {
		if _, err := NewSharedKeyBlobStore(c[0], c[1], c[2], c[3], c[4]); err == nil {
			t.Fatal("accepted", c)
		}
	}
	t.Setenv("MEMORY_BLOB_SHAREDKEY_ENDPOINT", "https://storage.example")
	t.Setenv("MEMORY_BLOB_S3_ENDPOINT", "https://s3.example")
	if _, err := BlobObjectStoreFromEnv(); !errors.Is(err, ErrInvalid) {
		t.Fatal("both storages", err)
	}
	t.Setenv("MEMORY_BLOB_SHAREDKEY_ENDPOINT", "")
	for k, v := range map[string]string{"BUCKET": "my-bucket", "REGION": "auto", "ACCESS_KEY_ID": "a", "SECRET_ACCESS_KEY": "s"} {
		t.Setenv("MEMORY_BLOB_S3_"+k, v)
	}
	t.Setenv("MEMORY_BLOB_S3_ADDRESSING", "virtual")
	store, err := BlobObjectStoreFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if u := store.(*S3BlobStore).objectURL("k", nil); u.String() != "https://my-bucket.s3.example/k" {
		t.Fatal(u)
	}
	t.Setenv("MEMORY_BLOB_S3_BUCKET", "my.bucket")
	if _, err = BlobObjectStoreFromEnv(); !errors.Is(err, ErrInvalid) {
		t.Fatal("dotted bucket as host label", err)
	}
	t.Setenv("MEMORY_BLOB_S3_ADDRESSING", "other")
	if _, err = BlobObjectStoreFromEnv(); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown addressing", err)
	}
}
