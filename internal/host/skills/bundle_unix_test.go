//go:build unix

package skills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

type bundleFixture struct {
	root, skill, script, hash string
}

func newBundleFixture(t *testing.T) *bundleFixture {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	f := &bundleFixture{root: root, skill: filepath.Join(root, "pack/skills/demo/SKILL.md"), script: filepath.Join(root, "pack/scripts/run.py")}
	os.MkdirAll(filepath.Dir(f.skill), 0o755)
	os.MkdirAll(filepath.Join(root, "pack/.claude-plugin"), 0o755)
	os.MkdirAll(filepath.Dir(f.script), 0o755)
	body := []byte("---\nname: demo\ndescription: demo\n---\nRead scripts/run.py.\n")
	os.WriteFile(f.skill, body, 0o644)
	os.WriteFile(f.script, []byte("print('한글')\r\n"), 0o644)
	f.hash = digest(body)
	return f
}

func (f *bundleFixture) capture(t *testing.T) (map[string]any, error) {
	return captureBundle(f.skill, f.root, f.hash)
}

func TestPluginCaptureIsBoundedAndImmutable(t *testing.T) {
	f := newBundleFixture(t)
	write(t, filepath.Join(filepath.Dir(f.script), ".env"), "password=synthetic-example")
	first, err := f.capture(t)
	if err != nil {
		t.Fatal(err)
	}
	files := bundleFiles(first)
	if files[0]["path"] != "scripts/run.py" || files[0]["content"] != "print('한글')\r\n" || len(files) != 1 {
		t.Fatal(first)
	}
	write(t, f.script, "print('second')")
	second, _ := f.capture(t)
	if second["sha256"] == first["sha256"] {
		t.Fatal("hash did not change")
	}
	broken := map[string]any{"sha256": first["sha256"], "files": []any{map[string]any{"path": "scripts/run.py", "sha256": files[0]["sha256"], "content": "changed"}}}
	if _, err := validateBundle(broken); err == nil {
		t.Fatal("tampered content validated")
	}
	write(t, f.skill, "changed")
	if _, err := f.capture(t); err == nil || !strings.Contains(err.Error(), "source_changed") {
		t.Fatal(err)
	}
}

func TestPluginScopeAndHashOnlyCompanions(t *testing.T) {
	f := newBundleFixture(t)
	r := f.root
	write(t, filepath.Join(r, "pack/skills/other/SKILL.md"), "---\nname: other\n---\n")
	write(t, filepath.Join(r, "pack/README.md"), "plugin readme")
	write(t, filepath.Join(r, "pack/skills/demo/references/a.md"), "A")
	write(t, filepath.Join(r, "pack/agents/x.toml"), "x")
	write(t, filepath.Join(r, "pack/scripts/logo.png"), "\x89PNG\x00")
	write(t, filepath.Join(r, "pack/scripts/bad.txt"), "\xff")
	write(t, filepath.Join(r, "pack/scripts/big.js"), strings.Repeat("x", 200001))
	write(t, filepath.Join(r, "pack/scripts/empty.txt"), "")
	b, err := f.capture(t)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]map[string]any{}
	for _, file := range bundleFiles(b) {
		got[file["path"].(string)] = file
	}
	want := []string{"agents/x.toml", "scripts/bad.txt", "scripts/big.js", "scripts/empty.txt", "scripts/logo.png", "scripts/run.py", "skills/demo/references/a.md"}
	if !slices.Equal(sortedKeys(got), want) {
		t.Fatal(sortedKeys(got))
	}
	logo := got["scripts/logo.png"]
	if canon(t, logo) != canon(t, map[string]any{"path": "scripts/logo.png", "sha256": digest([]byte("\x89PNG\x00")), "size": 5, "omitted": "binary"}) {
		t.Fatal(logo)
	}
	if got["scripts/bad.txt"]["omitted"] != "binary" || got["scripts/big.js"]["omitted"] != "too_large" || got["scripts/big.js"]["size"] != 200001 || got["scripts/empty.txt"]["content"] != "" {
		t.Fatal(got)
	}
	before := b["sha256"]
	write(t, filepath.Join(r, "pack/scripts/logo.png"), "\x89PNG\x01")
	if again, _ := f.capture(t); again["sha256"] == before {
		t.Fatal("hash-only change not detected")
	}
	mixed := map[string]any{"path": "scripts/logo.png", "sha256": logo["sha256"], "size": 5, "omitted": "binary", "content": "x"}
	files := []any{mixed}
	if _, err := validateBundle(map[string]any{"sha256": bundleHash(files), "files": files}); err == nil {
		t.Fatal("content plus omitted validated")
	}
}

