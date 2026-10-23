package crew

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

func TestActionAssistReportsBadRequestsAsFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		a    map[string]any
		want string
	}{
		"not json":       {map[string]any{"kind": "fix", "prompt": "{"}, "ValueError"},
		"empty fix":      {map[string]any{"kind": "fix"}, "PR 번호가 올바르지 않아요"},
		"bad finding":    {map[string]any{"kind": "fix", "prompt": `{"number":1,"repo":"o/r","path":"../x","text":"t"}`}, "지적 정보가 올바르지 않아요"},
		"bad comfy host": {map[string]any{"kind": "comfy", "prompt": `{"url":"file:///etc"}`}, "ComfyUI 주소가 올바르지 않아요"},
	} {
		got := actionAssist(tc.a, "a1")
		if got["status"] != "failed" || !strings.Contains(pyStr(got["error"]), tc.want) {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestInstallAssistWritesOnceAndReportsClashes(t *testing.T) {
	prompt := `{"name":"tidy-notes","content":"---\nname: tidy-notes\n---\n1. 정리\n\n","agents":["claude_code","codex","gemini"]}`
	got := installAssist(map[string]any{"prompt": prompt})
	if got["status"] != "done" || got["output"] != `["~/.agents/skills/tidy-notes/SKILL.md", "~/.claude/skills/tidy-notes/SKILL.md"]` {
		t.Fatal(got)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(filepath.Join(host.Home, ".agents/skills/tidy-notes"))
		_ = os.RemoveAll(filepath.Join(host.Home, ".claude/skills/tidy-notes"))
	})
	raw, _ := os.ReadFile(filepath.Join(host.Home, ".claude/skills/tidy-notes/SKILL.md"))
	if string(raw) != "---\nname: tidy-notes\n---\n1. 정리\n" {
		t.Fatalf("%q", raw)
	}
	if again := installAssist(map[string]any{"prompt": prompt}); again["status"] != "failed" || !strings.Contains(pyStr(again["error"]), "이미 있어요") {
		t.Fatal(again)
	}
	for _, p := range []string{"{", `{"name":"Bad Name","content":"x"}`, `{"name":"ok","content":"x","agents":["nobody"]}`} {
		if res := installAssist(map[string]any{"prompt": p}); res["status"] != "failed" || res["error"] == "" {
			t.Errorf("%s: %v", p, res)
		}
	}
	if res := installAssist(map[string]any{}); res["status"] != "failed" {
		t.Fatal(res)
	}
}

func TestKeepOutputsCopiesIntoAnExistingFolder(t *testing.T) {
	outputs := t.TempDir()
	t.Setenv("ARKISM_CREW_OUTPUTS", outputs)
	if got := keepOutputs(filepath.Join(t.TempDir(), "gone"), "a1"); got != "" {
		t.Fatal(got)
	}
	empty := t.TempDir()
	if got := keepOutputs(empty, "a1"); got != "" || exists(empty) {
		t.Fatal(got, exists(empty))
	}
	// The destination exists already, so the rename fails and the run's files are copied in.
	dest := filepath.Join(outputs, "a2")
	if err := os.MkdirAll(filepath.Join(dest, "old"), 0o700); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	_ = os.MkdirAll(filepath.Join(work, "slides"), 0o700)
	_ = os.WriteFile(filepath.Join(work, "slides", "deck.md"), []byte("# 발표"), 0o600)
	if runtime.GOOS != "windows" { // symlinks need privileges on Windows
		_ = os.Symlink("slides/deck.md", filepath.Join(work, "latest"))
	}
	if got := keepOutputs(work, "a2"); got != dest || exists(work) {
		t.Fatal(got, exists(work))
	}
	if raw, err := os.ReadFile(filepath.Join(dest, "slides", "deck.md")); err != nil || string(raw) != "# 발표" {
		t.Fatal(string(raw), err)
	}
	if runtime.GOOS != "windows" {
		if link, err := os.Readlink(filepath.Join(dest, "latest")); err != nil || link != "slides/deck.md" {
			t.Fatal(link, err)
		}
	}
	if err := copyTree(filepath.Join(t.TempDir(), "missing"), dest); err == nil {
		t.Fatal("a missing source was copied")
	}
}

func TestAssistsRoundSkipsBusyAndAnswersTheRest(t *testing.T) {
	posted := map[string]string{}
	listed := 0
	patch(t, &runnerCall, func(path string, body any, _ time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/crew/assists?") {
			listed++
			return map[string]any{"items": []any{
				map[string]any{"id": "busy-1", "crew_assist": map[string]any{"kind": "install_skill"}},
				map[string]any{"id": "new-1", "crew_assist": map[string]any{"kind": "install_skill", "prompt": `{"name":"Bad"}`}},
			}}, nil
		}
		assistMu.Lock()
		posted[path] = dumpLine(body)
		assistMu.Unlock()
		return map[string]any{}, nil
	})
	assistMu.Lock()
	assistBusy["busy-1"] = true
	assistMu.Unlock()
	t.Cleanup(func() {
		assistMu.Lock()
		delete(assistBusy, "busy-1")
		assistMu.Unlock()
	})
	started, err := assistsRound()
	poolJobs.Wait()
	if err != nil || started != 1 || listed != 1 || len(posted) != 1 {
		t.Fatal(started, err, posted)
	}
	if body := posted["/v1/crew/assists/new-1/result"]; !strings.Contains(body, `"failed"`) || !strings.Contains(body, "스킬 이름이나 내용이 올바르지 않아요") {
		t.Fatal(posted)
	}
	patch(t, &runnerCall, func(string, any, time.Duration) (map[string]any, error) { return nil, os.ErrDeadlineExceeded })
	if n, err := assistsRound(); n != 0 || err == nil {
		t.Fatal(n, err)
	}
}
