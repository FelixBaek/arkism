package crew

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
)

func watchLoop(gate *desktopcontrol.Gate, stop <-chan struct{}, interval time.Duration, round func()) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		if release, err := gate.Enter(desktopcontrol.Round); err == nil {
			func() { defer release(); round() }()
		}
		timer := time.NewTimer(interval)
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func desktopProvidersWatch(interval, every time.Duration, stdin io.Reader, stdout io.Writer) error {
	control, err := desktopcontrol.FromEnvironment("watcher", stdin, stdout)
	if err != nil {
		return err
	}
	if control == nil {
		providersWatch(interval, every, os.Stderr)
		return nil
	}
	return runDesktopWatcher(control, func(stop <-chan struct{}) {
		providersWatchUntil(interval, every, os.Stderr, stop)
	}, poolJobs.Wait, host.AccountsWait)
}

func runDesktopWatcher(control *desktopcontrol.Controller, loop func(<-chan struct{}), waitPool, waitAccounts func()) error {
	stop, done := make(chan struct{}), make(chan struct{})
	shutdown := func(ctx context.Context) error {
		close(stop)
		select {
		case <-done:
			waitPool()
			waitAccounts()
			return nil
		case <-ctx.Done():
			return desktopcontrol.ErrTimeout
		}
	}
	if err := control.Start(shutdown); err != nil {
		return err
	}
	loop(stop)
	close(done)
	return control.AwaitShutdown()
}

// Both waits are registered before executing the report, including its error paths.
// Pool jobs can submit account probes, so the final account wait follows the pool.
func drainProviderReport(report func() (int, error), waitPool, waitAccounts func()) (int, error) {
	defer waitAccounts()
	defer waitPool()
	return report()
}

func providersReport(fresh bool) (int, error) {
	return drainProviderReport(func() (int, error) {
		if fresh {
			_ = os.Remove(stateFile("gemini-quota.json"))
			host.AccountsForce()
			if err := refreshModels(); err != nil {
				return 0, err
			}
			host.AccountsWait()
		}
		return providersRound(true)
	}, poolJobs.Wait, host.AccountsWait)
}
