package desktopcontrol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{role: "gateway", generation: 7, nonce: strings.Repeat("a", 64), pid: os.Getpid()}
}

type frameWriter struct {
	rows   chan []byte
	count  int
	failAt int
}

func (w *frameWriter) Write(raw []byte) (int, error) {
	w.count++
	if w.count == w.failAt {
		return 0, io.ErrClosedPipe
	}
	w.rows <- bytes.Clone(raw)
	return len(raw), nil
}

func (w *frameWriter) next(t *testing.T) response {
	t.Helper()
	select {
	case raw := <-w.rows:
		if bytes.Contains(raw, []byte(testConfig().nonce)) {
			t.Fatal("nonce leaked to response")
		}
		var row response
		if json.Unmarshal(raw, &row) != nil {
			t.Fatal("invalid response")
		}
		return row
	case <-time.After(time.Second):
		t.Fatal("control response timeout")
		return response{}
	}
}

func startControl(t *testing.T, stop func(context.Context) error, idleCheck ...func() bool) (*Controller, *io.PipeWriter, *frameWriter) {
	t.Helper()
	in, input := io.Pipe()
	out := &frameWriter{rows: make(chan []byte, 32)}
	c := newController(testConfig(), &Gate{}, in, out)
	if err := c.Start(stop, idleCheck...); err != nil {
		t.Fatal(err)
	}
	ready := out.next(t)
	if ready.State != "ready" || ready.PID != os.Getpid() || ready.Generation != 7 || ready.ID != 0 {
		t.Fatal("invalid readiness binding")
	}
	t.Cleanup(func() { _ = input.Close(); <-c.Done() })
	return c, input, out
}

func TestDesktopControlRoleBusyCheckReopensWithoutStopping(t *testing.T) {
	var stopped atomic.Int32
	c, input, output := startControl(t, func(context.Context) error { stopped.Add(1); return nil }, func() bool { return false })
	send(t, input, command(1, "pause", 0))
	if output.next(t).State != "busy" {
		t.Fatal("role busy check was ignored")
	}
	end, err := c.gate.Enter(Handler)
	if err != nil {
		t.Fatal("role busy response left admission paused")
	}
	end()
	if c.Requested() || stopped.Load() != 0 {
		t.Fatal("role busy check stopped work")
	}
}

func TestDesktopControlLostPipeDoesNotReleaseHeldPauseOrClaimStop(t *testing.T) {
	var stopped atomic.Int32
	c, input, output := startControl(t, func(context.Context) error { stopped.Add(1); return nil })
	send(t, input, command(1, "pause", 0))
	if output.next(t).State != "idle" {
		t.Fatal("pause failed")
	}
	_ = input.Close()
	<-c.Done()
	if !errors.Is(c.Err(), ErrTransport) || c.Requested() || stopped.Load() != 0 {
		t.Fatal("EOF claimed or triggered a verified stop")
	}
	if end, err := c.gate.Enter(Handler); err == nil {
		end()
		t.Fatal("EOF released another owner's pause")
	}
	if c.gate.Resume(1) != nil {
		t.Fatal("EOF changed the held lease")
	}
}

func command(id uint64, operation string, lease uint64) request {
	config := testConfig()
	return request{Version: 1, Role: config.role, Generation: config.generation, Nonce: config.nonce, PID: config.pid, ID: id, Operation: operation, Lease: lease}
}

