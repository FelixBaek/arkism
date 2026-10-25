package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/memory"
	"github.com/jackc/pgx/v5"
)

// With MEMORYD_TEST_ARGS set, the test binary runs main() with those arguments
// (a JSON array) as the memoryd binary: flags, environment and exit codes are real.
func TestMain(m *testing.M) {
	if raw, ok := os.LookupEnv("MEMORYD_TEST_ARGS"); ok {
		var args []string
		if json.Unmarshal([]byte(raw), &args) != nil {
			os.Exit(99)
		}
		os.Args = append([]string{"memoryd"}, args...)
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// daemonEnv is the parent environment without any memoryd setting, plus env.
func daemonEnv(args []string, env map[string]string) []string {
	var out []string
	settings := []string{"MEMORY", "ARKISM_", "SKILL_", "TEI_", "OLLAMA_", "GEMINI_", "TYPESAFE_", "CLOUDFLARE_", "LANGSMITH_", "OTEL_", "JEV_"}
	for _, kv := range os.Environ() {
		ours := false
		for _, prefix := range settings {
			ours = ours || strings.HasPrefix(kv, prefix)
		}
		if !ours {
			out = append(out, kv)
		}
	}
	raw, _ := json.Marshal(args)
	out = append(out, "MEMORYD_TEST_ARGS="+string(raw))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

type daemonRun struct {
	stdout bytes.Buffer
	stderr lockedBuffer
	cmd    *exec.Cmd
}

// lockedBuffer lets a probe read stderr while the process still writes it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newDaemon(env map[string]string, args ...string) *daemonRun {
	d := &daemonRun{cmd: exec.Command(os.Args[0], "-test.run=^$")}
	d.cmd.Env = daemonEnv(args, env)
	d.cmd.Stdout, d.cmd.Stderr = &d.stdout, &d.stderr
	return d
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

// runDaemon runs one memoryd command to its end and returns stdout and the exit code.
func runDaemon(t *testing.T, env map[string]string, args ...string) (string, int) {
	t.Helper()
	d := newDaemon(env, args...)
	done := make(chan error, 1)
	if err := d.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- d.cmd.Wait() }()
	select {
	case err := <-done:
		t.Logf("memoryd %v: exit %d; stderr: %s", args, exitCode(err), d.stderr.String())
		return d.stdout.String(), exitCode(err)
	case <-time.After(2 * time.Minute):
		_ = d.cmd.Process.Kill()
		t.Fatalf("memoryd %v did not finish; stderr: %s", args, d.stderr.String())
		return "", -1
	}
}

// serveUntilSIGTERM starts a long-running memoryd, waits until probe succeeds, then
// sends SIGTERM and returns the exit code.
func serveUntilSIGTERM(t *testing.T, env map[string]string, probe func(stderr string) bool, args ...string) int {
	t.Helper()
	d := newDaemon(env, args...)
	if err := d.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.cmd.Wait() }()
	for deadline := time.Now().Add(30 * time.Second); !probe(d.stderr.String()); time.Sleep(20 * time.Millisecond) {
		select {
		case err := <-done:
			t.Fatalf("memoryd %v exited early (%d); stderr: %s", args, exitCode(err), d.stderr.String())
		default:
		}
		if time.Now().After(deadline) {
			_ = d.cmd.Process.Kill()
			t.Fatalf("memoryd %v never became ready; stderr: %s", args, d.stderr.String())
		}
	}
	if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Logf("memoryd %v: exit %d; stderr: %s", args, exitCode(err), d.stderr.String())
		return exitCode(err)
	case <-time.After(time.Minute):
		_ = d.cmd.Process.Kill()
		t.Fatalf("memoryd %v ignored SIGTERM", args)
		return -1
	}
}

// freeAddress picks below 32768, outside the default ephemeral ranges (Linux
// 32768+, macOS and Windows 49152+): a parallel test's port-0 listener or
// outgoing connection cannot take the port before the daemon binds it.
func freeAddress(t *testing.T) string {
	t.Helper()
	for range 100 {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", 20000+rand.IntN(12768))); err == nil {
			_ = ln.Close()
			return ln.Addr().String()
		}
	}
	t.Fatal("no free loopback port")
	return ""
}

type testDB struct {
	url   string
	token string // a service principal with every permission in default/default
}

// newTestDB creates a fresh database next to POSTGRES_TEST_DSN. With initialize,
// it holds the schema and a service principal whose token is returned.
func newTestDB(t *testing.T, initialize bool) testDB {
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
	db := testDB{url: u.String()}
	if !initialize {
		return db
	}
	pg, err := memory.NewPostgres(ctx, db.url)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err = pg.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if db.token, err = memory.NewPrincipalToken(); err != nil {
		t.Fatal(err)
	}
	if err = pg.ProvisionPrincipal(ctx, memory.PrincipalGrant{PrincipalID: "memoryd-test-worker", Role: "service", Token: db.token,
		OrganizationID: "default", ProjectID: "default", Actions: []string{"*"}}); err != nil {
		t.Fatal(err)
	}
	// Blob maintenance is authorized in its own storage scope.
	if err = pg.ProvisionPrincipal(ctx, memory.PrincipalGrant{PrincipalID: "memoryd-test-worker", Role: "service", Token: db.token,
		OrganizationID: "__memory_blobs__", ProjectID: "__memory_blobs__", Actions: []string{"maintain:blobs"}}); err != nil {
		t.Fatal(err)
	}
	return db
}

func (db testDB) env(kv ...string) map[string]string {
	env := map[string]string{"MEMORY_DATABASE_URL": db.url, "MEMORY_WORKER_TOKEN": db.token}
	for i := 0; i+1 < len(kv); i += 2 {
		env[kv[i]] = kv[i+1]
	}
	return env
}
