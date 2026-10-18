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
	info, err := captureWindowsAttributes(alias)
	if err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 || info.ReparseTag != windows.IO_REPARSE_TAG_MOUNT_POINT {
		t.Fatal("test junction did not retain its native name-surrogate tag", err)
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
		info, err := captureWindowsAttributes(root)
		if err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
			t.Fatal("native root handle could not inspect the directory", root, err)
		}
	}
}

func TestWindowsResolvedReparseTagsMatchSDK(t *testing.T) {
	// Numeric tags and the N-bit macro are fixed to Microsoft's SDK source:
	// https://github.com/microsoft/win32metadata/blob/1bfb76db1c360653bdcb56512af0fdf987aceab8/generation/WinSDK/RecompiledIdlHeaders/um/winnt.h#L14714-L14769
	// These are metadata vectors, not execution of Cloud, Dedup or SMB drivers.
	cases := []struct {
		name  string
		tag   uint32
		alias bool
	}{
		{"mount_point", 0xA0000003, true},
		{"symlink", 0xA000000C, true},
		{"global_reparse", 0xA0000019, true},
		{"wci_link", 0xA0000027, true},
		{"dedup", 0x80000013, false},
		{"file_placeholder", 0x80000015, false},
		{"wof", 0x80000017, false},
		{"cloud", 0x9000001A, false},
		{"cloud_1", 0x9000101A, false},
		{"cloud_f", 0x9000F01A, false},
		{"projfs", 0x9000001C, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			info := captureWindowsAttributeTagInfo{FileAttributes: windows.FILE_ATTRIBUTE_REPARSE_POINT, ReparseTag: test.tag}
			if captureWindowsNameSurrogate(info) != test.alias {
				t.Fatal("native reparse tag was classified against the SDK contract")
			}
		})
	}
}

func TestWindowsResolvedAttributeQueryFailuresStayErrors(t *testing.T) {
	if _, err := captureWindowsHandleAttributes(windows.InvalidHandle); err == nil {
		t.Fatal("failed native tag query was accepted")
	}
	path := filepath.Join(t.TempDir(), "missing")
	if _, err := captureWindowsAttributes(path); err == nil {
		t.Fatal("failed native attribute lookup was accepted")
	}
	if resolvedPathEqual(path, path) {
		t.Fatal("failed attribute lookup was accepted as an ordinary spelling")
	}
}