func send(t *testing.T, input io.Writer, row request) {
	t.Helper()
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = input.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopControlAdmissionAndSubmitAreAtomic(t *testing.T) {
	for scope := Handler; scope < scopeCount; scope++ {
		for attempt := 0; attempt < 32; attempt++ {
			raceAdmission(t, scope)
		}
	}
}

func raceAdmission(t *testing.T, scope Scope) {
	t.Helper()
	gate := &Gate{}
	start := make(chan struct{})
	entered := make(chan bool, 1)
	idle := make(chan bool, 1)
	release := make(chan struct{})
	var done sync.WaitGroup
	done.Add(2)
	go func() {
		defer done.Done()
		<-start
		end, err := gate.Enter(scope)
		entered <- err == nil
		if err == nil {
			<-release
			end()
			end()
		}
	}()
	go func() {
		defer done.Done()
		<-start
		paused, err := gate.Pause(1)
		if err != nil {
			panic(err)
		}
		idle <- paused
	}()
	close(start)
	work, paused := <-entered, <-idle
	if work == paused {
		t.Fatal("pause must either own idle admission or preserve existing work")
	}
	if paused {
		if gate.Resume(1) != nil {
			t.Fatal("resume failed")
		}
	}
	end, err := gate.Enter(scope)
	if err != nil {
		t.Fatal("busy pause did not reopen")
	}
	end()
	close(release)
	done.Wait()
}

func TestDesktopControlBusyPreservesStreamingHandlerAndReopens(t *testing.T) {
	gate := &Gate{}
	entered, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	handler := gate.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1); close(entered); <-finish }))
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/stream", nil))
		close(done)
	}()
	<-entered
	idle, err := gate.Pause(1)
	if err != nil || idle {
		t.Fatal("active stream reported idle")
	}
	select {
	case <-done:
		t.Fatal("pause cancelled a handler")
	default:
	}
	close(finish)
	<-done
	if idle, err = gate.Pause(2); err != nil || !idle {
		t.Fatal("idle pause failed")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("POST", "/model", nil))
	if response.Code != 503 || calls.Load() != 1 {
		t.Fatal("paused request reached handler")
	}
	if gate.Resume(2) != nil {
		t.Fatal("resume failed")
	}
}

func TestDesktopControlBindsContextReplayAndExactPauseLease(t *testing.T) {
	var stopped atomic.Int32
	c, input, output := startControl(t, func(context.Context) error { stopped.Add(1); return nil })
	mutations := []func(*request){func(r *request) { r.Role = "watcher" }, func(r *request) { r.Generation++ },
		func(r *request) { r.PID++ }, func(r *request) { r.Nonce = strings.Repeat("b", 64) }, func(r *request) { r.Version++ }}
	for _, mutate := range mutations {
		row := command(1, "pause", 0)
		mutate(&row)
		send(t, input, row)
		if output.next(t).Error != "invalid_context_or_replay" {
			t.Fatal("foreign context accepted")
		}
	}
	send(t, input, command(1, "pause", 0))
	if row := output.next(t); row.State != "idle" || row.Lease != 1 {
		t.Fatal("invalid context spent the valid cursor")
	}
	send(t, input, command(1, "resume", 1))
	if output.next(t).Error != "invalid_context_or_replay" {
		t.Fatal("replay accepted")
	}
	send(t, input, command(2, "resume", 99))
	if output.next(t).Error != "invalid_lease" {
		t.Fatal("foreign lease accepted")
	}
	if _, err := c.gate.Enter(Pool); err == nil {
		t.Fatal("foreign resume opened admission")
	}
	send(t, input, command(3, "resume", 1))
	if output.next(t).State != "resumed" {
		t.Fatal("resume failed")
	}
	send(t, input, command(4, "stop", 1))
	if output.next(t).Error != "invalid_lease" {
		t.Fatal("spent lease stopped service")
	}
	send(t, input, command(5, "pause", 0))
	if output.next(t).State != "idle" {
		t.Fatal("second pause failed")
	}
	send(t, input, command(6, "stop", 5))
	if output.next(t).State != "stopped" {
		t.Fatal("checked stop failed")
	}
	<-c.Done()
	if c.Err() != nil || stopped.Load() != 1 {
		t.Fatal("stop call count/error changed")
	}
}

func TestDesktopControlBusyResponseAndUnknownOperationDoNotCancel(t *testing.T) {
	c, input, output := startControl(t, func(context.Context) error { t.Error("unexpected stop"); return nil })
	end, err := c.gate.Enter(Account)
	if err != nil {
		t.Fatal(err)
	}
	send(t, input, command(1, "pause", 0))
	if output.next(t).State != "busy" {
		t.Fatal("pending probe missed")
	}
	other, err := c.gate.Enter(Round)
	if err != nil {
		t.Fatal("busy reply left admission closed")
	}
	other()
	send(t, input, command(2, "invalid", 0))
	if output.next(t).Error != "unknown_operation" {
		t.Fatal("unknown op accepted")
	}
	end()
}

