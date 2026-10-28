package capture

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepoKeyFindsTheRepositoryFromAnySubfolder(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "Work", "Arkism")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(repo, "desktop", "ui"), 0o755)
	if got := repoKey(filepath.Join(repo, "desktop", "ui")); got != "arkism" {
		t.Fatal("folder name without a remote:", got)
	}
	os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[core]\n\tbare = false\n[remote \"origin\"]\n\turl = git@github.com:FelixBaek/arkism.git\n\tfetch = +refs/heads/*\n[branch \"main\"]\n\tremote = origin\n"), 0o644)
	if got := repoKey(filepath.Join(repo, "desktop")); got != "felixbaek/arkism" {
		t.Fatal("ssh remote:", got)
	}
	os.WriteFile(filepath.Join(repo, ".git", "config"), []byte("[remote \"origin\"]\n\turl = https://github.com/FelixBaek/Arkism/\n"), 0o644)
	if got := repoKey(repo); got != "felixbaek/arkism" {
		t.Fatal("https remote:", got)
	}
	// A worktree or submodule keeps a .git file: the folder name is the key.
	wt := filepath.Join(root, "arkism-feature")
	os.MkdirAll(wt, 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: ../Work/Arkism/.git/worktrees/x\n"), 0o644)
	if got := repoKey(wt); got != "arkism-feature" {
		t.Fatal("worktree:", got)
	}
	if got := repoKey(filepath.Join(root, "Work")); got != "" {
		t.Fatal("outside a repository:", got)
	}
}

func TestRepoKeyIgnoresIncompleteGitDirectory(t *testing.T) {
	for _, state := range []string{"empty", "config-only", "head-directory"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			dotGit := filepath.Join(root, ".git")
			if err := os.Mkdir(dotGit, 0o755); err != nil {
				t.Fatal(err)
			}
			if state == "config-only" {
				if err := os.WriteFile(filepath.Join(dotGit, "config"), []byte("[remote \"origin\"]\n\turl = git@github.com:wrong/repo.git\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if state == "head-directory" {
				if err := os.Mkdir(filepath.Join(dotGit, "HEAD"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if got := repoKey(root); got != "" {
				t.Fatalf("incomplete .git directory produced repository key %q", got)
			}
		})
	}
}

func TestForRepoKeepsSharedAndCurrentRepositoryMemories(t *testing.T) {
	items := []map[string]any{
		{"id": "shared", "tags": []any{"session:s1"}},
		{"id": "here", "tags": []any{"repo:acme/arkism"}},
		{"id": "elsewhere", "tags": []any{"repo:acme/inkism"}},
		{"id": "untagged"},
	}
	var ids []string
	for _, m := range forRepo(items, "acme/arkism") {
		ids = append(ids, m["id"].(string))
	}
	if len(ids) != 3 || ids[0] != "shared" || ids[1] != "here" || ids[2] != "untagged" {
		t.Fatal(ids)
	}
	// Outside any repository only memories without a repo tag remain.
	if got := forRepo(items, ""); len(got) != 2 {
		t.Fatal(got)
	}
}
