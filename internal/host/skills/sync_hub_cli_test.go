package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// syncWorld is one plain skill folder under a temp root, with the hub faked through syncPost.
type syncWorld struct {
	root, token, skill string
	imports            []map[string]any
	importErr          error
}

func newSyncWorld(t *testing.T, body string) *syncWorld {
	d := swapSync(t)
	w := &syncWorld{root: t.TempDir(), token: filepath.Join(d, "token")}
	w.skill = filepath.Join(w.root, "tidy", "SKILL.md")
	write(t, w.skill, body)
	write(t, w.token, "synthetic-local-token\n")
	syncRoots = func() []skillRoot { return []skillRoot{{"agent-skills", w.root, "generic"}} }
	syncPost = func(_, token, path string, payload any) (map[string]any, error) {
		if token != "synthetic-local-token" {
			return nil, errors.New("wrong token")
		}
		if strings.HasSuffix(path, "/resolve") {
			return map[string]any{"items": []any{map[string]any{"ref": "tidy@latest-approved", "error": "skill_not_found"}}}, nil
		}
		if w.importErr != nil {
			return nil, w.importErr
		}
		w.imports = append(w.imports, payload.(map[string]any))
		return map[string]any{"reused": false}, nil
	}
	return w
}

var syncTestScope = map[string]any{"workspace_id": "w", "project_id": "p", "user_id": "u", "agent_id": ""}

func (w *syncWorld) run(t *testing.T, execute bool) host.Obj {
	t.Helper()
	out, err := syncRun("http://hub", w.token, syncTestScope, 50, execute)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSyncWithoutAnalysisModelListsSkillFromFrontmatter(t *testing.T) {
	w := newSyncWorld(t, "---\nname: tidy\ndescription: 메모를 정리해요\n---\n1. 정리\n")
	llmURL = ""
	if got := w.run(t, false); canon(t, objGet(got, "new")) != "1" || len(w.imports) != 0 {
		t.Fatal(got, w.imports)
	}
	got := w.run(t, true)
	if canon(t, objGet(got, "new")) != "1" || canon(t, objGet(got, "generated_analyses")) != "0" || len(w.imports) != 1 {
		t.Fatal(got)
	}
	analysis := asMap(w.imports[0]["analysis"])
	if analysis["name"] != "tidy" || analysis["description"] != "메모를 정리해요" || canon(t, analysis["use_when"]) != `["메모를 정리해요"]` ||
		analysis["source_file"] != "SKILL.md" || w.imports[0]["skill_id"] != "tidy" {
		t.Fatal(canon(t, analysis))
	}
	if _, err := os.Stat(syncCache); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("a frontmatter-only analysis was cached", err)
	}
	// The state remembers the import: the next run sends nothing.
	if got := w.run(t, true); canon(t, objGet(got, "up_to_date")) != "1" || len(w.imports) != 1 {
		t.Fatal(got)
	}
}

func TestSyncFrontmatterWithoutDescriptionGetsAPlainDefault(t *testing.T) {
	got := basicAnalysis("pack/tidy", "---\nname: \n---\nbody", "h")
	if got["name"] != "pack/tidy" || got["description"] != "SKILL.md에 설명이 없는 스킬이에요." || got["problem"] != got["description"] {
		t.Fatal(got)
	}
}

func TestSyncReportsAHubRefusalPerSkill(t *testing.T) {
	w := newSyncWorld(t, "---\nname: tidy\ndescription: d\n---\nbody\n")
	llmURL = ""
	w.importErr = &host.HTTPError{Code: 400, Body: []byte(`{"code":"invalid_request","detail":"secret body"}`)}
	got := w.run(t, true)
	if canon(t, objGet(got, "failed")) != `{"tidy":"400 invalid_request"}` {
		t.Fatal(canon(t, got))
	}
	// A refused skill is not remembered as imported.
	w.importErr = nil
	if got := w.run(t, true); canon(t, objGet(got, "new")) != "1" || len(w.imports) != 1 {
		t.Fatal(got)
	}
}

