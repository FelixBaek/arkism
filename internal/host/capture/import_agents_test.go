package capture

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// importHome makes a temp home with one file per source and points the package at it.
func importHome(t *testing.T) (home, repoDir string) {
	t.Helper()
	dir := t.TempDir()
	home = filepath.Join(dir, "home")
	oldHome, oldState := host.Home, capState
	t.Cleanup(func() { host.Home, capState = oldHome, oldState })
	host.Home, capState = home, filepath.Join(dir, "state")
	t.Setenv("ARKISM_CAPTURE_SETTINGS", filepath.Join(dir, "capture.json"))
	t.Setenv("ARKISM_REPO_TAGS", "1")

	write := func(rel, text string) {
		p := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude/CLAUDE.md", `# Arkism 중앙 연결

## 내 규칙

- 보고는 한국어로 짧게 한다.
- https://example.com/docs
- [링크](https://example.com)

`+"```bash\necho secret-command-in-code\n```"+`

| 이름 | 링크 |
|---|---|
| a | https://a.example |

## 스킬 사용

- 스킬의 승인 버전은 Arkism이 관리한다.

## 지속 메모리

- recall 로 더 찾는다.

## 작업 방식

커밋 전에 테스트를 돌린다.
`)
	write(".codex/AGENTS.md", "- Use gofmt before every commit.\n- /usr/local/go/bin\n")
	write(".gemini/GEMINI.md", "## 지속 메모리\n\n- managed text only\n")
	repoDir = filepath.Join(dir, "work", "my.repo")
	write("../work/my.repo/.git/config", "[remote \"origin\"]\n\turl = git@github.com:Owner/Name.git\n")
	proj := ".claude/projects/" + importEncode(repoDir) + "/memory/"
	write(proj+"MEMORY.md", "- [보고 짧게](report.md) — 결론부터 짧게\n- [Deploy steps](deploy.md) — compose 배포 후 nginx reload\n")
	write(proj+"report.md", "---\nname: 보고 짧게\ndescription: short\ntype: user\n---\n\n보고는 결론 1~2줄.\n\n**Why:** 긴 글 읽기 힘듦.\n")
	write(proj+"deploy.md", "---\nname: deploy steps\ntype: feedback\n---\nDeploy with compose then reload nginx.\n\n```\ndocker compose up -d\n```\n")
	write(proj+"facts.md", "---\nname: facts\ntype: project\n---\nThe API lives in the new-site container.\n")
	write(proj+"ref.md", "---\nname: refs\ntype: reference\n---\nDashboards are in Grafana board 12.\n")
	return home, repoDir
}

func importRun(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := importAgentsMain(args, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func TestImportAgentsSplitAndSkip(t *testing.T) {
	_, repoDir := importHome(t)
	stmts, rep := importCollect()
	texts := map[string]importStatement{}
	for _, s := range stmts {
		texts[s.Text] = s
		for _, bad := range []string{"승인 버전", "recall", "managed text", "secret-command", "https://", "/usr/local", "docker compose"} {
			if strings.Contains(s.Text, bad) {
				t.Errorf("statement %q should have been skipped (%s)", s.Text, bad)
			}
		}
	}
	for _, want := range []string{"내 규칙: 보고는 한국어로 짧게 한다.", "작업 방식: 커밋 전에 테스트를 돌린다.", "Use gofmt before every commit.",
		"보고 짧게 — 결론부터 짧게"} {
		if _, ok := texts[want]; !ok {
			t.Errorf("missing statement %q in %v", want, stmts)
		}
	}
	if rep.Skipped["arkism_section"] != 4 || rep.Skipped["code_block"] != 2 || rep.Skipped["table"] != 1 || rep.Skipped["path_or_url"] < 3 {
		t.Errorf("skipped = %v", rep.Skipped)
	}
	if got := claudeProjectPath(importEncode(repoDir)); got != repoDir {
		t.Errorf("project path = %q, want %q", got, repoDir)
	}
}

func TestImportAgentsFrontmatterKinds(t *testing.T) {
	importHome(t)
	stmts, _ := importCollect()
	want := map[string]string{
		"보고 짧게: 보고는 결론 1~2줄.\n\nWhy: 긴 글 읽기 힘듦.":               "user",
		"deploy steps: Deploy with compose then reload nginx.": "procedural",
		"facts: The API lives in the new-site container.":      "semantic",
		"refs: Dashboards are in Grafana board 12.":            "semantic",
		"내 규칙: 보고는 한국어로 짧게 한다.":                                "procedural",
	}
	got := map[string]importStatement{}
	for _, s := range stmts {
		got[s.Text] = s
	}
	for text, kind := range want {
		s, ok := got[text]
		if !ok {
			t.Errorf("missing %q", text)
			continue
		}
		if s.Kind != kind {
			t.Errorf("%q kind = %s, want %s", text, s.Kind, kind)
		}
	}
	if s := got["deploy steps: Deploy with compose then reload nginx."]; s.Repo != "owner/name" {
		t.Errorf("repo = %q", s.Repo)
	}
	if s := got["보고 짧게: 보고는 결론 1~2줄.\n\nWhy: 긴 글 읽기 힘듦."]; s.Repo != "" {
		t.Errorf("a user preference must not carry a repo, got %q", s.Repo)
	}
}

func TestImportAgentsDryRunSummary(t *testing.T) {
	importHome(t)
	capPost = func(string, any, time.Duration) (map[string]any, error) {
		t.Fatal("dry run must not send")
		return nil, nil
	}
	t.Cleanup(func() {
		capPost = func(p string, b any, d time.Duration) (map[string]any, error) { return host.Post(p, b, d) }
	})
	var rep importReport
	if err := json.Unmarshal([]byte(importRun(t, "--dry-run")), &rep); err != nil {
		t.Fatal(err)
	}
	// CLAUDE.md 2, AGENTS.md 1, MEMORY.md 2, four topic files 4; GEMINI.md has only Arkism's section.
	if rep.Files != 8 || rep.Statements != 9 {
		t.Errorf("summary = %+v", rep)
	}
	if rep.ByKind["procedural"] != 4 || rep.ByKind["semantic"] != 4 || rep.ByKind["user"] != 1 {
		t.Errorf("by_kind = %v", rep.ByKind)
	}
}

func TestImportAgentsStableIDsAndDedupe(t *testing.T) {
	importHome(t)
	a, _ := importCollect()
	b, _ := importCollect()
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("runs differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatalf("id %d changed: %s vs %s", i, a[i].ID, b[i].ID)
		}
	}
	if importEventID(a[:1]) != importEventID(b[:1]) {
		t.Fatal("event id not stable")
	}

	var bodies []map[string]any
	capPost = func(path string, body any, _ time.Duration) (map[string]any, error) {
		raw, _ := json.Marshal(body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		bodies = append(bodies, m)
		items := []any{}
		for range m["claims"].([]any) {
			items = append(items, map[string]any{"status": "candidate", "reason": "review_required"})
		}
		return map[string]any{"items": items}, nil
	}
	t.Cleanup(func() {
		capPost = func(p string, b any, d time.Duration) (map[string]any, error) { return host.Post(p, b, d) }
	})
	var first importSent
	if err := json.Unmarshal([]byte(importRun(t)), &first); err != nil {
		t.Fatal(err)
	}
	if first != (importSent{Sent: 9, Candidates: 9}) {
		t.Errorf("first run = %+v", first)
	}
	for _, m := range bodies {
		ev := m["event"].(map[string]any)
		if ev["type"] != "backfill" || !strings.HasPrefix(ev["id"].(string), "agent-import:") {
			t.Errorf("event = %v", ev)
		}
		for _, c := range m["claims"].([]any) {
			if c.(map[string]any)["basis"] != "inferred" {
				t.Errorf("claim basis = %v", c)
			}
		}
	}
	posts := len(bodies)
	var second importSent
	if err := json.Unmarshal([]byte(importRun(t)), &second); err != nil {
		t.Fatal(err)
	}
	if second != (importSent{Duplicates: 9}) || len(bodies) != posts {
		t.Errorf("second run = %+v, posts %d -> %d", second, posts, len(bodies))
	}
}

func TestImportAgentsExcludedFolder(t *testing.T) {
	_, repoDir := importHome(t)
	settings, _ := json.Marshal(map[string]any{"exclude": []string{repoDir, filepath.Join(host.Home, ".codex")}})
	if err := os.WriteFile(os.Getenv("ARKISM_CAPTURE_SETTINGS"), settings, 0o644); err != nil {
		t.Fatal(err)
	}
	stmts, rep := importCollect()
	for _, s := range stmts {
		if s.Origin == "codex" || strings.Contains(s.Label, "/memory/") {
			t.Errorf("excluded source imported: %s", s.Label)
		}
	}
	if rep.Skipped["excluded_folder"] != 6 {
		t.Errorf("skipped = %v", rep.Skipped)
	}
}
