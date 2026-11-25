package main

import (
	"context"
	"errors"
	"testing"

	"github.com/FelixBaek/arkism/internal/memory"
)

func TestCandidateOnlyShadowDaemon(t *testing.T) {
	t.Setenv("MEMORY_DYNAMICS_READER_TOKEN", "")
	t.Setenv("MEMORY_WORKER_TOKEN", "")
	scope := memory.Scope{WorkspaceID: "qa-space", ProjectID: "qa-project", UserID: "qa-user"}
	input := func(mode, policy string, scopes []memory.Scope) daemonOptions {
		load, bounds := daemonDynamicsFixture(t)
		bounds.Scopes = scopes
		runtimePath, loadPath := daemonDynamicsFiles(t, load, bounds)
		settings := daemonSearchModeFiles(t, func(m map[string]any) { m["candidate_only"] = true; m["mode"] = mode; m["policy"] = policy })
		return daemonOptions{Listen: "127.0.0.1:0", DynamicsRuntimeConfig: runtimePath, DynamicsLoadConfig: loadPath, DynamicsSearchConfig: settings}
	}
	base := input("shadow", "B1", []memory.Scope{scope})
	cfg, err := prepareDaemonDynamics(base)
	if err != nil || cfg.dynamicsPlan != nil || cfg.dynamicsReaderToken != "" || cfg.dynamicsSearch == nil || !cfg.dynamicsSearch.CandidateOnly {
		t.Fatal("unexpected authorization or state wiring", err)
	}
	service := &memory.Service{} // a diagnostic cannot require a database or service principal
	if err := configureDaemonDynamics(context.Background(), service, cfg); err != nil || service.NativeSearchShadow == nil || service.DynamicsLoad != nil || service.NativeDynamics != nil {
		t.Fatal("unneeded runtime enabled", err)
	}
	if bg, err := newDaemonDynamicsBackground(context.Background(), service, cfg, nil); err != nil || bg != nil {
		t.Fatal("unexpected refresh", err)
	}
	for _, bad := range []daemonOptions{input("dynamic", "B1", []memory.Scope{scope}), input("shadow", "B2", []memory.Scope{scope}), input("shadow", "B1", []memory.Scope{{WorkspaceID: "qa-space", ProjectID: "qa-project"}}), input("shadow", "B1", []memory.Scope{scope, {WorkspaceID: "qa-space", ProjectID: "qa-project", UserID: "other"}})} {
		if _, err := prepareDaemonDynamics(bad); !errors.Is(err, memory.ErrInvalid) {
			t.Fatal("invalid candidate-only mode accepted", err)
		}
	}
	for _, edit := range []func(*daemonOptions){func(c *daemonOptions) { c.Worker = true }, func(c *daemonOptions) { c.DynamicsRepair = true }, func(c *daemonOptions) { c.DynamicsLoadConfig = "" }} {
		bad := base
		edit(&bad)
		if _, err := prepareDaemonDynamics(bad); !errors.Is(err, memory.ErrInvalid) {
			t.Fatal("invalid role or missing load accepted", err)
		}
	}
}
