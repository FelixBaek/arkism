package capture

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

type boundaryFixture struct {
	pc, server, logical, path, copy, cwd string
	meta                                 any
}

func newBoundaryFixture(t *testing.T) *boundaryFixture {
	root := t.TempDir()
	f := &boundaryFixture{pc: filepath.Join(root, "pc"), server: filepath.Join(root, "server"), logical: "day/rollout.jsonl",
		cwd: `C:\Users\user\project`}
	f.path = filepath.Join(f.pc, "day", "rollout.jsonl")
	f.copy = filepath.Join(f.server, "day", "rollout.jsonl")
	os.MkdirAll(filepath.Dir(f.path), 0o755)
	os.MkdirAll(filepath.Dir(f.copy), 0o755)
	f.meta = host.Obj{{Key: "type", Value: "session_meta"}, {Key: "payload", Value: host.Obj{{Key: "id", Value: "s"}, {Key: "cwd", Value: f.cwd}, {Key: "source", Value: "cli"}}}}
	f.write()
	return f
}

func (f *boundaryFixture) write() {
	os.WriteFile(f.path, []byte(host.MustDumps(f.meta, host.DumpOpts{})+"\n"), 0o644)
}

func (f *boundaryFixture) marker(t *testing.T) map[string]any {
	m, err := boundaryCapture(f.pc, f.logical, "codex", "s", f.cwd)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func boundaryWantErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("error %v, want %q", err, sub)
	}
}

func boundaryAppend(path, text string) {
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(text)
	f.Close()
}

func TestBoundaryDelayedSyncPreservesExactStartAfterFileGrows(t *testing.T) {
	f := newBoundaryFixture(t)
	marker := f.marker(t)
	raw, _ := os.ReadFile(f.path)
	os.WriteFile(f.copy, raw[:len(raw)-1], 0o644)
	_, err := boundaryVerify(f.server, marker)
	boundaryWantErr(t, err, "sync_pending")
	boundaryAppend(f.path, `{"role": "user", "content": "new work"}`+"\n")
	raw, _ = os.ReadFile(f.path)
	os.WriteFile(f.copy, raw, 0o644)
	if p, err := boundaryVerify(f.server, marker); err != nil || p != f.copy {
		t.Fatal(p, err)
	}
	if marker["offset"].(int64) >= int64(len(raw)) {
		t.Fatal("offset not before growth")
	}
	text := host.MustDumps(marker, host.DumpOpts{})
	if strings.Contains(text, "new work") || strings.Contains(text, f.path) {
		t.Fatal(text)
	}
}

func TestBoundaryRewrittenPrefixAndForeignSessionAreRejected(t *testing.T) {
	f := newBoundaryFixture(t)
	marker := f.marker(t)
	raw, _ := os.ReadFile(f.path)
	os.WriteFile(f.copy, bytes.Replace(raw, []byte(`"cli"`), []byte(`"app"`), 1), 0o644)
	_, err := boundaryVerify(f.server, marker)
	boundaryWantErr(t, err, "prefix_changed")
	os.WriteFile(f.copy, bytes.Replace(raw, []byte(`"s"`), []byte(`"x"`), 1), 0o644)
	_, err = boundaryVerify(f.server, marker)
	boundaryWantErr(t, err, "session_invalid")
}

func TestBoundaryUnsafePathsAliasesAndIncompleteLinesAreRejected(t *testing.T) {
	f := newBoundaryFixture(t)
	for _, logical := range []string{"../outside.jsonl", "/absolute.jsonl", "C:/a.jsonl", `day\file.jsonl`} {
		_, err := boundaryCapture(f.pc, logical, "codex", "s", f.cwd)
		boundaryWantErr(t, err, "path_invalid")
	}
	os.Symlink(f.path, f.copy)
	_, err := boundaryVerify(f.server, f.marker(t))
	boundaryWantErr(t, err, "source_unavailable")
	boundaryAppend(f.path, "partial")
	_, err = boundaryCapture(f.pc, f.logical, "codex", "s", f.cwd)
	boundaryWantErr(t, err, "line_pending")
}

