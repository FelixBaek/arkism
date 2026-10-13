package capture

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// Portable JSONL boundaries, verified against a delayed PC sync copy.

var (
	boundaryFields = []string{"version", "origin", "session_id", "cwd", "logical", "offset", "prefix_sha256"}
	boundaryHash   = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// boundaryFolder normalises an absolute checkout folder: Windows drive paths compare
// case-insensitively whatever OS reads them; other paths use this OS's rules.
func boundaryFolder(value any) (string, error) {
	s, ok := value.(string)
	if !ok || s == "" {
		return "", valueErr("work_pc_folder_invalid")
	}
	if ntIsAbs(s) && ntDrive(s) != "" {
		return strings.ToLower(ntNormpath(s)), nil
	}
	if runtime.GOOS == "windows" {
		if ntIsAbs(s) {
			return ntNormpath(s), nil
		}
	} else if strings.HasPrefix(s, "/") {
		return posixNormpath(s), nil
	}
	return "", valueErr("work_pc_folder_invalid")
}

// ntIsAbs is ntpath.isabs: a drive with a root, or a UNC/device prefix.
func ntIsAbs(s string) bool {
	h := strings.ReplaceAll(head(s, 3), "/", `\`)
	return strings.HasPrefix(h, `\\`) || (len([]rune(h)) >= 3 && strings.HasPrefix(string([]rune(h)[1:]), `:\`))
}

// ntSplitRoot is ntpath.splitroot.
func ntSplitRoot(p string) (drive, root, tail string) {
	n := strings.ReplaceAll(p, "/", `\`)
	switch {
	case strings.HasPrefix(n, `\\`):
		start := 2
		if strings.ToUpper(head(n, 8)) == `\\?\UNC\` {
			start = 8
		}
		i := strings.Index(n[start:], `\`)
		if i < 0 {
			return p, "", ""
		}
		i += start
		j := strings.Index(n[i+1:], `\`)
		if j < 0 {
			return p, "", ""
		}
		j += i + 1
		return p[:j], p[j : j+1], p[j+1:]
	case strings.HasPrefix(n, `\`):
		return "", p[:1], p[1:]
	case len(n) >= 2 && n[1] == ':':
		if len(n) >= 3 && n[2] == '\\' {
			return p[:2], p[2:3], p[3:]
		}
		return p[:2], "", p[2:]
	}
	return "", "", p
}

func ntDrive(p string) string { d, _, _ := ntSplitRoot(p); return d }

// ntNormpath is ntpath.normpath.
func ntNormpath(p string) string {
	p = strings.ReplaceAll(p, "/", `\`)
	drive, root, rest := ntSplitRoot(p)
	var comps []string
	for _, c := range strings.Split(rest, `\`) {
		switch {
		case c == "" || c == ".":
		case c == "..":
			if len(comps) > 0 && comps[len(comps)-1] != ".." {
				comps = comps[:len(comps)-1]
			} else if !(len(comps) == 0 && root != "") {
				comps = append(comps, c)
			}
		default:
			comps = append(comps, c)
		}
	}
	if drive+root == "" && len(comps) == 0 {
		comps = []string{"."}
	}
	return drive + root + strings.Join(comps, `\`)
}

// posixNormpath is posixpath.normpath for an absolute path (two leading slashes are kept).
func posixNormpath(p string) string {
	out := path.Clean(p)
	if strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///") {
		return "/" + out
	}
	return out
}

func boundaryLogical(logical any) error {
	s, ok := logical.(string)
	if !ok || s == "" || strings.ContainsAny(s, `\:`) || strings.HasPrefix(s, "/") || path.Ext(path.Base(s)) != ".jsonl" ||
		strings.HasPrefix(path.Base(s), ".") && strings.Count(path.Base(s), ".") == 1 {
		return valueErr("work_pc_path_invalid")
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." {
			return valueErr("work_pc_path_invalid")
		}
	}
	return nil
}

// boundarySource is root/logical when neither has a symlink and it is a regular file.
func boundarySource(root string, logical any) (string, error) {
	if err := boundaryLogical(logical); err != nil {
		return "", err
	}
	root = absPath(root)
	p := filepath.Join(root, filepath.FromSlash(logical.(string)))
	if !resolved(root) || !resolved(p) || !isFile(p) {
		return "", valueErr("work_pc_source_unavailable")
	}
	return p, nil
}

type boundarySig struct {
	dev, ino uint64
	size     int64
	mtime    int64
}

func boundarySignature(p string) (boundarySig, error) {
	st, err := pathStamp(p, true)
	return boundarySig{st.Dev, st.Ino, st.Size, st.MtimeNs}, err
}

// boundaryMetadata checks the first lines of a transcript name this session and folder.
func boundaryMetadata(p, origin string, sid any, cwd any) (err error) {
	defer recovered(&err)
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for range 8 {
		raw, err := readLine(r, 1<<20+1)
		if err != nil {
			return err
		}
		if len(raw) == 0 || len(raw) > 1<<20 || raw[len(raw)-1] != '\n' {
			break
		}
		v, err := loadsBytes(raw)
		if err != nil {
			return err
		}
		row, ok := v.(map[string]any)
		if !ok {
			return valueErr("work_pc_metadata_invalid")
		}
		if origin == "codex" && row["type"] == "session_meta" {
			data, has := row["payload"]
			if !has {
				data = map[string]any{}
			}
			payload, ok := data.(map[string]any)
			if !ok {
				return valueErr("work_pc_metadata_invalid")
			}
			_, srcDict := payload["source"].(map[string]any)
			if !eq(payload["id"], sid) || payload["originator"] == "codex_exec" || srcDict {
				return valueErr("work_pc_session_invalid")
			}
			if err := boundarySameFolder(payload["cwd"], cwd); err != nil {
				return err
			}
			return nil
		}
		if origin == "claude_code" && truthy(row["sessionId"]) {
			if !eq(row["sessionId"], sid) {
				return valueErr("work_pc_session_invalid")
			}
			return boundarySameFolder(row["cwd"], cwd)
		}
	}
	return valueErr("work_pc_metadata_pending")
}

// boundarySameFolder is folder(a) != folder(b) -> work_pc_session_invalid (folder errors first).
func boundarySameFolder(a, b any) error {
	fa, err := boundaryFolder(a)
	if err != nil {
		return err
	}
	fb, err := boundaryFolder(b)
	if err != nil {
		return err
	}
	if fa != fb {
		return valueErr("work_pc_session_invalid")
	}
	return nil
}

// boundaryPrefix hashes the first offset bytes, which must end a line. Tests replace it.
var boundaryPrefix = func(p string, offset int64) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyN(h, f, offset)
	if n < offset {
		if err != nil && err != io.EOF {
			return "", err
		}
		return "", valueErr("work_pc_sync_pending")
	}
	last := make([]byte, 1)
	if offset == 0 {
		return "", valueErr("work_pc_line_pending")
	}
	if _, err := f.ReadAt(last, offset-1); err != nil || last[0] != '\n' {
		if err != nil {
			return "", err
		}
		return "", valueErr("work_pc_line_pending")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// boundaryCapture records where a session's transcript ends now.
func boundaryCapture(root string, logical any, origin string, sid any, cwd any) (map[string]any, error) {
	if s, ok := sid.(string); (origin != "claude_code" && origin != "codex") || !ok || s == "" {
		return nil, valueErr("work_pc_session_invalid")
	}
	if _, err := boundaryFolder(cwd); err != nil {
		return nil, err
	}
	p, err := boundarySource(root, logical)
	if err != nil {
		return nil, err
	}
	before, err := boundarySignature(p)
	if err != nil {
		return nil, err
	}
	if err := boundaryMetadata(p, origin, sid, cwd); err != nil {
		return nil, err
	}
	digest, err := boundaryPrefix(p, before.size)
	if err != nil {
		return nil, err
	}
	if after, err := boundarySignature(p); err != nil {
		return nil, err
	} else if after != before {
		return nil, valueErr("work_pc_source_changed")
	}
	return map[string]any{"version": int64(1), "origin": origin, "session_id": sid, "cwd": cwd, "logical": logical,
		"offset": before.size, "prefix_sha256": digest}, nil
}

// boundaryValidate checks a boundary's schema; version 2 is a Zcode boundary.
func boundaryValidate(b any) error {
	m, isMap := b.(map[string]any)
	if v, ok := num(m["version"]); isMap && ok && v == 2 {
		if _, isBool := m["version"].(bool); !isBool {
			return pcZcodeValidate(m)
		}
	}
	invalid := valueErr("work_pc_boundary_invalid")
	if !isMap || len(m) != len(boundaryFields) {
		return invalid
	}
	for _, k := range boundaryFields {
		if _, ok := m[k]; !ok {
			return invalid
		}
	}
	version, vok := host.Int(m["version"])
	origin, _ := m["origin"].(string)
	sid, sok := m["session_id"].(string)
	offset, ook := host.Int(m["offset"])
	digest, dok := m["prefix_sha256"].(string)
	if !vok || version != 1 || (origin != "claude_code" && origin != "codex") || !sok || sid == "" ||
		!ook || offset <= 0 || !dok || !boundaryHash.MatchString(digest) {
		return invalid
	}
	if _, err := boundaryFolder(m["cwd"]); err != nil {
		return err
	}
	return boundaryLogical(m["logical"])
}

// boundaryVerify checks the server copy still starts with the marked prefix.
func boundaryVerify(root string, b any) (string, error) {
	if err := boundaryValidate(b); err != nil {
		return "", err
	}
	m := b.(map[string]any)
	p, err := boundarySource(root, m["logical"])
	if err != nil {
		return "", err
	}
	before, err := boundarySignature(p)
	if err != nil {
		return "", err
	}
	offset, _ := host.Int(m["offset"])
	if before.size < offset {
		return "", valueErr("work_pc_sync_pending")
	}
	if err := boundaryMetadata(p, m["origin"].(string), m["session_id"], m["cwd"]); err != nil {
		return "", err
	}
	digest, err := boundaryPrefix(p, offset)
	if err != nil {
		return "", err
	}
	if digest != m["prefix_sha256"] {
		return "", valueErr("work_pc_prefix_changed")
	}
	if after, err := boundarySignature(p); err != nil {
		return "", err
	} else if after != before {
		return "", valueErr("work_pc_source_changed")
	}
	return p, nil
}