func TestDesktopControlMalformedFramesNeverStopOrExposeInput(t *testing.T) {
	valid, _ := json.Marshal(command(1, "pause", 0))
	bad := [][]byte{append(bytes.Clone(valid), valid...), []byte(`[]`), bytes.Replace(valid, []byte(`"lease_id":0`), []byte(`"lease_id":null`), 1),
		bytes.Replace(valid, []byte(`"request_id":1`), []byte(`"request_id":true`), 1), bytes.Replace(valid, []byte(`"request_id":1`), []byte(`"request_id":1.0`), 1),
		bytes.Replace(valid, []byte(`"request_id":1`), []byte(`"request_id":-1`), 1), bytes.Replace(valid, []byte(`"role"`), []byte(`"ROLE"`), 1),
		bytes.Replace(valid, []byte(`"v":1`), []byte(`"v":1,"v":1`), 1), bytes.Repeat([]byte("x"), MaxFrame)}
	for _, raw := range bad {
		out := &frameWriter{rows: make(chan []byte, 2)}
		c := newController(testConfig(), &Gate{}, bytes.NewReader(append(raw, '\n')), out)
		if c.Start(func(context.Context) error { t.Error("malformed message stopped work"); return nil }) != nil {
			t.Fatal("start failed")
		}
		<-c.Done()
		if !errors.Is(c.Err(), ErrTransport) || c.Requested() {
			t.Fatal("malformed frame was accepted")
		}
		if end, err := c.gate.Enter(Round); err != nil {
			t.Fatal("malformed frame changed admission")
		} else {
			end()
		}
	}
}

func TestDesktopControlExactFrameBound(t *testing.T) {
	valid, _ := json.Marshal(command(1, "pause", 0))
	raw := append(valid, bytes.Repeat([]byte(" "), MaxFrame-len(valid)-1)...)
	raw = append(raw, '\n')
	if _, err := readRequest(bufio.NewReaderSize(bytes.NewReader(raw), MaxFrame)); err != nil {
		t.Fatal("exact frame rejected")
	}
	if _, err := readRequest(bufio.NewReaderSize(bytes.NewReader(append([]byte(" "), raw...)), MaxFrame)); err == nil {
		t.Fatal("oversize frame accepted")
	}
}

func TestDesktopControlStopFailureAndWriteFailureAreBothRetained(t *testing.T) {
	failure := errors.New("private failure must not appear on the wire")
	c, input, output := startControl(t, func(context.Context) error { return failure })
	send(t, input, command(1, "pause", 0))
	_ = output.next(t)
	output.failAt = 3
	send(t, input, command(2, "stop", 1))
	<-c.Done()
	if !errors.Is(c.Err(), failure) || !errors.Is(c.Err(), ErrTransport) {
		t.Fatal("original shutdown or evidence error was lost")
	}
}

func TestDesktopControlBoundedStopTimeoutDoesNotClaimStopped(t *testing.T) {
	c, input, output := startControl(t, func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })
	c.stopBudget = time.Millisecond
	send(t, input, command(1, "pause", 0))
	_ = output.next(t)
	send(t, input, command(2, "stop", 1))
	row := output.next(t)
	<-c.Done()
	if row.State != "error" || row.Error != "shutdown_timeout" || !errors.Is(c.Err(), ErrTimeout) {
		t.Fatal("timeout claimed successful shutdown")
	}
}

func TestDesktopControlEnvironmentOptInIsExplicitAndBoundToActualPID(t *testing.T) {
	values := map[string]string{}
	get := func(key string) string { return values[key] }
	if config, err := ParseEnvironment(get, "gateway"); config != nil || err != nil {
		t.Fatal("ordinary CLI changed")
	}
	values["ARKISM_DESKTOP_CONTROL"] = "1"
	if _, err := ParseEnvironment(get, "gateway"); err == nil {
		t.Fatal("incomplete opt-in accepted")
	}
	values["ARKISM_DESKTOP_ROLE"] = "gateway"
	values["ARKISM_DESKTOP_GENERATION"] = "7"
	values["ARKISM_DESKTOP_NONCE"] = testConfig().nonce
	config, err := ParseEnvironment(get, "gateway")
	if err != nil || config.pid != os.Getpid() {
		t.Fatal("actual PID binding failed")
	}
	if _, err := ParseEnvironment(get, "watcher"); err == nil {
		t.Fatal("foreign role accepted")
	}
}

