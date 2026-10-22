package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
	"github.com/jackc/pgx/v5"
)

// With MEMORY_PRINCIPAL_TEST_ARGS set, the test binary runs main() with those
// arguments (a JSON array) as the memory-principal binary.
func TestMain(m *testing.M) {
	if raw, ok := os.LookupEnv("MEMORY_PRINCIPAL_TEST_ARGS"); ok {
		var args []string
		if json.Unmarshal([]byte(raw), &args) != nil {
			os.Exit(99)
		}
		os.Args = append([]string{"memory-principal"}, args...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runBinary(t *testing.T, env []string, args ...string) (string, int) {
	t.Helper()
	raw, _ := json.Marshal(args)
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(append(os.Environ(), "MEMORY_PRINCIPAL_TEST_ARGS="+string(raw)), env...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.String(), exit.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.String(), 0
}

// testDatabase creates an initialized database next to POSTGRES_TEST_DSN and returns its URL.
func testDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN is not set; local database not tested")
	}
	u, err := url.Parse(dsn)
	if err != nil || !strings.HasPrefix(strings.TrimPrefix(u.Path, "/"), "arkism_") || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		t.Fatal("requires an explicitly disposable loopback arkism_ database URL")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := pgx.Identifier{fmt.Sprintf("arkism_cmd_%d", time.Now().UnixNano())}
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+name.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error("remove test database:", err)
		}
		_ = admin.Close(context.Background())
	})
	// A dbname or database query key would override the path and miss the new database.
	query := u.Query()
	query.Del("dbname")
	query.Del("database")
	u.Path, u.RawQuery = "/"+name[0], query.Encode()
	pg, err := memory.NewPostgres(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err = pg.Init(ctx); err != nil {
		t.Fatal(err)
	}
	return u.String()
}

func TestHelpAndParseErrors(t *testing.T) {
	if err := runArgs([]string{"-h"}, os.Getenv); err != nil {
		t.Fatal("help", err)
	}
	if err := runArgs([]string{"--no-such-flag"}, os.Getenv); err == nil {
		t.Fatal("unknown flag accepted")
	}
	noDB := func(key string) string {
		if key == "MEMORY_PRINCIPAL_TOKEN" {
			return "not-a-token"
		}
		return ""
	}
	if err := runArgs([]string{"--principal", "p"}, noDB); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("weak token accepted", err)
	}
	if err := runArgs([]string{"--revoke", "--principal", "p"}, noDB); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("revoke without a database", err)
	}
	if err := runArgs([]string{"--generate-token-file", t.TempDir() + "/missing/token"}, noDB); !errors.Is(err, memory.ErrUnavailable) {
		t.Fatal("token file in a missing directory", err)
	}
}

func TestBinaryExitCodes(t *testing.T) {
	if _, code := runBinary(t, nil, "-h"); code != 0 {
		t.Fatal("help exit", code)
	}
	if _, code := runBinary(t, []string{"MEMORY_DATABASE_URL="}, "--revoke", "--principal", "p"); code != 1 {
		t.Fatal("failure exit", code)
	}
}

func TestProvisionStatusRotateAndRevoke(t *testing.T) {
	dsn := testDatabase(t)
	first, _ := memory.NewPrincipalToken()
	second, _ := memory.NewPrincipalToken()
	env := map[string]string{"MEMORY_DATABASE_URL": dsn, "MEMORY_PRINCIPAL_TOKEN": first}
	getenv := func(key string) string { return env[key] }
	grant := []string{"--principal", "svc-1", "--role", "service", "--organization", "w", "--project", "p", "--actions", "memory:read,memory:read"}
	if err := runArgs(grant, getenv); err != nil {
		t.Fatal("provision", err)
	}
	out, code := runBinary(t, []string{"MEMORY_DATABASE_URL=" + dsn, "MEMORY_PRINCIPAL_TOKEN=" + first}, "--token-status", "--principal", "svc-1")
	if code != 0 || strings.Contains(out, first) || !json.Valid([]byte(out)) {
		t.Fatal("status", code, out)
	}
	if err := runArgs([]string{"--rotate-token", "--principal", "svc-1", "--role", "agent"}, getenv); !errors.Is(err, memory.ErrInvalid) {
		t.Fatal("rotation changed the grant", err)
	}
	env["MEMORY_PREVIOUS_PRINCIPAL_TOKEN"], env["MEMORY_PRINCIPAL_TOKEN"] = first, second
	if err := runArgs([]string{"--rotate-token", "--principal", "svc-1"}, getenv); err != nil {
		t.Fatal("rotate", err)
	}
	if err := runArgs([]string{"--token-status", "--principal", "missing"}, getenv); err == nil {
		t.Fatal("status of an unknown principal")
	}
	if err := runArgs([]string{"--revoke", "--principal", "svc-1"}, getenv); err != nil {
		t.Fatal("revoke", err)
	}
}