func TestSyncGeneratesAnalysisAndUpdatesTheReviewedCatalog(t *testing.T) {
	w := newSyncWorld(t, "---\nname: tidy\ndescription: d\n---\nbody v2\n")
	model := `{"name": "tidy", "description": "d", "problem": "p", "use_when": ["u"], "avoid_when": [], "input_format": "i", "output_format": "o", "success_criteria": ["s"], "verification": ["v"], "change_summary": "본문 수정"}`
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		io.WriteString(rw, host.MustDumps(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": model}}}}, host.DumpOpts{}))
	}))
	defer srv.Close()
	llmURL = srv.URL
	write(t, llmKey, "key")
	write(t, syncCatalog, `{"skill_id": "tidy", "content_hash": "old", "name": "tidy", "source_path": "/skills/tidy", "use_when": ["u"]}`+"\n")
	got := w.run(t, true)
	if canon(t, objGet(got, "generated_analyses")) != "1" || canon(t, objGet(got, "catalog_updated")) != `["tidy"]` || len(w.imports) != 1 {
		t.Fatal(canon(t, got))
	}
	if cached, err := os.ReadFile(syncCache); err != nil || !strings.Contains(string(cached), `"skill_id": "tidy"`) || strings.Contains(string(cached), "change_summary") {
		t.Fatal(string(cached), err)
	}
	catalog, _ := os.ReadFile(syncCatalog)
	if !strings.Contains(string(catalog), `"name": "tidy"`) || !strings.Contains(string(catalog), `"source_path": "/skills/tidy"`) {
		t.Fatal(string(catalog))
	}
	if ch, _ := os.ReadFile(syncChanges); !strings.Contains(string(ch), "본문 수정") {
		t.Fatal(string(ch))
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string { return "slow" }
func (timeoutErr) Timeout() bool { return true }

func TestSyncFailureCodesNeverCarryMessages(t *testing.T) {
	var syntax *json.SyntaxError
	syntaxErr := json.Unmarshal([]byte("{x"), new(any))
	if !errors.As(syntaxErr, &syntax) {
		t.Fatal(syntaxErr)
	}
	for want, err := range map[any]error{
		403:                       &host.HTTPError{Code: 403},
		"needs_analysis":          hubErr("needs_analysis", "private detail"),
		"FileNotFoundError":       fmt.Errorf("read: %w", fs.ErrNotExist),
		"PermissionError":         &fs.PathError{Op: "open", Path: "/p", Err: fs.ErrPermission},
		"OSError":                 &fs.PathError{Op: "open", Path: "/p", Err: errors.New("io")},
		"JSONDecodeError":         syntaxErr,
		"KeyError":                keyErr("reused"),
		"URLError":                timeoutErr{},
		"ValueError":              errors.New("UnicodeDecodeError"),
		"502 upstream: bad model": &host.HTTPError{Code: 502, Body: []byte(`{"error":{"code":"upstream","message":"bad model"}}`)},
	} {
		if got := failureCode(err); got != want {
			t.Errorf("%v: %v", want, got)
		}
	}
}

func TestPostHubSendsBearerJSONToTheTrimmedURL(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization") + " " + r.Header.Get("Content-Type") + " " + string(raw)
		io.WriteString(w, `{"ok": true}`)
	}))
	defer srv.Close()
	got, err := postHub(srv.URL+"/", "tok", "/v1/skills/import", map[string]any{"a": 1})
	if err != nil || got["ok"] != true || seen != `POST /v1/skills/import Bearer tok application/json {"a": 1}` {
		t.Fatal(got, err, seen)
	}
}

// swapHome points the inventory at a temp home (never this machine's skills).
func swapHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	h, root := host.Home, host.SkillsRoot
	host.Home, host.SkillsRoot = home, filepath.Join(home, "skills")
	t.Cleanup(func() { host.Home, host.SkillsRoot = h, root })
	return home
}

func skillsCLI(args ...string) (int, string, string) {
	var out, errOut strings.Builder
	rc := skillsMain(args, nil, &out, &errOut)
	return rc, out.String(), errOut.String()
}

