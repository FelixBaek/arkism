package skills

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// reviewHub is a fake Arkism skill hub for the skill-review commands.
type reviewHub struct {
	mu    sync.Mutex
	items map[string]map[string]any // resolve answers by ref
	calls []string                  // "METHOD path token"
	posts map[string][]map[string]any
	// availability last stored by POST .../availability
	status string
	compat any
}

const ownerTok, workerTok = "owner-t", "worker-t"

func (h *reviewHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	h.calls = append(h.calls, r.Method+" "+r.URL.Path+" "+tok)
	var body map[string]any
	if r.Method == "POST" {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		h.posts[r.URL.Path] = append(h.posts[r.URL.Path], body)
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	p := r.URL.Path
	switch {
	case p == "/v1/skills/resolve":
		ref, _ := body["refs"].([]any)[0].(string)
		if ref == "boom@1" {
			http.Error(w, `{"detail":"exploded"}`, 500)
			return
		}
		if item, ok := h.items[ref]; ok {
			reply(map[string]any{"items": []any{item}})
		} else {
			reply(map[string]any{"items": []any{map[string]any{"error": "skill_not_found"}}})
		}
	case p == "/v1/skills" && r.Method == "GET":
		if r.URL.Query().Get("cursor") == "" {
			reply(map[string]any{"items": []any{map[string]any{"id": "demo"}}, "withheld": []any{"held"}, "next_cursor": "c2"})
		} else {
			reply(map[string]any{"items": []any{}})
		}
	case strings.HasPrefix(p, "/v1/skills/") && r.Method == "GET":
		id := strings.TrimPrefix(p, "/v1/skills/")
		if id == "held" {
			reply(map[string]any{"id": id, "active_version": "h1"})
			return
		}
		reply(map[string]any{"id": id, "revision": 7, "status": h.status, "active_version": "v1",
			"version_history": []any{"v1", "v2", "v3"}, "skill_compatibility": h.compat})
	case strings.HasPrefix(p, "/v1/skill_versions/") && r.Method == "GET":
		id := strings.TrimPrefix(p, "/v1/skill_versions/")
		status := "candidate"
		if id == "v3" {
			status = "rejected"
		}
		reply(map[string]any{"id": id, "revision": 2, "status": status, "skill_id": "demo", "created_at": "2026-10-01",
			"analysis": map[string]any{"content_hash": "h2"}, "sources": []any{"s1"}})
	case p == "/v1/evaluations":
		reply(map[string]any{"id": "ev" + string(rune('0'+len(h.posts[p])))})
	case strings.HasSuffix(p, "/transition"):
		reply(map[string]any{"id": strings.TrimSuffix(strings.TrimPrefix(p, "/v1/skill_versions/"), "/transition")})
	case strings.HasSuffix(p, "/availability"):
		h.status, h.compat = body["status"], body["compatibility"]
		reply(map[string]any{"id": "demo", "status": body["status"], "skill_compatibility": body["compatibility"]})
	case strings.HasSuffix(p, "/apply"), strings.HasSuffix(p, "/rollback"):
		// The applied version becomes what latest-approved resolves to.
		skill := strings.TrimPrefix(p[:strings.LastIndex(p, "/")], "/v1/skills/")
		version, _ := body["version_id"].(string)
		if item, ok := h.items[skill+"@"+strings.TrimPrefix(version, "v")]; ok {
			h.items[skill+"@latest-approved"] = item
		}
		reply(map[string]any{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func (h *reviewHub) called(prefix string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, c := range h.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

const reviewSkillMD = "---\nname: demo\ndescription: does demo things\n---\nbody\n"

func newReviewHub(t *testing.T) (*reviewHub, string) {
	t.Helper()
	swapReview(t)
	dir := t.TempDir()
	owner, worker := filepath.Join(dir, "owner.token"), filepath.Join(dir, "worker.token")
	os.WriteFile(owner, []byte(ownerTok+"\n"), 0o600)
	os.WriteFile(worker, []byte(workerTok), 0o600)
	h := &reviewHub{posts: map[string][]map[string]any{}, status: "active", compat: map[string]any{"codex": "unsupported"}, items: map[string]map[string]any{
		"demo@2":               {"skill_id": "demo", "version_id": "v2", "resolved_version": 2, "instructions": reviewSkillMD + "new\n", "validation_status": "candidate"},
		"demo@1":               {"skill_id": "demo", "version_id": "v1", "resolved_version": 1, "instructions": reviewSkillMD, "validation_status": "superseded"},
		"demo@latest-approved": {"skill_id": "demo", "version_id": "v1", "resolved_version": 1, "instructions": reviewSkillMD},
		"bare@2":               {"skill_id": "bare", "version_id": "v2", "instructions": "no frontmatter", "validation_status": "approved"},
	}}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, o, wk, ch, ed := reviewURL, reviewOwner, reviewWorker, syncChanges, evalsDir
	t.Cleanup(func() { reviewURL, reviewOwner, reviewWorker, syncChanges, evalsDir = u, o, wk, ch, ed })
	reviewURL, reviewOwner, reviewWorker = srv.URL, owner, worker
	syncChanges = filepath.Join(dir, "changes.jsonl")
	os.WriteFile(syncChanges, []byte(`{"skill_id":"demo","content_hash":"h2","summary":"tightened wording"}`+"\n"), 0o600)
	evalsDir = filepath.Join(dir, "evals")
	return h, dir
}

func runReview(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := reviewMain(args, nil, &out, &errb)
	return code, out.String(), errb.String()
}

func TestReviewListWalksPagesAndHistory(t *testing.T) {
	newReviewHub(t)
	// The withheld skill is warned on os.Stderr and still has its history walked.
	code, out, errOut := runReview("list")
	if code != 0 {
		t.Fatal(code, errOut)
	}
	if !strings.Contains(out, `"demo@2"`) || !strings.Contains(out, "tightened wording") || strings.Contains(out, "demo@3") {
		t.Fatalf("list output = %s", out)
	}
}

func TestReviewApproveRunsBehaviorEvalThenApplies(t *testing.T) {
	h, dir := newReviewHub(t)
	evals := filepath.Join(dir, "my-evals.json")
	os.WriteFile(evals, []byte(`{"evals":[{"id":"e1","prompt":"do it","expected_output":"done","assertions":["mentions demo"]}]}`), 0o600)
	var judged string
	claudeRun = func(prompt, system, schema string) (any, error) {
		if schema != "" {
			judged = prompt
			return map[string]any{"A": true, "B": true, "reason": "fine"}, nil
		}
		return "answer", nil
	}
	code, out, errOut := runReview("approve", "demo@2", "--evals", evals)
	if code != 0 || !strings.Contains(out, "approved and active: demo@2") {
		t.Fatal(code, out, errOut)
	}
	if !strings.Contains(judged, "- mentions demo") {
		t.Fatalf("judge prompt %q stderr %q", judged, errOut)
	}
	evalCalls := h.called("POST /v1/evaluations")
	if len(evalCalls) != 2 || !strings.HasSuffix(evalCalls[0], workerTok) {
		t.Fatalf("evaluations = %v", evalCalls)
	}
	tr := h.posts["/v1/skill_versions/v2/transition"]
	if len(tr) != 1 || tr[0]["status"] != "approved" || tr[0]["validation_id"] != "ev2" {
		t.Fatalf("transition = %v", tr)
	}
	if ap := h.posts["/v1/skills/demo/apply"]; len(ap) != 1 || ap[0]["version_id"] != "v2" || ap[0]["expected_revision"] != float64(7) {
		t.Fatalf("apply = %v", ap)
	}
}

func TestReviewApproveStopsOnGreenFailure(t *testing.T) {
	h, _ := newReviewHub(t)
	// No --evals: the default $SKILL_EVALS_DIR/<skill_id>/evals.json is used.
	os.MkdirAll(filepath.Join(evalsDir, "demo"), 0o755)
	os.WriteFile(filepath.Join(evalsDir, "demo", "evals.json"), []byte(`{"evals":[{"prompt":"p","expected_output":"e"}]}`), 0o600)
	claudeRun = func(prompt, system, schema string) (any, error) {
		if schema != "" {
			return map[string]any{"A": false, "B": false, "reason": "missed it"}, nil
		}
		return "x", nil
	}
	code, _, errOut := runReview("approve", "demo@2")
	if code != 1 || !strings.Contains(errOut, "behavior_eval_failed") || !strings.Contains(errOut, "missed it") {
		t.Fatal(code, errOut)
	}
	if len(h.called("POST /v1/skill_versions/")) != 0 {
		t.Fatal("a failed eval must not transition the version")
	}
	if ev := h.posts["/v1/evaluations"]; len(ev) != 1 || ev[0]["passed"] != false {
		t.Fatalf("failed eval not recorded: %v", ev)
	}
}

func TestReviewApproveRejectsMissingEvalsAndBadCandidates(t *testing.T) {
	newReviewHub(t)
	if code, _, errOut := runReview("approve", "demo@2", "--evals", "/nonexistent/evals.json"); code != 1 || !strings.Contains(errOut, "evals_not_found") {
		t.Fatal(code, errOut)
	}
	code, _, errOut := runReview("approve", "bare@2")
	if code != 1 || !strings.Contains(errOut, "automated_check_failed") || !strings.Contains(errOut, "frontmatter") || !strings.Contains(errOut, "not candidate") {
		t.Fatal(code, errOut)
	}
	if code, _, errOut := runReview("approve", "nope@1"); code != 1 || !strings.Contains(errOut, "skill_not_found") {
		t.Fatal(code, errOut)
	}
	if code, _, errOut := runReview("diff", "boom@1"); code != 1 || !strings.Contains(errOut, "http_500") || !strings.Contains(errOut, "/v1/skills/resolve") {
		t.Fatal(code, errOut)
	}
}

func TestReviewEvalsFileErrors(t *testing.T) {
	_, dir := newReviewHub(t)
	noKey := filepath.Join(dir, "nokey.json")
	os.WriteFile(noKey, []byte(`{"cases":[]}`), 0o600)
	if code, _, errOut := runReview("approve", "demo@2", "--evals", noKey); code != 1 || !strings.Contains(errOut, "evals") {
		t.Fatal(code, errOut)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{not json`), 0o600)
	if code, _, _ := runReview("approve", "demo@2", "--evals", bad); code != 1 {
		t.Fatal(code)
	}
}

func TestReviewRejectRollbackAvailabilityDiff(t *testing.T) {
	h, _ := newReviewHub(t)
	if code, out, errOut := runReview("reject", "demo@2"); code != 0 || !strings.Contains(out, "rejected: demo@2") {
		t.Fatal(code, out, errOut)
	}
	if tr := h.posts["/v1/skill_versions/v2/transition"]; len(tr) != 1 || tr[0]["status"] != "rejected" {
		t.Fatalf("reject transition = %v", tr)
	}
	if code, out, errOut := runReview("rollback", "demo@1"); code != 0 || !strings.Contains(out, "rolled back, active: demo@1") {
		t.Fatal(code, out, errOut)
	}
	if rb := h.posts["/v1/skills/demo/rollback"]; len(rb) != 1 || rb[0]["version_id"] != "v1" || rb[0]["validation_id"] != "ev1" {
		t.Fatalf("rollback = %v", rb)
	}
	code, out, errOut := runReview("availability", "demo", "--status", "deprecated", "--unsupported", "codex", "claude_code")
	if code != 0 || !strings.Contains(out, "demo: deprecated") || !strings.Contains(out, "claude_code") {
		t.Fatal(code, out, errOut)
	}
	code, out, _ = runReview("availability", "demo")
	if code != 0 || !strings.Contains(out, "demo: deprecated") || !strings.Contains(out, "claude_code") {
		t.Fatal("availability without changes must keep status and compatibility:", out)
	}
	if code, out, _ := runReview("diff", "demo@2"); code != 0 || !strings.Contains(out, "+new") {
		t.Fatal(code, out)
	}
	if code, out, _ := runReview("diff", "demo@latest-approved"); code != 0 || !strings.Contains(out, "(same content)") {
		t.Fatal(code, out)
	}
}

func TestReviewMainArguments(t *testing.T) {
	newReviewHub(t)
	if code, out, _ := runReview("--help"); code != 0 || !strings.Contains(out, "approve-pins") {
		t.Fatal(code, out)
	}
	for _, args := range [][]string{{}, {"explode"}, {"availability", "demo", "--status", "gone"}, {"approve"}} {
		if code, _, _ := runReview(args...); code != 2 {
			t.Errorf("%v exit = %d, want 2", args, code)
		}
	}
	if code, out, _ := runReview("approve-pins"); code != 0 || !strings.Contains(out, "후보 0개") {
		t.Fatal(code, out)
	}
}

func TestReviewMissingTokenFails(t *testing.T) {
	newReviewHub(t)
	reviewOwner = filepath.Join(t.TempDir(), "absent.token")
	if code, _, errOut := runReview("reject", "demo@2"); code != 1 || !strings.Contains(errOut, "failed:") {
		t.Fatal(code, errOut)
	}
}

func TestClaudeCLIReadsTheJSONReply(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake claude is a shell script")
	}
	bin := t.TempDir()
	reply := filepath.Join(bin, "reply.json")
	script := "#!/bin/sh\nprintf '%s ' \"$@\" > \"" + filepath.Join(bin, "args") + "\"\ncat \"" + reply + "\"\n"
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	set := func(s string) { os.WriteFile(reply, []byte(s), 0o600) }

	set(`{"result":"hello","structured_output":{"A":true}}`)
	if v, err := claudeCLI("p", "sys", ""); err != nil || v != "hello" {
		t.Fatal(v, err)
	}
	args, _ := os.ReadFile(filepath.Join(bin, "args"))
	if !strings.Contains(string(args), "--append-system-prompt sys") || strings.Contains(string(args), "--json-schema") {
		t.Fatalf("args = %s", args)
	}
	if v, err := claudeCLI("p", "", judgeSchema); err != nil || asMap(v)["A"] != true {
		t.Fatal(v, err)
	}
	set(`{"is_error":true,"result":"quota"}`)
	if _, err := claudeCLI("p", "", ""); hubCode(err) != "eval_run_failed" || !strings.Contains(err.Error(), "quota") {
		t.Fatal(err)
	}
	set(`not json`)
	if _, err := claudeCLI("p", "", ""); hubCode(err) != "eval_run_failed" || !strings.Contains(err.Error(), "not json") {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := claudeCLI("p", "", ""); err == nil {
		t.Fatal("missing claude binary must fail")
	}
}
