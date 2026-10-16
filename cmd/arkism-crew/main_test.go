package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/crewrun"
	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/capture"
)

// A crew run's receipt reaches the session spool in the shape EnqueueSessionTrace reads.
func TestSessionSinkSpoolsTheReceipt(t *testing.T) {
	dir := t.TempDir()
	capture.SetBenchState(dir)
	defer capture.SetBenchState(host.CaptureState)
	t.Setenv("ARKISM_WORKSPACE", "w")
	t.Setenv("ARKISM_PROJECT", "p")
	t.Setenv("ARKISM_USER", "u")
	t.Setenv("ARKISM_AGENT", "crew")
	t.Setenv("ARKISM_SHARED_AUDIENCE_SCOPE", "")
	t.Setenv("ARKISM_SESSION_PROCESSOR", "1")
	r := crewrun.TraceReceipt{
		Scope:        map[string]string{"workspace_id": "w", "project_id": "p", "user_id": "u", "agent_id": "crew"},
		SessionID:    "crew-run-1",
		OccurredAt:   "2026-10-16T00:00:00.000Z",
		Trace:        crewrun.Ref{Collection: "execution_traces", ID: "tr", Revision: 1},
		Run:          crewrun.Ref{Collection: "agent_runs", ID: "run", Revision: 1},
		SourceEvents: []crewrun.Ref{{Collection: "sources", ID: "e1", Revision: 1}},
	}
	if err := sessionSink(r); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "spool", "*.json"))
	if len(files) != 1 {
		t.Fatal(files)
	}
	raw, _ := os.ReadFile(files[0])
	if !strings.Contains(string(raw), "execution_receipt") || !strings.Contains(string(raw), "execution_traces") {
		t.Fatal(string(raw))
	}
}

func TestCommandLine(t *testing.T) {
	cfg := crewrun.FromEnv()
	cfg.Engine.HooksFile = filepath.Join(t.TempDir(), "crew-hooks.json")
	cfg.Engine.PublicURL = "http://127.0.0.1:8095"
	var out bytes.Buffer
	for _, args := range [][]string{nil, {"nope"}, {"hook-url"}, {"run"}, {"run", "x", "--input", "{bad"}} {
		if code := run(context.Background(), args, &out, cfg); code != 2 {
			t.Fatal("usage error", args, code)
		}
	}
	if code := run(context.Background(), []string{"hook-url", "auto-1"}, &out, cfg); code != 0 {
		t.Fatal("hook-url", code)
	}
	first := out.String()
	if !strings.HasPrefix(first, "POST http://127.0.0.1:8095/hooks/auto-1\nX-Arkism-Hook-Token: ") {
		t.Fatal("hook-url output", first)
	}
	out.Reset()
	_ = run(context.Background(), []string{"hook-url", "auto-1"}, &out, cfg)
	if out.String() != first {
		t.Fatal("hook token changed on the second call")
	}
}
