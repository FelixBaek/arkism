package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeBackend struct {
	called, closed bool
	command        string
	revision       int64
	failure        error
	input          maintenanceInput
}

func (b *fakeBackend) Close() { b.closed = true }
func (b *fakeBackend) Execute(ctx context.Context, o options, in maintenanceInput) (any, error) {
	b.called = true
	b.command = o.command
	b.revision = o.revision
	b.input = in
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("missing deadline")
	}
	return map[string]any{"revision": o.revision, "active": true}, b.failure
}
func TestMaintenanceDispatchAndFailBeforeConnection(t *testing.T) {
	f := &fakeBackend{}
	opened := 0
	open := func(context.Context, string) (backend, error) { opened++; return f, nil }
	getenv := func(key string) string {
		if key == "MEMORY_DATABASE_URL" {
			return "private-dsn"
		}
		return ""
	}
	var out bytes.Buffer
	if e := runArgs(context.Background(), []string{"advance", "--expected-revision", "7"}, getenv, &out, open); e != nil || !f.closed || f.command != "advance" || f.revision != 7 || !strings.Contains(out.String(), `"revision":7`) {
		t.Fatal("dispatch failed", e)
	}
	opened = 0
	if e := runArgs(context.Background(), []string{"begin-privacy", "--request", "missing"}, getenv, &out, open); e == nil || opened != 0 {
		t.Fatal("missing acknowledgement connected")
	}
	if e := runArgs(context.Background(), []string{"begin-privacy", "--request", "missing", "--allow-global-downtime"}, getenv, &out, open); e == nil || opened != 0 {
		t.Fatal("missing credential connected")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	if e := os.WriteFile(path, []byte(`{"unknown":"secret"}`), 0600); e != nil {
		t.Fatal(e)
	}
	getToken := func(key string) string {
		if key == "MEMORY_MAINTENANCE_TOKEN" {
			return "private-token"
		}
		return getenv(key)
	}
	if e := runArgs(context.Background(), []string{"begin-privacy", "--request", path, "--allow-global-downtime"}, getToken, &out, open); e == nil || opened != 0 {
		t.Fatal("malformed request connected")
	}
	if e := os.WriteFile(path, []byte(`{"scope":{"workspace_id":"w","project_id":"p","user_id":"u"},"target_type":"subject","target_ids":["u"],"reason":"request","idempotency_key":"request-1"}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := runArgs(context.Background(), []string{"begin-privacy", "--request", path, "--allow-global-downtime"}, getToken, &out, open); e != nil || f.command != "begin-privacy" || f.input.privacy == nil || f.input.privacy.TargetType != "subject" || f.input.token != "private-token" {
		t.Fatal("begin forwarding", e)
	}
	if strings.Contains(out.String(), "private-") || strings.Contains(errorCode(errors.New("private-dsn password")), "private-") {
		t.Fatal("secret echoed")
	}
}

func TestMaintenanceHelpStatesClientBoundaryWithoutConnecting(t *testing.T) {
	var out bytes.Buffer
	open := func(context.Context, string) (backend, error) { t.Fatal("help opened database"); return nil, nil }
	if e := runArgs(context.Background(), []string{"--help"}, func(string) string { return "" }, &out, open); e != nil {
		t.Fatal(e)
	}
	for _, part := range []string{"--allow-global-downtime", "all older clients", "raw administrative SQL bypass it", "cannot attest"} {
		if !strings.Contains(out.String(), part) {
			t.Fatalf("missing client boundary %q", part)
		}
	}
}
