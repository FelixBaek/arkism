package main

// A managed local model server. A neural embedding model or cross-encoder cannot
// run inside this pure Go server: it needs a native inference runtime (cgo plus a
// platform library) and model weights, which would make every build and install
// depend on them. Instead memoryd can start one local inference server next to
// itself (ARKISM_MODEL_SIDECAR, a JSON argv array), wait until its loopback ready
// URL answers, and stop it on exit. Point TEI_ENDPOINT_URL, ARKISM_RERANK_URL or
// SKILL_LLM_PRESET=local at it: text then never leaves this machine.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
)

// sidecarEnv is what the sidecar inherits; the server's own secrets stay out.
var sidecarEnv = []string{"PATH", "HOME", "TMPDIR", "LANG", "USERPROFILE", "SYSTEMROOT", "LOCALAPPDATA"}

// startModels starts the optional model sidecar, then reads the embedder.
func startModels(ctx context.Context, cfg daemonOptions) (memory.Embedder, func(), error) {
	stop, err := startModelSidecar(ctx)
	if err != nil {
		return nil, nil, err
	}
	embed, err := configuredEmbedder(cfg.Worker, cfg.Recovery)
	if err != nil {
		stop()
		return nil, nil, err
	}
	return embed, stop, nil
}

func startModelSidecar(ctx context.Context) (func(), error) {
	raw := os.Getenv("ARKISM_MODEL_SIDECAR")
	if raw == "" {
		return func() {}, nil
	}
	var argv []string
	if json.Unmarshal([]byte(raw), &argv) != nil || len(argv) == 0 || argv[0] == "" {
		return nil, fmt.Errorf(`ARKISM_MODEL_SIDECAR must be a JSON array such as ["/opt/model/server","--port","8089"]: %w`, memory.ErrInvalid)
	}
	extra, err := sidecarExtraEnv(os.Getenv("ARKISM_MODEL_SIDECAR_ENV"))
	if err != nil {
		return nil, err
	}
	ready := os.Getenv("ARKISM_MODEL_SIDECAR_READY_URL")
	if u, err := url.Parse(ready); err != nil || u.Scheme != "http" || !memory.LoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("ARKISM_MODEL_SIDECAR_READY_URL must be a loopback http URL: %w", memory.ErrInvalid)
	}
	wait := 2 * time.Minute
	if v := os.Getenv("ARKISM_MODEL_SIDECAR_WAIT"); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil || parsed <= 0 || parsed > 30*time.Minute {
			return nil, fmt.Errorf("ARKISM_MODEL_SIDECAR_WAIT: %w", memory.ErrInvalid)
		}
		wait = parsed
	}
	// The API server and the worker share one env file: the second one finds it running.
	if sidecarReady(ctx, ready) {
		return func() {}, nil
	}
	return launchSidecar(ctx, argv, extra, ready, wait)
}

// sidecarSecretName matches names the server keeps to itself: its own settings
// and anything shaped like a credential or a database address.
var sidecarSecretName = regexp.MustCompile(`(?i)^(ARKISM|MEMORY|SKILL|TEI)_|KEY|TOKEN|SECRET|PASS|CREDENTIAL|AUTH|DSN|DATABASE|COOKIE|SESSION`)

// sidecarExtraEnv reads ARKISM_MODEL_SIDECAR_ENV and refuses any name that could hand the sidecar a server secret.
func sidecarExtraEnv(raw string) ([]string, error) {
	var names []string
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if sidecarSecretName.MatchString(name) {
			return nil, fmt.Errorf("ARKISM_MODEL_SIDECAR_ENV may not pass %s to the sidecar: %w", name, memory.ErrInvalid)
		}
		names = append(names, name)
	}
	return names, nil
}

func launchSidecar(ctx context.Context, argv, extra []string, ready string, wait time.Duration) (func(), error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.Env = []string{} // never nil: a nil Env would inherit everything
	for _, name := range append(slices.Clone(sidecarEnv), extra...) {
		if v, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+v)
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("model sidecar: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stop := func() {
		if runtime.GOOS == "windows" {
			_ = cmd.Process.Kill()
		} else {
			_ = cmd.Process.Signal(os.Interrupt)
		}
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for !sidecarReady(ctx, ready) {
		select {
		case err := <-exited:
			exited <- err // stop() reads it again
			return nil, fmt.Errorf("model sidecar exited before it was ready (%v): %w", err, memory.ErrUnavailable)
		case <-ctx.Done():
			stop()
			return nil, ctx.Err()
		case <-deadline.C:
			stop()
			return nil, fmt.Errorf("model sidecar not ready after %s: %w", wait, memory.ErrUnavailable)
		case <-time.After(250 * time.Millisecond):
		}
	}
	// shortcut: a sidecar that dies later is not restarted (calls fail until memoryd restarts); add supervision if that proves common.
	slog.Info("model sidecar ready", "command", argv[0], "pid", cmd.Process.Pid)
	return stop, nil
}

func sidecarReady(ctx context.Context, ready string) bool {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probe, http.MethodGet, ready, nil)
	if err != nil {
		return false
	}
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
