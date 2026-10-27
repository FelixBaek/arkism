package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDiagnosticsRequireSeparateSecretAndLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "localhost:0", "bad-address"} {
		if stopDiagnostics, err := startDiagnostics(context.Background(), addr, strings.Repeat("x", 32)); err == nil {
			stopDiagnostics()
			t.Fatal("unsafe listener", addr)
		}
	}
	if stopDiagnostics, err := startDiagnostics(context.Background(), "127.0.0.1:0", ""); err == nil {
		stopDiagnostics()
		t.Fatal("anonymous diagnostics")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	token := strings.Repeat("test", 8)
	stopDiagnostics, err := startDiagnostics(context.Background(), addr, token)
	if err != nil {
		t.Fatal(err)
	}
	defer stopDiagnostics()
	client := &http.Client{Timeout: 2 * time.Second}
	for _, auth := range []string{"", "Bearer wrong", "Bearer " + token} {
		req, _ := http.NewRequest("GET", "http://"+addr+"/debug/pprof/", nil)
		req.Header.Set("Authorization", auth)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		want := 401
		if auth == "Bearer "+token {
			want = 200
		}
		if resp.StatusCode != want {
			t.Fatal("diagnostic auth boundary", resp.StatusCode, want)
		}
	}
}

func TestCandidateSweepRejectsPartialOrUnboundedConfiguration(t *testing.T) {
	for _, test := range []struct {
		days, scope string
		valid       bool
	}{
		{"", "", true}, {"30", "w/p/u/", true}, {"30", "", false}, {"", "w/p/u/", false}, {"0", "w/p/u/", false}, {"3651", "w/p/u/", false},
	} {
		t.Setenv("MEMORY_CANDIDATE_EXPIRY_DAYS", test.days)
		t.Setenv("MEMORY_CANDIDATE_EXPIRY_SCOPE", test.scope)
		_, _, err := candidateSweepConfig()
		if (err == nil) != test.valid {
			t.Fatal(test, err)
		}
	}
}

func TestAuditRetentionDaysDefaultsToThirtyAndRejectsNonsense(t *testing.T) {
	for _, test := range []struct {
		raw   string
		days  int
		valid bool
	}{
		{"", 30, true}, {"0", 0, true}, {"7", 7, true}, {"3650", 3650, true}, {"-1", 0, false}, {"3651", 0, false}, {"soon", 0, false},
	} {
		t.Setenv("MEMORY_AUDIT_RETENTION_DAYS", test.raw)
		days, err := auditRetentionDays()
		if (err == nil) != test.valid || days != test.days {
			t.Fatal(test, days, err)
		}
	}
}
