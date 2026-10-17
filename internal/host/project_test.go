package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ST-824: one key per repository, shared by its worktrees, raw path never in the key.
func TestProjectKey(t *testing.T) {
	root := t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(root, "arkism")
	write(filepath.Join(repo, ".git", "config"), "[remote \"origin\"]\n\turl = git@github.com:FelixBaek/Arkism.git\n")
	write(filepath.Join(repo, ".git", "worktrees", "wt", "commondir"), "../..\n")
	wt := filepath.Join(root, "arkism-wt")
	write(filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(repo, ".git", "worktrees", "wt")+"\n")
	plain := filepath.Join(root, "Plain")
	write(filepath.Join(plain, ".git", "config"), "[core]\n")
	if err := os.MkdirAll(filepath.Join(root, "outside"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKISM_REPO", "")
	os.Unsetenv("ARKISM_REPO")
	for dir, want := range map[string]string{repo: "felixbaek/arkism", filepath.Join(wt, "sub"): "felixbaek/arkism", filepath.Join(root, "outside"): ""} {
		if dir == filepath.Join(wt, "sub") {
			_ = os.MkdirAll(dir, 0o755)
		}
		if got := ProjectKey(dir); got != want {
			t.Fatalf("ProjectKey(%s)=%q want %q", dir, got, want)
		}
	}
	key := ProjectKey(filepath.Join(plain, "x"))
	if !strings.HasPrefix(key, "path:") || strings.Contains(key, "Plain") || key != ProjectKey(plain) {
		t.Fatal("no-origin key", key)
	}
	t.Setenv("ARKISM_REPO", "override/repo")
	if got := ProjectKey(repo); got != "override/repo" {
		t.Fatal("ARKISM_REPO must win", got)
	}
}
