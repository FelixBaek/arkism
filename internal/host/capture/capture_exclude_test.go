package capture

import (
	"os"
	"path/filepath"
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
	if captureSkipped(filepath.Join(work, "secret-other")) {
		t.Fatal("an unreadable file means no settings")
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