func TestDesktopControlRuntimeRolesRequireExactCallerAndActualPID(t *testing.T) {
	roles := []string{"server", "worker", "gateway", "watcher"}
	for _, role := range roles {
		t.Run(role, func(t *testing.T) { checkRuntimeRoleEnvironment(t, role, roles) })
	}
	for _, role := range []string{"", "Server", "worker ", "postgres", "unknown"} {
		config := testConfig()
		config.role = role
		if _, err := New(config, &Gate{}, strings.NewReader(""), io.Discard); err == nil {
			t.Fatal("unsupported runtime role accepted")
		}
	}
}

func checkRuntimeRoleEnvironment(t *testing.T, role string, roles []string) {
	t.Helper()
	values := map[string]string{"ARKISM_DESKTOP_CONTROL": "1", "ARKISM_DESKTOP_ROLE": role,
		"ARKISM_DESKTOP_GENERATION": "7", "ARKISM_DESKTOP_NONCE": testConfig().nonce}
	get := func(key string) string { return values[key] }
	config, err := ParseEnvironment(get, role)
	if err != nil || config == nil || config.pid != os.Getpid() || config.role != role {
		t.Fatal("runtime role was not bound to its actual caller and PID")
	}
	if _, err := New(*config, &Gate{}, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal("valid runtime role rejected")
	}
	for _, other := range roles {
		if other == role {
			continue
		}
		if _, err := ParseEnvironment(get, other); err == nil {
			t.Fatal("foreign runtime caller accepted")
		}
	}
	config.pid++
	if _, err := New(*config, &Gate{}, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("foreign native PID accepted")
	}
}

func TestDesktopControlServerWorkerProtocolRetainsContextAndLeases(t *testing.T) {
	for _, role := range []string{"server", "worker"} {
		t.Run(role, func(t *testing.T) { checkRuntimeRoleProtocol(t, role) })
	}
}

func checkRuntimeRoleProtocol(t *testing.T, role string) {
	t.Helper()
	config := testConfig()
	config.role = role
	reader, input := io.Pipe()
	t.Cleanup(func() { _ = input.Close(); _ = reader.Close() })
	output := &frameWriter{rows: make(chan []byte, 32)}
	var stops atomic.Int32
	control, err := New(config, &Gate{}, reader, output)
	if err != nil {
		t.Fatal("runtime controller rejected")
	}
	if err := control.Start(func(context.Context) error { stops.Add(1); return nil }); err != nil {
		t.Fatal(err)
	}
	if row := output.next(t); row.State != "ready" || row.Role != role || row.PID != os.Getpid() {
		t.Fatal("runtime ready binding changed")
	}
	row := command(1, "pause", 0)
	row.Role = role
	checkRuntimeForeignContext(t, input, output, row)
	checkRuntimeLeaseConversation(t, control, input, output, row)
	select {
	case <-control.Done():
	case <-time.After(time.Second):
		t.Fatal("runtime final receipt did not finish")
	}
	if control.Err() != nil || stops.Load() != 1 {
		t.Fatal("runtime stop callback was not checked exactly once")
	}
}

func checkRuntimeForeignContext(t *testing.T, input io.Writer, output *frameWriter, row request) {
	t.Helper()
	otherRole := "gateway"
	if row.Role == otherRole {
		otherRole = "watcher"
	}
	for _, mutate := range []func(*request){func(r *request) { r.Role = otherRole },
		func(r *request) { r.PID++ }, func(r *request) { r.Generation++ },
		func(r *request) { r.Nonce = strings.Repeat("b", 64) }} {
		wrong := row
		mutate(&wrong)
		send(t, input, wrong)
		if output.next(t).Error != "invalid_context_or_replay" {
			t.Fatal("foreign runtime context accepted")
		}
	}
}

