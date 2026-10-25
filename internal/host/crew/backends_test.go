package crew

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner is an arkism.backend.v1 runner on loopback: its capabilities, and a handler per call that tests change.
type fakeRunner struct {
	*httptest.Server
	mu       sync.Mutex
	caps     map[string]bool
	start    func(w http.ResponseWriter, body map[string]any)
	status   func(w http.ResponseWriter, id string)
	cancel   func(w http.ResponseWriter, id string)
	starts   []map[string]any
	keys     []string
	auth     []string
	statuses int
	cancels  int
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func newFakeRunner(t *testing.T, caps ...string) *fakeRunner {
	t.Helper()
	f := &fakeRunner{caps: map[string]bool{}}
	for _, c := range caps {
		f.caps[c] = true
	}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities":
			reply(w, 200, map[string]any{"protocol": backendProtocol, "capabilities": f.caps})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.starts = append(f.starts, body)
			f.keys = append(f.keys, r.Header.Get("Idempotency-Key"))
			f.start(w, body)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/tasks/"):
			f.statuses++
			f.status(w, strings.TrimPrefix(r.URL.Path, "/v1/tasks/"))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/cancel"):
			f.cancels++
			f.cancel(w, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/cancel"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	f.status = func(w http.ResponseWriter, id string) { http.NotFound(w, nil) }
	f.cancel = func(w http.ResponseWriter, id string) {
		reply(w, 200, map[string]any{"task_id": id, "status": "cancelled"})
	}
	return f
}

// backendEnv keeps the keyring out of tests (tokens go to the 0600 file) and makes polling instant.
func backendEnv(t *testing.T) {
	t.Helper()
	patch(t, &secretPut, func(string, string, string) (string, error) { return "", errors.New("no keyring in tests") })
	patch(t, &backendPoll, []time.Duration{time.Millisecond})
	patch(t, &backendSleep, func(context.Context, time.Duration) {})
	t.Setenv("ARKISM_CAPTURE_CHILD", "")
	_ = os.Remove(backendsFile())
	_ = os.RemoveAll(stateFile("backend-tasks"))
}

func addFake(t *testing.T, f *fakeRunner, name string, isolated, ownModel bool) {
	t.Helper()
	if _, err := addBackend(name, f.URL, "conn-secret-123", true, isolated, ownModel); err != nil {
		t.Fatal(err)
	}
}

func stepSpec(name, key string) map[string]any {
	return map[string]any{"backend": name, "task": "요약해 줘", "once": true, "access": "read",
		"run": map[string]any{"kind": "assist", "id": "a1", "key": key, "expert_id": "e1"}}
}

func trace(out map[string]any) map[string]any { m, _ := out["backend"].(map[string]any); return m }

func TestBackendURLPolicy(t *testing.T) {
	for _, c := range []struct {
		url   string
		local bool
		ok    bool
	}{
		{"https://runner.example.com/base", false, true},
		{"http://runner.example.com", false, false},
		{"http://127.0.0.1:8080", true, true},
		{"http://127.0.0.1:8080", false, false},
		{"http://192.168.0.5:8080", true, false}, // plain http only on loopback
		{"https://user:pw@runner.example.com", false, false},
		{"https://runner.example.com/?q=1", false, false},
		{"ftp://runner.example.com", false, false},
	} {
		_, err := checkBackendURL(c.url, c.local)
		if (err == nil) != c.ok {
			t.Errorf("%s local=%v: err %v", c.url, c.local, err)
		}
	}
	for _, c := range []struct {
		ip    string
		local bool
		ok    bool
	}{
		{"169.254.169.254", true, false}, // cloud metadata (link-local)
		{"100.100.100.200", true, false},
		{"fd00:ec2::254", true, false},
		{"0.0.0.0", true, false},
		{"224.0.0.1", true, false},
		{"127.0.0.1", false, false},
		{"10.0.0.8", false, false},
		{"100.64.1.2", false, false},
		{"127.0.0.1", true, true},
		{"10.0.0.8", true, true},
		{"93.184.216.34", false, true},
	} {
		err := allowBackendIP(net.ParseIP(c.ip), c.local)
		if (err == nil) != c.ok {
			t.Errorf("%s local=%v: err %v", c.ip, c.local, err)
		}
	}
}

func TestBackendRedirectAndNonLocalRefused(t *testing.T) {
	backendEnv(t)
	target := newFakeRunner(t, "status")
	jump := httptest.NewServer(http.RedirectHandler(target.URL+"/v1/capabilities", http.StatusFound))
	t.Cleanup(jump.Close)
	if _, err := addBackend("jump", jump.URL, "", true, false, false); err != nil {
		t.Fatal(err)
	}
	if _, err := checkBackend("jump"); err == nil {
		t.Fatal("a redirect must not be followed")
	}
	if len(target.auth) != 0 {
		t.Fatal("the redirect target was called")
	}
	// The same loopback runner without --local is refused at connect time (http fails the URL check first).
	b := &backend{Name: "x", URL: strings.Replace(target.URL, "http://", "https://", 1)}
	if _, err := fetchCaps(context.Background(), b); err == nil || !strings.Contains(err.Error(), "--local") {
		t.Fatal("loopback without --local:", err)
	}
}

func TestBackendTokenNeverShown(t *testing.T) {
	backendEnv(t)
	f := newFakeRunner(t)
	addFake(t, f, "mine", false, false)
	out, err := runBackendsCmd("backends list", &args{}, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "conn-secret-123") || !strings.Contains(string(raw), `"token":"file"`) {
		t.Fatal("list output:", string(raw))
	}
	if st, err := os.Stat(backendsFile()); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("backends.json mode", st, err)
	}
	t.Setenv("ARKISM_CAPTURE_CHILD", "1")
	if _, err := addBackend("other", f.URL, "", true, false, false); err == nil {
		t.Fatal("an agent Arkism started must not add a connection")
	}
	if _, err := useBackend("mine", "@writer"); err == nil {
		t.Fatal("an agent Arkism started must not change an expert")
	}
}

func TestBackendSyncSuccess(t *testing.T) {
	backendEnv(t)
	f := newFakeRunner(t, "status", "tool_restriction", "usage", "idempotency", "instructions", "model_choice")
	f.start = func(w http.ResponseWriter, _ map[string]any) {
		reply(w, 200, map[string]any{"task_id": "t-1", "status": "succeeded", "model": "runner-model-1",
			"result": map[string]any{"text": "답이에요"}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}})
	}
	addFake(t, f, "mine", false, false)
	spec := stepSpec("mine", "assist/a1")
	spec["model"], spec["instructions"] = "gpt-x", "짧게"
	t.Setenv("ARKISM_TOKEN", "owner-should-never-leave")
	out, err := runNode(spec)
	if err != nil || out["ok"] != true || out["output"] != "답이에요" {
		t.Fatal(out, err)
	}
	u, _ := out["usage"].(map[string]any)
	if u["estimated"] != false || u["source"] != "backend" || u["cost_usd"] != nil {
		t.Fatal("usage must be the runner's report, with no invented cost:", u)
	}
	tr := trace(out)
	if tr["task_id"] != "t-1" || tr["state"] != "succeeded" || tr["model"] != "runner-model-1" || tr["fallback"] != false {
		t.Fatal(tr)
	}
	body := f.starts[0]
	if f.keys[0] != "assist/a1" || obj(body["access"])["mode"] != "read" || obj(body["model"])["requested"] != "gpt-x" || body["protocol"] != backendProtocol {
		t.Fatal("request:", f.keys, body)
	}
	for _, a := range f.auth {
		if a != "Bearer conn-secret-123" {
			t.Fatal("only the connection's own token goes to the runner:", a)
		}
	}
	raw, _ := os.ReadFile(ledgerPath("assist/a1"))
	if !strings.Contains(string(raw), `"task_id":"t-1"`) || strings.Contains(string(raw), "답이에요") {
		t.Fatal("ledger holds ids, not answers:", string(raw))
	}
}

