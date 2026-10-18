package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/desktopcontrol"
)

func gatewayRecoveryTokenFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "absent-parent", "gateway.token")
	old := host.GatewayTokenFile
	host.GatewayTokenFile = path
	t.Cleanup(func() { host.GatewayTokenFile = old })
	t.Setenv("ARKISM_GATEWAY_TOKEN", "")
	t.Setenv("ARKISM_DESKTOP_RECOVERY", "1")
	return path
}

func TestDesktopControlRecoveryTokenMissingNeverCreatesFileOrParent(t *testing.T) {
	path := gatewayRecoveryTokenFixture(t)
	if _, err := desktopGatewayToken(true); err == nil {
		t.Fatal("missing recovery token accepted")
	}
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("recovery created the token directory")
	}
}

func TestDesktopControlRecoveryTokenReadsValidAndRejectsInvalidWithoutWrites(t *testing.T) {
	for _, text := range []string{"invalid\n", strings.Repeat("a", 64) + "\n"} {
		t.Run(text[:3], func(t *testing.T) {
			path := gatewayRecoveryTokenFixture(t)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			token, err := desktopGatewayToken(true)
			valid := len(text) == 65
			if (err == nil) != valid || (valid && token != strings.TrimSpace(text)) {
				t.Fatal("recovery token result changed")
			}
			after, statErr := os.Stat(path)
			raw, readErr := os.ReadFile(path)
			if statErr != nil || readErr != nil || string(raw) != text || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
				t.Fatal("recovery changed the token")
			}
		})
	}
}

func TestDesktopControlNormalTokenStartupRetainsFirstUseCreation(t *testing.T) {
	path := gatewayRecoveryTokenFixture(t)
	t.Setenv("ARKISM_DESKTOP_RECOVERY", "0")
	token, err := desktopGatewayToken(true)
	if err != nil || !host.ValidGatewayToken(token) {
		t.Fatal("normal first-use token creation failed")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != token+"\n" {
		t.Fatal("normal startup token was not retained")
	}
	if again, err := desktopGatewayToken(false); err != nil || again != token {
		t.Fatal("normal CLI startup stopped reusing its token")
	}
}

func TestDesktopControlRecoveryTokenExplicitEnvironmentAndMarkerValidation(t *testing.T) {
	path := gatewayRecoveryTokenFixture(t)
	token := strings.Repeat("b", 64)
	t.Setenv("ARKISM_GATEWAY_TOKEN", token)
	if got, err := desktopGatewayToken(true); err != nil || got != token {
		t.Fatal("explicit original token was not used")
	}
	if _, err := desktopGatewayToken(false); err == nil {
		t.Fatal("uncontrolled recovery marker accepted")
	}
	t.Setenv("ARKISM_GATEWAY_TOKEN", "invalid")
	if _, err := desktopGatewayToken(true); err == nil {
		t.Fatal("invalid explicit token accepted")
	}
	t.Setenv("ARKISM_DESKTOP_RECOVERY", "unknown")
	if _, err := desktopGatewayToken(true); err == nil {
		t.Fatal("unknown recovery marker accepted")
	}
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("marker validation wrote a credential")
	}
}

func TestDesktopControlGatewayActualStartupRecoveryTokenFailureHasNoWriterFallback(t *testing.T) {
	path := gatewayRecoveryTokenFixture(t)
	for key, value := range map[string]string{"ARKISM_DESKTOP_CONTROL": "1", "ARKISM_DESKTOP_ROLE": "gateway",
		"ARKISM_DESKTOP_GENERATION": "7", "ARKISM_DESKTOP_NONCE": strings.Repeat("a", 64)} {
		t.Setenv(key, value)
	}
	old := Listen
	Listen = "127.0.0.1:0"
	t.Cleanup(func() { Listen = old })
	var out, diagnostic bytes.Buffer
	if code := runGateway(nil, strings.NewReader(""), &out, &diagnostic); code == 0 {
		t.Fatal("gateway recovered without its token")
	}
	if out.Len() != 0 || !strings.Contains(diagnostic.String(), "desktop_gateway_token_unavailable") {
		t.Fatal("failed token read claimed readiness or lost its fixed diagnostic")
	}
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("actual startup recreated the credential")
	}
}

func TestDesktopControlGatewayBusyRunsArePreserved(t *testing.T) {
	run := &claudeRun{busy: true, closed: make(chan struct{})}
	bridge := &claudeBridge{runs: []*claudeRun{run}}
	if bridge.desktopIdle() {
		t.Fatal("busy CLI run reported idle")
	}
	if !errors.Is(bridge.closeForDesktop(context.Background()), errDesktopBusy) {
		t.Fatal("busy run was closed")
	}
	select {
	case <-run.closed:
		t.Fatal("busy work was cancelled")
	default:
	}
	if len(bridge.runs) != 1 {
		t.Fatal("busy run lost ownership")
	}
	bridge.mu.Lock()
	run.busy = false
	bridge.mu.Unlock()
	if !bridge.desktopIdle() {
		t.Fatal("idle cached run counted as permanently busy")
	}
	if err := bridge.closeForDesktop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-run.closed:
	default:
		t.Fatal("idle run was not closed")
	}
	if len(bridge.runs) != 0 {
		t.Fatal("idle run retained")
	}
}

