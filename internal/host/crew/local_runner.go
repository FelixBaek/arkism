package crew

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/FelixBaek/arkism/internal/crewrun"
	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
)

// localRunnerConfig is the workflow runner for the desktop's own Arkism: the same engine the
// server runs as arkism-crew, pointed at this PC's files. Explicit ARKISM_CREW_* settings win.
func localRunnerConfig(exe string) crewrun.Config {
	cfg := crewrun.FromEnv()
	e := cfg.Engine
	if os.Getenv("ARKISM_CREW_RUNNER") == "" {
		e.Runner = []string{exe, "crew"} // a path with spaces stays one argument
	}
	if os.Getenv("ARKISM_CREW_KEYS") == "" {
		e.KeysFile = host.KeysFile // the keys 설정 › 모델 saves through this watcher
	}
	// A run holds the update gate like any other background job, so an update waits for it.
	e.Admit = func() (func(), error) { return desktopcontrol.Default.Enter(desktopcontrol.Pool) }
	return cfg
}

// startLocalRunner serves workflows (runs, schedules, webhooks) until stop closes; the returned
// wait blocks until the runner has let go of its runs. Only the desktop's watcher starts it.
func startLocalRunner(stop <-chan struct{}, stderr io.Writer) (wait func()) {
	if os.Getenv("ARKISM_PC_LOCAL") != "1" {
		return func() {
			// Not the desktop's own Arkism: the server runs arkism-crew itself.
		}
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "워크플로우 실행기를 켜지 못했어요: %s\n", errName(err))
		return func() {
			// Nothing was started.
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			// Serve returns at once when its port or lock is taken (another runner on this PC); try again later.
			if err := crewrun.Serve(ctx, localRunnerConfig(exe)); err != nil && ctx.Err() == nil {
				fmt.Fprintf(stderr, "워크플로우 실행기 다시 시도: %s\n", err.Error())
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Minute):
			}
		}
	}()
	go func() { <-stop; cancel() }()
	return func() { <-done }
}