func TestBackendAsyncAcceptThenSuccess(t *testing.T) {
	backendEnv(t)
	f := newFakeRunner(t, "status", "tool_restriction")
	f.start = func(w http.ResponseWriter, _ map[string]any) {
		reply(w, 202, map[string]any{"task_id": "t-2", "status": "accepted"})
	}
	seq := []string{"running", "accepted", "running", "succeeded"} // a late "accepted" and a repeat are ignored
	f.status = func(w http.ResponseWriter, id string) {
		st := seq[0]
		seq = seq[1:]
		r := map[string]any{"task_id": id, "status": st}
		if st == "succeeded" {
			r["result"] = map[string]any{"text": "끝"}
		}
		reply(w, 200, r)
	}
	addFake(t, f, "mine", false, false)
	accepted := 0
	spec := stepSpec("mine", "assist/a2")
	spec["on_accept"] = func(tr map[string]any) {
		accepted++
		if tr["task_id"] != "t-2" || tr["state"] != "accepted" {
			t.Error("accept trace", tr)
		}
	}
	out, err := runNode(spec)
	if err != nil || out["ok"] != true || out["output"] != "끝" || accepted != 1 || f.statuses != 4 {
		t.Fatal(out, err, accepted, f.statuses)
	}
	if out["usage"] != nil || trace(out)["usage"] != "unknown" {
		t.Fatal("a runner without usage leaves cost unknown, not 0:", out["usage"], trace(out))
	}
}

