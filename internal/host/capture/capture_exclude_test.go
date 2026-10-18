package capture

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCaptureSkippedByPauseFoldersAndIgnoreFile(t *testing.T) {
	dir := t.TempDir()
	settings := filepath.Join(dir, "capture.json")
	t.Setenv("ARKISM_CAPTURE_SETTINGS", settings)
	work, secret, open := filepath.Join(dir, "work"), filepath.Join(dir, "work", "secret"), filepath.Join(dir, "work", "open")
	for _, d := range []string{filepath.Join(secret, "src"), filepath.Join(open, "pkg"), filepath.Join(dir, "work", "secret-other")} {
		os.MkdirAll(d, 0o755)
	}
	if captureSkipped(open) {
		t.Fatal("no settings: nothing skipped")
	}
	os.WriteFile(settings, []byte(`{"exclude": ["`+strings.ReplaceAll(secret, `\`, `\\`)+`"]}`), 0o600)
	if !captureSkipped(secret) || !captureSkipped(filepath.Join(secret, "src")) {
		t.Fatal("excluded folder and its children are skipped")
	}
	if captureSkipped(filepath.Join(work, "secret-other")) || captureSkipped(open) {
		t.Fatal("a sibling that only shares a name prefix is not excluded")
	}
	os.WriteFile(filepath.Join(open, captureIgnoreFile), nil, 0o644)
	if !captureSkipped(filepath.Join(open, "pkg")) {
		t.Fatal(".arkism-ignore above the folder skips it")
	}
	os.WriteFile(settings, []byte(`{"paused": true}`), 0o600)
	if !captureSkipped("") || !captureSkipped(filepath.Join(work, "secret-other")) {
		t.Fatal("paused skips everything")
	}
	os.WriteFile(settings, []byte(`not json`), 0o600)
	if !captureSkipped(filepath.Join(work, "secret-other")) {
		t.Fatal("invalid settings must pause capture")
	}
}

func TestCaptureInvalidSettingsStopHookBeforeOutput(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("ARKISM_CAPTURE_SETTINGS", settings)
	for _, input := range []string{`{"paused":true`, `null`, `[]`, `{"paused":"false"}`, `{"exclude":[1]}`,
		`{"paused":null}`, `{"Paused":null}`, `{"exclude":null}`, `{"exclude":[null]}`, `{"instructions":null}`, "{\"instructions\":\"\xff\"}", strings.Repeat(" ", (1<<20)+1)} {
		if err := os.WriteFile(settings, []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if code := hookMain(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"ordinary prompt","session_id":"s"}`), &out); code != 0 || out.Len() != 0 {
			t.Fatalf("invalid settings allowed hook output: code=%d bytes=%d", code, out.Len())
		}
		if !captureSkipped("") {
			t.Fatal("invalid settings allowed capture without a working directory")
		}
	}
	if err := os.Remove(settings); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(settings, 0o700); err != nil {
		t.Fatal(err)
	}
	if !captureSkipped("") {
		t.Fatal("unreadable settings allowed capture")
	}
}

func TestCaptureExclusionsFromWindowsMetadata(t *testing.T) {
	s := captureSettings{Exclude: []string{`C:\Work\Secret`, `\\server\share\private`}}
	for _, cwd := range []string{`c:\work\SECRET\src`, `C:/Work/Secret/sub`, `C:\Work\open\..\Secret\sub`, `\\SERVER\share\private\sub`, `\\?\C:\WORK\secret\src`} {
		if !captureSkippedWithSettings(cwd, s) {
			t.Fatalf("excluded Windows source allowed: %s", cwd)
		}
	}
	for _, cwd := range []string{`C:\Work\Secret-other`, `D:\Work\Secret`} {
		if captureSkippedWithSettings(cwd, s) {
			t.Fatalf("ordinary Windows source excluded: %s", cwd)
		}
	}
	if pathWithin(`\\server\other\private`, `\\server\share\private`) {
		t.Fatal("UNC sibling share matched the exclusion")
	}
	if !strings.HasPrefix(capturePolicyPath(`\\server\share\folder`), "//") || capturePolicyPath(`C:\`) != "c:/" {
		t.Fatal("Windows volume roots lost their absolute path semantics")
	}
	if runtime.GOOS == "windows" && filepath.VolumeName(capturePolicyPath(`\\server\share\folder`)) == "" {
		t.Fatal("native Windows did not recognize the UNC volume")
	}
}

func TestCaptureExclusionsFollowNativeSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs optional Windows privileges")
	}
	base := t.TempDir()
	secret, alias := filepath.Join(base, "secret"), filepath.Join(base, "alias")
	if err := os.Mkdir(secret, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, alias); err != nil {
		t.Fatal(err)
	}
	if !captureSkippedWithSettings(alias, captureSettings{Exclude: []string{secret}}) {
		t.Fatal("symlink bypassed the excluded folder")
	}
	if !captureSkippedWithSettings(secret, captureSettings{Exclude: []string{alias}}) {
		t.Fatal("resolved folder bypassed the excluded symlink")
	}
	if !captureSkippedWithSettings(secret, captureSettings{Exclude: []string{"/" + alias}}) {
		t.Fatal("repeated root in an excluded symlink bypassed its resolved folder")
	}
	if err := os.WriteFile(filepath.Join(secret, captureIgnoreFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !captureSkippedWithSettings(alias, captureSettings{}) {
		t.Fatal("symlink bypassed the folder opt-out")
	}
	if !captureSkippedWithSettings("/"+secret, captureSettings{}) {
		t.Fatal("repeated native root separator bypassed the folder opt-out")
	}
	for _, cwd := range []string{filepath.Join(alias, "missing", "leaf"), "/" + filepath.Join(alias, "missing", "leaf")} {
		if !captureSkippedWithSettings(cwd, captureSettings{}) {
			t.Fatal("missing leaf or repeated root bypassed a resolved ancestor opt-out")
		}
		if !captureSkippedWithSettings(cwd, captureSettings{Exclude: []string{secret}}) {
			t.Fatal("missing leaf bypassed a resolved ancestor exclusion")
		}
	}
}

func TestHookDoesNothingInAnExcludedFolder(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ARKISM_CAPTURE_SETTINGS", filepath.Join(dir, "capture.json"))
	os.WriteFile(filepath.Join(dir, captureIgnoreFile), nil, 0o644)
	var out strings.Builder
	in := strings.NewReader(`{"hook_event_name": "UserPromptSubmit", "prompt": "hello", "session_id": "s", "cwd": "` + strings.ReplaceAll(dir, `\`, `\\`) + `"}`)
	if code := hookMain(in, &out); code != 0 || out.Len() != 0 {
		t.Fatal(code, out.String())
	}
	got, err := winHook("codex", []byte(`{"hook_event_name": "Stop", "session_id": "s", "cwd": "`+strings.ReplaceAll(dir, `\`, `\\`)+`"}`), &out)
	if err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}
