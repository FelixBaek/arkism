package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestInjectedChars(t *testing.T) {
	dir := capFake(t)
	var got []host.Obj
	metric = func(fields ...host.KV) { got = append(got, host.Obj(fields)) }
	logInjected("SessionStart", "s1", "")
	logInjected("SessionStart", "s1", "가나다 abc")
	if len(got) != 1 {
		t.Fatalf("empty text must not log: %v", got)
	}
	if c, _ := got[0].Get("chars"); c != 7 {
		t.Fatalf("chars are runes: %v", got[0])
	}
	if _, has := got[0].Get("text"); has {
		t.Fatal("text must never be logged")
	}
	rows := `{"ts":"2026-01-01T00:00:00Z","event":"inject","hook":"SessionStart","session":"old","chars":500}
{"ts":"2026-02-01T00:00:00Z","event":"inject","hook":"SessionStart","session":"a","chars":300}
{"ts":"2026-02-01T00:01:00Z","event":"inject","hook":"UserPromptSubmit","session":"a","chars":100}
{"ts":"2026-02-02T00:00:00Z","event":"inject","hook":"SessionStart","session":"b","chars":200}
{"ts":"2026-02-02T00:00:00Z","event":"recall","hook":"SessionStart","session":"b","injected":3}
{"ts":"2026-02-02T00:00:00Z","event":"inject","hook":"SessionStart","session":"","chars":999}
{"ts":"2026-02-02T00:00:00Z","event":"inject","hook":"SessionStart","session":"c","chars":1.5}
`
	if err := os.WriteFile(filepath.Join(dir, "metrics.jsonl"), []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	n, chars, err := injectedChars("2026-01-15T00:00:00Z")
	if err != nil || n != 2 || chars != 600 {
		t.Fatal(n, chars, err)
	}
	if n, chars, _ = injectedChars(""); n != 3 || chars != 1100 {
		t.Fatal("all time", n, chars)
	}
}

// The desktop's injection preview (desktop/ui/inject.js) rebuilds the SessionStart text from the
// same /v1/memories pages; both sides check one golden file so they cannot drift apart.
func TestSessionContextMatchesDesktopPreview(t *testing.T) {
	capFake(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "desktop", "ui", "testdata", "inject-session.json"))
	if err != nil {
		t.Fatal(err)
	}
	golden, err := loadObj(raw)
	if err != nil {
		t.Fatal(err)
	}
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		for _, kind := range []string{"user", "procedural"} {
			if strings.Contains(path, "kind="+kind+"&") || strings.HasSuffix(path, "kind="+kind) {
				return map[string]any{"items": golden[kind]}, nil
			}
		}
		t.Fatalf("unexpected get %s", path)
		return nil, nil
	}
	got, err := sessionContext("s", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if want := str(golden["want"]); got != want {
		t.Fatalf("SessionStart text differs from the desktop preview\n got: %q\nwant: %q", got, want)
	}
}
