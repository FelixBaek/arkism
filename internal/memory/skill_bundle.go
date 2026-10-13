package memory

import (
	"encoding/json"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SkillBundle contains text companions captured by the importer, never server paths.
// SHA256 commits to sorted path + LF + file SHA256 + LF entries (UTF-8).
// A binary, oversized, over-budget or secret-looking companion is pinned by hash only: Omitted names
// why, Size is its byte length and Content stays empty; the server never serves it.
type SkillBundle struct {
	SHA256 string            `json:"sha256" bson:"sha256"`
	Files  []SkillBundleFile `json:"files" bson:"files"`
}
type SkillBundleFile struct {
	Path    string `json:"path" bson:"path"`
	SHA256  string `json:"sha256" bson:"sha256"`
	Content string `json:"content" bson:"content"`
	Size    int64  `json:"size,omitempty" bson:"size,omitempty"`
	Omitted string `json:"omitted,omitempty" bson:"omitted,omitempty"`
}

const maxSkillBundleEntries = 2048

// A hash-only entry carries no content key at all, so clients can tell it from an empty file.
func (f SkillBundleFile) MarshalJSON() ([]byte, error) {
	if f.Omitted != "" {
		return json.Marshal(struct {
			Path    string `json:"path"`
			SHA256  string `json:"sha256"`
			Size    int64  `json:"size"`
			Omitted string `json:"omitted"`
		}{f.Path, f.SHA256, f.Size, f.Omitted})
	}
	return json.Marshal(struct {
		Path    string `json:"path"`
		SHA256  string `json:"sha256"`
		Content string `json:"content"`
	}{f.Path, f.SHA256, f.Content})
}

func skillBundleHash(files []SkillBundleFile) string {
	var manifest strings.Builder
	for _, f := range files {
		manifest.WriteString(f.Path + "\n" + f.SHA256 + "\n")
	}
	return ContentHash(manifest.String())
}

func validSkillBundle(b *SkillBundle) bool {
	if b == nil {
		return true
	}
	if len(b.Files) > maxSkillBundleEntries || b.SHA256 != skillBundleHash(b.Files) || b.Files == nil {
		return false
	}
	total, previous := 0, ""
	for _, f := range b.Files {
		if !utf8.ValidString(f.Path) || len(f.Path) > 512 || f.Path <= previous || path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || strings.ContainsAny(f.Path, "\\:") || strings.IndexFunc(f.Path, unicode.IsControl) >= 0 {
			return false
		}
		for _, part := range strings.Split(f.Path, "/") {
			p := strings.ToLower(part)
			if strings.HasPrefix(p, ".") || p == "node_modules" || p == "__pycache__" || strings.HasSuffix(p, ".key") || strings.HasSuffix(p, ".pem") || strings.HasSuffix(p, ".token") {
				return false
			}
		}
		if f.Omitted != "" {
			if (f.Omitted != "binary" && f.Omitted != "too_large" && f.Omitted != "limit" && f.Omitted != "secret") || f.Content != "" || f.Size <= 0 || !crewHashRe.MatchString(f.SHA256) {
				return false
			}
			previous = f.Path
			continue
		}
		if f.Size != 0 || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) || len(f.Content) > 200000 || f.SHA256 != ContentHash(f.Content) {
			return false
		}
		total += len(f.Content)
		if total > 1<<20 {
			return false
		}
		previous = f.Path
	}
	return true
}
