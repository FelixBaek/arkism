package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
	"github.com/FelixBaek/arkism/internal/memory"
)

func serveDaemon(ctx context.Context, svc *memory.Service, cfg daemonOptions) error {
	role := "server"
	if cfg.Worker {
		role = "worker"
	}
	control, err := desktopcontrol.FromEnvironment(role, os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	if cfg.Worker {
		return serveDaemonWorker(ctx, svc, cfg, control)
	}
	return serveDaemonHTTP(ctx, svc, cfg, control)
}

func serveDaemonHTTP(ctx context.Context, svc *memory.Service, cfg daemonOptions, control *desktopcontrol.Controller) error {
	handler := memory.Handler(svc)
	if cfg.Recovery {
		handler = memory.RecoveryHandler(svc)
	}
	server := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 50 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	slog.Info("memory API starting", "address", listener.Addr().String(), "recovery", cfg.Recovery)
	return serveMemoryServer(ctx, server, listener, control)
}

type daemonWorkerStep struct {
	service                  *memory.Service
	token                    string
	recovery, noAgentRuntime bool
	expiryScope              memory.Scope
	expiryDays               int
	nextSweep, nextWatch     time.Time
}

func serveDaemonWorker(ctx context.Context, svc *memory.Service, cfg daemonOptions, control *desktopcontrol.Controller) error {
	token := os.Getenv("MEMORY_WORKER_TOKEN")
	if token == "" {
		return memory.ErrInvalid
	}
	scope, days, err := candidateSweepConfig()
	if err != nil {
		return err
	}
	worker := &daemonWorkerStep{service: svc, token: token, recovery: cfg.Recovery, noAgentRuntime: os.Getenv("MEMORY_PRIVACY_NO_AGENT_RUNTIME") == "true", expiryScope: scope, expiryDays: days}
	return runMemoryWorker(ctx, control, 2*time.Second, worker.run)
}

func (w *daemonWorkerStep) run(ctx context.Context) {
	op, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if w.noAgentRuntime {
		if _, err := w.service.RunPrivacyRuntimeStep(op, w.token); err != nil && !errors.Is(err, memory.ErrNotFound) {
			slog.Warn("privacy runtime step deferred")
		}
	}
	if !w.recovery && w.expiryDays > 0 && time.Now().After(w.nextSweep) {
		if err := sweepCandidates(op, w.service, w.token, w.expiryScope, w.expiryDays); err != nil {
			slog.Warn("candidate expiry deferred; check scoped expiry permission")
		}
		w.nextSweep = time.Now().Add(time.Hour)
	}
	// Skill watches roll a worse applied version back even when nobody opens the skill.
	if !w.recovery && time.Now().After(w.nextWatch) {
		if _, err := w.service.RunSkillWatches(op, w.token); err != nil {
			slog.Warn("skill watch check deferred; retried next hour")
		}
		w.nextWatch = time.Now().Add(time.Hour)
	}
	slog.Debug("memory worker iteration")
	if err := w.service.WorkOnce(op, w.token); err != nil && !errors.Is(err, memory.ErrNotFound) {
		slog.Warn("memory job deferred; see scoped job status")
	}
}