func checkRuntimeLeaseConversation(t *testing.T, control *Controller, input io.Writer, output *frameWriter, row request) {
	t.Helper()
	send(t, input, row)
	if reply := output.next(t); reply.State != "idle" || reply.Lease != 1 {
		t.Fatal("foreign context consumed the runtime cursor")
	}
	row.ID, row.Operation, row.Lease = 2, "resume", 99
	send(t, input, row)
	if output.next(t).Error != "invalid_lease" {
		t.Fatal("wrong runtime lease accepted")
	}
	if end, err := control.Admission().Enter(Handler); err == nil {
		end()
		t.Fatal("invalid resume reopened admission")
	}
	row.ID, row.Lease = 3, 1
	send(t, input, row)
	if output.next(t).State != "resumed" {
		t.Fatal("runtime resume rejected")
	}
	row.ID, row.Operation, row.Lease = 4, "pause", 0
	send(t, input, row)
	if reply := output.next(t); reply.State != "idle" || reply.Lease != 4 {
		t.Fatal("runtime second lease changed")
	}
	row.ID, row.Operation, row.Lease = 5, "stop", 4
	send(t, input, row)
	if reply := output.next(t); reply.State != "stopped" || reply.Role != row.Role || reply.Lease != 4 {
		t.Fatal("runtime stop receipt changed")
	}
}

func recoveryConfig(t *testing.T, role string) Config {
	t.Helper()
	values := map[string]string{"ARKISM_DESKTOP_CONTROL": "1", "ARKISM_DESKTOP_ROLE": role,
		"ARKISM_DESKTOP_GENERATION": "7", "ARKISM_DESKTOP_NONCE": testConfig().nonce, "ARKISM_DESKTOP_RECOVERY": "1"}
	config, err := ParseEnvironment(func(key string) string { return values[key] }, role)
	if err != nil || config == nil {
		t.Fatal("valid controlled recovery refused")
	}
	return *config
}

func assertRecoveryHeld(t *testing.T, gate *Gate) {
	t.Helper()
	if release, err := gate.Enter(Round); err == nil {
		release()
		t.Fatal("recovery admitted work before explicit resume")
	}
}

func newRecoveryController(t *testing.T, role string, output *frameWriter) (*Controller, *io.PipeWriter) {
	t.Helper()
	reader, input := io.Pipe()
	t.Cleanup(func() { _ = input.Close(); _ = reader.Close() })
	control, err := New(recoveryConfig(t, role), &Gate{}, reader, output)
	if err != nil {
		t.Fatal(err)
	}
	assertRecoveryHeld(t, control.Admission())
	return control, input
}

func TestDesktopControlRecoveryEnvironmentRequiresControlledContext(t *testing.T) {
	for _, marker := range []string{"", "0", "1", "unknown", "1 "} {
		for _, optIn := range []string{"", "1"} {
			checkRecoveryEnvironment(t, marker, optIn)
		}
	}
}

func checkRecoveryEnvironment(t *testing.T, marker, optIn string) {
	t.Helper()
	values := map[string]string{"ARKISM_DESKTOP_CONTROL": optIn, "ARKISM_DESKTOP_ROLE": "worker",
		"ARKISM_DESKTOP_GENERATION": "7", "ARKISM_DESKTOP_NONCE": testConfig().nonce, "ARKISM_DESKTOP_RECOVERY": marker}
	config, err := ParseEnvironment(func(key string) string { return values[key] }, "worker")
	valid := marker == "" || marker == "0" || marker == "1" && optIn == "1"
	if (err == nil) != valid {
		t.Fatal("recovery marker used an uncontrolled or unknown mode")
	}
	if valid && optIn == "" && config != nil {
		t.Fatal("normal uncontrolled startup changed")
	}
	if valid && optIn == "1" && marker != "1" {
		assertNormalOpen(t, *config)
	}
}

