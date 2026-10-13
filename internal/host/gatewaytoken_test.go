package host

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestEnsureGatewayTokenMakesOnePrivateTokenAndKeepsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "gateway.token")
	first, err := EnsureGatewayToken(path)
	if err != nil || !ValidGatewayToken(first) {
		t.Fatal(first, err)
	}
	if info, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode(), err)
	}
	if again, err := EnsureGatewayToken(path); err != nil || again != first {
		t.Fatal("token changed", again, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".gateway-token-*"))
	if len(leftovers) != 0 {
		t.Fatal(leftovers)
	}
}

func TestEnsureGatewayTokenRacesToOneValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.token")
	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], _ = EnsureGatewayToken(path)
		}()
	}
	wg.Wait()
	for _, v := range got {
		if v != got[0] || !ValidGatewayToken(v) {
			t.Fatal(got)
		}
	}
}

func TestReadGatewayTokenRefusesUnsafeFiles(t *testing.T) {
	dir := t.TempDir()
	malformed := filepath.Join(dir, "bad.token")
	os.WriteFile(malformed, []byte("not-a-token\n"), 0o600)
	if _, err := EnsureGatewayToken(malformed); err == nil {
		t.Fatal("malformed token accepted (and must not be replaced)")
	}
	if raw, _ := os.ReadFile(malformed); string(raw) != "not-a-token\n" {
		t.Fatal("existing file overwritten")
	}
	if runtime.GOOS == "windows" {
		return
	}
	open := filepath.Join(dir, "open.token")
	os.WriteFile(open, []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n"), 0o644)
	if _, err := ReadGatewayToken(open); err == nil {
		t.Fatal("group-readable token accepted")
	}
	link := filepath.Join(dir, "link.token")
	os.Symlink(open, link)
	if _, err := ReadGatewayToken(link); err == nil {
		t.Fatal("symlink accepted")
	}
}
