package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecentSessionsCanonicalRepositoryBudgetAndDisable(t *testing.T) {
	capFake(t)
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	calls := 0
	capPost = func(path string, body any, timeout time.Duration) (map[string]any, error) {
		calls++
		payload := body.(map[string]any)
		if path != "/v1/work-checkpoints/recent" || payload["repository"] != dir || payload["session_id"] != "now" || timeout > 2*time.Second {
			t.Fatal(path, payload, timeout)
		}
		return map[string]any{"authority": "data_only_untrusted", "items": []any{map[string]any{"id": "past", "revision": 1, "authority": "data_only_untrusted", "updated_at": time.Now().UTC().Format(time.RFC3339Nano), "package": map[string]any{"repository": dir, "session_id": "old", "title": "previous task", "source_refs": []any{}}}}}, nil
	}
	hook := map[string]any{"cwd": alias, "session_id": "now"}
	if out := recentSessionContext(hook, 900); !strings.Contains(out, "previous task") {
		t.Fatal(out)
	}
	t.Setenv("ARKISM_RECENT_SESSIONS", "0")
	if out := recentSessionContext(hook, 900); out != "" || calls != 1 {
		t.Fatal(out, calls)
	}
	t.Setenv("ARKISM_RECENT_SESSIONS", "3")
	if out := recentSessionContext(hook, 100); out != "" || calls != 1 {
		t.Fatal(out, calls)
	}
}