func TestBoundaryWrongFolderExecAndSubagentAreRejected(t *testing.T) {
	f := newBoundaryFixture(t)
	for _, c := range []host.KV{{Key: "cwd", Value: `C:\other`}, {Key: "originator", Value: "codex_exec"}, {Key: "source", Value: map[string]any{"subagent": "worker"}}} {
		payload := host.Obj{{Key: "id", Value: "s"}, {Key: "cwd", Value: f.cwd}, {Key: "source", Value: "cli"}}
		payload.Set(c.Key, c.Value)
		f.meta = host.Obj{{Key: "type", Value: "session_meta"}, {Key: "payload", Value: payload}}
		f.write()
		_, err := boundaryCapture(f.pc, f.logical, "codex", "s", f.cwd)
		boundaryWantErr(t, err, "session_invalid")
	}
	a, _ := boundaryFolder(f.cwd)
	b, _ := boundaryFolder("c:/users/user/project")
	if a != b {
		t.Fatal(a, b)
	}
	_, err := boundaryFolder("relative")
	boundaryWantErr(t, err, "folder_invalid")
	_, err = boundaryFolder(`\rooted-without-drive`)
	boundaryWantErr(t, err, "folder_invalid")
}

func TestBoundaryClaudeMetadataAndChangedDuringRead(t *testing.T) {
	f := newBoundaryFixture(t)
	f.meta = map[string]any{"sessionId": "s", "cwd": f.cwd, "type": "user", "message": map[string]any{"role": "user", "content": "old"}}
	f.write()
	marker, err := boundaryCapture(f.pc, f.logical, "claude_code", "s", f.cwd)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(f.path)
	os.WriteFile(f.copy, raw, 0o644)
	if p, err := boundaryVerify(f.server, marker); err != nil || p != f.copy {
		t.Fatal(p, err)
	}
	real := boundaryPrefix
	t.Cleanup(func() { boundaryPrefix = real })
	boundaryPrefix = func(p string, offset int64) (string, error) {
		d, err := real(p, offset)
		boundaryAppend(p, "{}\n")
		return d, err
	}
	_, err = boundaryVerify(f.server, marker)
	boundaryWantErr(t, err, "source_changed")
}

func TestBoundarySchemaCannotSmuggleBodyOrInvalidOffset(t *testing.T) {
	f := newBoundaryFixture(t)
	marker := f.marker(t)
	for _, c := range []host.KV{{Key: "text", Value: "private"}, {Key: "offset", Value: true}, {Key: "offset", Value: int64(-1)}, {Key: "version", Value: true}, {Key: "prefix_sha256", Value: "bad"}} {
		m := map[string]any{}
		for k, v := range marker {
			m[k] = v
		}
		m[c.Key] = c.Value
		_, err := boundaryVerify(f.server, m)
		boundaryWantErr(t, err, "boundary_invalid")
	}
}

// Golden: ntpath folder normalisation matches Python on every OS.
func TestBoundaryFolderGolden(t *testing.T) {
	var cases []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	}
	boundaryGolden(t, "boundary_folder.json", &cases)
	for _, c := range cases {
		got, err := boundaryFolder(c.In)
		if err != nil {
			got = err.Error()
		}
		want := c.Out
		if runtime.GOOS == "windows" && strings.HasPrefix(c.In, "/") && !ntIsAbs(c.In) {
			// The golden comes from Python on Linux; a POSIX path is not absolute under this OS's (ntpath) rules.
			want = "work_pc_folder_invalid"
		}
		if got != want {
			t.Errorf("folder(%q) = %q, want %q", c.In, got, want)
		}
	}
}

// boundaryGolden decodes testdata/name into v.
func boundaryGolden(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatal(err)
	}
}
