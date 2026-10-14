//go:build unix

package crew

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

var (
	requestID = strings.Repeat("a", 32)
	revision  = strings.Repeat("b", 64)
)

type gatewayFixture struct {
	t        *testing.T
	state    string
	before   map[string]any
	statuses []any // queued status() results (map or error); the last one repeats
	calls    int   // status() calls
	runs     [][]string
	run      func(args []string) (int, []byte, error)
	liveURL  []string
	liveOK   bool
}

func newGatewayFixture(t *testing.T) *gatewayFixture {
	f := &gatewayFixture{t: t, state: filepath.Join(t.TempDir(), "state")}
	f.before = map[string]any{"unit": gatewayUnit, "load": "loaded", "active": "active", "sub": "running", "revision": revision,
		"url": "http://127.0.0.1:8099", "listening": true}
	f.statuses = []any{f.before}
	f.run = func([]string) (int, []byte, error) { t.Fatal("unexpected systemctl"); return 0, nil, nil }
	saved := []any{gatewaySystemctl, gatewayLive, gatewayStatus, gatewaySaveJSON}
	t.Cleanup(func() {
		gatewaySystemctl = saved[0].(func(...string) (int, []byte, error))
		gatewayLive = saved[1].(func(string, int64) bool)
		gatewayStatus = saved[2].(func() (map[string]any, error))
		gatewaySaveJSON = saved[3].(func(string, any) error)
	})
	t.Setenv("ARKISM_GATEWAY_LISTEN", "127.0.0.1:8099")
	gatewaySystemctl = func(args ...string) (int, []byte, error) {
		f.runs = append(f.runs, args)
		return f.run(args)
	}
	gatewayLive = func(url string, pid int64) bool {
		f.liveURL = append(f.liveURL, fmt.Sprint(url, " ", pid))
		return f.liveOK
	}
	gatewayStatus = func() (map[string]any, error) {
		f.calls++
		next := f.statuses[0]
		if len(f.statuses) > 1 {
			f.statuses = f.statuses[1:]
		}
		if err, ok := next.(error); ok {
			return nil, err
		}
		return next.(map[string]any), nil
	}
	return f
}

func (f *gatewayFixture) request(operation, id, rev string) map[string]any {
	return map[string]any{"operation": operation, "request_id": id, "expected_revision": rev}
}

func (f *gatewayFixture) handle(request any) (map[string]any, error) {
	// Round-trip like stdin, so numbers and shapes are what strictJSON gives.
	if m, ok := request.(map[string]any); ok {
		v, _ := strictJSON([]byte(mustCanonical(m)))
		request = v
	}
	return handleGateway(request, f.state)
}

func (f *gatewayFixture) accept(rc int) {
	f.run = func([]string) (int, []byte, error) { return rc, []byte("synthetic-secret"), nil }
}

func gwCode(err error) string {
	if ce, ok := err.(*controlError); ok {
		return ce.code
	}
	if err != nil {
		return "other"
	}
	return ""
}

func (f *gatewayFixture) files(folder string) map[string]string {
	out := map[string]string{}
	entries, _ := os.ReadDir(folder)
	for _, e := range entries {
		raw, _ := os.ReadFile(filepath.Join(folder, e.Name()))
		out[e.Name()] = string(raw)
	}
	return out
}

func TestGatewayValidRequests(t *testing.T) {
	f := newGatewayFixture(t)
	reqs := []any{map[string]any{"operation": "status"}, map[string]any{"operation": "status", "request_id": requestID}}
	for _, op := range []string{"start", "stop", "restart"} {
		reqs = append(reqs, f.request(op, requestID, revision))
	}
	for _, r := range reqs {
		if err := validate(r); err != nil {
			t.Error(r, err)
		}
	}
}

