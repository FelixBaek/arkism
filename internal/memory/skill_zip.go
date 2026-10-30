package memory

import (
	"archive/zip"
	"bytes"
	"context"
	"regexp"
	"strings"
)

var skillZipNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// SkillZipExport is the zip answered as JSON (format=json), for clients that read text only.
type SkillZipExport struct {
	Filename      string `json:"filename"`
	ContentBase64 string `json:"content_base64"`
}

// skillZipNote names the companions a zip leaves out: the bundle keeps only their hash.
const skillZipNote = "OMITTED.txt"

// ExportSkillZip packs one skill version as name/SKILL.md plus every bundle companion that has
// content. Hash-only companions (secret-looking, binary, too large) are listed in OMITTED.txt.
func (s *Service) ExportSkillZip(ctx context.Context, token string, scope Scope, id string) ([]byte, string, error) {
	v, err := s.Get(ctx, token, colSkillVersions, scope, id)
	if err != nil {
		return nil, "", err
	}
	if v.Analysis == nil || v.ContentErased || len(v.Sources) == 0 {
		return nil, "", ErrNotFound
	}
	src, err := s.Get(ctx, token, "sources", scope, v.Sources[0].SourceID)
	if err != nil {
		return nil, "", err
	}
	if staleSkillSource(src, v) || mismatchedSkillBundle(src, v) {
		return nil, "", ErrConflict
	}
	name := v.SkillID
	if !skillZipNameRe.MatchString(name) {
		name = "skill"
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(rel, body string) error {
		w, e := zw.Create(name + "/" + rel)
		if e == nil {
			_, e = w.Write([]byte(body))
		}
		return e
	}
	if err = put("SKILL.md", src.Text); err != nil {
		return nil, "", err
	}
	var omitted []string
	if src.SkillBundle != nil {
		for _, f := range src.SkillBundle.Files {
			switch {
			case f.Path == "SKILL.md" || f.Path == skillZipNote:
			case f.Omitted != "":
				omitted = append(omitted, f.Path+" ("+f.Omitted+")")
			default:
				err = put(f.Path, f.Content)
			}
			if err != nil {
				return nil, "", err
			}
		}
	}
	if len(omitted) > 0 {
		note := "These files were not exported: Arkism keeps only their hash.\n\n" + strings.Join(omitted, "\n") + "\n"
		if err = put(skillZipNote, note); err != nil {
			return nil, "", err
		}
	}
	if err = zw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), name + ".zip", nil
}
