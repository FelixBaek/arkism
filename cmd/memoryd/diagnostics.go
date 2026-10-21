package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"time"
)

func configureLogging(level string) error {
	var parsed slog.Level
	if err := parsed.UnmarshalText([]byte(level)); err != nil {
		return errors.New("invalid log level")
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: parsed})))
	return nil
}

// Diagnostics never share the API listener and require an independent secret.
// Profiles can contain memory contents; they must stay on the operator's host.
func startDiagnostics(ctx context.Context, address, token string) (func(), error) {
	if address == "" {
		return func() { /* Diagnostics disabled: no listener owns resources. */ }, nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || len(token) < 32 {
		return nil, errors.New("pprof requires a loopback IP and MEMORY_DIAGNOSTICS_TOKEN (at least 32 bytes)")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="arkism-diagnostics"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, errors.New("diagnostics listener unavailable")
	}
	go func() { _ = srv.Serve(ln) }()
	stop := context.AfterFunc(ctx, func() { _ = srv.Close() })
	return func() { stop(); _ = srv.Close() }, nil
}

func envLogLevel() string {
	if v := os.Getenv("ARKISM_LOG_LEVEL"); v != "" {
		return v
	}
	return "info"
}