func TestGatewayInvalidRequestsHaveNoEffects(t *testing.T) {
	f := newGatewayFixture(t)
	reqs := []any{nil, []any{}, "status", 1, true, map[string]any{}, map[string]any{"operation": "enable"},
		map[string]any{"operation": "START"}, map[string]any{"operation": []any{"start"}},
		map[string]any{"operation": "status", "expected_revision": revision}}
	with := func(base map[string]any, k string, v any) map[string]any {
		out := map[string]any{k: v}
		for bk, bv := range base {
			if bk != k {
				out[bk] = bv
			}
		}
		return out
	}
	for _, op := range []string{"status", "start", "stop", "restart"} {
		base := map[string]any{"operation": "status"}
		if op != "status" {
			base = f.request(op, requestID, revision)
		}
		for _, k := range []string{"command", "unit", "host", "path", "env", "url", "address"} {
			reqs = append(reqs, with(base, k, "untrusted"))
		}
		for _, id := range []any{nil, 123, true, "", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32),
			strings.Repeat("g", 32), requestID + "\n", "../" + strings.Repeat("a", 29)} {
			reqs = append(reqs, with(base, "request_id", id))
		}
		if op != "status" {
			reqs = append(reqs, map[string]any{"operation": op}, map[string]any{"operation": op, "request_id": requestID})
			for _, rev := range []any{nil, 123, true, "", strings.Repeat("b", 63), strings.Repeat("b", 65), strings.Repeat("B", 64),
				strings.Repeat("z", 64), revision + "\n"} {
				reqs = append(reqs, with(base, "expected_revision", rev))
			}
		}
	}
	for _, r := range reqs {
		if _, err := handleGateway(r, f.state); gwCode(err) != "gateway_invalid_request" {
			t.Errorf("%v: %v", r, err)
		}
	}
	if f.calls != 0 || len(f.runs) != 0 || exists(f.state) {
		t.Fatal("invalid request had effects")
	}
}

func (f *gatewayFixture) cli(raw string) (int, string) {
	var out strings.Builder
	old := gatewayStateFn
	gatewayStateFn = func() string { return f.state }
	defer func() { gatewayStateFn = old }()
	rc := gatewayControlMain(nil, strings.NewReader(raw), &out, &out)
	return rc, strings.TrimSpace(out.String())
}

func TestGatewayStdinRejectsInvalidStrictJSONAndOversizedInput(t *testing.T) {
	f := newGatewayFixture(t)
	for _, raw := range []string{"", "{", "\xff", `{"operation":"status","operation":"start"}`, `{"operation":"status","request_id":NaN}`,
		strings.Repeat(" ", gatewayLimit+1), "[]", `{"operation":"status","host":"untrusted"}`} {
		if rc, out := f.cli(raw); rc != 1 || out != `{"error": "gateway_invalid_request"}` {
			t.Errorf("%.40q: %d %s", raw, rc, out)
		}
	}
	if f.calls != 0 || len(f.runs) != 0 || exists(f.state) {
		t.Fatal("effects")
	}
}

func TestGatewayStdinStatusIsReadOnlyAtBoundaryLength(t *testing.T) {
	f := newGatewayFixture(t)
	raw := `{"operation":"status"}`
	rc, out := f.cli(raw + strings.Repeat(" ", gatewayLimit-len(raw)))
	want := `{"authority": "data_only_untrusted", "gateway": {"unit": "arkism-gateway.service", "load": "loaded", "active": "active", "sub": "running", "revision": "` +
		revision + `", "url": "http://127.0.0.1:8099", "listening": true}, "receipt": null}`
	if rc != 0 || out != want || len(f.runs) != 0 || exists(f.state) {
		t.Fatal(rc, out)
	}
}

func TestGatewayStdinReturnsSafeErrorOnUnexpectedFailure(t *testing.T) {
	f := newGatewayFixture(t)
	f.statuses = []any{os.ErrPermission}
	if rc, out := f.cli(`{"operation":"status"}`); rc != 1 || out != `{"error": "gateway_control_unavailable"}` {
		t.Fatal(rc, out)
	}
}

func (f *gatewayFixture) show(changes map[string]string, reversed bool) (map[string]any, error) {
	fields := [][2]string{{"Id", gatewayUnit}, {"LoadState", "loaded"}, {"ActiveState", "active"}, {"SubState", "running"},
		{"MainPID", "1234"}, {"ExecMainStartTimestampMonotonic", "5678"}, {"InvocationID", strings.Repeat("c", 32)}}
	var lines []string
	for _, kv := range fields {
		if v, ok := changes[kv[0]]; ok {
			kv[1] = v
		}
		lines = append(lines, kv[0]+"="+kv[1]+"\n")
	}
	if reversed {
		for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
			lines[i], lines[j] = lines[j], lines[i]
		}
	}
	f.setShow(0, strings.Join(lines, ""))
	return status()
}

func (f *gatewayFixture) setShow(rc int, out string) {
	f.run = func([]string) (int, []byte, error) { return rc, []byte(out), nil }
}

