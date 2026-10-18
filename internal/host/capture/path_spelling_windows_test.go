package capture

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func windowsShortSpelling(t *testing.T, path string) string {
	t.Helper()
	input, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	output := make([]uint16, 32768)
	n, err := windows.GetShortPathName(input, &output[0], uint32(len(output)))
	if err != nil || n == 0 || n >= uint32(len(output)) {
		t.Fatal("cannot obtain native short path", n, err)
	}
	return windows.UTF16ToString(output[:n])
}

func TestWindowsResolvedSpellingAndReparseProtection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Mixed Case Project")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "Session Data.jsonl")
	if err := os.WriteFile(file, []byte("synthetic content"), 0o600); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, canonical, strings.ToLower(canonical), windowsShortSpelling(t, canonical)} {
		if !resolved(path) {
			t.Fatalf("native spelling rejected as a reparse alias: %s", path)
		}
		if !resolved(filepath.Join(filepath.Dir(path), "missing", "session.jsonl")) {
			t.Fatal("normal missing tail rejected")
		}
	}

	alias := filepath.Join(t.TempDir(), "junction")
	// Directory junctions do not need the optional symbolic-link privilege. Use
	// a real NTFS reparse point so accepting short names cannot hide an alias.
	out, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", alias, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("cannot create test junction: %v: %s", err, out)
	}
	name, err := windows.UTF16PtrFromString(alias)
	if err != nil {
		t.Fatal(err)
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil || attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		t.Fatal("test junction is not a reparse point", err)
	}
	for _, path := range []string{alias, filepath.Join(alias, filepath.Base(file)), filepath.Join(alias, "missing", "session.jsonl")} {
		if resolved(path) {
			t.Fatal("real junction accepted as an ordinary spelling")
		}
	}
	if _, err := cleanupSpool(alias, "queued_at", timeNow()); !errors.Is(err, valueErr("capture_spool_alias")) {
		t.Fatal("cleanup followed the test junction", err)
	}
	if raw, err := os.ReadFile(file); err != nil || string(raw) != "synthetic content" {
		t.Fatal("alias check changed the target", err)
	}
}

func TestWindowsResolvedSupportsLongPathsAndUNCSpelling(t *testing.T) {
	root := filepath.Join(t.TempDir(), strings.Repeat("long-a", 15), strings.Repeat("long-b", 15), strings.Repeat("long-c", 15))
	if len(root) <= 260 {
		t.Fatal("fixture does not exercise a long native path")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "Session Data.jsonl")
	if err := os.WriteFile(file, []byte("synthetic long path"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{file, strings.ToLower(file)} {
		if !resolved(spelling) {
			t.Fatal("native long path rejected")
		}
	}
	native, err := captureWindowsPath(file)
	if err != nil || !resolved(native) {
		t.Fatal("verbatim long path rejected", err)
	}
	for _, raw := range []string{`\\fixture-host\share\folder`, `\\?\UNC\fixture-host\share\folder`} {
		converted, err := captureWindowsPath(raw)
		if err != nil || converted != `\\?\UNC\fixture-host\share\folder` {
			t.Fatal("UNC namespace changed", converted, err)
		}
	}
	for _, root := range []string{`\\fixture-host\share`, `\\?\UNC\fixture-host\share`} {
		root = filepath.Clean(root)
		if filepath.Dir(root) != root {
			t.Fatal("UNC share root was not recognized as a volume boundary", root)
		}
	}
	volumeRoot := filepath.VolumeName(file) + string(os.PathSeparator)
	nativeRoot, err := captureWindowsPath(volumeRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{volumeRoot, nativeRoot} {
		attributes, err := captureWindowsAttributes(root)
		if err != nil || attributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			t.Fatal("native root handle could not inspect the directory", root, err)
		}
	}
}
