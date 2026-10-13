package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportSkipsSymlinksAndHashesContent(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "one", "SKILL.md"), "test")
	if err := os.Symlink(filepath.Join(root, "one"), filepath.Join(root, "alias")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	rows, err := catalogSkills(root)
	if err != nil || len(rows) != 1 || rows[0].skillID != "one" || len(rows[0].contentHash) != 64 {
		t.Fatal(rows, err)
	}
}

func TestImportPreservesCRLFBytes(t *testing.T) {
	root := t.TempDir()
	raw := "# Skill\r\n\r\nBody\r\n"
	write(t, filepath.Join(root, "one", "SKILL.md"), raw)
	rows, err := catalogSkills(root)
	if err != nil || len(rows) != 1 || rows[0].content != raw || rows[0].contentHash != digest([]byte(raw)) {
		t.Fatal(rows, err)
	}
}

func TestImportDryRunNeedsExactCatalog(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a", "SKILL.md"), "a")
	write(t, filepath.Join(root, ".hidden", "SKILL.md"), "hidden")
	cat := filepath.Join(t.TempDir(), "c.jsonl")
	write(t, cat, `{"skill_id": "a", "content_hash": "`+digest([]byte("a"))+`"}`+"\n")
	var out, errOut strings.Builder
	if code := importMain([]string{"--root", root, "--catalog", cat}, nil, &out, &errOut); code != 0 || out.String() != `{"skill_id": "a", "content_hash": "`+digest([]byte("a"))+`", "dry_run": true}`+"\n" {
		t.Fatal(code, out.String(), errOut.String())
	}
	write(t, filepath.Join(root, "b", "SKILL.md"), "b")
	out.Reset()
	if code := importMain([]string{"--root", root, "--catalog", cat}, nil, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "does not exactly match") {
		t.Fatal(code, errOut.String())
	}
	if code := importMain([]string{"--root", root, "--catalog", cat, "--execute"}, nil, &out, &errOut); code != 2 {
		t.Fatal(code)
	}
}