func TestGatewayShowUsesFixedPropertiesAndProbes(t *testing.T) {
	f := newGatewayFixture(t)
	f.liveOK = true
	result, err := f.show(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.runs[0], " ") != "show --property=Id,LoadState,ActiveState,SubState,MainPID,ExecMainStartTimestampMonotonic,InvocationID" {
		t.Fatal(f.runs)
	}
	if len(f.liveURL) != 1 || f.liveURL[0] != "http://127.0.0.1:8099 1234" || result["unit"] != gatewayUnit ||
		!hex64.MatchString(result["revision"].(string)) || result["listening"] != true || result["pid"] != int64(1234) {
		t.Fatal(result, f.liveURL)
	}
}

func TestGatewayRevisionIsStableAndChangesPerField(t *testing.T) {
	f := newGatewayFixture(t)
	first, _ := f.show(nil, false)
	baseline := first["revision"]
	again, _ := f.show(nil, true)
	f.liveOK = true
	live, _ := f.show(nil, false)
	if again["revision"] != baseline || live["revision"] != baseline {
		t.Fatal("revision depends on order or liveness")
	}
	for k, v := range map[string]string{"LoadState": "not-found", "ActiveState": "inactive", "SubState": "dead", "MainPID": "1235",
		"ExecMainStartTimestampMonotonic": "5679", "InvocationID": strings.Repeat("d", 32)} {
		if r, _ := f.show(map[string]string{k: v}, false); r["revision"] == baseline {
			t.Error(k)
		}
	}
}

func TestGatewayInactiveUnitDoesNotProbe(t *testing.T) {
	for _, changes := range []map[string]string{{"LoadState": "not-found"}, {"ActiveState": "inactive", "SubState": "dead"}} {
		f := newGatewayFixture(t)
		f.liveOK = true
		if r, _ := f.show(changes, false); r["listening"] != false || len(f.liveURL) != 0 {
			t.Error(changes, r)
		}
	}
}

func TestGatewayShowRejectsMalformedState(t *testing.T) {
	f := newGatewayFixture(t)
	valid := "Id=arkism-gateway.service\nLoadState=loaded\nActiveState=active\nSubState=running\nMainPID=1234\n" +
		"ExecMainStartTimestampMonotonic=5678\nInvocationID=" + strings.Repeat("c", 32) + "\n"
	r := strings.Replace
	outputs := []string{"", "\xff", strings.Repeat("x", 8193), r(valid, "Id=arkism-gateway.service", "Id=other.service", 1),
		r(valid, "MainPID=1234", "MainPID=-1", 1), r(valid, "MainPID=1234", "MainPID=true", 1),
		r(valid, "ExecMainStartTimestampMonotonic=5678", "ExecMainStartTimestampMonotonic=x", 1),
		r(valid, "InvocationID="+strings.Repeat("c", 32), "InvocationID=untrusted", 1),
		r(valid, "ActiveState=active", "ActiveState=active\x00", 1), r(valid, "SubState=running", "SubState="+strings.Repeat("x", 129), 1),
		r(valid, "LoadState=loaded\n", "", 1), valid + "Unexpected=value\n", "missing-delimiter\n"}
	f.setShow(1, valid)
	if _, err := status(); gwCode(err) != "gateway_status_unavailable" {
		t.Error("rc 1:", err)
	}
	for _, out := range outputs {
		f.setShow(0, out)
		if _, err := status(); gwCode(err) != "gateway_status_unavailable" {
			t.Errorf("%.60q: %v", out, err)
		}
	}
}

func TestGatewayMutationsPersistRequestBeforeExecution(t *testing.T) {
	f := newGatewayFixture(t)
	for i, op := range []string{"start", "stop", "restart"} {
		request := f.request(op, fmt.Sprintf("%032x", i), revision)
		folder := filepath.Join(f.state, request["request_id"].(string))
		after := map[string]any{"revision": strings.Repeat("e", 64)}
		f.statuses, f.calls, f.runs = []any{f.before, after}, 0, nil
		f.run = func(args []string) (int, []byte, error) {
			raw, _ := os.ReadFile(filepath.Join(folder, "request.json"))
			if strings.TrimSpace(string(raw)) != mustCanonical(request) || exists(filepath.Join(folder, "result.json")) {
				t.Fatal("request not persisted first")
			}
			return 0, nil, nil
		}
		response, err := f.handle(request)
		if err != nil {
			t.Fatal(err)
		}
		if len(f.runs) != 1 || strings.Join(f.runs[0], " ") != "--no-block "+op || f.calls != 2 ||
			mustCanonical(response) != mustCanonical(map[string]any{"authority": gatewayAuthority, "gateway": after, "receipt": withRequest(request, map[string]any{"status": "accepted"})}) {
			t.Fatal(f.runs, response)
		}
		raw, _ := os.ReadFile(filepath.Join(folder, "result.json"))
		folderInfo, _ := os.Stat(folder)
		stateInfo, _ := os.Stat(f.state)
		if strings.TrimSpace(string(raw)) != `{"status":"accepted"}` || folderInfo.Mode().Perm() != 0o700 || stateInfo.Mode().Perm() != 0o700 {
			t.Fatal(string(raw), folderInfo.Mode(), stateInfo.Mode())
		}
	}
}

