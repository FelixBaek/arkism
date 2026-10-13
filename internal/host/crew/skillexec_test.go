//go:build unix

package crew

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type execFixture struct {
	t                   *testing.T
	root, state, config string
	count               string
	scope, plan, row    map[string]any
	request             map[string]any
	mu                  sync.Mutex
	failPublish, deny   bool
	published           []string
}

func newExecFixture(t *testing.T) *execFixture {
	f := &execFixture{t: t, root: t.TempDir(), failPublish: true}
	f.state, f.config, f.count = filepath.Join(f.root, "state"), filepath.Join(f.root, "config.json"), filepath.Join(f.root, "calls")
	f.scope = map[string]any{"workspace_id": "test", "project_id": "test", "user_id": "owner", "agent_id": ""}
	bin := testBinary(t)
	cases := []any{}
	for _, c := range []string{"quality", "regression", "safety"} {
		cases = append(cases, map[string]any{"id": c, "category": c, "input": c, "expected": c, "held_out": true})
	}
	dataset := map[string]any{"cases": cases}
	env := map[string]any{"argv": []any{bin, "adapter-count"}, "files": map[string]any{bin: fileDigest(t, bin)},
		"env": map[string]any{"COUNT_FILE": f.count}, "timeout_ms": 5000, "max_output_bytes": 1024}
	for name, v := range map[string]any{"dataset": dataset, "environment": env} {
		_ = os.WriteFile(filepath.Join(f.root, name+".json"), []byte(mustCanonical(v)), 0o600)
	}
	f.plan = map[string]any{"baseline_version": "", "dataset_sha256": fileDigest(t, filepath.Join(f.root, "dataset.json")),
		"environment_sha256": fileDigest(t, filepath.Join(f.root, "environment.json")), "min_score_gain": 0.1, "max_cost": 1, "max_duration_ms": 10000}
	f.row = map[string]any{"id": "candidate", "scope": f.scope, "revision": 1, "status": "candidate", "skill_id": "review", "comparison_plan": f.plan,
		"comparison_plan_sha256": strings.Repeat("a", 64), "analysis": map[string]any{"content_hash": digest([]byte("verify"))},
		"sources": []any{map[string]any{"source_id": "source"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		answer := func(code int, v any) { w.WriteHeader(code); _, _ = w.Write([]byte(mustCanonical(v))) }
		if r.Method == http.MethodPost {
			raw, _ := io.ReadAll(r.Body)
			f.published = append(f.published, string(raw))
			if f.failPublish {
				answer(503, map[string]any{"code": "dependency_unavailable"})
			} else {
				answer(200, map[string]any{"evaluation": map[string]any{"collection": "evaluations", "id": "evaluation", "revision": 1}})
			}
			return
		}
		switch {
		case f.deny:
			answer(403, map[string]any{"code": "access_denied"})
		case strings.HasPrefix(r.URL.Path, "/v1/skill_versions/"):
			answer(200, f.row)
		case strings.HasPrefix(r.URL.Path, "/v1/skills/"):
			answer(200, map[string]any{"status": "active", "active_version": ""})
		case strings.HasPrefix(r.URL.Path, "/v1/sources/"):
			answer(200, map[string]any{"text": "verify"})
		default:
			answer(404, map[string]any{})
		}
	}))
	t.Cleanup(srv.Close)
	_ = os.WriteFile(filepath.Join(f.root, "token"), []byte("synthetic-independent-token"), 0o600)
	_ = os.WriteFile(f.config, []byte(mustCanonical(map[string]any{"scope": f.scope, "url": srv.URL, "client_url": "https://synthetic.invalid",
		"token_file": filepath.Join(f.root, "token"), "profiles": map[string]any{"fixed": map[string]any{"name": "Fixed comparison",
			"dataset": filepath.Join(f.root, "dataset.json"), "environment": filepath.Join(f.root, "environment.json")}}})), 0o600)
	f.request = map[string]any{"action": "start", "scope": f.scope, "profile_id": "fixed", "candidate": map[string]any{"id": "candidate", "revision": 1}, "event_id": "event-1"}
	return f
}

// call goes through the real command, as the desktop does over SSH.
func (f *execFixture) call(value map[string]any) map[string]any {
	f.t.Helper()
	v := map[string]any{"server": "https://synthetic.invalid"}
	for k, x := range value {
		v[k] = x
	}
	var out strings.Builder
	rc := skillExecutionMain([]string{"--config", f.config, "--state", f.state}, strings.NewReader(mustCanonical(v)), &out, io.Discard)
	res, err := strictJSON([]byte(out.String()))
	if err != nil {
		f.t.Fatal(out.String())
	}
	if rc != 0 {
		return map[string]any{"__error": obj(res)["error"]}
	}
	return obj(res)
}

