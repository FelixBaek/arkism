package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
)

// Normal startup retains its first-use token creation. Recovery is read-only and
// cannot turn an absent token into a new credential while replaying a running role.
func desktopGatewayToken(controlled bool) (string, error) {
	switch os.Getenv("ARKISM_DESKTOP_RECOVERY") {
	case "", "0":
		return gatewayToken()
	case "1":
		if !controlled {
			return "", desktopcontrol.ErrConfiguration
		}
		if token := os.Getenv("ARKISM_GATEWAY_TOKEN"); token != "" {
			if !host.ValidGatewayToken(token) {
				return "", desktopcontrol.ErrConfiguration
			}
			return token, nil
		}
		token, err := host.ReadGatewayToken(host.GatewayTokenFile)
		if err != nil {
			return "", errors.New("desktop_gateway_token_unavailable")
		}
		return token, nil
	default:
		return "", desktopcontrol.ErrConfiguration
	}
}

var (
	errDesktopBusy    = errors.New("desktop_gateway_busy")
	errClaudeTreeStop = errors.New("desktop_cli_tree_stop_failed")
	errClaudeRootStop = errors.New("desktop_cli_root_stop_failed")
	errClaudeWait     = errors.New("desktop_cli_exit_unconfirmed")
)

func serveDesktopGateway(server *http.Server, listener net.Listener, control *desktopcontrol.Controller, bridge *claudeBridge) error {
	if control == nil {
		return server.Serve(listener)
	}
	server.Handler = control.Admission().Middleware(server.Handler)
	stop := func(ctx context.Context) error {
		if !bridge.desktopIdle() {
			return errDesktopBusy
		}
		if err := server.Shutdown(ctx); err != nil {
			return desktopcontrol.ErrStop
		}
		return bridge.closeForDesktop(ctx)
	}
	if err := control.Start(stop, bridge.desktopIdle); err != nil {
		_ = listener.Close()
		return err
	}
	err := server.Serve(listener)
	if !control.Requested() {
		return err
	}
	// Serve can return before the control goroutine has finished checked child cleanup and
	// written the receipt. Keep the role alive until that write is complete (or unavailable).
	if shutdown := control.AwaitShutdown(); shutdown != nil {
		return shutdown
	}
	return err
}

func (b *claudeBridge) desktopIdle() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, run := range b.runs {
		if run.busy {
			return false
		}
	}
	return true
}

func (b *claudeBridge) closeForDesktop(ctx context.Context) error {
	b.mu.Lock()
	if ctx.Err() != nil {
		b.mu.Unlock()
		return desktopcontrol.ErrTimeout
	}
	for _, run := range b.runs {
		if run.busy {
			b.mu.Unlock()
			return errDesktopBusy
		}
	}
	runs := b.runs
	b.runs = nil
	for _, run := range runs {
		if run.timer != nil {
			run.timer.Stop()
		}
	}
	b.mu.Unlock()
	finished := make(chan error, len(runs))
	for _, run := range runs {
		go func(run *claudeRun) { finished <- run.closeChecked(ctx) }(run)
	}
	var failure error
	for range runs {
		select {
		case err := <-finished:
			failure = errors.Join(failure, err)
		case <-ctx.Done():
			return errors.Join(failure, desktopcontrol.ErrTimeout)
		}
	}
	if ctx.Err() != nil {
		return errors.Join(failure, desktopcontrol.ErrTimeout)
	}
	return failure
}

// A root-only fallback never turns a failed tree stop into a verified success. Native
// Job accounting is still required even after this role-level close returns nil.
func stopClaudeProcess(ctx context.Context, tree, root func() error, exited <-chan struct{}) error {
	if ctx.Err() != nil {
		return errClaudeWait
	}
	var failure error
	if tree() != nil {
		failure = errClaudeTreeStop
		if ctx.Err() != nil {
			return errors.Join(failure, errClaudeWait)
		}
		if root() != nil {
			failure = errors.Join(failure, errClaudeRootStop)
		}
	}
	// The legacy background-context path keeps its original five-second wait *after*
	// tree termination. Desktop shutdown additionally carries the parent's total deadline.
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case <-exited:
		if wait.Err() != nil {
			return errors.Join(failure, errClaudeWait)
		}
		return failure
	case <-wait.Done():
		return errors.Join(failure, errClaudeWait)
	}
}
