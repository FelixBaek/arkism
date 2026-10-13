package backup

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestArchiveRoundtripAndFailedOutputs(t *testing.T) {
	if _, err := exec.LookPath(openssl); err != nil {
		t.Skip("openssl not installed")
	}
	root := t.TempDir()
	cert, key := filepath.Join(root, "cert.pem"), filepath.Join(root, "key.pem")
	if err := exec.Command(openssl, "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-subj", "/CN=synthetic-backup",
		"-days", "1", "-keyout", key, "-out", cert).Run(); err != nil {
		t.Fatal(err)
	}
	encrypted, restored := filepath.Join(root, "backup.cms"), filepath.Join(root, "restored.archive")
	command := []string{"printf", `backup\000payload`}
	if err := archive("encrypt", encrypted, cert, command, "", ""); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(encrypted); info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
	if err := archive("decrypt", restored, cert, nil, encrypted, key); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(restored); !bytes.Equal(b, []byte("backup\x00payload")) {
		t.Fatalf("%q", b)
	}
	if info, _ := os.Stat(restored); info.Mode().Perm() != 0o600 {
		t.Fatal(info.Mode())
	}
	before, _ := os.ReadFile(encrypted)
	if err := archive("encrypt", encrypted, cert, command, "", ""); err == nil {
		t.Fatal("existing output overwritten")
	}
	if after, _ := os.ReadFile(encrypted); !bytes.Equal(before, after) {
		t.Fatal("existing output changed")
	}
	failed := filepath.Join(root, "failed.cms")
	if err := archive("encrypt", failed, cert, []string{"sh", "-c", `printf 'backup'; exit 7`}, "", ""); err == nil {
		t.Fatal("failed command published")
	}
	assertMissing(t, failed)
	// Truncation must never publish the plaintext emitted before auth completes.
	corrupt := filepath.Join(root, "corrupt.cms")
	os.WriteFile(corrupt, before[:len(before)-12], 0o600)
	failed = filepath.Join(root, "unauthenticated.archive")
	if err := archive("decrypt", failed, cert, nil, corrupt, key); err == nil {
		t.Fatal("truncated archive published")
	}
	assertMissing(t, failed)
	wrongKey := filepath.Join(root, "wrong-key.pem")
	if err := exec.Command(openssl, "genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", wrongKey).Run(); err != nil {
		t.Fatal(err)
	}
	if err := archive("decrypt", failed, cert, nil, encrypted, wrongKey); err == nil {
		t.Fatal("wrong key published")
	}
	assertMissing(t, failed)
	link := filepath.Join(root, "link")
	os.Symlink(filepath.Join(root, "missing"), link)
	if err := archive("encrypt", link, cert, command, "", ""); err == nil {
		t.Fatal("dangling link replaced")
	}
	if leftovers, _ := filepath.Glob(filepath.Join(root, ".backup-*")); len(leftovers) > 0 {
		t.Fatal(leftovers)
	}
	// CLI: "--" separates the backup command; an empty command publishes nothing.
	code, out, _ := run(t, []string{"backup-archive", "encrypt", "--cert", cert, "--output", filepath.Join(root, "cli.cms"), "--", "printf", "x"})
	if code != 0 || out != "backup archive completed\n" {
		t.Fatal(code, out)
	}
	if code, _, errOut := run(t, []string{"backup-archive", "encrypt", "--cert", cert, "--output", filepath.Join(root, "empty.cms"), "--"}); code != 1 ||
		errOut != "backup archive failed; no new completed output was published\n" {
		t.Fatal(code, errOut)
	}
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal(path, "published")
	}
}
