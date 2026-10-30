package skills

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type zipEntry struct {
	name, body string
	mode       os.FileMode
}

func makeZip(t *testing.T, entries []zipEntry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		h.SetMode(e.mode)
		if e.mode == 0 {
			h.SetMode(0o644)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(e.body))
	}
	zw.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestSkillZipPreviewsThenInstalls(t *testing.T) {
	calls, syncs := stubGitHub(t, nil)
	z := makeZip(t, []zipEntry{
		{name: "pdf/", mode: os.ModeDir | 0o755},
		{name: "pdf/SKILL.md", body: "---\nname: pdf\ndescription: PDF\n---\n본문"},
		{name: "pdf/scripts/fill.py", body: "print(1)", mode: 0o755},
	})
	res, _ := applyCatalogRequest(map[string]any{"id": "z1", "agent": "shared", "kind": "skill_install", "name": "pdf.zip", "zip": z, "enabled": false}, map[string]any{})
	d := j(t, objGet(res, "detail").(string))
	if objGet(res, "status") != "done" || d["source"] != "pdf.zip" || len(asList(d["skills"])) != 1 || len(*calls) != 0 {
		t.Fatal(res, *calls)
	}
	res, _ = applyCatalogRequest(map[string]any{"id": "z2", "agent": "shared", "kind": "skill_install", "name": "pdf.zip", "zip": z, "enabled": true, "targets": []any{"codex"}}, map[string]any{})
	if objGet(res, "status") != "done" || *syncs != 1 {
		t.Fatal(res)
	}
	base := filepath.Join(agentsHome, ".codex/skills/pdf")
	if body, err := os.ReadFile(filepath.Join(base, "scripts/fill.py")); err != nil || string(body) != "print(1)" {
		t.Fatal(string(body), err)
	}
	src := j(t, string(must(os.ReadFile(filepath.Join(base, ".arkism-source.json")))))
	if src["zip"] != "pdf.zip" || src["path"] != "pdf" || len(str(src["sha256"])) != 64 || src["repo"] != nil {
		t.Fatal(src)
	}
}

func TestSkillZipRefusesUnsafeArchives(t *testing.T) {
	skill := zipEntry{name: "s/SKILL.md", body: "---\nname: s\n---"}
	big := strings.Repeat("x", 1<<20)
	many := []zipEntry{skill}
	for i := 0; i < skillMaxFiles; i++ {
		many = append(many, zipEntry{name: "s/f" + strconv.Itoa(i), body: "x"})
	}
	for name, c := range map[string]struct {
		zip, want string
	}{
		"symlink":   {makeZip(t, []zipEntry{skill, {name: "s/key", body: "/etc/ssh/ssh_host_rsa_key", mode: os.ModeSymlink | 0o777}}), "링크"},
		"escape":    {makeZip(t, []zipEntry{skill, {name: "../evil", body: "x"}}), "경로"},
		"absolute":  {makeZip(t, []zipEntry{skill, {name: "/etc/evil", body: "x"}}), "경로"},
		"backslash": {makeZip(t, []zipEntry{skill, {name: "s\\..\\evil", body: "x"}}), "경로"},
		"dot":       {makeZip(t, []zipEntry{skill, {name: "s/./x", body: "x"}}), "경로"},
		"device":    {makeZip(t, []zipEntry{skill, {name: "s/fifo", mode: os.ModeNamedPipe | 0o644}}), "일반 파일"},
		"duplicate": {makeZip(t, []zipEntry{skill, {name: "s/a", body: "1"}, {name: "s/a", body: "2"}}), "두 번"},
		"too big":   {makeZip(t, []zipEntry{skill, {name: "s/a", body: big}, {name: "s/b", body: big}, {name: "s/c", body: big}, {name: "s/d", body: big}, {name: "s/e", body: big}, {name: "s/f", body: big}}), "5MB"},
		"too many":  {makeZip(t, many), "5MB"},
		"not a zip": {base64.StdEncoding.EncodeToString([]byte("plain text")), "zip 파일"},
		"no skill":  {makeZip(t, []zipEntry{{name: "README.md", body: "x"}}), "SKILL.md"},
	} {
		t.Run(name, func(t *testing.T) {
			_, syncs := stubGitHub(t, nil)
			res, _ := applyCatalogRequest(map[string]any{"id": "z", "kind": "skill_install", "name": "s.zip", "zip": c.zip, "enabled": true, "targets": []any{"codex"}}, map[string]any{})
			if objGet(res, "status") != "failed" || !strings.Contains(objGet(res, "error").(string), c.want) || *syncs != 0 {
				t.Fatal(res)
			}
			if entries, _ := os.ReadDir(filepath.Join(agentsHome, ".codex/skills")); len(entries) != 0 {
				t.Fatal("wrote", entries)
			}
		})
	}
}
