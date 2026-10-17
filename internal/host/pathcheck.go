package host

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	// shortcut: only paths with a file extension count (dir names and extensionless files are skipped), widen if missed paths show up.
	// a lowercase extension (x.go) or a listed all-caps one (Guide.PDF) keeps Go qualified names (path/filepath.Join, net/url.URL) out; ~/ alone may lead a file.
	memoryPathPattern = regexp.MustCompile(`(?:~/(?:[\w.-]+/)*|(?:\.{1,2}/|/)?(?:[\w.-]+/)+)[\w.-]*\.(?:[a-z][A-Za-z0-9]{0,4}|PDF|PNG|JPE?G|GIF|HEIC|TXT|MD|CSV|JSON|ZIP|DOCX?|XLSX?|PPTX?|MP[34]|MOV)\b`)
	urlPattern        = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S+`)
)

const maxCheckedPaths = 8

// MissingPaths (ST-818) is the file paths named in texts (a memory body, its source locators) that
// do not exist from cwd: absolute and ~/ paths as they are, others under the repository root or cwd.
// It only marks; callers never hide or reorder by it, and the paths never go to a log.
func MissingPaths(cwd string, texts ...string) []string {
	root := repoRoot(cwd)
	home, _ := os.UserHomeDir()
	var missing, seen []string
	for _, text := range texts {
		for _, p := range memoryPathPattern.FindAllString(urlPattern.ReplaceAllString(text, " "), -1) {
			if len(seen) == maxCheckedPaths {
				return missing
			}
			if slices.Contains(seen, p) {
				continue
			}
			seen = append(seen, p)
			var tries []string
			switch {
			case strings.HasPrefix(p, "~/"):
				if home != "" {
					tries = []string{filepath.Join(home, p[2:])}
				}
			case filepath.IsAbs(p):
				tries = []string{p}
			case strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../"):
				tries = []string{filepath.Join(cwd, p)}
			default:
				tries = []string{filepath.Join(root, p), filepath.Join(cwd, p)}
			}
			if !anyExists(tries) {
				missing = append(missing, p)
			}
		}
	}
	return missing
}

// repoRoot is the nearest directory at or above cwd holding .git (a worktree's own root), else cwd.
func repoRoot(cwd string) string {
	for dir := cwd; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return cwd
		}
		dir = parent
	}
}

func anyExists(paths []string) bool {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}