func TestTotalBudgetTurnsRemainingTextIntoHashOnly(t *testing.T) {
	f := newBundleFixture(t)
	for i := range 6 {
		write(t, filepath.Join(f.root, "pack/scripts/part"+string(rune('0'+i))+".txt"), strings.Repeat("x", 199_000))
	}
	b, err := f.capture(t)
	if err != nil {
		t.Fatal(err)
	}
	var stored, omitted []string
	total := 0
	for _, file := range bundleFiles(b) {
		if c, ok := file["content"].(string); ok {
			stored = append(stored, file["path"].(string))
			total += len(c)
		} else {
			omitted = append(omitted, file["omitted"].(string))
		}
	}
	if !slices.Equal(stored, []string{"scripts/part0.txt", "scripts/part1.txt", "scripts/part2.txt", "scripts/part3.txt", "scripts/part4.txt", "scripts/run.py"}) ||
		!slices.Equal(omitted, []string{"limit"}) || total > maxTotalBytes {
		t.Fatal(stored, omitted, total)
	}
}

func TestUnsafeOrIncompleteCaptureFails(t *testing.T) {
	f := newBundleFixture(t)
	os.Remove(f.script)
	os.Symlink("/etc/hostname", f.script)
	if _, err := f.capture(t); err == nil {
		t.Fatal("linked companion captured")
	}
	os.Remove(f.script)
	if err := syscall.Mkfifo(f.script, 0o644); err != nil {
		t.Skip(err)
	}
	if _, err := f.capture(t); err == nil || !strings.Contains(err.Error(), "not_regular") {
		t.Fatal(err)
	}
}

func TestInventoryBoundaryIsCheckedAfterNormalizing(t *testing.T) {
	f := newBundleFixture(t)
	if _, err := captureBundle(filepath.Join(f.root, "pack/../../outside/SKILL.md"), f.root, ""); err == nil || err.Error() != "skill_bundle_outside_root" {
		t.Fatal(err)
	}
}

func TestLinkedAncestorIsNotFollowed(t *testing.T) {
	f := newBundleFixture(t)
	alias := filepath.Join(f.root, "alias")
	os.Symlink(filepath.Join(f.root, "pack"), alias)
	if _, err := captureBundle(filepath.Join(alias, "skills/demo/SKILL.md"), f.root, f.hash); err == nil {
		t.Fatal("linked ancestor followed")
	}
}

func TestLinkedFolderIsRefused(t *testing.T) {
	f := newBundleFixture(t)
	os.Symlink(f.root, filepath.Join(filepath.Dir(f.script), "lib"))
	if _, err := f.capture(t); err == nil || err.Error() != "skill_bundle_symlink" {
		t.Fatal(err)
	}
}

func TestImportRechecksScanSnapshot(t *testing.T) {
	f := newBundleFixture(t)
	var row map[string]any
	for _, r := range discover([]skillRoot{{"agent-skills", f.root, "generic"}}) {
		if r["path"] == f.skill {
			row = r
		}
	}
	snap, err := f.capture(t)
	if err != nil {
		t.Fatal(err)
	}
	row["bundle"] = snap
	first, err := importPayload("generic", row, map[string]any{"name": "demo"}, map[string]any{}, "", "")
	if err != nil || canon(t, first["bundle"]) != canon(t, snap) {
		t.Fatal(err)
	}
	write(t, f.script, "changed")
	if _, err := importPayload("generic", row, map[string]any{"name": "demo"}, map[string]any{}, "", ""); hubCode(err) != "bundle_capture_failed" {
		t.Fatal(err)
	}
}
