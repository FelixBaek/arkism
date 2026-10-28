package host

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var remoteURL = regexp.MustCompile(`(?m)^\s*url\s*=\s*(\S+)\s*$`)

// RemoteKey reads the origin url of a git config as lower-case owner/name, or "".
func RemoteKey(config string) string {
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

// ProjectKey (ST-824) is the project a session in cwd recalls project-reach memories for:
// ARKISM_REPO when set, else the repository's origin as owner/name, else "path:" and a hash of
// the repository root (the raw path never leaves the host). A linked worktree resolves to its
// main repository, so every worktree shares one key. "" outside a repository.
func ProjectKey(cwd string) string {
	if v, ok := os.LookupEnv("ARKISM_REPO"); ok {
		return strings.TrimSpace(v)
	}
	if cwd == "" {
		return ""
	}
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return ""
	}
	for {
		dotGit := filepath.Join(dir, ".git")
		if info, err := os.Stat(dotGit); err == nil {
			common := dotGit
			if !info.IsDir() {
				common = worktreeCommonDir(dotGit)
			}
			if common == "" {
				return ""
			}
			// An empty .git directory is not repository metadata. Check HEAD in the
			// common directory so linked worktrees keep the main repository's key.
			if head, err := os.Stat(filepath.Join(common, "HEAD")); err == nil && head.Mode().IsRegular() {
				if key := RemoteKey(filepath.Join(common, "config")); key != "" {
					return key
				}
				sum := sha256.Sum256([]byte(filepath.Dir(common)))
				return "path:" + hex.EncodeToString(sum[:8])
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// worktreeCommonDir follows a worktree's ".git" file (gitdir: …) to the main repository's .git.
func worktreeCommonDir(dotGitFile string) string {
	raw, err := os.ReadFile(dotGitFile)
	if err != nil {
		return ""
	}
	gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok {
		return ""
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(filepath.Dir(dotGitFile), gitdir)
	}
	common := gitdir
	if c, err := os.ReadFile(filepath.Join(gitdir, "commondir")); err == nil {
		common = strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitdir, common)
		}
	}
	return filepath.Clean(common)
}