func TestGatewaySameIDNeverReexecutes(t *testing.T) {
	f := newGatewayFixture(t)
	for i, rc := range []int{0, 1} {
		request := f.request("start", fmt.Sprintf("%032x", i), revision)
		f.statuses, f.runs = []any{f.before}, nil
		f.accept(rc)
		first, err := f.handle(request)
		expected := withRequest(request, map[string]any{"status": "accepted"})
		if rc != 0 {
			expected = withRequest(request, map[string]any{"status": "failed", "error": "gateway_control_failed"})
		}
		if err != nil || !same(first["receipt"], expected) {
			t.Fatal(first, err)
		}
		folder := filepath.Join(f.state, request["request_id"].(string))
		saved := f.files(folder)
		current := map[string]any{"revision": strings.Repeat("e", 64), "load": "loaded", "listening": false}
		f.statuses, f.calls = []any{current}, 0
		repeated, _ := f.handle(request)
		queried, _ := f.handle(map[string]any{"operation": "status", "request_id": request["request_id"]})
		if !same(repeated, queried) || !same(repeated["gateway"], current) || !same(repeated["receipt"], expected) || len(f.runs) != 1 || f.calls != 2 ||
			mustCanonical(f.files(folder)) != mustCanonical(saved) {
			t.Fatal(repeated, queried, f.runs, f.calls)
		}
	}
}

func (f *gatewayFixture) receiptFiles(request, result any) string {
	folder := filepath.Join(f.state, obj(request)["request_id"].(string))
	_ = os.MkdirAll(f.state, 0o700)
	_ = os.Mkdir(folder, 0o700)
	_ = os.WriteFile(filepath.Join(folder, "request.json"), []byte(mustCanonical(request)), 0o600)
	if result != nil {
		_ = os.WriteFile(filepath.Join(folder, "result.json"), []byte(mustCanonical(result)), 0o600)
	}
	return folder
}

func TestGatewaySameIDDifferentPayloadConflicts(t *testing.T) {
	f := newGatewayFixture(t)
	request := f.request("start", requestID, revision)
	folder := f.receiptFiles(request, map[string]any{"status": "accepted"})
	saved := f.files(folder)
	for _, conflicting := range []map[string]any{f.request("stop", requestID, revision), f.request("start", requestID, strings.Repeat("e", 64))} {
		if _, err := f.handle(conflicting); gwCode(err) != "gateway_receipt_conflict" {
			t.Error(err)
		}
	}
	if f.calls != 0 || len(f.runs) != 0 || mustCanonical(f.files(folder)) != mustCanonical(saved) {
		t.Fatal("effects")
	}
}

func TestGatewayStaleRevisionAndUninstalledUnit(t *testing.T) {
	f := newGatewayFixture(t)
	if _, err := f.handle(f.request("start", requestID, strings.Repeat("e", 64))); gwCode(err) != "gateway_state_changed" || f.calls != 1 {
		t.Fatal(err, f.calls)
	}
	f.statuses = []any{map[string]any{"revision": revision, "load": "not-found", "active": "inactive", "listening": false}}
	if _, err := f.handle(f.request("start", requestID, revision)); gwCode(err) != "gateway_not_installed" {
		t.Fatal(err)
	}
	if len(f.runs) != 0 || exists(filepath.Join(f.state, requestID)) {
		t.Fatal("effects")
	}
}

