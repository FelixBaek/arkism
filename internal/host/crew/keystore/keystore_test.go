package keystore

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func resetProbe(t *testing.T, plat func() bool) {
	t.Helper()
	mu.Lock()
	prevPlat, prevCached := platform, cached
	platform, cached = plat, nil
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		platform, cached = prevPlat, prevCached
		mu.Unlock()
	})
}

func TestRefRoundTripsThroughParseRef(t *testing.T) {
	service, id, ok := ParseRef(Ref("k1"))
	if !ok || service != Service || id != "k1" {
		t.Fatalf("ParseRef(Ref) = %q %q %v", service, id, ok)
	}
	for _, bad := range []string{"", "plain", "keyring:", "keyring:svc", "keyring:/id", "keyring:svc/"} {
		if _, _, ok := ParseRef(bad); ok {
			t.Errorf("ParseRef(%q) accepted", bad)
		}
	}
}

func TestPutGetDeleteWithMockStore(t *testing.T) {
	resetProbe(t, nil)
	UseMock(nil)
	t.Setenv("ARKISM_KEYRING", "")
	if !Available() {
		t.Fatal("mock store not available")
	}
	ref, err := Put("k1", "secret")
	if err != nil || ref != Ref("k1") {
		t.Fatalf("Put = %q, %v", ref, err)
	}
	if got, err := Get(ref); err != nil || got != "secret" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := Delete(ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := Get(ref); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete err = %v", err)
	}
	if err := Delete(ref); err != nil {
		t.Fatalf("second Delete should be a no-op, got %v", err)
	}
	if _, err := Get("not-a-ref"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(bad ref) err = %v", err)
	}
	if err := Delete("not-a-ref"); err != nil {
		t.Fatalf("Delete(bad ref) err = %v", err)
	}
}

func TestResolveFillsKeysAndStorage(t *testing.T) {
	resetProbe(t, nil)
	UseMock(nil)
	t.Setenv("ARKISM_KEYRING", "")
	ref, err := Put("good", "s3cret")
	if err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{
		{"key": "plain"},
		{"secret_ref": ref},
		{"secret_ref": Ref("missing"), "key": "fallback"},
		{"secret_ref": Ref("missing")},
	}
	Resolve(rows)
	if rows[0]["storage"] != StorageFile {
		t.Errorf("plain row storage = %v", rows[0]["storage"])
	}
	if rows[1]["key"] != "s3cret" || rows[1]["storage"] != StorageKeyring {
		t.Errorf("ref row = %v", rows[1])
	}
	if _, has := rows[2]["secret_ref"]; has || rows[2]["storage"] != StorageFile || rows[2]["key"] != "fallback" {
		t.Errorf("unresolved row with key = %v", rows[2])
	}
	if rows[3]["storage"] != StorageKeyring || rows[3]["secret_ref"] != Ref("missing") {
		t.Errorf("unresolved row without key = %v", rows[3])
	}
}

func TestFailingStoreIsUnavailable(t *testing.T) {
	resetProbe(t, nil)
	UseMock(errors.New("boom"))
	t.Setenv("ARKISM_KEYRING", "")
	if Available() {
		t.Fatal("failing store reported available")
	}
	if _, err := Put("k", "v"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Put err = %v", err)
	}
	if _, err := Get(Ref("k")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Get err = %v", err)
	}
	if err := Delete(Ref("k")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Delete err = %v", err)
	}
}

func TestEnvOffAndMissingPlatformDisableStore(t *testing.T) {
	UseMock(nil)
	for _, v := range []string{"off", "0", "FALSE", " file "} {
		resetProbe(t, func() bool { return true })
		t.Setenv("ARKISM_KEYRING", v)
		if Available() {
			t.Errorf("ARKISM_KEYRING=%q left the store on", v)
		}
	}
	resetProbe(t, func() bool { return false })
	t.Setenv("ARKISM_KEYRING", "")
	if Available() {
		t.Error("store on without a platform store")
	}
	// The probe result is cached for the process.
	mu.Lock()
	platform = func() bool { return true }
	mu.Unlock()
	if Available() {
		t.Error("cached probe result was not kept")
	}
}

func TestSessionStorePresent(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if !sessionStorePresent() {
			t.Fatal("windows/darwin always have a store")
		}
		return
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/x")
	if !sessionStorePresent() {
		t.Error("session bus address ignored")
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	if sessionStorePresent() {
		t.Error("store reported without a bus socket")
	}
	if err := os.WriteFile(filepath.Join(dir, "bus"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !sessionStorePresent() {
		t.Error("bus socket in XDG_RUNTIME_DIR ignored")
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	if sessionStorePresent() {
		t.Error("store reported with no bus at all")
	}
}

func TestCallTimesOut(t *testing.T) {
	prev := callTimeout
	callTimeout = 10 * time.Millisecond
	t.Cleanup(func() { callTimeout = prev })
	block := make(chan struct{})
	defer close(block)
	if _, err := call(func() (string, error) { <-block; return "", nil }); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("call err = %v", err)
	}
}
