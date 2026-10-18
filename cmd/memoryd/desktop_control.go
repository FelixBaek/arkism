package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
	"github.com/FelixBaek/arkism/internal/memory"
)

func memoryWorkerRound(ctx context.Context, gate *desktopcontrol.Gate, round func(context.Context)) bool {
	if gate == nil {
		round(ctx)
		return true
	}
	release, err := gate.Enter(desktopcontrol.Round)
	if err != nil {
		return false
	}
	defer release()
	round(ctx)
	return true
}

func memoryWorkerLoop(ctx context.Context, gate *desktopcontrol.Gate, stop <-chan struct{}, interval time.Duration, round func(context.Context)) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		default:
		}
		memoryWorkerRound(ctx, gate, round)
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// The admission covers the entire privacy/WorkOnce round, including its deferred
// FinishJob. Idle timer waits are not active work and can be stopped immediately.
func runMemoryWorker(ctx context.Context, control *desktopcontrol.Controller, interval time.Duration, round func(context.Context)) error {
	if control == nil {
		memoryWorkerLoop(ctx, nil, nil, interval, round)
		return nil
	}
	stop, done := make(chan struct{}), make(chan struct{})
	shutdown := func(ctx context.Context) error {
		close(stop)
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return desktopcontrol.ErrTimeout
		}
	}
	if err := control.Start(shutdown); err != nil {
		return err
	}
	memoryWorkerLoop(ctx, control.Admission(), stop, interval, round)
	close(done)
	if control.Requested() {
		return control.AwaitShutdown()
	}
	return ctx.Err()
}

func serveMemoryServer(ctx context.Context, server *http.Server, listener net.Listener, control *desktopcontrol.Controller) error {
	if control != nil {
		// Both the outer request and the actual mux work inside TimeoutHandler
		// retain admission; a timed-out response is not proof of idle model work.
		server.Handler = memory.WithRequestWork(server.Handler, control.Admission().Middleware)
		if err := control.Start(func(ctx context.Context) error {
			if err := server.Shutdown(ctx); err != nil {
				return desktopcontrol.ErrStop
			}
			return nil
		}); err != nil {
			return errors.Join(err, listener.Close())
		}
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdown)
		case <-done:
		}
	}()
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	if control != nil && control.Requested() {
		// Serve returns before Shutdown's final result/receipt is accounted for.
		return errors.Join(err, control.AwaitShutdown())
	}
	return err
}
