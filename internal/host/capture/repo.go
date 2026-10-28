package capture

// Repository of a conversation: memories that can differ between repositories (decisions, work rules,
// lessons) are tagged repo:<name> when captured, and recall leaves out those of other repositories.
// A preference about the person ("보고는 짧게") is never tagged and applies everywhere.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// repoTagsOn: only when the receiving Arkism accepts the repo field (the app's built-in one sets it).
func repoTagsOn() bool { return os.Getenv("ARKISM_REPO_TAGS") == "1" }

// repoKey is the repository cwd belongs to: owner/name from its origin remote when there is one
// (so clones on other computers match), otherwise the repository folder's name. "" outside a repository.
func repoKey(cwd string) string {
	if cwd == "" {
		return ""
	}
	dir, _ := filepath.Abs(cwd)
	for {
		dotGit := filepath.Join(dir, ".git")
		if info, err := os.Stat(dotGit); err == nil {
			if info.IsDir() {
				if head, err := os.Stat(filepath.Join(dotGit, "HEAD")); err == nil && head.Mode().IsRegular() {
					if key := host.RemoteKey(filepath.Join(dotGit, "config")); key != "" {
						return key
					}
					return strings.ToLower(filepath.Base(dir))
				}
			} else {
				return strings.ToLower(filepath.Base(dir))
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// forRepo drops recalled memories tagged for another repository.
func forRepo(items []map[string]any, repo string) []map[string]any {
	out := items[:0:0]
	for _, item := range items {
		keep := true
		tags, _ := item["tags"].([]any)
		for _, t := range tags {
			if s, ok := t.(string); ok && strings.HasPrefix(s, "repo:") && s != "repo:"+repo {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, item)
		}
	}
	return out
}
