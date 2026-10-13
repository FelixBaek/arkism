package memory

import (
	"encoding/hex"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostgresRecoveryIdentity(t *testing.T) {
	admin := isolatedPostgres(t)
	app := restrictedPostgres(t, admin)
	handler := RecoveryHandler(&Service{Repo: app})
	checkUnavailable := func() {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/recovery-identity", nil))
		if w.Code != 503 {
			t.Fatalf("invalid metadata accepted: %d", w.Code)
		}
		w = httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
		if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"status":"recovery"}` {
			t.Fatal("liveness changed")
		}
	}
	checkUnavailable()
	manifest := ContentHash("identity test restore")
	if err := admin.RequireRestoreReview(testCtx, manifest); err != nil {
		t.Fatal(err)
	}
	var seed []byte
	if err := admin.q().QueryRow(testCtx, `SELECT value FROM memory_storage_metadata WHERE key='storage_identity'`).Scan(&seed); err != nil {
		t.Fatal(err)
	}
	var got RestoredRecoveryIdentity
	server := httptest.NewServer(handler)
	defer server.Close()
	recoveryHTTP(t, server.URL, "", "GET", "/recovery-identity", nil, &got, 200)
	if got.RestoreManifest != manifest || got.RestoredIdentity != hex.EncodeToString(seed) {
		t.Fatal("wrong live identity")
	}
	for _, key := range []string{"storage_identity", "restore_review_pending"} {
		original := seed
		if key == "restore_review_pending" {
			original = []byte(manifest)
		}
		if _, err := admin.q().Exec(testCtx, `UPDATE memory_storage_metadata SET value=$1 WHERE key=$2`, []byte("broken"), key); err != nil {
			t.Fatal(err)
		}
		checkUnavailable()
		if _, err := admin.q().Exec(testCtx, `UPDATE memory_storage_metadata SET value=$1 WHERE key=$2`, original, key); err != nil {
			t.Fatal(err)
		}
	}
}
