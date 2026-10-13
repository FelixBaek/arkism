package capture

// Repository of a conversation: memories that can differ between repositories (decisions, work rules,
// lessons) are tagged repo:<name> when captured, and recall leaves out those of other repositories.
// A preference about the person ("보고는 짧게") is never tagged and applies everywhere.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// repoTagsOn: only when the receiving Arkism accepts the repo field (the app's built-in one sets it).
func repoTagsOn() bool { return os.Getenv("ARKISM_REPO_TAGS") == "1" }

var remoteURL = regexp.MustCompile(`(?m)^\s*url\s*=\s*(\S+)\s*$`)

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
				if key := remoteKey(filepath.Join(dotGit, "config")); key != "" {
					return key
				}
			}
			return strings.ToLower(filepath.Base(dir))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// remoteKey reads the origin url of a git config as owner/name.
func remoteKey(config string) string {
	raw, err := os.ReadFile(config)
	if err != nil {
		return ""
	}
	text := string(raw)
	i := strings.Index(text, `[remote "origin"]`)
	if i < 0 {
		return ""
	}
	section := text[i:]
	if j := strings.Index(section[1:], "\n["); j >= 0 {
		section = section[:j+1]
	}
	m := remoteURL.FindStringSubmatch(section)
	if m == nil {
		return ""
	}
	url := strings.TrimSuffix(strings.TrimRight(m[1], "/"), ".git")
	url = strings.ReplaceAll(url, ":", "/") // git@github.com:owner/name
	parts := strings.Split(url, "/")
	if len(parts) < 2 {
		return ""
	}
	key := strings.ToLower(parts[len(parts)-2] + "/" + parts[len(parts)-1])
	if len(key) > 128 || strings.TrimSpace(key) != key {
		return ""
	}
	return key
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