func TestGatewayUncertaintyPersistsWithoutRetry(t *testing.T) {
	f := newGatewayFixture(t)
	request := f.request("start", requestID, revision)
	f.run = func([]string) (int, []byte, error) { return 0, nil, ctlErr("gateway_control_uncertain") }
	expected := withRequest(request, map[string]any{"status": "uncertain", "error": "gateway_control_uncertain"})
	response, err := f.handle(request)
	if err != nil || !same(response["receipt"], expected) {
		t.Fatal(response, err)
	}
	if r, err := readReceipt(filepath.Join(f.state, requestID)); err != nil || !same(r, expected) {
		t.Fatal(r, err)
	}
	again, _ := f.handle(request)
	queried, _ := f.handle(map[string]any{"operation": "status", "request_id": requestID})
	if !same(again["receipt"], expected) || !same(queried["receipt"], expected) || len(f.runs) != 1 {
		t.Fatal(again, queried, f.runs)
	}
}

func TestGatewayInterruptedReceiptIsUncertainAndNeverReexecutes(t *testing.T) {
	f := newGatewayFixture(t)
	request := f.request("restart", requestID, revision)
	f.run = func([]string) (int, []byte, error) { panic("interrupted") }
	func() {
		defer func() { _ = recover() }()
		_, _ = f.handle(request)
		t.Fatal("no interruption")
	}()
	folder := filepath.Join(f.state, requestID)
	if raw, _ := os.ReadFile(filepath.Join(folder, "request.json")); strings.TrimSpace(string(raw)) != mustCanonical(request) || exists(filepath.Join(folder, "result.json")) {
		t.Fatal("prepared receipt missing")
	}
	expected := withRequest(request, map[string]any{"status": "uncertain", "error": "gateway_control_uncertain"})
	queried, _ := f.handle(map[string]any{"operation": "status", "request_id": requestID})
	again, _ := f.handle(request)
	if !same(queried["receipt"], expected) || !same(again["receipt"], expected) || len(f.runs) != 1 || exists(filepath.Join(folder, "result.json")) {
		t.Fatal(queried, again)
	}
}

func TestGatewayResponseStatusFailureDoesNotRepeatCommand(t *testing.T) {
	f := newGatewayFixture(t)
	request := f.request("start", requestID, revision)
	f.accept(0)
	f.statuses = []any{f.before, ctlErr("gateway_status_unavailable"), f.before}
	if _, err := f.handle(request); gwCode(err) != "gateway_status_unavailable" {
		t.Fatal(err)
	}
	if r, _ := f.handle(request); !same(r["receipt"], withRequest(request, map[string]any{"status": "accepted"})) || len(f.runs) != 1 {
		t.Fatal(r, f.runs)
	}
}

func TestGatewayReceiptWriteFailurePreventsExecution(t *testing.T) {
	f := newGatewayFixture(t)
	gatewaySaveJSON = func(string, any) error { return os.ErrPermission }
	if _, err := f.handle(f.request("start", requestID, revision)); err == nil {
		t.Fatal("write failure ignored")
	}
	if len(f.runs) != 0 || exists(filepath.Join(f.state, requestID)) {
		t.Fatal("effects")
	}
	if r, _ := f.handle(map[string]any{"operation": "status", "request_id": requestID}); r["receipt"] != nil {
		t.Fatal(r)
	}
	gatewaySaveJSON = saveJSON
	f.accept(0)
	if r, _ := f.handle(f.request("start", requestID, revision)); obj(r["receipt"])["status"] != "accepted" || len(f.runs) != 1 {
		t.Fatal(r)
	}
}

func TestGatewaySharedOrLinkedStateIsRejected(t *testing.T) {
	f := newGatewayFixture(t)
	_ = os.Mkdir(f.state, 0o700)
	_ = os.Chmod(f.state, 0o777)
	for _, r := range []map[string]any{{"operation": "status"}, f.request("start", requestID, revision)} {
		if _, err := f.handle(r); gwCode(err) != "gateway_state_permissions" {
			t.Error(err)
		}
	}
	_ = os.Remove(f.state)
	target := filepath.Join(filepath.Dir(f.state), "target")
	_ = os.Mkdir(target, 0o700)
	_ = os.Symlink(target, f.state)
	if _, err := f.handle(f.request("start", requestID, revision)); gwCode(err) != "gateway_state_permissions" {
		t.Error(err)
	}
	if len(f.runs) != 0 || f.calls != 0 {
		t.Fatal("effects")
	}
}

func TestGatewayMissingReceiptStatusDoesNotCreateState(t *testing.T) {
	f := newGatewayFixture(t)
	r, err := f.handle(map[string]any{"operation": "status", "request_id": requestID})
	if err != nil || r["receipt"] != nil || !same(r["gateway"], f.before) || exists(f.state) || len(f.runs) != 0 {
		t.Fatal(r, err)
	}
}

