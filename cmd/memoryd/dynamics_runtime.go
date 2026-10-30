package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
	"github.com/FelixBaek/arkism/internal/memory"
)

func validateDaemonDynamicsOptions(cfg daemonOptions) error {
	if cfg.DynamicsRuntimeConfig == "" && cfg.DynamicsLoadConfig == "" {
		return nil
	}
	if cfg.DynamicsRuntimeConfig == "" || cfg.DynamicsLoadConfig == "" || cfg.Recovery {
		return memory.ErrInvalid
	}
	mode, err := daemonMode(cfg)
	if err != nil || (mode != "HTTP" && mode != "Worker") {
		return memory.ErrInvalid
	}
	return nil
}

// Called once before any DB/diagnostic connection. Tokens select authority but
// never grant it; actual scoped proof/binding checks remain in every operation.
func prepareDaemonDynamics(cfg daemonOptions) (daemonOptions, error) {
	if err := validateDaemonDynamicsOptions(cfg); err != nil {
		return cfg, err
	}
	plan, err := loadDaemonDynamicsPlan(cfg.DynamicsRuntimeConfig, cfg.DynamicsLoadConfig)
	if err != nil {
		return cfg, err
	}
	cfg.dynamicsPlan = plan
	if plan == nil {
		return cfg, nil
	}
	if cfg.Worker {
		if strings.TrimSpace(os.Getenv("MEMORY_WORKER_TOKEN")) == "" {
			return cfg, memory.ErrInvalid
		}
	} else {
		cfg.dynamicsReaderToken = os.Getenv("MEMORY_DYNAMICS_READER_TOKEN")
		if strings.TrimSpace(cfg.dynamicsReaderToken) == "" {
			return cfg, memory.ErrInvalid
		}
	}
	return cfg, nil
}

func configureDaemonDynamics(ctx context.Context, svc *memory.Service, cfg daemonOptions) error {
	if cfg.dynamicsPlan == nil {
		return nil
	}
	runtime, err := memory.NewDynamicsLoadRuntime(cfg.dynamicsPlan.raw)
	if err != nil {
		return err
	}
	svc.DynamicsLoad = runtime
	svc.NativeDynamics = &memory.NativeDynamicsRuntime{Enabled: true}
	if cfg.Worker {
		return nil
	}
	if svc.Auth == nil || cfg.dynamicsReaderToken == "" {
		return memory.ErrInvalid
	}
	// This checks only the configured reader's service/read:jobs grant. It does
	// not claim all-scope absence or grant future state/source/deletion access.
	for _, scope := range cfg.dynamicsPlan.config.Scopes {
		decision, e := svc.Auth.Authorize(ctx, cfg.dynamicsReaderToken, memory.Authorization{Action: "read:jobs", Scope: scope})
		if e != nil {
			return e
		}
		if !decision.Allowed || decision.Role != "service" {
			return memory.ErrDenied
		}
	}
	return nil
}

type daemonDynamicsRefresher interface {
	RefreshDynamicsSnapshot(context.Context, string, memory.Scope) error
}

func daemonDynamicsRefreshRound(ctx context.Context, r daemonDynamicsRefresher, token string, plan *daemonDynamicsPlan, first int) error {
	if len(plan.config.Scopes) == 0 || first < 0 {
		return memory.ErrInvalid
	}
	op, cancel := context.WithTimeout(ctx, time.Duration(plan.config.Limits.WorkerSubstepTimeoutNS))
	defer cancel()
	var result error
	for offset := range len(plan.config.Scopes) {
		scope := plan.config.Scopes[(first+offset)%len(plan.config.Scopes)]
		if op.Err() != nil {
			return errors.Join(result, op.Err())
		}
		if err := r.RefreshDynamicsSnapshot(op, token, scope); err != nil && !errors.Is(err, memory.ErrNotFound) && !errors.Is(err, memory.ErrDynamicsSnapshotUnavailable) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func newDaemonDynamicsBackground(ctx context.Context, svc *memory.Service, cfg daemonOptions, control *desktopcontrol.Controller) (*memoryServerBackground, error) {
	if cfg.dynamicsPlan == nil {
		return nil, nil
	}
	if cfg.Worker || cfg.Recovery || cfg.dynamicsReaderToken == "" || svc == nil || svc.DynamicsLoad == nil {
		return nil, memory.ErrInvalid
	}
	var gate *desktopcontrol.Gate
	if control != nil {
		gate = control.Admission()
	}
	return daemonDynamicsBackground(ctx, gate, time.Duration(cfg.dynamicsPlan.config.Limits.PublishIntervalNS), daemonDynamicsRefreshStep(svc, cfg.dynamicsReaderToken, cfg.dynamicsPlan)), nil
}

func daemonDynamicsRefreshStep(r daemonDynamicsRefresher, token string, plan *daemonDynamicsPlan) func(context.Context) {
	var warned sync.Once
	nextScope := 0
	return func(op context.Context) {
		first := nextScope
		nextScope = (nextScope + 1) % len(plan.config.Scopes)
		if err := daemonDynamicsRefreshRound(op, r, token, plan, first); err != nil && op.Err() == nil {
			// A short explicit publish interval must not create an unbounded warning stream.
			warned.Do(func() { slog.Warn("dynamics snapshot refresh deferred") })
		}
	}
}

func daemonDynamicsBackground(ctx context.Context, gate *desktopcontrol.Gate, interval time.Duration, round func(context.Context)) *memoryServerBackground {
	run, cancel := context.WithCancel(ctx)
	ready, done := make(chan struct{}), make(chan struct{})
	var once sync.Once
	go func() {
		defer close(done)
		select {
		case <-run.Done():
			return
		case <-ready:
		}
		memoryWorkerLoop(run, gate, nil, interval, round)
	}()
	return &memoryServerBackground{
		startWork: func() { once.Do(func() { close(ready) }) },
		stopWork: func(stop context.Context) error {
			cancel()
			select {
			case <-done:
				return nil
			case <-stop.Done():
				return desktopcontrol.ErrTimeout
			}
		},
	}
}