func TestBackendFailuresAreNotSuccess(t *testing.T) {
	for _, c := range []struct {
		name string
		code int
		body map[string]any
		want string
	}{
		{"auth", 401, map[string]any{}, "backend_auth"},
		{"rejected", 422, map[string]any{}, "backend_rejected"},
		{"no status", 200, map[string]any{"task_id": "t", "result": map[string]any{"text": "x"}}, "backend_unknown"},
		{"made-up status", 200, map[string]any{"task_id": "t", "status": "done", "result": map[string]any{"text": "x"}}, "backend_unknown"},
		{"empty success", 200, map[string]any{"task_id": "t", "status": "succeeded"}, "backend_unknown"},
		{"odd task id", 200, map[string]any{"task_id": "t\n<x>", "status": "accepted"}, "backend_unknown"},
		{"task failed", 200, map[string]any{"task_id": "t", "status": "failed", "error": map[string]any{"message": "boom"}}, "backend_failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			backendEnv(t)
			f := newFakeRunner(t, "status", "tool_restriction")
			f.start = func(w http.ResponseWriter, _ map[string]any) { reply(w, c.code, c.body) }
			addFake(t, f, "mine", false, false)
			out, err := runNode(stepSpec("mine", "k/"+c.name))
			if err != nil || out["ok"] != false || !strings.HasPrefix(sOr(out["error"]), c.want) {
				t.Fatal(out, err)
			}
		})
	}
}

func TestBackendLostStartIsNeverResent(t *testing.T) {
	backendEnv(t)
	f := newFakeRunner(t, "status", "tool_restriction")
	f.start = func(w http.ResponseWriter, _ map[string]any) { reply(w, 502, map[string]any{}) }
	addFake(t, f, "mine", false, false)
	out, _ := runNode(stepSpec("mine", "k/lost"))
	if out["ok"] != false || trace(out)["state"] != "unknown" || len(f.starts) != 1 {
		t.Fatal(out, len(f.starts))
	}
	// The same step again (a restart): the ledger says a start may have happened, so nothing is sent.
	out, _ = runNode(stepSpec("mine", "k/lost"))
	if out["ok"] != false || len(f.starts) != 1 {
		t.Fatal("resent:", out, len(f.starts))
	}
}

func TestBackendIdempotentStartRetriesWithSameKey(t *testing.T) {
	backendEnv(t)
	f := newFakeRunner(t, "status", "tool_restriction", "idempotency")
	f.start = func(w http.ResponseWriter, _ map[string]any) {
		if len(f.starts) < 3 {
			reply(w, 503, map[string]any{})
			return
		}
		reply(w, 200, map[string]any{"task_id": "t-3", "status": "succeeded", "result": map[string]any{"text": "ok"}})
	}
	addFake(t, f, "mine", false, false)
	out, _ := runNode(stepSpec("mine", "k/idem"))
	if out["ok"] != true || len(f.keys) != 3 || f.keys[0] != "k/idem" || f.keys[2] != "k/idem" {
		t.Fatal(out, f.keys)
	}
}

func TestBackendStatusErrorsAreNotTaskFailures(t *testing.T) {
	backendEnv(t)
	f := newFakeRunner(t, "status", "tool_restriction")
	f.start = func(w http.ResponseWriter, _ map[string]any) {
		reply(w, 202, map[string]any{"task_id": "t-4", "status": "running"})
	}
	f.status = func(w http.ResponseWriter, id string) {
		if f.statuses <= 2 {
			reply(w, 500, map[string]any{})
			return
		}
		reply(w, 200, map[string]any{"task_id": id, "status": "succeeded", "result": map[string]any{"text": "늦은 답"}})
	}
	addFake(t, f, "mine", false, false)
	out, _ := runNode(stepSpec("mine", "k/status"))
	if out["ok"] != true || trace(out)["status_failures"] != 2 || len(f.starts) != 1 {
		t.Fatal(out, len(f.starts))
	}
	// A runner that forgets the task: unknown, never re-run.
	f.status = func(w http.ResponseWriter, _ string) { http.NotFound(w, nil) }
	out, _ = runNode(stepSpec("mine", "k/forgot"))
	if out["ok"] != false || trace(out)["state"] != "unknown" || len(f.starts) != 2 {
		t.Fatal(out)
	}
}

