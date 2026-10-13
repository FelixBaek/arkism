//go:build !unix

package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Windows capture keeps the Unix rules: bounded, hash-checked, and no linked folders.
func TestWindowsBundleCaptureMatchesTheUnixRules(t *testing.T) {
	root := t.TempDir()
	skill := filepath.Join(root, "pack", "skills", "demo", "SKILL.md")
	script := filepath.Join(root, "pack", "scripts", "run.py")
	os.MkdirAll(filepath.Dir(skill), 0o755)
	os.MkdirAll(filepath.Join(root, "pack", ".claude-plugin"), 0o755)
	os.MkdirAll(filepath.Dir(script), 0o755)
	body := []byte("---\nname: demo\ndescription: demo\n---\nRead scripts/run.py.\n")
	os.WriteFile(skill, body, 0o644)
	os.WriteFile(script, []byte("print('한글')\r\n"), 0o644)
	os.WriteFile(filepath.Join(filepath.Dir(script), ".env"), []byte("password=synthetic-example"), 0o644)
	first, err := captureBundle(skill, root, digest(body))
	if err != nil {
		t.Fatal(err)
	}
	files := bundleFiles(first)
	if len(files) != 1 || files[0]["path"] != "scripts/run.py" || files[0]["content"] != "print('한글')\r\n" {
		t.Fatal(first)
	}
	sha, raw, err := readLocal(filepath.Join(root, "pack"), "scripts/run.py")
	if err != nil || string(raw) != "print('한글')\r\n" || sha != files[0]["sha256"] {
		t.Fatal(sha, raw, err)
	}
	os.WriteFile(skill, []byte("changed"), 0o644)
	if _, err := captureBundle(skill, root, digest(body)); err == nil || !strings.Contains(err.Error(), "source_changed") {
		t.Fatal(err)
	}
	os.WriteFile(skill, body, 0o644)
	// A linked folder inside the skill is refused (creating one may need Developer Mode).
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "pack", "scripts", "linked")); err != nil {
		t.Skip("cannot create a symlink here:", err)
	}
	if _, err := captureBundle(skill, root, digest(body)); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatal(err)
	}
}
