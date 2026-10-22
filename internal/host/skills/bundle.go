package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"
)

// Skill bundles: bounded text companions for the versioned import contract.

const (
	maxEntries    = 2048
	maxFileBytes  = 200_000
	maxTotalBytes = 1 << 20
)

// omittedReasons: "secret" is set by the server, never by capture.
var omittedReasons = []string{"binary", "too_large", "limit", "secret"}

// sharedDirs: a plugin skill also ships the plugin root's shared folders, never its sibling skills.
var sharedDirs = []string{"agents", "hooks", "references", "scripts"}

func digest(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func excluded(name string) bool {
	name = strings.ToLower(name)
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "__pycache__" ||
		strings.HasSuffix(name, ".key") || strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".token")
}

func validPath(v any) bool {
	s, ok := v.(string)
	if !ok || s == "" || len(s) > 512 || purePosix(s) != s || strings.HasPrefix(s, "/") || strings.ContainsAny(s, `\:`) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	for _, p := range strings.Split(s, "/") {
		if excluded(p) {
			return false
		}
	}
	return true
}

func bundleHash(files []any) string {
	var b strings.Builder
	for _, raw := range files {
		f := raw.(map[string]any)
		b.WriteString(f["path"].(string) + "\n" + f["sha256"].(string) + "\n")
	}
	return digest([]byte(b.String()))
}

func isHex64(v any) bool {
	s, ok := v.(string)
	return ok && hex64.MatchString(s)
}

func keysAre(m map[string]any, keys ...string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}

// validateBundle checks a bundle's shape, limits and hashes and returns it unchanged.
func validateBundle(v any) (map[string]any, error) {
	b, ok := v.(map[string]any)
	files, isList := b["files"].([]any)
	if !ok || !keysAre(b, "sha256", "files") || !isList || len(files) > maxEntries {
		return nil, errors.New("invalid_skill_bundle")
	}
	previous, total := "", 0
	for _, raw := range files {
		f, ok := raw.(map[string]any)
		if !ok || !validPath(f["path"]) || f["path"].(string) <= previous {
			return nil, errors.New("invalid_skill_bundle_file")
		}
		previous = f["path"].(string)
		var err error
		if total, err = validateBundleFile(f, total); err != nil {
			return nil, err
		}
	}
	if _, isStr := b["sha256"].(string); !isStr || bundleHash(files) != b["sha256"] {
		return nil, errors.New("skill_bundle_hash_mismatch")
	}
	return b, nil
}

// validateBundleFile checks one file entry; total is the content bytes so far, returned with this file's.
func validateBundleFile(f map[string]any, total int) (int, error) {
	if keysAre(f, "path", "sha256", "size", "omitted") {
		// Pinned by hash only; the bytes are never stored or served.
		size, isInt := pyInt(f["size"])
		reason, _ := f["omitted"].(string)
		if !containsStr(omittedReasons, reason) || !isInt || size <= 0 || !isHex64(f["sha256"]) {
			return total, errors.New("invalid_skill_bundle_file")
		}
		return total, nil
	}
	content, isStr := f["content"].(string)
	if !keysAre(f, "path", "sha256", "content") || !isStr {
		return total, errors.New("invalid_skill_bundle_file")
	}
	total += len(content)
	if strings.Contains(content, "\x00") || len(content) > maxFileBytes || total > maxTotalBytes || digest([]byte(content)) != f["sha256"] {
		return total, errors.New("invalid_skill_bundle_content")
	}
	return total, nil
}

// captureBundleRoot is the nearest plugin root (a folder with .claude-plugin) from skillDir up
// to boundary, otherwise skillDir.
func captureBundleRoot(skillDir, boundary string) string {
	for folder := skillDir; ; folder = filepath.Dir(folder) {
		if folder != boundary && !isUnder(folder, boundary) {
			break
		}
		if isDir(filepath.Join(folder, ".claude-plugin")) {
			return folder
		}
		if folder == filepath.Dir(folder) {
			break
		}
	}
	return skillDir
}

// captureBundleStarts is the folders to walk: the skill's, plus the plugin root's shared folders.
func captureBundleStarts(root, skillDir, skillRel string) []string {
	starts := []string{skillRel}
	if root != skillDir {
		for _, d := range sharedDirs {
			if !strings.HasPrefix(skillRel, d+"/") && skillRel != d {
				starts = append(starts, d)
			}
		}
	}
	return starts
}

func bundleFiles(b map[string]any) []map[string]any {
	list, _ := b["files"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, raw := range list {
		if f, ok := raw.(map[string]any); ok {
			out = append(out, f)
		}
	}
	return out
}

func fingerprint(contentHash string, bundle map[string]any) map[string]any {
	return map[string]any{"content_hash": contentHash, "bundle_sha256": bundle["sha256"]}
}

func bundleMatches(item map[string]any, contentHash string, bundle map[string]any) bool {
	b, _ := item["bundle"].(map[string]any)
	return !truthy(item["error"]) && item["content_hash"] == contentHash && b != nil && reflect.DeepEqual(b["sha256"], bundle["sha256"])
}

// captureEntry is one walked companion: raw is nil when it was over the per-file cap.
type captureEntry struct {
	sha  string
	size int
	raw  []byte
}

// buildBundle turns walked companions into a validated bundle (sorted paths, limits, hash-only reasons).
func buildBundle(entries map[string]captureEntry) (map[string]any, error) {
	if len(entries) > maxEntries {
		return nil, errors.New("skill_bundle_limit")
	}
	files, total := []any{}, 0
	for _, rel := range sortedKeys(entries) {
		e := entries[rel]
		reason := ""
		if e.raw == nil {
			reason = "too_large"
		} else if !utf8Valid(e.raw) || strings.Contains(string(e.raw), "\x00") {
			reason = "binary"
		}
		if reason == "" && total+e.size > maxTotalBytes {
			reason = "limit"
		}
		if reason != "" {
			files = append(files, map[string]any{"path": rel, "sha256": e.sha, "size": e.size, "omitted": reason})
		} else {
			total += e.size
			files = append(files, map[string]any{"path": rel, "sha256": e.sha, "content": string(e.raw)})
		}
	}
	return validateBundle(map[string]any{"sha256": bundleHash(files), "files": files})
}