func TestSkillsInventoryCountsDefaultRepoAndExtraRoots(t *testing.T) {
	home := swapHome(t)
	skill := "---\nname: s\ndescription: d\n---\nbody\n"
	write(t, filepath.Join(home, ".claude/skills/a/SKILL.md"), skill)
	write(t, filepath.Join(home, "110-app/.agents/skills/b/SKILL.md"), skill)
	write(t, filepath.Join(home, "notes/.claude/skills/c/SKILL.md"), skill) // not a numbered repository
	extra := t.TempDir()
	write(t, filepath.Join(extra, "d/SKILL.md"), "no frontmatter\n")
	rc, out, errOut := skillsCLI("inventory", "--root", "extra="+extra)
	if rc != 0 {
		t.Fatal(rc, errOut)
	}
	v, err := host.Loads(out)
	if err != nil {
		t.Fatal(err)
	}
	report := asMap(v)
	if got := canon(t, report["by_source"]); !strings.Contains(got, `"claude-code":1`) || !strings.Contains(got, `"repo:110-app/.agents/skills":1`) ||
		!strings.Contains(got, `"extra":1`) || strings.Contains(got, "notes") {
		t.Fatal(got)
	}
	if canon(t, report["missing_frontmatter"]) != "1" {
		t.Fatal(canon(t, report))
	}
}

func TestSkillsCommandResolveAndProject(t *testing.T) {
	swapHome(t)
	_, url, cache := newStub(t)
	t.Setenv("ARKISM_URL", url)
	t.Setenv("ARKISM_API_TOKEN", "t")
	t.Setenv("ARKISM_SKILL_CACHE", cache)
	t.Setenv("ARKISM_SKILL_SCOPE", `{"workspace_id":"w","project_id":"p","user_id":"u","agent_id":"a"}`)
	if rc, out, errOut := skillsCLI("resolve", "code-review@1"); rc != 0 || !strings.Contains(out, `"resolved_version": 1`) {
		t.Fatal(rc, out, errOut)
	}
	target := t.TempDir()
	rc, out, errOut := skillsCLI("project", "code-review@1", "--runtime", "codex", "--target", target)
	if rc != 0 || !strings.HasPrefix(strings.TrimSpace(out), target) {
		t.Fatal(rc, out, errOut)
	}
	if raw, err := os.ReadFile(filepath.Join(strings.TrimSpace(out), "SKILL.md")); err != nil || string(raw) != hubV1 {
		t.Fatal(string(raw), err)
	}
	if rc, _, _ := skillsCLI("project", "code-review@1", "--runtime", "vim", "--target", target); rc != 2 {
		t.Fatal(rc)
	}
	t.Setenv("ARKISM_SKILL_SCOPE", "{")
	if rc, _, errOut := skillsCLI("resolve", "code-review@1"); rc != 1 || errOut == "" {
		t.Fatal(rc, errOut)
	}
	for _, args := range [][]string{nil, {"--help"}, {"nope"}, {"resolve"}} {
		if rc, _, errOut := skillsCLI(args...); rc != 2 || errOut == "" {
			t.Errorf("%v: %d %q", args, rc, errOut)
		}
	}
}

