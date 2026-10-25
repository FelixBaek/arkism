// Command backendref is a local reference runner for the arkism.backend.v1 contract (guide/agent-backends.md).
// It runs no model and no tools: a task's answer is a short echo, so the host runner's side of the contract
// (start, idempotent retry, status, cancel, memory section) can be tried on localhost. It is not any product's runner.
//
//	go run ./tools/backendref -addr 127.0.0.1:18790 -delay 2s
package main

import (
	"crypto/subtle"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type task struct {
	ID     string
	Status string
	Text   string
	Done   time.Time
}

type runner struct {
	mu     sync.Mutex
	token  string
	delay  time.Duration
	caps   map[string]bool
	tasks  map[string]*task
	byKey  map[string]string // Idempotency-Key -> task id
	nextID int
}

func main() {
	addr := flag.String("addr", "127.0.0.1:18790", "listen address (keep it on loopback)")
	token := flag.String("token", "", "bearer token to require; empty accepts any caller")
	delay := flag.Duration("delay", 0, "0 answers at once; otherwise accept and finish after this long")
	caps := flag.String("caps", "status,cancel,idempotency,tool_restriction,instructions,memory", "capabilities to report")
	flag.Parse()
	r := &runner{token: *token, delay: *delay, caps: map[string]bool{}, tasks: map[string]*task{}, byKey: map[string]string{}}
	for _, c := range strings.Split(*caps, ",") {
		if c = strings.TrimSpace(c); c != "" {
			r.caps[c] = true
		}
	}
	log.Printf("backendref (local reference runner, not a product) on http://%s", *addr)
	srv := &http.Server{Addr: *addr, Handler: r, ReadTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

func (r *runner) ServeHTTP(w http.ResponseWriter, q *http.Request) {
	if r.token != "" && subtle.ConstantTimeCompare([]byte(q.Header.Get("Authorization")), []byte("Bearer "+r.token)) != 1 {
		reply(w, 401, map[string]any{"error": map[string]any{"code": "auth", "message": "bad token"}})
		return
	}
	if q.Method == http.MethodPost && q.URL.Path == "/v1/tasks" {
		r.start(w, q) // it reads the body before taking the lock, so a slow sender does not hold up the others
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch path := q.URL.Path; {
	case q.Method == http.MethodGet && path == "/v1/capabilities":
		reply(w, 200, map[string]any{"protocol": "arkism.backend.v1", "capabilities": r.caps})
	case q.Method == http.MethodPost && strings.HasSuffix(path, "/cancel"):
		t := r.tasks[strings.TrimSuffix(strings.TrimPrefix(path, "/v1/tasks/"), "/cancel")]
		if t == nil || !r.caps["cancel"] {
			http.NotFound(w, q)
			return
		}
		if r.view(t)["status"] != "succeeded" { // a task past its delay has already succeeded
			t.Status = "cancelled"
		}
		reply(w, 200, r.view(t))
	case q.Method == http.MethodGet && strings.HasPrefix(path, "/v1/tasks/"):
		t := r.tasks[strings.TrimPrefix(path, "/v1/tasks/")]
		if t == nil || !r.caps["status"] {
			http.NotFound(w, q)
			return
		}
		reply(w, 200, r.view(t))
	default:
		http.NotFound(w, q)
	}
}

func (r *runner) start(w http.ResponseWriter, q *http.Request) {
	var body struct {
		Protocol string `json:"protocol"`
		Task     string `json:"task"`
		Access   struct {
			Mode string `json:"mode"`
		} `json:"access"`
		Memory *struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		} `json:"memory"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, q.Body, 1<<20)).Decode(&body) != nil || body.Protocol != "arkism.backend.v1" || body.Access.Mode != "read" {
		reply(w, 400, map[string]any{"error": map[string]any{"code": "bad_request", "message": "need arkism.backend.v1 and read access"}})
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := q.Header.Get("Idempotency-Key")
	if id, ok := r.byKey[key]; ok && key != "" && r.caps["idempotency"] {
		reply(w, 200, r.view(r.tasks[id])) // the same key is the same task, never a second one
		return
	}
	r.nextID++
	t := &task{ID: fmt.Sprintf("ref-%d", r.nextID), Status: "accepted", Done: time.Now().Add(r.delay)}
	memory := 0
	if body.Memory != nil {
		memory = len(body.Memory.Items)
	}
	t.Text = fmt.Sprintf("reference runner echo (%d memory items): %s", memory, clip(body.Task, 200))
	r.tasks[t.ID] = t
	if key != "" {
		r.byKey[key] = t.ID
	}
	code := 202
	if r.delay == 0 {
		code = 200
	}
	reply(w, code, r.view(t))
}

// view is a task's reply; an accepted task turns succeeded once its delay has passed.
func (r *runner) view(t *task) map[string]any {
	if t.Status == "accepted" && !time.Now().Before(t.Done) {
		t.Status = "succeeded"
	}
	out := map[string]any{"task_id": t.ID, "status": t.Status}
	if t.Status == "succeeded" {
		out["result"] = map[string]any{"text": t.Text}
	}
	return out
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
