package crew

import (
	"path/filepath"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/notifycfg"
)

func TestLocalRunnerConfigUsesThisPC(t *testing.T) {
	for _, k := range []string{"ARKISM_CREW_RUNNER", "ARKISM_CREW_KEYS", "ARKISM_CREW_NOTIFY"} {
		t.Setenv(k, "")
	}
	exe := filepath.Join("C:", "Program Files", "Arkism", "arkism.exe")
	e := localRunnerConfig(exe).Engine
	if len(e.Runner) != 2 || e.Runner[0] != exe || e.Runner[1] != "crew" {
		t.Fatalf("runner %q: the program path must stay one argument", e.Runner)
	}
	if e.KeysFile != host.KeysFile || e.NotifyFile != notifycfg.Path() || e.Admit == nil {
		t.Fatalf("keys %q notify %q admit %v", e.KeysFile, e.NotifyFile, e.Admit != nil)
	}
	release, err := e.Admit()
	if err != nil {
		t.Fatal(err)
	}
	release()

	t.Setenv("ARKISM_CREW_RUNNER", "/srv/arkism/bin/arkism crew")
	if got := localRunnerConfig(exe).Engine.Runner; got[0] != "/srv/arkism/bin/arkism" {
		t.Fatalf("an explicit runner must win, got %q", got)
	}
}

func TestLocalRunnerOnlyOnTheDesktop(t *testing.T) {
	t.Setenv("ARKISM_PC_LOCAL", "")
	startLocalRunner(make(chan struct{}), nil)() // returns at once: nothing started
}
