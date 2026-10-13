package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogsCaptureQueuesAnsweredGeminiMessagesOnce(t *testing.T) {
	dir := t.TempDir()
	oldState, oldOrigin := capState, capOrigin
	t.Cleanup(func() { capState, capOrigin = oldState, oldOrigin })
	capState, capOrigin = filepath.Join(dir, "state"), "gemini"
	t.Setenv("ARKISM_CAPTURE_SETTINGS", filepath.Join(dir, "capture.json"))
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	chats := filepath.Join(dir, "tmp", "proj", "chats")
	os.MkdirAll(chats, 0o755)
	work := filepath.Join(dir, "work")
	os.MkdirAll(work, 0o755)
	file := filepath.Join(chats, "session-a.jsonl")
	write := func(lines ...string) {
		os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
		os.Chtimes(file, now, now)
	}
	meta := `{"sessionId":"g1","projectHash":"h","directories":["` + strings.ReplaceAll(work, `\`, `\\`) + `"]}`
	write(meta,
		`{"id":"u0","type":"user","timestamp":"2026-10-13T06:00:00Z","content":[{"text":"old talk"}]}`,
		`{"id":"a0","type":"gemini","timestamp":"2026-10-13T06:00:05Z","content":"ok"}`,
		`{"id":"u1","type":"user","timestamp":"2026-10-13T11:59:00Z","content":[{"text":"앞으로 커밋 메시지는 한국어로 써줘"}]}`,
		`{"id":"a1","type":"gemini","timestamp":"2026-10-13T11:59:05Z","content":"알겠어요"}`,
		`{"id":"u2","type":"user","timestamp":"2026-10-13T11:59:30Z","content":"still typing"}`)
	spool := func() []string { s, _ := filepath.Glob(filepath.Join(spoolDir(), "*.json")); return s }
	if n, err := logsCapture(filepath.Join(dir, "tmp"), now); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	jobs := spool()
	raw, _ := os.ReadFile(jobs[0])
	if len(jobs) != 1 || !strings.Contains(string(raw), "커밋 메시지") || strings.Contains(string(raw), "old talk") || !strings.Contains(string(raw), `"origin": "gemini"`) {
		t.Fatal(jobs, string(raw))
	}
	if n, _ := logsCapture(filepath.Join(dir, "tmp"), now); n != 0 {
		t.Fatal("queued twice", n)
	}
	write(meta, `{"id":"u2","type":"user","timestamp":"2026-10-13T11:59:30Z","content":"still typing"}`,
		`{"id":"a2","type":"gemini","timestamp":"2026-10-13T11:59:40Z","content":"done"}`)
	if n, _ := logsCapture(filepath.Join(dir, "tmp"), now); n != 1 {
		t.Fatal("answered later", n)
	}
	os.WriteFile(filepath.Join(work, captureIgnoreFile), nil, 0o644)
	write(meta, `{"id":"u3","type":"user","timestamp":"2026-10-13T11:59:50Z","content":"secret"}`, `{"id":"a3","type":"gemini","timestamp":"2026-10-13T11:59:55Z","content":"x"}`)
	if n, _ := logsCapture(filepath.Join(dir, "tmp"), now); n != 0 {
		t.Fatal("excluded folder captured", n)
	}
}
