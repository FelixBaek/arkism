package host

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// shortcut: only paths with a file extension count (dir names and extensionless files are skipped), widen if missed paths show up.
	// a lowercase extension (x.go) or a listed all-caps one (Guide.PDF) keeps Go qualified names (path/filepath.Join, net/url.URL) out; ~/ or / alone may lead a file; a bare name (README.md, Next.js) never counts.
	memoryPathPattern = regexp.MustCompile(`(?:~/(?:[\w.-]+/)*|/(?:[\w.-]+/)*|(?:\.{1,2}/)?(?:[\w.-]+/)+)[\w.-]*\.(?:[a-z][A-Za-z0-9]{0,4}|PDF|PNG|JPE?G|GIF|HEIC|TXT|MD|CSV|JSON|ZIP|DOCX?|XLSX?|PPTX?|MP[34]|MOV)\b`)
	// protocol-relative //host/... too, unless the // sits inside a path.
	urlPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://\S+|(?:^|[^\w.~/-])//\S+`)
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
		clean := urlPattern.ReplaceAllString(text, " ")
		for _, at := range memoryPathPattern.FindAllStringIndex(clean, -1) {
			p := clean[at[0]:at[1]]
			// \w is ASCII only: in 설정/config.yaml the match starts at "/" but is not absolute.
			if before, _ := utf8.DecodeLastRuneInString(clean[:at[0]]); p[0] == '/' && (unicode.IsLetter(before) || unicode.IsDigit(before)) {
				continue
			}
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
