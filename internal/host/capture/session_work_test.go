package capture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionBehaviorRecordsWorkOutcomes(t *testing.T) {
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339Nano) }
	line := func(v map[string]any) string { raw, _ := json.Marshal(v); return string(raw) + "\n" }
	use := func(d time.Duration, id, name, cmd string) string {
		return line(map[string]any{"type": "assistant", "timestamp": at(d), "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": name, "input": map[string]any{"command": cmd}}}}})
	}
	result := func(d time.Duration, id string, isError bool, content string) string {
		return line(map[string]any{"type": "user", "timestamp": at(d), "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": isError, "content": content}}}})
	}
	codex := func(d time.Duration, payload map[string]any) string {
		return line(map[string]any{"type": "response_item", "timestamp": at(d), "payload": payload})
	}
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "t.jsonl")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	work := func(transcript string) map[string]any {
		t.Helper()
		b := sessionBehavior(transcript, at(-time.Minute), at(time.Hour))
		if b == nil {
			t.Fatal("no behavior")
		}
		return b["work"].(map[string]any)
	}
	claude := write(use(time.Second, "e1", "Edit", "") + result(2*time.Second, "e1", false, "ok") +
		use(3*time.Second, "t1", "Bash", "go test ./internal/memory") + result(4*time.Second, "t1", true, "Exit code 1\nFAIL") +
		use(5*time.Second, "t2", "Bash", "cd /r && go test ./...") + result(6*time.Second, "t2", false, "ok") +
		use(7*time.Second, "c1", "Bash", "gh pr checks 12") + result(8*time.Second, "c1", true, "Exit code 8\npending") +
		use(9*time.Second, "c2", "Bash", "gh pr checks 12") + result(10*time.Second, "c2", false, "all pass") +
		use(11*time.Second, "x", "Bash", "ls") + result(12*time.Second, "x", true, "Exit code 2"))
	if w := work(claude); w["diff"] != true || w["tests"] != "passed" || w["ci"] != "passed" {
		t.Fatalf("claude: %v", w)
	}
	codexRun := write(codex(time.Second, map[string]any{"type": "function_call", "name": "shell", "call_id": "k1", "arguments": `{"command":["bash","-lc","pytest -q"]}`}) +
		codex(2*time.Second, map[string]any{"type": "function_call_output", "call_id": "k1", "output": `{"output":"1 failed","metadata":{"exit_code":1}}`}) +
		codex(3*time.Second, map[string]any{"type": "custom_tool_call", "name": "apply_patch", "call_id": "k2", "input": "*** Begin Patch"}) +
		codex(4*time.Second, map[string]any{"type": "custom_tool_call_output", "call_id": "k2", "output": []any{map[string]any{"type": "input_text", "text": "Exit code: 1\nverification failed"}}}) +
		// An output without an exit code decides nothing.
		codex(5*time.Second, map[string]any{"type": "function_call", "name": "shell", "call_id": "k3", "arguments": `{"cmd":"gh pr checks 4"}`}) +
		codex(6*time.Second, map[string]any{"type": "function_call_output", "call_id": "k3", "output": "Script failed\nWall time 0.0 seconds"}))
	if w := work(codexRun); w["diff"] != false || w["tests"] != "failed" || w["ci"] != "none" {
		t.Fatalf("codex: %v", w)
	}
	// A session that ran nothing observable reports no work result at all.
	idle := write(line(map[string]any{"type": "assistant", "timestamp": at(time.Second), "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "done"}}}}))
	if w := work(idle); w["diff"] != false || w["tests"] != "none" || w["ci"] != "none" {
		t.Fatalf("idle: %v", w)
	}
}

func TestWorkCommandsAreJudgedInCommandPosition(t *testing.T) {
	for cmd, want := range map[string]string{
		"go test ./...":                         "test",
		"cd /r && GOFLAGS=-count=1 go test ./x": "test",
		"python3 tools/check_local.py":          "test",
		"npm run test":                          "test",
		"echo 'go test ./...'":                  "",
		"gh pr checks 3 --watch":                "ci",
		"gh run watch 9 --exit-status":          "ci",
		"gh run watch 9":                        "", // exits 0 on a failed run
		"git commit -m 'go test passes'":        "edit",
	} {
		w := newSessionWork()
		w.call("id", "Bash", shellText(cmd))
		if w.pending["id"] != want {
			t.Errorf("%q: %q, want %q", cmd, w.pending["id"], want)
		}
	}
}