func TestBackendDeadlineCancel(t *testing.T) {
	for _, c := range []struct {
		name   string
		caps   []string
		answer string
		state  string
	}{
		{"confirmed", []string{"status", "tool_restriction", "cancel"}, "cancelled", "cancelled"},
		{"requested only", []string{"status", "tool_restriction", "cancel"}, "running", "cancel_requested"},
		{"cannot cancel", []string{"status", "tool_restriction"}, "", "unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			backendEnv(t)
			clock := time.Now()
			patch(t, &backendNow, func() time.Time { clock = clock.Add(time.Minute); return clock })
			f := newFakeRunner(t, c.caps...)
			f.start = func(w http.ResponseWriter, _ map[string]any) {
				reply(w, 202, map[string]any{"task_id": "t-5", "status": "running"})
			}
			f.status = func(w http.ResponseWriter, id string) {
				reply(w, 200, map[string]any{"task_id": id, "status": "running"})
			}
			f.cancel = func(w http.ResponseWriter, id string) {
				reply(w, 202, map[string]any{"task_id": id, "status": c.answer})
			}
			addFake(t, f, "mine", false, false)
			spec := stepSpec("mine", "k/deadline")
			spec["timeout_s"] = 300
			out, _ := runNode(spec)
			if out["ok"] != false || trace(out)["state"] != c.state {
				t.Fatal(out)
			}
			if wantCalls := map[bool]int{true: 1, false: 0}[c.answer != ""]; f.cancels != wantCalls {
				t.Fatal("cancel calls", f.cancels)
			}
		})
	}
}

func TestBackendApprovalIsNeverGiven(t *testing.T) {
	backendEnv(t)
	f := newFakeRunner(t, "status", "tool_restriction", "cancel", "approval")
	f.start = func(w http.ResponseWriter, _ map[string]any) {
		reply(w, 202, map[string]any{"task_id": "t-6", "status": "waiting_approval"})
	}
	addFake(t, f, "mine", false, false)
	out, _ := runNode(stepSpec("mine", "k/approve"))
	if out["ok"] != false || f.cancels != 1 || !strings.Contains(sOr(out["error"]), "승인") {
		t.Fatal(out, f.cancels)
	}
	if f.starts[0]["approval"] != "none" {
		t.Fatal("request must say Arkism relays no approval", f.starts[0])
	}
}

func TestBackendResultIsClippedAndMasked(t *testing.T) {
	backendEnv(t)
	f := newFakeRunner(t, "status", "tool_restriction")
	long := "conn-secret-123 " + strings.Repeat("가", backendResultChars+50)
	f.start = func(w http.ResponseWriter, _ map[string]any) {
		reply(w, 200, map[string]any{"task_id": "t-7", "status": "succeeded", "result": map[string]any{"text": long}})
	}
	addFake(t, f, "mine", false, false)
	out, _ := runNode(stepSpec("mine", "k/long"))
	text := sOr(out["output"])
	if out["ok"] != true || len([]rune(text)) > backendResultChars || strings.Contains(text, "conn-secret-123") || !strings.Contains(text, "여기까지만") {
		t.Fatal(len([]rune(text)), text[:40])
	}
}

func TestBackendRestartAsksAboutTheSameTask(t *testing.T) {
	backendEnv(t)
	f := newFakeRunner(t, "status", "tool_restriction")
	f.start = func(w http.ResponseWriter, _ map[string]any) { t.Error("a resumed step must not start a task") }
	f.status = func(w http.ResponseWriter, id string) {
		reply(w, 200, map[string]any{"task_id": id, "status": "succeeded", "result": map[string]any{"text": "이어 받음"}})
	}
	addFake(t, f, "mine", false, false)
	e := &taskEntry{Key: "k/resume", Backend: "mine", TaskID: "t-8", State: "running", Started: nowZ()}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	out, _ := runNode(stepSpec("mine", "k/resume"))
	if out["ok"] != true || out["output"] != "이어 받음" {
		t.Fatal(out)
	}
	// No ledger, but the server's record knows the task (another host restarted): asked about, not started.
	spec := stepSpec("mine", "k/server")
	obj(spec["run"])["task_id"], obj(spec["run"])["resume"] = "t-9", "1"
	if out, _ := runNode(spec); out["ok"] != true {
		t.Fatal(out)
	}
	// Neither knows it: unknown.
	spec = stepSpec("mine", "k/nothing")
	obj(spec["run"])["resume"] = "1"
	if out, _ := runNode(spec); out["ok"] != false || trace(out)["state"] != "unknown" {
		t.Fatal(out)
	}
}

