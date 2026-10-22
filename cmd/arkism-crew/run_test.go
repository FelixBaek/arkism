package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/crewrun"
)

// With ARKISM_CREW_TEST_MAIN=1 the test binary runs main() as arkism-crew with no arguments.
func TestMain(m *testing.M) {
	if os.Getenv("ARKISM_CREW_TEST_MAIN") == "1" {
		os.Args = []string{"arkism-crew"}
		main()
	}
	os.Exit(m.Run())
}

// memStore keeps runs in memory; listErr and getErr make the store unavailable.
type memStore struct {
	mu      sync.Mutex
	crew    []crewrun.CrewItem
	runs    map[string]crewrun.Run
	listErr error
	getErr  error
}

func (s *memStore) ListCrew(context.Context) ([]crewrun.CrewItem, error) {
	return s.crew, s.listErr
}

func (s *memStore) ListRuns(context.Context, string, int) ([]crewrun.Run, error) { return nil, nil }

func (s *memStore) GetRun(_ context.Context, id string) (crewrun.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return crewrun.Run{}, s.getErr
	}
	r, ok := s.runs[id]
	if !ok {
		return crewrun.Run{}, &crewrun.APIError{Status: 404, Code: "not_found"}
	}
	return copyRun(r), nil
}

func (s *memStore) CreateRun(_ context.Context, st crewrun.RunState) (crewrun.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := crewrun.Run{ID: fmt.Sprintf("run-%d", len(s.runs)+1), Revision: 1, State: st}
	s.runs[r.ID] = copyRun(r)
	return r, nil
}

func (s *memStore) UpdateRun(_ context.Context, r crewrun.Run) (crewrun.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs[r.ID].Revision != r.Revision {
		return crewrun.Run{}, &crewrun.APIError{Status: 409, Code: "revision_conflict"}
	}
	r.Revision++
	s.runs[r.ID] = copyRun(r)
	return copyRun(r), nil
}

func copyRun(r crewrun.Run) crewrun.Run {
	raw, _ := json.Marshal(r)
	var c crewrun.Run
	_ = json.Unmarshal(raw, &c)
	return c
}

func workflow(id string, nodes ...crewrun.Node) crewrun.CrewItem {
	g := &crewrun.Graph{Nodes: nodes}
	for i := 1; i < len(nodes); i++ {
		g.Edges = append(g.Edges, crewrun.Edge{From: nodes[i-1].ID, To: nodes[i].ID})
	}
	return crewrun.CrewItem{ID: id, Revision: 1, Crew: crewrun.Crew{Type: "workflow", Name: id, Handle: id, Enabled: true,
		Trigger: &crewrun.Trigger{Kind: "webhook"}, Graph: g}}
}

func testConfig(t *testing.T, store *memStore) crewrun.Config {
	dir := t.TempDir()
	return crewrun.Config{Listen: "127.0.0.1:0", LockPath: filepath.Join(dir, "crewrun.lock"), Engine: &crewrun.Engine{
		Store: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Loc: time.UTC, DefaultModel: "opus 5.5",
		KeysFile: filepath.Join(dir, "keys.json"), NotifyFile: filepath.Join(dir, "notify.json"),
		HooksFile: filepath.Join(dir, "hooks.json"), PublicURL: "http://127.0.0.1:1"}}
}

func TestRunCommandDrivesAWorkflowToItsEnd(t *testing.T) {
	ok := workflow("ok", crewrun.Node{ID: "start", Type: "start"}, crewrun.Node{ID: "end", Type: "end"})
	boom := workflow("boom", crewrun.Node{ID: "start", Type: "start"},
		crewrun.Node{ID: "fail", Type: "code", Config: map[string]string{"lang": "sh", "code": "exit 3"}}, crewrun.Node{ID: "end", Type: "end"})
	store := &memStore{crew: []crewrun.CrewItem{ok, boom}, runs: map[string]crewrun.Run{}}
	cfg := testConfig(t, store)
	var out bytes.Buffer
	if code := run(context.Background(), []string{"run", "ok", "--input", `{"n":1}`}, &out, cfg); code != 0 {
		t.Fatal("succeeded run exit", code, out.String())
	}
	var got struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.RunID == "" || got.Status != "succeeded" {
		t.Fatal("run output", out.String(), err)
	}
	out.Reset()
	if code := run(context.Background(), []string{"run", "boom"}, &out, cfg); code != 1 || !strings.Contains(out.String(), `"status":"failed"`) {
		t.Fatal("failed run", code, out.String())
	}
	if code := run(context.Background(), []string{"run", "missing"}, &out, cfg); code != 1 {
		t.Fatal("unknown workflow started", code)
	}
}

func TestRunCommandReportsAnUnavailableStore(t *testing.T) {
	ok := workflow("ok", crewrun.Node{ID: "start", Type: "start"}, crewrun.Node{ID: "end", Type: "end"})
	down := errors.New("store down")
	cfg := testConfig(t, &memStore{listErr: down})
	if code := run(context.Background(), []string{"run", "ok"}, io.Discard, cfg); code != 1 {
		t.Fatal("crew load failure", code)
	}
	cfg = testConfig(t, &memStore{crew: []crewrun.CrewItem{ok}, runs: map[string]crewrun.Run{}, getErr: down})
	if code := run(context.Background(), []string{"run", "ok"}, io.Discard, cfg); code != 1 {
		t.Fatal("unreadable final run", code)
	}
}

func TestServeCommandStopsWithItsContext(t *testing.T) {
	cfg := testConfig(t, &memStore{runs: map[string]crewrun.Run{}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code := run(ctx, []string{"serve"}, io.Discard, cfg); code != 0 {
		t.Fatal("serve after shutdown", code)
	}
	cfg.Listen = "0.0.0.0:0"
	if code := run(context.Background(), []string{"serve"}, io.Discard, cfg); code != 1 {
		t.Fatal("non-loopback listener accepted", code)
	}
}

func TestHookURLReportsAnUnwritableHooksFile(t *testing.T) {
	cfg := testConfig(t, &memStore{})
	cfg.Engine.HooksFile = t.TempDir() // a directory, not a file
	if code := run(context.Background(), []string{"hook-url", "auto-1"}, io.Discard, cfg); code != 1 {
		t.Fatal("hook token from a directory", code)
	}
}

func TestBinaryWithoutCommandPrintsUsage(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "ARKISM_CREW_TEST_MAIN=1", "ARKISM_SESSION_PROCESSOR=1")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.HasPrefix(stderr.String(), "usage:\n  arkism-crew serve") {
		t.Fatal("usage", err, stderr.String())
	}
}
