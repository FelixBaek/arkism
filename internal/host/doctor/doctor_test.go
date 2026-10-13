package doctor

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fixture struct {
	t        *testing.T
	home     string
	env      *env
	installs int
}

func write(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

func jsonText(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// healthy builds a temp home with the app in local mode, Claude and Codex connected, and
// an empty capture queue.
func healthy(t *testing.T, ready int) *fixture {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" || r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(ready)
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	f := &fixture{t: t, home: home}
	f.env = &env{
		home:      home,
		connPath:  filepath.Join(home, "data", "dev.arkism.desktop", "runtime", "agents.json"),
		goos:      runtime.GOOS,
		lookPath:  func(name string) (string, error) { return filepath.Join(home, "bin", name), nil },
		http:      srv.Client(),
		freeBytes: func(string) (uint64, error) { return 10 << 30, nil },
		runInstall: func() error {
			f.installs++
			f.writeAgents(true)
			return nil
		},
	}
	token := filepath.Join(home, "data", "dev.arkism.desktop", "runtime", "token")
	write(t, token, "SECRET-TOKEN-VALUE", 0o600)
	write(t, f.env.connPath, jsonText(t, map[string]any{"mode": "local", "url": srv.URL, "token_file": token}), 0o600)
	write(t, f.env.client(), "#!/bin/sh\n", 0o755)
	f.writeAgents(true)
	if err := os.MkdirAll(filepath.Join(f.env.folder(), "local-capture-claude_code", "spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.env.folder(), "capture.json"), `{"paused": false, "exclude": ["/private/a", "/private/b"]}`, 0o600)
	return f
}

func (f *fixture) writeAgents(withStop bool) {
	client := f.env.client()
	handler := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": `"` + client + `" windows hook claude_code`}}}}
	hooks := map[string]any{}
	for _, ev := range hookEvents4 {
		hooks[ev] = handler
	}
	write(f.t, filepath.Join(f.home, ".claude", "settings.json"), jsonText(f.t, map[string]any{"hooks": hooks, "theme": "dark"}), 0o600)
	write(f.t, filepath.Join(f.home, ".claude.json"), jsonText(f.t, map[string]any{"mcpServers": map[string]any{
		"skill-router": map[string]any{"command": client, "args": []any{"windows", "mcp", "claude_code"}}}}), 0o600)
	codex := map[string]any{}
	for _, ev := range hookEvents4 {
		if ev != "Stop" || withStop {
			codex[ev] = handler
		}
	}
	write(f.t, filepath.Join(f.home, ".codex", "hooks.json"), jsonText(f.t, map[string]any{"hooks": codex}), 0o600)
	write(f.t, filepath.Join(f.home, ".codex", "config.toml"), "model = \"x\"\n\n[mcp_servers.skill-router]\ncommand = \""+client+"\"\nargs = [\"windows\", \"mcp\", \"codex\"]\n", 0o600)
}

func (f *fixture) run(args ...string) (int, Report, string) {
	f.t.Helper()
	var out, errb bytes.Buffer
	code := runMain(f.env, append([]string{"--json"}, args...), &out, &errb)
	var r Report
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		f.t.Fatalf("bad JSON %q: %v", out.String(), err)
	}
	if strings.Contains(out.String(), "SECRET") || strings.Contains(out.String(), "/private/") {
		f.t.Fatalf("report leaks a secret, message text or folder: %s", out.String())
	}
	return code, r, out.String()
}

func find(t *testing.T, r Report, id string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no check %s in %+v", id, r.Checks)
	return Check{}
}

func TestHealthy(t *testing.T) {
	f := healthy(t, http.StatusOK)
	code, r, raw := f.run()
	if code != 0 || !r.OK || r.Summary[Warn] != 0 || r.Summary[Fail] != 0 {
		t.Fatalf("code %d report %s", code, raw)
	}
	for _, id := range []string{"app.connection", "app.token", "app.readyz", "client", "agent.claude", "agent.codex", "cli.claude", "cli.codex", "queue.claude_code", "capture.settings", "disk"} {
		find(t, r, id)
	}
	if got := find(t, r, "capture.settings").Details["excluded"]; got != float64(2) {
		t.Fatalf("excluded = %v", got)
	}
	var text bytes.Buffer
	if runMain(f.env, nil, &text, &text) != 0 || !strings.Contains(text.String(), "[정상] agent.claude") {
		t.Fatalf("text report: %s", text.String())
	}
}

func TestServerModeAndReadyzDown(t *testing.T) {
	f := healthy(t, http.StatusServiceUnavailable)
	_, r, _ := f.run()
	if c := find(t, r, "app.readyz"); c.Status != Fail {
		t.Fatalf("readyz %+v", c)
	}
	write(t, f.env.connPath, `{"mode": "server"}`, 0o600)
	code, r, _ := f.run()
	if code != 0 || find(t, r, "app.connection").Details["mode"] != "server" {
		t.Fatalf("server mode: %d %+v", code, r.Checks)
	}
}

func TestMissingHooksFixReinstalls(t *testing.T) {
	f := healthy(t, http.StatusOK)
	f.writeAgents(false)
	os.Remove(filepath.Join(f.home, ".claude.json"))
	code, r, _ := f.run()
	if code != 1 || find(t, r, "agent.codex").Status != Fail || !strings.Contains(find(t, r, "agent.codex").Message, "Stop") {
		t.Fatalf("codex: %d %+v", code, find(t, r, "agent.codex"))
	}
	if c := find(t, r, "agent.claude"); c.Status != Fail || !strings.Contains(c.Message, "skill-router") {
		t.Fatalf("claude: %+v", c)
	}
	code, r, _ = f.run("--fix")
	if code != 0 || f.installs != 1 || r.Fixes[0].ID != "fix.install" || r.Fixes[0].Status != OK {
		t.Fatalf("fix: %d installs %d %+v", code, f.installs, r.Fixes)
	}
	f.env.runInstall = func() error { return errors.New("boom") }
	if runtime.GOOS == "windows" {
		// Windows has no execute bit; a missing client is the Windows way to break it.
		os.Remove(f.env.client())
	} else {
		os.Chmod(f.env.client(), 0o644)
	}
	_, r, _ = f.run("--fix")
	if r.Fixes[0].Status != Fail || find(t, r, "client").Status != Fail {
		t.Fatalf("failed install: %+v", r)
	}
}

func TestFailedSpoolAndRequeue(t *testing.T) {
	f := healthy(t, http.StatusOK)
	q := filepath.Join(f.env.folder(), "local-capture-claude_code")
	write(t, filepath.Join(q, "failed", "a.json"), `{"session_id": "s1", "turns": ["SECRET message text"], "attempts": 5, "last_error": "http_503", "queued_at": "2026-10-13T00:00:00Z"}`, 0o600)
	write(t, filepath.Join(q, "failed", "b.json"), `{"attempts": 5, "last_error": "SECRET free text, not a code"}`, 0o600)
	os.Chtimes(filepath.Join(q, "failed", "b.json"), timeAgo(3600), timeAgo(3600))
	write(t, filepath.Join(q, "spool", "c.json"), `{"attempts": 1}`, 0o600)
	write(t, filepath.Join(f.env.folder(), "capture.json"), `{"paused": true}`, 0o600)

	code, r, _ := f.run()
	c := find(t, r, "queue.claude_code")
	if code != 0 || c.Status != Warn || c.Details["failed"] != float64(2) || c.Details["spool"] != float64(1) || c.Details["last_error"] != "http_503" {
		t.Fatalf("queue: %d %+v", code, c)
	}
	if s := find(t, r, "capture.settings"); s.Status != Warn || s.Details["paused"] != true {
		t.Fatalf("settings: %+v", s)
	}

	_, r, _ = f.run("--fix")
	if f.installs != 0 {
		t.Fatalf("installer ran for a healthy setup")
	}
	if c := find(t, r, "queue.claude_code"); c.Status != OK || c.Details["spool"] != float64(3) || c.Details["failed"] != float64(0) {
		t.Fatalf("after fix: %+v fixes %+v", c, r.Fixes)
	}
	if r.Fixes[0].ID != "fix.requeue.claude_code" || !strings.Contains(r.Fixes[0].Message, "2건") {
		t.Fatalf("fixes: %+v", r.Fixes)
	}
	var job map[string]any
	data, _ := os.ReadFile(filepath.Join(q, "spool", "a.json"))
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	if job["attempts"] != float64(0) || job["last_error"] != nil || job["session_id"] != "s1" || job["queued_at"] != "2026-10-13T00:00:00Z" {
		t.Fatalf("requeued job: %v", job)
	}
}

func TestNoAgentsAndMissingCLIs(t *testing.T) {
	f := healthy(t, http.StatusOK)
	os.RemoveAll(filepath.Join(f.home, ".claude"))
	os.RemoveAll(filepath.Join(f.home, ".codex"))
	f.env.lookPath = func(string) (string, error) { return "", errors.New("not found") }
	f.env.freeBytes = func(string) (uint64, error) { return 1 << 30, nil }
	code, r, _ := f.run()
	if code != 0 || find(t, r, "agents").Status != Warn || find(t, r, "cli.claude").Status != Warn || find(t, r, "disk").Status != Warn {
		t.Fatalf("%d %+v", code, r.Checks)
	}
}

func TestWindowsClaudeShim(t *testing.T) {
	f := healthy(t, http.StatusOK)
	f.env.goos = "windows"
	npm := filepath.Join(f.home, "npm")
	write(t, filepath.Join(npm, "node_modules", "@anthropic-ai", "claude-code", "bin", "claude.exe"), "", 0o755)
	f.env.lookPath = func(name string) (string, error) { return filepath.Join(npm, name+".cmd"), nil }
	_, r, _ := f.run()
	c := find(t, r, "cli.claude")
	if c.Details["npm_shim"] != true || c.Details["native_exe"] != true {
		t.Fatalf("%+v", c)
	}
}

func timeAgo(seconds int) time.Time { return time.Now().Add(-time.Duration(seconds) * time.Second) }