func TestRouterPostJSONHidesRefusalBodies(t *testing.T) {
	swapRouter(t)
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		if r.URL.Path == "/denied" {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"detail":"secret body"}`)
			return
		}
		io.WriteString(w, `{"ok": true}`)
	}))
	defer srv.Close()
	routerBaseURL = srv.URL
	token := filepath.Join(t.TempDir(), "token")
	write(t, token, " router-token\n")
	refused := func(code int) error { return fmt.Errorf("refused %d", code) }
	if got, err := postJSON("/ok", nil, token, refused); err != nil || got["ok"] != true {
		t.Fatal(got, err)
	}
	if _, err := postJSON("/denied", map[string]any{"a": 1}, token, refused); err == nil || err.Error() != "refused 403" {
		t.Fatal(err)
	}
	if strings.Join(seen, "|") != "GET /ok Bearer router-token|POST /denied Bearer router-token" {
		t.Fatal(seen)
	}
	if _, err := postJSON("/ok", nil, filepath.Join(t.TempDir(), "none"), refused); err == nil {
		t.Fatal("a missing token was accepted")
	}
	routerBaseURL = "http://127.0.0.1:1"
	if _, err := postJSON("/ok", nil, token, refused); err == nil || err.Error() != "Arkism request failed" {
		t.Fatal(err)
	}
}

func TestRecordSkillUsageSendsOnlyIDsAndOutcome(t *testing.T) {
	swapRouter(t)
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, _ := host.Decode(r.Body)
		body = asMap(v)
		if r.URL.Path != "/v1/execution_traces/completed" {
			w.WriteHeader(404)
			return
		}
		io.WriteString(w, `{"trace": {"id": "tr"}, "run": {"id": "run"}, "learning": {"lesson": "l"}}`)
	}))
	defer srv.Close()
	routerBaseURL = srv.URL
	token := filepath.Join(t.TempDir(), "token")
	write(t, token, "agent-token")
	oldAgent := agentTokenFile
	agentTokenFile = token
	t.Cleanup(func() { agentTokenFile = oldAgent })

	if _, err := recordSkillUsage("never@1", "success", "", ""); err == nil {
		t.Fatal("an unloaded ref was recorded")
	}
	loadedSkills["code-review@1"] = loadedSkill{hubItem("code-review@1", 1, hubV1, "active"), time.Now().Add(-time.Second)}
	routerRuntime = "vim"
	if _, err := recordSkillUsage("code-review@1", "success", "", ""); err == nil || !strings.Contains(err.Error(), "runtime must be one of") {
		t.Fatal(err)
	}
	routerRuntime = "codex"
	out, err := recordSkillUsage("code-review@1", "cancelled", "review", "user_stopped")
	if err != nil || objGet(out, "trace_id") != "tr" || objGet(out, "run_id") != "run" || objGet(out, "learning") == nil {
		t.Fatal(out, err)
	}
	run := asMap(body["agent_run"])
	event := asMap(asMap(asList(body["events"])[0])["payload"])
	if run["outcome"] != "partial" || run["task_type"] != "review" || asMap(body["scope"])["agent_id"] == "" ||
		event["error_code"] != "user_stopped" || event["version_id"] != "skill-v1" || strings.Contains(canon(t, body), "Review v1") {
		t.Fatal(canon(t, body))
	}
	if _, err := recordSkillUsage("code-review@1", "success", "", "user_stopped"); err == nil {
		t.Fatal("an error code on success was accepted")
	}
}

func TestPythonStyleValueHelpers(t *testing.T) {
	for v, want := range map[string]any{"NoneType": nil, "bool": true, "str": "s", "int": json.Number("1"), "float": json.Number("1.5"),
		"list": []any{}, "dict": map[string]any{}, "object": 3} {
		if got := pyTypeName(want); got != v {
			t.Errorf("%v: %s", want, got)
		}
	}
	if attrErr([]any{}).Error() != "'list' object has no attribute 'get'" {
		t.Fatal(attrErr([]any{}))
	}
	if got, err := pyIter("한글"); err != nil || canon(t, got) != `["한","글"]` {
		t.Fatal(got, err)
	}
	if got, err := pyIter(nil); err != nil || got != nil {
		t.Fatal(got, err)
	}
	if _, err := pyIter(json.Number("1")); err == nil {
		t.Fatal("a number was iterable")
	}
	if n, err := pyLen("한글"); n != 2 || err != nil {
		t.Fatal(n, err)
	}
	if n, _ := pyLen(map[string]any{"a": 1}); n != 1 {
		t.Fatal(n)
	}
	if _, err := pyLen(true); err == nil || err.Error() != "object of type 'bool' has no len()" {
		t.Fatal(err)
	}
	if f, err := pyFloat(" 2.5 "); f != 2.5 || err != nil {
		t.Fatal(f, err)
	}
	if f, _ := pyFloat(true); f != 1 {
		t.Fatal(f)
	}
	if _, err := pyFloat("x"); err == nil || err.Error() != "could not convert string to float: 'x'" {
		t.Fatal(err)
	}
	if _, err := pyFloat(nil); err == nil || !strings.Contains(err.Error(), "'NoneType'") {
		t.Fatal(err)
	}
}