func TestGatewayInvalidReceiptsFailClosed(t *testing.T) {
	f := newGatewayFixture(t)
	type pair struct{ request, result any }
	cases := []pair{{nil, nil}, {"{", nil}, {"\xff", nil}, {strings.Repeat("x", gatewayLimit+1), nil}, {"[]", nil},
		{`{"operation":"start","operation":"stop"}`, nil}, {`{"operation":"start","request_id":NaN}`, nil},
		{map[string]any{"operation": "status", "request_id": requestID}, nil}, {f.request("start", strings.Repeat("f", 32), revision), nil},
		{"extra", nil}, {f.request("start", "", "invalid"), nil}}
	for _, raw := range []any{"{", "\xff", strings.Repeat("x", gatewayLimit+1), "[]", "null", `{"status":"accepted","status":"failed"}`, `{"status":NaN}`,
		map[string]any{"status": "prepared"}, map[string]any{"status": "failed"}, map[string]any{"status": "accepted", "command": "untrusted"},
		map[string]any{"status": "uncertain", "error": "synthetic-secret"}, map[string]any{"status": "failed", "error": "gateway_control_uncertain"}} {
		cases = append(cases, pair{"valid", raw})
	}
	for i, c := range cases {
		id := fmt.Sprintf("%032x", i)
		request := f.request("start", id, revision)
		folder := filepath.Join(f.state, id)
		_ = os.MkdirAll(folder, 0o700)
		_ = os.Chmod(f.state, 0o700)
		reqRaw := c.request
		switch reqRaw {
		case "valid":
			reqRaw = request
		case "extra":
			reqRaw = map[string]any{"operation": "start", "request_id": id, "expected_revision": revision, "command": "untrusted"}
		}
		if m, ok := reqRaw.(map[string]any); ok && m["request_id"] == "" {
			m["request_id"] = id
		}
		for name, raw := range map[string]any{"request.json": reqRaw, "result.json": c.result} {
			switch x := raw.(type) {
			case nil:
			case string:
				_ = os.WriteFile(filepath.Join(folder, name), []byte(x), 0o600)
			default:
				_ = os.WriteFile(filepath.Join(folder, name), []byte(mustCanonical(x)), 0o600)
			}
		}
		saved := f.files(folder)
		for _, incoming := range []map[string]any{{"operation": "status", "request_id": id}, request} {
			if _, err := f.handle(incoming); gwCode(err) != "gateway_receipt_invalid" {
				t.Errorf("case %d: %v", i, err)
			}
		}
		if mustCanonical(f.files(folder)) != mustCanonical(saved) {
			t.Errorf("case %d changed files", i)
		}
	}
	if f.calls != 0 || len(f.runs) != 0 {
		t.Fatal("effects")
	}
}

func TestGatewayRealLockContentionIsBusy(t *testing.T) {
	f := newGatewayFixture(t)
	_ = os.Mkdir(f.state, 0o700)
	request := f.request("start", requestID, revision)
	holder, err := os.OpenFile(filepath.Join(f.state, "control.lock"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err := f.handle(request); gwCode(err) != "gateway_control_busy" || f.calls != 0 || exists(filepath.Join(f.state, requestID)) {
		t.Fatal(err)
	}
	if r, _ := f.handle(map[string]any{"operation": "status"}); r["receipt"] != nil {
		t.Fatal(r)
	}
	holder.Close()
	f.accept(0)
	if r, _ := f.handle(request); obj(r["receipt"])["status"] != "accepted" || len(f.runs) != 1 {
		t.Fatal(r)
	}
}

// From the gateway health suite: anything but the gateway's own identity is not live.
func TestGatewayLiveRejectsOtherMalformedLargeAndRedirect(t *testing.T) {
	var seen []string
	var body string
	var code int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Location", "/redirected")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	for _, c := range []struct {
		body string
		code int
	}{{`{"service":"other","pid":1}`, 200}, {`{"service":"arkism-gateway","pid":true}`, 200}, {"not-json", 200},
		{strings.Repeat(" ", gatewayLimit+1), 200}, {"{}", 302}} {
		body, code = c.body, c.code
		if live(srv.URL, 1) {
			t.Errorf("%.40q %d is live", c.body, c.code)
		}
	}
	body, code = `{"service":"arkism-gateway","pid":7}`, 200
	if !live(srv.URL, 7) || live(srv.URL, 8) {
		t.Error("own identity not recognised")
	}
	if len(seen) != 7 || seen[0] != "/healthz" {
		t.Fatal(seen)
	}
}