func TestBackendPolicyRefusesBeforeSending(t *testing.T) {
	backendEnv(t)
	f := newFakeRunner(t, "status")
	f.start = func(w http.ResponseWriter, _ map[string]any) { t.Error("a refused step reached the runner") }
	addFake(t, f, "plain", false, false)
	addFake(t, f, "sandboxed", true, true)
	for _, c := range []struct {
		name string
		spec map[string]any
	}{
		{"write access", map[string]any{"backend": "sandboxed", "task": "x", "access": "write"}},
		{"no tool restriction", map[string]any{"backend": "plain", "task": "x", "once": true}},
		{"instructions without the capability", map[string]any{"backend": "sandboxed", "task": "x", "once": true, "instructions": "규칙"}},
		{"missing connection", map[string]any{"backend": "nope", "task": "x", "once": true}},
	} {
		if _, err := runNode(c.spec); err == nil || !isCrewErr(err) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	f.caps["tool_restriction"] = true
	if _, err := runNode(map[string]any{"backend": "plain", "task": "x", "once": true, "model": "gpt-x"}); err == nil {
		t.Error("a fixed model on a runner without model choice must be refused")
	}
	// own-model: the runner's model is accepted, and the step says the requested one was not applied.
	f.start = func(w http.ResponseWriter, _ map[string]any) {
		reply(w, 200, map[string]any{"task_id": "t", "status": "succeeded", "result": map[string]any{"text": "ok"}})
	}
	out, err := runNode(map[string]any{"backend": "sandboxed", "task": "x", "once": true, "model": "gpt-x"})
	if err != nil || out["ok"] != true || !strings.Contains(strings.Join(trace(out)["not_applied"].([]string), ","), "model") {
		t.Fatal(out, err)
	}
}

func TestTryAgentOnBackend(t *testing.T) {
	backendEnv(t)
	f := newFakeRunner(t, "status", "tool_restriction", "instructions")
	f.start = func(w http.ResponseWriter, _ map[string]any) {
		reply(w, 202, map[string]any{"task_id": "t-10", "status": "accepted"})
	}
	f.status = func(w http.ResponseWriter, id string) {
		reply(w, 200, map[string]any{"task_id": id, "status": "succeeded", "result": map[string]any{"text": "외부 답"}})
	}
	addFake(t, f, "mine", false, false)
	var posts []map[string]any
	patch(t, &runnerCall, func(path string, body any, _ time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/crew/resolve") {
			return map[string]any{"item": map[string]any{"id": "e1", "crew": map[string]any{"type": "agent", "role": "작가", "backend": "mine", "fallback_model": "other"}}}, nil
		}
		raw, _ := json.Marshal(body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		posts = append(posts, m)
		return map[string]any{}, nil
	})
	rec := map[string]any{"id": "assist-1", "crew_assist": map[string]any{"kind": "try", "status": "pending"}}
	res, err := tryAgent(rec, map[string]any{"expert": "@writer", "prompt": "써 줘"}, func(s map[string]any) (map[string]any, error) { return runNode(s) })
	if err != nil || res["status"] != "done" || res["output"] != "외부 답" {
		t.Fatal(res, err)
	}
	if len(posts) != 1 || posts[0]["status"] != "running" || obj(posts[0]["trace"])["task_id"] != "t-10" {
		t.Fatal("the accepted report:", posts)
	}
	if len(f.starts) != 1 || f.keys[0] != "assist/assist-1" {
		t.Fatal("no fallback model or second start:", f.keys)
	}
	if run := assistRun(map[string]any{"id": "x", "crew_assist": map[string]any{"status": "running", "trace": map[string]any{"task_id": "t-11"}}}, nil); run["resume"] != "1" || run["task_id"] != "t-11" {
		t.Fatal(run)
	}
}
