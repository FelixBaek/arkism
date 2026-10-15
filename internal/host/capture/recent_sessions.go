package capture

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func recentSetting(key string, fallback, minimum, maximum int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < minimum || n > maximum {
		return fallback
	}
	return n
}

// Recent sessions are separate from stable rule text: fetched afresh only at
// SessionStart and never written into a session cache or a selected work binding.
func recentSessionContext(hook map[string]any, budget int) (out string) {
	defer func() {
		if recover() != nil {
			out = ""
		}
	}()
	count := recentSetting("ARKISM_RECENT_SESSIONS", 3, 0, 10)
	sid := str(hook["session_id"])
	cwd := str(hook["cwd"])
	if count == 0 || sid == "" || cwd == "" || !filepath.IsAbs(cwd) || budget < 500 {
		return ""
	}
	repository, err := filepath.EvalSymlinks(filepath.Clean(cwd))
	if err != nil {
		return ""
	}
	maxChars := min(budget, recentSetting("ARKISM_RECENT_SESSION_CHARS", 2500, 500, 4000))
	days := recentSetting("ARKISM_RECENT_SESSION_DAYS", 7, 1, 30)
	read := func(path string, body map[string]any) (map[string]any, error) {
		return capPost(path, body, 2*time.Second)
	}
	out, err = host.RecentSessionsContext(read, capScope(), repository, sid, count, days, maxChars)
	if err != nil {
		metric(kv("event", "hook_error"), kv("hook", "recent_sessions"), kv("error", pyType(err)))
		return ""
	}
	return out
}
