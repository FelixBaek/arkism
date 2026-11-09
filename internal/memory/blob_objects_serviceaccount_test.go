package memory

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func serviceAccountKeyFile(t *testing.T, tokenURL string) ([]byte, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	raw, _ := json.Marshal(map[string]string{"type": "service_account", "client_email": "blobs@project.example", "token_uri": tokenURL,
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))})
	return raw, key
}

// fakeServiceAccountStorage serves the token endpoint (checking the signed
// assertion against key) and an in-memory bucket behind the bearer token.
type fakeServiceAccountStorage struct {
	objects map[string][]byte
	tokens  atomic.Int32
	key     *rsa.PrivateKey
}

func (f *fakeServiceAccountStorage) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	parts := strings.Split(r.Form.Get("assertion"), ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if rsa.VerifyPKCS1v15(&f.key.PublicKey, crypto.SHA256, sum[:], sig) != nil || !strings.Contains(string(claims), `"scope":"storage.rw"`) {
		w.WriteHeader(400)
		return
	}
	f.tokens.Add(1)
	_, _ = io.WriteString(w, `{"access_token":"tok","expires_in":3600}`)
}

func (f *fakeServiceAccountStorage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/token" {
		f.token(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(401)
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/upload/storage/v1/b/blobs/o" && r.URL.Query().Get("uploadType") == "media":
		f.objects[r.URL.Query().Get("name")], _ = io.ReadAll(r.Body)
	case r.Method == http.MethodGet && r.URL.Path == "/storage/v1/b/blobs/o":
		items := []string{}
		for k, v := range f.objects {
			items = append(items, fmt.Sprintf(`{"name":%q,"size":"%d"}`, k, len(v)))
		}
		_, _ = io.WriteString(w, `{"items":[`+strings.Join(items, ",")+`]}`)
	case r.Method == http.MethodGet && r.URL.Query().Get("alt") == "media":
		if v, ok := f.objects[strings.TrimPrefix(r.URL.Path, "/storage/v1/b/blobs/o/")]; ok {
			_, _ = w.Write(v)
			return
		}
		w.WriteHeader(404)
	case r.Method == http.MethodDelete:
		delete(f.objects, strings.TrimPrefix(r.URL.Path, "/storage/v1/b/blobs/o/"))
		w.WriteHeader(204)
	default:
		w.WriteHeader(400)
	}
}

// The store trades a signed assertion for a token once, then round-trips an
// object through upload, read, list and delete.
func TestServiceAccountStoreRoundTrip(t *testing.T) {
	fake := &fakeServiceAccountStorage{objects: map[string][]byte{}}
	tokens := &fake.tokens
	srv := httptest.NewServer(fake)
	defer srv.Close()
	keyFile, k := serviceAccountKeyFile(t, srv.URL+"/token")
	fake.key = k
	s, err := NewServiceAccountBlobStore(srv.URL, "blobs", "p/", "storage.rw", keyFile)
	if err != nil {
		t.Fatal(err)
	}
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
	if tokens.Load() != 1 {
		t.Fatalf("token fetched %d times", tokens.Load())
	}
	// A token the service stops accepting is dropped, so the next call trades a fresh one.
	s.mu.Lock()
	s.token = "revoked"
	s.mu.Unlock()
	if _, err = s.Get(ctx, digest, 10); err == nil {
		t.Fatal("revoked token accepted")
	}
	if _, err = s.Get(ctx, digest, 10); !errors.Is(err, ErrNotFound) || tokens.Load() != 2 {
		t.Fatal(err, tokens.Load())
	}
}

func TestServiceAccountStoreSettings(t *testing.T) {
	good, _ := serviceAccountKeyFile(t, "https://token.example/token")
	if _, err := NewServiceAccountBlobStore("https://storage.example", "blobs", "", "storage.rw", good); err != nil {
		t.Fatal(err)
	}
	insecure, _ := serviceAccountKeyFile(t, "http://token.example/token")
	for _, c := range []struct {
		endpoint, bucket, prefix, scope string
		key                             []byte
	}{
		{"https://storage.example", "Blobs", "", "storage.rw", good},
		{"https://storage.example", "blobs", "../", "storage.rw", good},
		{"https://storage.example", "blobs", "", "", good},
		{"http://storage.example", "blobs", "", "storage.rw", good},
		{"https://storage.example", "blobs", "", "storage.rw", insecure},
		{"https://storage.example", "blobs", "", "storage.rw", []byte(`{"type":"user"}`)},
	} {
		if _, err := NewServiceAccountBlobStore(c.endpoint, c.bucket, c.prefix, c.scope, c.key); err == nil {
			t.Fatal("accepted", c.endpoint, c.bucket, c.prefix, c.scope)
		}
	}
	path := filepath.Join(t.TempDir(), "key.json")
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"ENDPOINT": "https://storage.example", "BUCKET": "blobs", "SCOPE": "storage.rw", "KEY_FILE": path} {
		t.Setenv("MEMORY_BLOB_SERVICE_ACCOUNT_"+k, v)
	}
	if store, err := BlobObjectStoreFromEnv(); err != nil || store.(*ServiceAccountBlobStore).Bucket != "blobs" {
		t.Fatal(store, err)
	}
	t.Setenv("MEMORY_BLOB_S3_ENDPOINT", "https://s3.example")
	if _, err := BlobObjectStoreFromEnv(); !errors.Is(err, ErrInvalid) {
		t.Fatal("two storages", err)
	}
}
