package host

import "path/filepath"

// CodexSessionsDir resolves the transcript root for every host session reader.
// The Arkism override wins over the producer's home; neither setting moves data.
func CodexSessionsDir(home string) string {
	codexHome := Env("CODEX_HOME", filepath.Join(home, ".codex"))
	return Env("ARKISM_CODEX_SESSIONS", filepath.Join(codexHome, "sessions"))
}

// CodexSessionIndex follows the selected session root unless explicitly set.
func CodexSessionIndex(sessions string) string {
	return Env("ARKISM_CODEX_SESSION_INDEX", filepath.Join(filepath.Dir(filepath.Clean(sessions)), "session_index.jsonl"))
}
