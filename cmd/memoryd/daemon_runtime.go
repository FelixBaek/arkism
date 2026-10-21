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
		token := os.Getenv("MEMORY_WORKER_TOKEN")
		if token == "" {
			return memory.ErrInvalid
		}
		expiryScope, expiryDays, err := candidateSweepConfig()
		if err != nil {
			return err
		}
		nextSweep := time.Time{}
		noAgentRuntime := os.Getenv("MEMORY_PRIVACY_NO_AGENT_RUNTIME") == "true"
		return runMemoryWorker(ctx, control, 2*time.Second, func(ctx context.Context) {
			op, c := context.WithTimeout(ctx, 90*time.Second)
			defer c()
			if noAgentRuntime {
				if _, perr := svc.RunPrivacyRuntimeStep(op, token); perr != nil && !errors.Is(perr, memory.ErrNotFound) {
					slog.Warn("privacy runtime step deferred")
				}
			}
			if !cfg.Recovery && expiryDays > 0 && time.Now().After(nextSweep) {
				if err := sweepCandidates(op, svc, token, expiryScope, expiryDays); err != nil {
					slog.Warn("candidate expiry deferred; check scoped expiry permission")
				}
				nextSweep = time.Now().Add(time.Hour)
			}
			slog.Debug("memory worker iteration")
			err := svc.WorkOnce(op, token)
			if err != nil && !errors.Is(err, memory.ErrNotFound) {
				slog.Warn("memory job deferred; see scoped job status")
			}
		})
	}
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
