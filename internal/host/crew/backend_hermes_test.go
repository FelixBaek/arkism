package crew

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeHermes answers like Hermes Agent v0.22's API server Runs API (shapes from backend_hermes.go's sources): a fake
// on loopback, not evidence that a real Hermes install works.
type fakeHermes struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
	keys   []string
	byKey  map[string]string
	seq    []map[string]any // successive GET /v1/runs/{id} answers
	stops  int
	down   int // the next run starts answer 503 after admitting the run (a lost answer)
}

func newFakeHermes(t *testing.T) *fakeHermes {
	t.Helper()
	f := &fakeHermes{byKey: map[string]string{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities":
			reply(w, 200, map[string]any{"object": "hermes.api_server.capabilities", "platform": "hermes-agent",
				"features": map[string]any{"run_submission": true, "run_status": true, "run_stop": true, "run_approval_response": true,
					"runs_idempotency": map[string]any{"supported": true, "durable": true, "retention_seconds": 86400}}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runs":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			key := r.Header.Get("Idempotency-Key")
			f.bodies, f.keys = append(f.bodies, body), append(f.keys, key)
			if id, ok := f.byKey[key]; ok {
				reply(w, 202, map[string]any{"run_id": id, "status": "running", "replayed": true})
				return
			}
			f.byKey[key] = "run_abc"
			if f.down > 0 {
				f.down--
				reply(w, 503, map[string]any{})
				return
			}
			reply(w, 202, map[string]any{"run_id": "run_abc", "status": "started", "replayed": false})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/runs/"):
			st := f.seq[0]
			if len(f.seq) > 1 {
				f.seq = f.seq[1:]
			}
			reply(w, 200, st)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stop"):
			f.stops++
			reply(w, 200, map[string]any{"run_id": "run_abc", "status": "stopping"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func addHermes(t *testing.T, f *fakeHermes, isolated bool) {
	t.Helper()
	if _, err := addBackend("hermes", f.URL, backendKindHermes, "hermes-key-123", true, isolated, false, true); err != nil {
		t.Fatal(err)
	}
}

func TestHermesCapabilitiesAndReadOnlyNeedsIsolated(t *testing.T) {
	backendEnv(t)
	f := newFakeHermes(t)
	addHermes(t, f, false)
	b, err := checkBackend("hermes")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"status": true, "cancel": true, "idempotency": true, "instructions": true, "model_choice": true,
		"tool_restriction": false, "usage": false, "memory": false}
	for k, v := range want {
		if b.Caps[k] != v {
			t.Errorf("capability %s = %v, want %v", k, b.Caps[k], v)
		}
	}
	if b.public()["kind"] != backendKindHermes {
		t.Fatal(b.public())
	}
	if _, err := runNode(stepSpec("hermes", "k/h0")); err == nil || len(f.bodies) != 0 {
		t.Fatal("Hermes cannot restrict tools: a read step needs --isolated, and nothing is sent", err, f.bodies)
	}
	if _, err := addBackend("other", f.URL, "openclaw", "", true, false, false, false); err == nil {
		t.Fatal("an unknown kind must be refused")
	}
}

func TestHermesRunSucceeds(t *testing.T) {
	backendEnv(t)
	f := newFakeHermes(t)
	f.seq = []map[string]any{{"run_id": "run_abc", "status": "queued"}, {"run_id": "run_abc", "status": "running"},
		{"run_id": "run_abc", "status": "completed", "output": "요약 끝", "usage": map[string]any{"input_tokens": 0},
			"runtime": map[string]any{"provider": "openai", "model": "gpt-5"}}}
	addHermes(t, f, true)
	spec := stepSpec("hermes", "assist/h1")
	spec["instructions"], spec["model"] = "역할: 연구자", "gpt-5"
	out, err := runNode(spec)
	if err != nil || out["ok"] != true || out["output"] != "요약 끝" {
		t.Fatal(out, err)
	}
	body := f.bodies[0]
	if body["input"] != "요약해 줘" || body["instructions"] != "역할: 연구자" || body["model"] != "gpt-5" || f.keys[0] != "assist/h1" {
		t.Fatal("runs request", body, f.keys)
	}
	if _, ok := body["memory"]; ok {
		t.Fatal("no memory goes to Hermes (no separate data section)", body)
	}
	tr := trace(out)
	if tr["task_id"] != "run_abc" || tr["model"] != "gpt-5" || tr["usage"] != "unknown" || out["usage"] != nil {
		t.Fatal("trace", tr, out["usage"])
	}
}

func TestHermesLostStartReplaysTheSameRun(t *testing.T) {
	backendEnv(t)
	f := newFakeHermes(t)
	f.down = 1
	f.seq = []map[string]any{{"run_id": "run_abc", "status": "completed", "output": "한 번만"}}
	addHermes(t, f, true)
	out, _ := runNode(stepSpec("hermes", "assist/h2"))
	if out["ok"] != true || len(f.keys) != 2 || f.keys[0] != f.keys[1] || len(f.byKey) != 1 {
		t.Fatal("a lost answer is asked again under the same key and finds the same run", out, f.keys)
	}
}

func TestHermesApprovalWaitIsStoppedAndInterruptedFails(t *testing.T) {
	backendEnv(t)
	f := newFakeHermes(t)
	f.seq = []map[string]any{{"run_id": "run_abc", "status": "waiting_for_approval"}}
	addHermes(t, f, true)
	out, _ := runNode(stepSpec("hermes", "assist/h3"))
	if out["ok"] != false || f.stops != 1 || trace(out)["state"] != "cancel_requested" {
		t.Fatal("Arkism never approves: it asks Hermes to stop; stopping is not cancelled", out, f.stops)
	}
	f.seq = []map[string]any{{"run_id": "run_abc", "status": "interrupted", "error": "Gateway shutdown interrupted the run."}}
	out, _ = runNode(stepSpec("hermes", "assist/h4"))
	if out["ok"] != false || trace(out)["state"] != "failed" || !strings.Contains(sOr(out["error"]), "interrupted") {
		t.Fatal(out)
	}
	f.seq = []map[string]any{{"run_id": "run_abc", "status": "mystery"}}
	out, _ = runNode(stepSpec("hermes", "assist/h5"))
	if out["ok"] != false || trace(out)["state"] != "unknown" {
		t.Fatal("an unknown Hermes status is never success", out)
	}
}
