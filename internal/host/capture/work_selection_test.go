package capture

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// selectionSetup gives each test its own state folder and a checkout.
func selectionSetup(t *testing.T) (root, cwd string) {
	t.Helper()
	root, _ = filepath.EvalSymlinks(t.TempDir())
	cwd = filepath.Join(root, "checkout")
	os.Mkdir(cwd, 0o700)
	t.Setenv("ARKISM_WORK_SELECTION_STATE", filepath.Join(root, "settings"))
	t.Setenv("ARKISM_WORK_ID", "")
	t.Setenv("ARKISM_CLIENT", "server")
	return root, cwd
}

func TestSelectionRoundtripIsolatedExactFolderAndProjectUnchanged(t *testing.T) {
	root, cwd := selectionSetup(t)
	project := filepath.Join(cwd, "notes.txt")
	os.WriteFile(project, []byte("original"), 0o600)
	child := filepath.Join(cwd, "subdir")
	os.Mkdir(child, 0o700)
	other := filepath.Join(root, "other")
	os.Mkdir(other, 0o700)
	got, err := selectionSelect(cwd, "task")
	if v, _ := got.Get("server_verified"); err != nil || v != false {
		t.Fatal(got, err)
	}
	if selectionSelected(cwd) != "task" || selectionSelected(child) != "" || selectionSelected(other) != "" {
		t.Fatal("selection leaked")
	}
	selectionSelect(other, "second")
	cleared, err := selectionClear(cwd)
	if v, _ := cleared.Get("work_deleted"); err != nil || v != false {
		t.Fatal(cleared, err)
	}
	if selectionSelected(cwd) != "" || selectionSelected(other) != "second" {
		t.Fatal("clear touched the wrong folder")
	}
	if raw, _ := os.ReadFile(project); string(raw) != "original" {
		t.Fatal("project changed")
	}
	entries, _ := os.ReadDir(cwd)
	if len(entries) != 2 {
		t.Fatal(entries)
	}
}

func TestSelectionInvalidAndCorruptSelectionsAreNotUsed(t *testing.T) {
	root, cwd := selectionSetup(t)
	for _, c := range []any{nil, "", "relative", filepath.Join(root, "missing")} {
		if selectionSelected(c) != "" {
			t.Fatal(c)
		}
	}
	selectionSelect(cwd, "task")
	normalized, path, err := selectionPath(cwd)
	if err != nil {
		t.Fatal(err)
	}
	good := func(k string, v any) string {
		o := host.Obj{{Key: "version", Value: 1}, {Key: "cwd", Value: normalized}, {Key: "id", Value: "task"}}
		if k != "" {
			o.Set(k, v)
		}
		return host.MustDumps(o, host.DumpOpts{})
	}
	for _, raw := range []string{good("version", true), good("cwd", root), good("id", "../task"), good("extra", "body"), "[]", "null",
		"\xff", "{broken", strings.Repeat(" ", 4097), strings.Repeat("[", 1500) + strings.Repeat("]", 1500)} {
		os.WriteFile(path, []byte(raw), 0o600)
		if selectionSelected(cwd) != "" {
			t.Fatal(raw)
		}
	}
	os.WriteFile(path, []byte(good("", nil)), 0o600)
	if selectionSelected(cwd) != "task" {
		t.Fatal("valid selection rejected")
	}
	os.Remove(path)
	os.Mkdir(path, 0o700)
	if selectionSelected(cwd) != "" {
		t.Fatal("directory used")
	}
	os.Remove(path)
	original := filepath.Join(root, "target")
	os.WriteFile(original, []byte(good("", nil)), 0o600)
	os.Symlink(original, path)
	if selectionSelected(cwd) != "" {
		t.Fatal("symlink used")
	}
	if _, err := selectionSelect(cwd, "other"); err == nil {
		t.Fatal("select through symlink")
	}
	if _, err := selectionClear(cwd); err == nil {
		t.Fatal("clear through symlink")
	}
	if raw, _ := os.ReadFile(original); string(raw) != good("", nil) {
		t.Fatal("symlink target changed")
	}
}

func TestSelectionFailedAtomicPublishPreservesPreviousSelection(t *testing.T) {
	root, cwd := selectionSetup(t)
	selectionSelect(cwd, "previous")
	keep := selectionRename
	t.Cleanup(func() { selectionRename = keep })
	selectionRename = func(string, string) error { return errors.New("disk failure") }
	if _, err := selectionSelect(cwd, "next"); err == nil {
		t.Fatal("publish failure ignored")
	}
	if selectionSelected(cwd) != "previous" {
		t.Fatal("previous selection lost")
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "settings")); len(entries) != 1 {
		t.Fatal(entries)
	}
}

func TestSelectionCLI(t *testing.T) {
	_, cwd := selectionSetup(t)
	run := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		code := selectionMain(args, nil, &out, &errOut)
		return code, out.String()
	}
	if code, out := run("select", "--cwd", cwd, "--id", "task"); code != 0 || out != `{"id": "task", "selected": true, "server_verified": false}`+"\n" {
		t.Fatal(code, out)
	}
	if code, out := run("get", "--cwd", cwd); code != 0 || out != `{"id": "task", "server_verified": false}`+"\n" {
		t.Fatal(code, out)
	}
	if code, out := run("clear", "--cwd="+cwd); code != 0 || out != `{"selected": false, "work_deleted": false}`+"\n" {
		t.Fatal(code, out)
	}
	if code, out := run("get", "--cwd", cwd); code != 0 || out != `{"id": null, "server_verified": false}`+"\n" {
		t.Fatal(code, out)
	}
	if code, out := run("select", "--cwd", cwd, "--id", "../bad"); code != 1 || out != `{"error": "invalid_selection"}`+"\n" {
		t.Fatal(code, out)
	}
	for _, args := range [][]string{{"get"}, {"drop", "--cwd", cwd}, {"--cwd", cwd}, {"get", "--cwd"}} {
		if code, _ := run(args...); code != 2 {
			t.Fatal(args, code)
		}
	}
}
