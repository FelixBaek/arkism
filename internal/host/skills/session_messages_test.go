package skills

import (
	"errors"
	"strings"
	"testing"
)

func TestSendSessionMessageCarriesSenderAndWakesWithoutBody(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_SESSION_ID", "claude-sess-1")
	routerRuntime = "claude_code"
	calls := fakeRequest(t, `{"id":"m1","target_session":"codex-1","created_at":"2026-11-01T00:00:00Z","authority":"data_only_untrusted"}`)
	var woke []string
	old := codexQueue
	codexQueue = func(thread, message string) error { woke = append(woke, thread, message); return nil }
	t.Cleanup(func() { codexQueue = old })

	out := structured(t, call(t, "send_session_message", `{"target_session":"codex-1","text":"PR #526 checks passed","wake":true}`)).(map[string]any)
	if len(*calls) != 1 || (*calls)[0].path != "/v1/session-messages/send" {
		t.Fatalf("calls = %+v", *calls)
	}
	p := (*calls)[0].payload.(map[string]any)
	if p["from_session"] != "claude-sess-1" || p["from_origin"] != "claude_code" || p["target_session"] != "codex-1" || p["text"] != "PR #526 checks passed" || len(p["event_id"].(string)) != 64 {
		t.Fatalf("payload = %+v", p)
	}
	if len(woke) != 2 || woke[0] != "codex-1" || strings.Contains(woke[1], "PR #526") || woke[1] != CodexWakeNudge {
		t.Fatalf("wake = %q", woke)
	}
	if w := out["wake"].(map[string]any); w["ok"] != true {
		t.Fatalf("wake result = %+v", w)
	}

	// The same retry gets the same event id; a failed wake is reported, not fatal.
	codexQueue = func(string, string) error { return errors.New("no such thread") }
	out = structured(t, call(t, "send_session_message", `{"target_session":"codex-1","text":"PR #526 checks passed","wake":true}`)).(map[string]any)
	if (*calls)[1].payload.(map[string]any)["event_id"] != p["event_id"] {
		t.Fatal("retry changed the event id")
	}
	if w := out["wake"].(map[string]any); w["ok"] != false || w["error"] != "no such thread" {
		t.Fatalf("failed wake = %+v", w)
	}
}

func TestReadSessionMessagesDefaultsToOwnSessionAsData(t *testing.T) {
	swapRouter(t)
	t.Setenv("ARKISM_SESSION_ID", "hermes-run-7")
	calls := fakeRequest(t, `{"items":[{"id":"m1","from_session":"claude-sess-1","target_session":"hermes-run-7","text":"hi","created_at":"2026-11-01T00:00:00Z"}],"more":false,"authority":"data_only_untrusted"}`)
	out := structured(t, call(t, "read_session_messages", `{}`)).(map[string]any)
	p := (*calls)[0].payload.(map[string]any)
	if (*calls)[0].path != "/v1/session-messages/take" || p["session"] != "hermes-run-7" || out["authority"] != "data_only_untrusted" {
		t.Fatalf("call = %+v out = %+v", (*calls)[0], out)
	}
}
