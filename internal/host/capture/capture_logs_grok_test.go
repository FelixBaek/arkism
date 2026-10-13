package capture

import (
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The layout and line shapes follow real Grok CLI sessions (~/.grok/sessions).
func TestLogsCaptureGrokSessions(t *testing.T) {
	dir := t.TempDir()
	oldState, oldOrigin := capState, capOrigin
	t.Cleanup(func() { capState, capOrigin = oldState, oldOrigin })
	capState, capOrigin = filepath.Join(dir, "state"), "grok"
	settings := filepath.Join(dir, "capture.json")
	t.Setenv("ARKISM_CAPTURE_SETTINGS", settings)
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	// PathEscape keeps ':', which a Windows folder name cannot hold (C:\...); %3A unescapes the same.
	session := filepath.Join(dir, "sessions", strings.ReplaceAll(url.PathEscape(work), ":", "%3A"), "01a0-grok-1")
	os.MkdirAll(session, 0o755)
	os.WriteFile(filepath.Join(session, "summary.json"), []byte(`{"info":{"id":"01a0-grok-1","cwd":"`+filepath.ToSlash(work)+`"},"num_messages":9}`), 0o644)
	update := func(index int, at time.Time) string {
		return `{"timestamp":` + itoa(at.Unix()) + `,"method":"session/update","params":{"sessionId":"01a0-grok-1","update":{"sessionUpdate":"user_message_chunk","_meta":{"promptIndex":` + itoa(int64(index)) + `},"content":{"type":"text","text":"x"}},"_meta":{"agentTimestampMs":` + itoa(at.UnixMilli()) + `}}}`
	}
	user := func(index int, text string) string {
		return `{"type":"user","prompt_index":` + itoa(int64(index)) + `,"content":[{"type":"text","text":"<user_query>\n` + text + `\n</user_query>"}]}`
	}
	chat := filepath.Join(session, grokChatFile)
	write := func(updates []string, lines ...string) {
		os.WriteFile(filepath.Join(session, "updates.jsonl"), []byte(strings.Join(updates, "\n")+"\n"), 0o644)
		os.WriteFile(chat, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
		os.Chtimes(chat, now, now)
	}
	preamble := []string{`{"type":"system","content":"You are Grok"}`,
		`{"type":"user","content":[{"type":"text","text":"<user_info>\nOS Version: linux\n</user_info>"}]}`,
		`{"type":"user","content":[{"type":"text","text":"<system-reminder>\n앞으로 이건 시스템\n</system-reminder>"}],"synthetic_reason":"system_reminder"}`}
	lines := append(append([]string{}, preamble...),
		user(0, "옛날 이야기"),
		`{"type":"assistant","content":"네","model_id":"grok-4.7"}`,
		user(1, "앞으로 커밋 메시지는 한국어로 써줘 <private>토큰 abc</private>"),
		`{"type":"assistant","content":"","tool_calls":[{"id":"c1"}],"model_id":"grok-4.7"}`,
		`{"type":"tool_result","tool_call_id":"c1","content":"ok"}`,
		`{"type":"assistant","content":"알겠어요","model_id":"grok-4.7"}`,
		user(2, "아직 답 안 받은 말"),
		`{"type":"assistant","content":"","tool_calls":[{"id":"c2"}]}`)
	updates := []string{update(0, now.Add(-5*time.Hour)), update(1, now.Add(-time.Minute)), update(2, now.Add(-30*time.Second))}
	write(updates, lines...)
	spool := func() []string { s, _ := filepath.Glob(filepath.Join(spoolDir(), "*.json")); return s }
	root := filepath.Join(dir, "sessions")
	if n, err := logsCaptureFrom(root, grokChatFile, grokChat, now); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	raw, _ := os.ReadFile(spool()[0])
	job := string(raw)
	for _, bad := range []string{"옛날", "시스템", "user_query", "user_info", "abc", "아직"} {
		if strings.Contains(job, bad) {
			t.Fatal(bad, job)
		}
	}
	if !strings.Contains(job, "커밋 메시지는 한국어로") || !strings.Contains(job, `"origin": "grok"`) || !strings.Contains(job, `"session_id": "01a0-grok-1"`) ||
		!strings.Contains(job, filepath.ToSlash(work)) {
		t.Fatal(job)
	}
	if n, _ := logsCaptureFrom(root, grokChatFile, grokChat, now); n != 0 {
		t.Fatal("queued twice", n)
	}
	// The pending prompt is answered later.
	write(updates, append(lines, `{"type":"assistant","content":"끝","model_id":"grok-4.7"}`)...)
	if n, _ := logsCaptureFrom(root, grokChatFile, grokChat, now); n != 1 || len(spool()) != 2 {
		t.Fatal("answered later", n)
	}
	// Paused or excluded: nothing.
	write(append(updates, update(3, now)), append(lines, `{"type":"assistant","content":"끝"}`, user(3, "새 말"), `{"type":"assistant","content":"응"}`)...)
	os.WriteFile(settings, []byte(`{"paused":true}`), 0o600)
	if n, _ := logsCaptureFrom(root, grokChatFile, grokChat, now); n != 0 {
		t.Fatal("paused captured", n)
	}
	os.WriteFile(settings, []byte(`{"exclude":["`+filepath.ToSlash(work)+`"]}`), 0o600)
	if n, _ := logsCaptureFrom(root, grokChatFile, grokChat, now); n != 0 {
		t.Fatal("excluded captured", n)
	}
	os.WriteFile(settings, []byte(`{}`), 0o600)
	if n, _ := logsCaptureFrom(root, grokChatFile, grokChat, now); n != 1 {
		t.Fatal("resumed", n)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