func (f *execFixture) wait(job string) map[string]any {
	// Generous: the worker is this test binary, which is slow to start under -race.
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); {
		v := f.call(map[string]any{"action": "status", "scope": f.scope, "job_id": job})
		if v["status"] != "running" {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	f.t.Fatal("comparison job did not stop")
	return nil
}

func (f *execFixture) calls() int {
	raw, _ := os.ReadFile(f.count)
	return strings.Count(string(raw), "\n")
}

func TestSkillExecutionRealJobRetryPublishesWithoutRerun(t *testing.T) {
	f := newExecFixture(t)
	profiles := f.call(map[string]any{"action": "profiles", "scope": f.scope})
	profile := obj(list(profiles["items"])[0])
	if profile["dataset_sha256"] != f.plan["dataset_sha256"] || strings.Contains(mustCanonical(profile), "argv") {
		t.Fatal(profiles)
	}
	first := f.call(f.request)
	job, _ := first["job_id"].(string)
	if first["status"] != "running" || f.call(f.request)["job_id"] != job {
		t.Fatal(first)
	}
	normalized := map[string]any{}
	for k, v := range f.request {
		normalized[k] = v
	}
	normalized["scope"] = map[string]any{"workspace_id": "test", "project_id": "test", "user_id": "owner"}
	if f.call(normalized)["job_id"] != job {
		t.Fatal("normalized scope changed the job")
	}
	history := f.call(map[string]any{"action": "history", "scope": f.scope, "candidate": f.request["candidate"]})
	if obj(list(history["items"])[0])["job_id"] != job {
		t.Fatal(history)
	}
	if s := f.wait(job); s["status"] != "awaiting_publication" || f.calls() != 6 {
		t.Fatal(s, f.calls())
	}
	if f.call(f.request)["status"] != "awaiting_publication" {
		t.Fatal("retry changed state")
	}
	changed := map[string]any{}
	for k, v := range f.request {
		changed[k] = v
	}
	changed["candidate"] = map[string]any{"id": "candidate", "revision": 2}
	if r := f.call(changed); !strings.Contains(r["__error"].(string), "request_changed") {
		t.Fatal(r)
	}
	f.mu.Lock()
	f.failPublish = false
	f.mu.Unlock()
	f.call(map[string]any{"action": "publish", "scope": f.scope, "job_id": job})
	done := f.wait(job)
	f.mu.Lock()
	published := append([]string(nil), f.published...)
	f.mu.Unlock()
	if done["status"] != "published" || done["passed"] != true || f.calls() != 6 || len(published) < 2 || published[0] != published[1] {
		t.Fatal(done, f.calls(), len(published))
	}
	if text := mustCanonical(done); strings.Contains(text, "verify") || strings.Contains(text, "synthetic-independent-token") {
		t.Fatal(text)
	}
	f.mu.Lock()
	f.deny = true
	f.mu.Unlock()
	if r := f.call(map[string]any{"action": "status", "scope": f.scope, "job_id": job}); r["__error"] == nil {
		t.Fatal(r)
	}
}

func fakeLaunch(t *testing.T) *int {
	calls := 0
	old := launchJob
	launchJob = func(string, string, string) error { calls++; return nil }
	t.Cleanup(func() { launchJob = old })
	return &calls
}

func TestSkillExecutionNoResultIsNeverReexecuted(t *testing.T) {
	f := newExecFixture(t)
	launches := fakeLaunch(t)
	first := f.call(f.request)
	if first["status"] != "interrupted_or_failed" {
		t.Fatal(first)
	}
	f.call(f.request)
	if *launches != 1 {
		t.Fatal(*launches)
	}
	if r := f.call(map[string]any{"action": "publish", "scope": f.scope, "job_id": first["job_id"]}); r["__error"] != "execution_result_unavailable" {
		t.Fatal(r)
	}
	if exists(f.count) {
		t.Fatal("adapter ran")
	}
}

func TestSkillExecutionRejectsScopeCommandAndChangedMaterialBeforeSpawn(t *testing.T) {
	f := newExecFixture(t)
	launches := fakeLaunch(t)
	for _, mutation := range []string{"scope", "server", "command", "revision", "event", "profile"} {
		v := map[string]any{"server": "https://synthetic.invalid"}
		for k, x := range f.request {
			v[k] = x
		}
		switch mutation {
		case "server":
			v["server"] = "https://different.invalid"
		case "scope":
			v["scope"] = map[string]any{"workspace_id": "test", "project_id": "test", "user_id": "foreign", "agent_id": ""}
		case "command":
			v["argv"] = []any{"/bin/sh"}
		case "revision":
			v["candidate"] = map[string]any{"id": "candidate", "revision": true}
		case "event":
			v["event_id"] = "../escape"
		case "profile":
			v["profile_id"] = "missing"
		}
		raw, _ := json.Marshal(v)
		value, _ := strictJSON(raw)
		_, err := handleExecution(value, f.config, f.state)
		if code(err) == "" || code(err) == "other" {
			t.Errorf("%s: %v", mutation, err)
		}
	}
	dataset := filepath.Join(f.root, "dataset.json")
	raw, _ := os.ReadFile(dataset)
	_ = os.WriteFile(dataset, append(raw, '\n'), 0o600)
	if r := f.call(f.request); !strings.Contains(r["__error"].(string), "plan_mismatch") {
		t.Fatal(r)
	}
	if *launches != 0 || exists(f.count) {
		t.Fatal("spawned")
	}
}
