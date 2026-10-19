package host

import (
	"os"
	"path/filepath"
)

// CodexSessionsDir resolves the transcript root for every host session reader.
// The Arkism override wins over the producer's home; neither setting moves data.
func CodexSessionsDir(home string) string {
	codexHome := envOr("CODEX_HOME", filepath.Join(home, ".codex"))
	return envOr("ARKISM_CODEX_SESSIONS", filepath.Join(codexHome, "sessions"))
}

// CodexSessionIndex follows the selected session root unless explicitly set.
func CodexSessionIndex(sessions string) string {
	return envOr("ARKISM_CODEX_SESSION_INDEX", filepath.Join(filepath.Dir(filepath.Clean(sessions)), "session_index.jsonl"))
}

// envOr treats an empty path setting as unset, so a blank variable never yields a "" root.
func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