func assertNormalOpen(t *testing.T, config Config) {
	t.Helper()
	control, err := New(config, &Gate{}, strings.NewReader(""), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	release, err := control.Admission().Enter(Round)
	if err != nil {
		t.Fatal("normal startup unexpectedly held admission")
	}
	release()
}

func TestDesktopControlRecoveryAdmissionWaitsForExactResume(t *testing.T) {
	for _, role := range []string{"server", "worker", "gateway", "watcher"} {
		t.Run(role, func(t *testing.T) { checkRecoveryResume(t, role) })
	}
}

func checkRecoveryResume(t *testing.T, role string) {
	t.Helper()
	output := &frameWriter{rows: make(chan []byte, 32)}
	control, input := newRecoveryController(t, role, output)
	if err := control.Start(func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if ready := output.next(t); ready.State != "ready" || ready.Lease != 0 {
		t.Fatal("ready metadata contract changed")
	}
	assertRecoveryHeld(t, control.Admission())
	row := command(1, "pause", 0)
	row.Role = role
	checkRuntimeForeignContext(t, input, output, row)
	checkRuntimeLeaseConversation(t, control, input, output, row)
	// The shared conversation stopped the role. A separate cycle below verifies
	// that explicit resume reopens held admission without changing normal busy.
}

func TestDesktopControlRecoveryResumeReopensNormalBusySemantics(t *testing.T) {
	output := &frameWriter{rows: make(chan []byte, 32)}
	control, input := newRecoveryController(t, "gateway", output)
	if err := control.Start(func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	output.next(t)
	send(t, input, command(1, "pause", 0))
	if row := output.next(t); row.State != "idle" || row.Lease != 1 {
		t.Fatal("bootstrap hold did not acquire an ordinary lease")
	}
	send(t, input, command(2, "resume", 99))
	if output.next(t).Error != "invalid_lease" {
		t.Fatal("wrong resume lease accepted")
	}
	assertRecoveryHeld(t, control.Admission())
	send(t, input, command(3, "resume", 1))
	if output.next(t).State != "resumed" {
		t.Fatal("explicit recovery resume failed")
	}
	release, err := control.Admission().Enter(Round)
	if err != nil {
		t.Fatal("verified resume did not reopen admission")
	}
	send(t, input, command(4, "pause", 0))
	if output.next(t).State != "busy" {
		t.Fatal("normal busy semantics changed after recovery")
	}
	other, err := control.Admission().Enter(Handler)
	if err != nil {
		t.Fatal("normal busy receipt did not reopen admission")
	}
	other()
	release()
}

func TestDesktopControlRecoveryIdleCheckFailureKeepsHold(t *testing.T) {
	output := &frameWriter{rows: make(chan []byte, 32)}
	control, input := newRecoveryController(t, "gateway", output)
	if err := control.Start(func(context.Context) error { return nil }, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	output.next(t)
	send(t, input, command(1, "pause", 0))
	if row := output.next(t); row.State != "error" || row.Error != "invalid_lease" {
		t.Fatal("held idle-check failure falsely promised reopened busy admission")
	}
	assertRecoveryHeld(t, control.Admission())
}

func TestDesktopControlRecoveryTransportFailureKeepsHold(t *testing.T) {
	for _, mode := range []string{"eof", "malformed", "ready_write"} {
		t.Run(mode, func(t *testing.T) { checkRecoveryTransportFailure(t, mode) })
	}
}

func checkRecoveryTransportFailure(t *testing.T, mode string) {
	t.Helper()
	output := &frameWriter{rows: make(chan []byte, 32)}
	if mode == "ready_write" {
		output.failAt = 1
	}
	control, input := newRecoveryController(t, "gateway", output)
	err := control.Start(func(context.Context) error { t.Error("transport failure stopped the role"); return nil })
	if mode == "ready_write" {
		if err == nil {
			t.Fatal("failed ready write accepted")
		}
	} else {
		if err != nil {
			t.Fatal(err)
		}
		output.next(t)
		if mode == "malformed" {
			_, _ = input.Write([]byte("not-json\n"))
		} else {
			_ = input.Close()
		}
	}
	select {
	case <-control.Done():
	case <-time.After(time.Second):
		t.Fatal("failed control transport did not finish")
	}
	if control.Err() == nil {
		t.Fatal("transport failure claimed a valid session")
	}
	assertRecoveryHeld(t, control.Admission())
}

func TestDesktopControlRecoveryStopTimeoutKeepsHold(t *testing.T) {
	output := &frameWriter{rows: make(chan []byte, 32)}
	control, input := newRecoveryController(t, "gateway", output)
	control.stopBudget = time.Millisecond
	if err := control.Start(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	output.next(t)
	send(t, input, command(1, "pause", 0))
	output.next(t)
	send(t, input, command(2, "stop", 1))
	if row := output.next(t); row.State != "error" || row.Error != "shutdown_timeout" || row.Lease != 1 {
		t.Fatal("stop timeout lost its held lease")
	}
	assertRecoveryHeld(t, control.Admission())
	if control.Admission().Resume(1) == nil {
		t.Fatal("irreversible failed stop reopened admission")
	}
}

func TestDesktopControlRecoveryCannotTakeOverActiveGate(t *testing.T) {
	gate := &Gate{}
	release, err := gate.Enter(Round)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := New(recoveryConfig(t, "worker"), gate, strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("recovery attached after work admission")
	}
}