func TestDesktopControlCheckedCloseRetainsTreeAndWaitFailures(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	var rootCalls int
	failure := errors.New("inert failure")
	err := stopClaudeProcess(context.Background(), func() error { return failure }, func() error { rootCalls++; return nil }, exited)
	if !errors.Is(err, errClaudeTreeStop) || rootCalls != 1 {
		t.Fatal("root-only fallback claimed tree success")
	}
	ctx, cancel := context.WithCancel(context.Background())
	err = stopClaudeProcess(ctx, func() error { cancel(); return failure }, func() error { rootCalls++; return nil }, make(chan struct{}))
	if !errors.Is(err, errClaudeTreeStop) || !errors.Is(err, errClaudeWait) || rootCalls != 1 {
		t.Fatal("deadline lost error or allowed late fallback")
	}
}

func TestDesktopControlCheckedCloseDoesNotRunAfterExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := stopClaudeProcess(ctx, func() error { called = true; return nil }, func() error { called = true; return nil }, make(chan struct{}))
	if called || !errors.Is(err, errClaudeWait) {
		t.Fatal("expired deadline performed stop")
	}
	bridge := &claudeBridge{runs: []*claudeRun{{closed: make(chan struct{})}}}
	if bridge.closeForDesktop(ctx) == nil || len(bridge.runs) != 1 {
		t.Fatal("expired close discarded ownership")
	}
}

func TestDesktopControlCheckedCloseKeepsOneCachedResult(t *testing.T) {
	run := &claudeRun{closed: make(chan struct{}), lock: &sync.Mutex{}}
	var done sync.WaitGroup
	for i := 0; i < 4; i++ {
		done.Add(1)
		go func() {
			defer done.Done()
			if run.closeChecked(context.Background()) != nil {
				t.Error("empty close failed")
			}
		}()
	}
	done.Wait()
}

type closeObservedListener struct {
	net.Listener
	closed chan struct{}
	once   sync.Once
}

func (l *closeObservedListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

type gatewayControlFixture struct {
	t       *testing.T
	parent  net.Conn
	decoder *json.Decoder
	done    chan error
	closed  <-chan struct{}
	address string
	client  *http.Client
}

func newGatewayControlFixture(t *testing.T, handler http.Handler, bridge *claudeBridge) *gatewayControlFixture {
	t.Helper()
	child, parent := net.Pipe()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	observed := &closeObservedListener{Listener: listener, closed: make(chan struct{})}
	settings := map[string]string{"ARKISM_DESKTOP_CONTROL": "1", "ARKISM_DESKTOP_ROLE": "gateway",
		"ARKISM_DESKTOP_GENERATION": "7", "ARKISM_DESKTOP_NONCE": strings.Repeat("a", 64)}
	config, err := desktopcontrol.ParseEnvironment(func(key string) string { return settings[key] }, "gateway")
	if err != nil {
		t.Fatal(err)
	}
	control, err := desktopcontrol.New(*config, &desktopcontrol.Gate{}, child, child)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	f := &gatewayControlFixture{t: t, parent: parent, decoder: json.NewDecoder(parent), done: make(chan error, 1),
		closed: observed.closed, address: "http://" + listener.Addr().String(),
		client: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: time.Second}}
	t.Cleanup(func() { _ = parent.Close(); _ = child.Close(); _ = server.Close(); f.client.CloseIdleConnections() })
	_ = parent.SetDeadline(time.Now().Add(3 * time.Second))
	go func() { f.done <- serveDesktopGateway(server, observed, control, bridge) }()
	f.receive("ready")
	return f
}

func (f *gatewayControlFixture) write(id uint64, op string, lease uint64) {
	f.t.Helper()
	row := map[string]any{"v": 1, "role": "gateway", "generation": 7, "nonce": strings.Repeat("a", 64),
		"pid": os.Getpid(), "request_id": id, "op": op, "lease_id": lease}
	if err := json.NewEncoder(f.parent).Encode(row); err != nil {
		f.t.Fatal(err)
	}
}

func (f *gatewayControlFixture) receive(state string) {
	f.t.Helper()
	var row struct{ State string }
	if err := f.decoder.Decode(&row); err != nil || row.State != state {
		f.t.Fatalf("control state = %q, error = %v; want %q", row.State, err, state)
	}
}

func (f *gatewayControlFixture) request(path string) int {
	f.t.Helper()
	response, err := f.client.Get(f.address + path)
	if err != nil {
		f.t.Error(err)
		return 0
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		f.t.Error(err)
	}
	return response.StatusCode
}

func TestDesktopControlGatewayActualServerPreservesWorkAndWaitsForReceipt(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	var finishOnce sync.Once
	t.Cleanup(func() { finishOnce.Do(func() { close(finish) }) })
	run := &claudeRun{closed: make(chan struct{})}
	f := newGatewayControlFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/work" {
			close(entered)
			<-finish
		}
		w.WriteHeader(http.StatusNoContent)
	}), &claudeBridge{runs: []*claudeRun{run}})
	completed := make(chan int, 1)
	go func() { completed <- f.request("/work") }()
	<-entered
	f.write(1, "pause", 0)
	f.receive("busy")
	if f.request("/probe") != http.StatusNoContent {
		t.Fatal("busy pause failed to reopen admission")
	}
	finishOnce.Do(func() { close(finish) })
	if <-completed != http.StatusNoContent {
		t.Fatal("pause cancelled the active handler")
	}
	f.write(2, "pause", 0)
	f.receive("idle")
	if f.request("/probe") != http.StatusServiceUnavailable {
		t.Fatal("held pause admitted another handler")
	}
	f.write(3, "stop", 2)
	select {
	case <-f.closed:
	case <-time.After(time.Second):
		t.Fatal("checked shutdown did not close the listener")
	}
	select {
	case err := <-f.done:
		t.Fatalf("role returned before writing its receipt: %v", err)
	default:
	}
	f.receive("stopped")
	if err := <-f.done; !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
	select {
	case <-run.closed:
	default:
		t.Fatal("idle cached CLI was not closed")
	}
}
