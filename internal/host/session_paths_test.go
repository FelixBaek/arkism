package host

import (
	"path/filepath"
	"testing"
)

func TestCodexSessionPathPrecedence(t *testing.T) {
	home := t.TempDir()
	producer := t.TempDir()
	override := filepath.Join(t.TempDir(), "transcripts")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("ARKISM_CODEX_SESSIONS", "")
	t.Setenv("ARKISM_CODEX_SESSION_INDEX", "")
	if got := CodexSessionsDir(home); got != filepath.Join(home, ".codex", "sessions") {
		t.Fatal(got)
	}
	t.Setenv("CODEX_HOME", producer)
	if got := CodexSessionsDir(home); got != filepath.Join(producer, "sessions") {
		t.Fatal(got)
	}
	t.Setenv("ARKISM_CODEX_SESSIONS", override)
	if got := CodexSessionsDir(home); got != override {
		t.Fatal(got)
	}
	if got := CodexSessionIndex(override + string(filepath.Separator)); got != filepath.Join(filepath.Dir(override), "session_index.jsonl") {
		t.Fatal(got)
	}
	index := filepath.Join(t.TempDir(), "titles.jsonl")
	t.Setenv("ARKISM_CODEX_SESSION_INDEX", index)
	if got := CodexSessionIndex(override); got != index {
		t.Fatal(got)
	}
}
