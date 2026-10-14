package skills

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

func write(t *testing.T, p, text string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSpecificSkillsAreUnsupportedElsewhere(t *testing.T) {
	cases := map[string]string{
		"codex-plugin/example/example":            `{"claude_code":"unsupported"}`,
		"example-pack/codex/skills/example-skill": `{"claude_code":"unsupported"}`,
		"claude/docx":                       `{"codex":"unsupported"}`,
		"example-pack/skills/example-skill": `{}`,
		"claude-plugin/design/ux-copy":      `{}`,
	}
	for sid, want := range cases {
		if got := canon(t, runtimeCompatibility(sid)); got != want {
			t.Fatal(sid, got)
		}
	}
}

func TestClaudeAIPluginSkillIsNamedByPluginJSON(t *testing.T) {
	plugin := filepath.Join(t.TempDir(), "acct_org", "2ca4e30e")
	write(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), `{"name": "design"}`)
	got, err := syncSkillID(map[string]any{"source": "claude-plugins", "skill_id": "acct_org/2ca4e30e/skills/ux-copy", "path": filepath.Join(plugin, "skills/ux-copy/SKILL.md")})
	if err != nil || got != "claude-plugin/design/ux-copy" {
		t.Fatal(got, err)
	}
}

func TestStableIDsAcrossRuntimesAndPluginVersions(t *testing.T) {
	cases := [][3]string{
		{"agent-skills", "pdf-ocr/mineru-local", "pdf-ocr/mineru-local"},
		{"agent-plugins", "demo-plugin/skills/plug-skill", "kit-plugin/demo-plugin/plug-skill"},
		{"codex", ".system/imagegen", "codex/system/imagegen"},
		{"claude-code", "synced/abc_def/xlsx", "claude/xlsx"},
		{"agents", "impeccable", "agents/impeccable"},
		{"codex-plugins", "openai-curated-remote/google-drive/0.1.16/skills/google-docs", "codex-plugin/google-drive/google-docs"},
		{"codex-plugins", "example/example/1.0.0/.openclaw/skills/example", "codex-plugin/example/openclaw-example"},
	}
	for _, c := range cases {
		if got, err := syncSkillID(map[string]any{"source": c[0], "skill_id": c[1]}); err != nil || got != c[2] {
			t.Fatal(c, got, err)
		}
	}
}

func swapSync(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	cat, ch, rt, cache, state, url, key, post, roots := syncCatalog, syncChanges, syncRuntimeCatalog, syncCache, syncState, llmURL, llmKey, syncPost, syncRoots
	syncCatalog, syncChanges, syncRuntimeCatalog = filepath.Join(d, "c.jsonl"), filepath.Join(d, "ch.jsonl"), filepath.Join(d, "rt.jsonl")
	syncCache, syncState, llmKey = filepath.Join(d, "cache", "generated.jsonl"), filepath.Join(d, "state.json"), filepath.Join(d, "k")
	t.Cleanup(func() {
		syncCatalog, syncChanges, syncRuntimeCatalog, syncCache, syncState, llmURL, llmKey, syncPost, syncRoots = cat, ch, rt, cache, state, url, key, post, roots
	})
	return d
}

func TestNewVersionKeepsReviewedRulesAndLogsChange(t *testing.T) {
	d := swapSync(t)
	prevJSON := `{"skill_id": "design/x", "content_hash": "old", "name": "x", "source_path": "/home/user/skills/design/x", "use_when": ["검토된 사용 시점"], "avoid_when": ["검토된 회피"], "conflicts": ["x vs y"], "example_requests": ["예"]}`
	model := `{"name": "x", "description": "d", "problem": "p", "use_when": [], "avoid_when": ["검토된 회피", "새 기능 회피"], "input_format": "i", "output_format": "o", "success_criteria": ["s"], "verification": ["v"], "change_summary": "옵션 하나 추가"}`
	var sent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sent = string(raw)
		io.WriteString(w, host.MustDumps(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": model}}}}, host.DumpOpts{}))
	}))
	defer srv.Close()
	llmURL = srv.URL
	os.WriteFile(llmKey, []byte("key"), 0o600)
	prevAny, _ := loadsOrdered(prevJSON)
	prev := prevAny.(host.Obj)
	out, err := generateAnalysis("design/x", "---\nname: x\n---", prev, "")
	if err != nil {
		t.Fatal(err)
	}
	msg := asMap(asList(j(t, sent)["messages"])[1])["content"].(string)
	if !strings.Contains(msg, "검토된 사용 시점") {
		t.Fatal(msg)
	}
	if canon(t, objGet(out, "use_when")) != `["검토된 사용 시점"]` || canon(t, objGet(out, "avoid_when")) != `["검토된 회피","새 기능 회피"]` || objGet(out, "change_summary") != "옵션 하나 추가" {
		t.Fatal(out)
	}
	other := strings.Replace(strings.Replace(prevJSON, `"design/x"`, `"a/y"`, 1), `"/home/user/skills/design/x"`, `"/a"`, 1)
	os.WriteFile(syncCatalog, []byte(prevJSON+"\n"+other+"\n"), 0o644)
	updated := append(prev[:0:0], prev...)
	updated.Set("content_hash", "new")
	if err := writeReviewed(updated, host.Obj{{Key: "skill_id", Value: "design/x"}, {Key: "content_hash", Value: "new"}, {Key: "summary", Value: "s"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(syncCatalog)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], `{"skill_id": "a/y", "content_hash": "old"`) || !strings.HasPrefix(lines[1], `{"skill_id": "design/x", "content_hash": "new"`) {
		t.Fatal(lines)
	}
	if ch, _ := os.ReadFile(filepath.Join(d, "ch.jsonl")); j(t, string(ch))["summary"] != "s" {
		t.Fatal(string(ch))
	}
}

func TestAnalysisWithoutLLMURLAbortsTheRun(t *testing.T) {
	swapSync(t)
	llmURL = ""
	if _, err := generateAnalysis("x", "body", nil, ""); err != errNotConfigured {
		t.Fatal(err)
	}
}
