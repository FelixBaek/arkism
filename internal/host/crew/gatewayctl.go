//go:build unix

package crew

// Owner-SSH control of the existing Arkism gateway unit; no HTTP administration.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

const (
	gatewayUnit      = "arkism-gateway.service"
	gatewayAuthority = "data_only_untrusted"
	gatewayLimit     = 4096
)

var gatewayProperties = []string{"Id", "LoadState", "ActiveState", "SubState", "MainPID",
	"ExecMainStartTimestampMonotonic", "InvocationID"}

type controlError struct{ code string }

func (e *controlError) Error() string { return e.code }

func ctlErr(code string) error { return &controlError{code} }

// Test seams: the real systemctl, liveness probe, status and receipt writer.
var (
	gatewaySystemctl = runSystemctl
	gatewayLive      = live
	gatewayStatus    = status
	gatewaySaveJSON  = saveJSON
)

var gatewayStateFn = func() string { return filepath.Join(host.Home, ".local/state/arkism-gateway-control") }

var (
	hex32 = regexp.MustCompile(`^[a-f0-9]{32}$`)
	hex64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

func validate(request any) error {
	r, ok := request.(map[string]any)
	if !ok {
		return ctlErr("gateway_invalid_request")
	}
	operation, _ := r["operation"].(string)
	allowed := map[string]bool{"operation": true, "request_id": true, "expected_revision": operation != "status"}
	switch operation {
	case "status", "start", "stop", "restart":
	default:
		return ctlErr("gateway_invalid_request")
	}
	for k := range r {
		if !allowed[k] {
			return ctlErr("gateway_invalid_request")
		}
	}
	if _, has := r["request_id"]; has || operation != "status" {
		if id, ok := r["request_id"].(string); !ok || !hex32.MatchString(id) {
			return ctlErr("gateway_invalid_request")
		}
	}
	if rev, ok := r["expected_revision"].(string); operation != "status" && (!ok || !hex64.MatchString(rev)) {
		return ctlErr("gateway_invalid_request")
	}
	return nil
}

// runSystemctl runs the fixed systemctl argv for the gateway unit; a timeout or start failure is
// gateway_control_uncertain.
func runSystemctl(arguments ...string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	argv := append(append([]string{"--no-pager", "--no-ask-password"}, arguments...), gatewayUnit)
	cmd := exec.CommandContext(ctx, "/usr/bin/systemctl", argv...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, io.Discard
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && ctx.Err() == nil {
		return exit.ExitCode(), out.Bytes(), nil
	}
	if err != nil {
		return 0, nil, ctlErr("gateway_control_uncertain")
	}
	return 0, out.Bytes(), nil
}

var listenAddress = regexp.MustCompile(`^127\.0\.0\.1:[0-9]{1,5}$`)

func endpoint() (string, error) {
	address := host.Env("ARKISM_GATEWAY_LISTEN", "127.0.0.1:8099")
	if !listenAddress.MatchString(address) {
		return "", ctlErr("gateway_invalid_address")
	}
	port, _ := strconv.Atoi(address[strings.LastIndexByte(address, ':')+1:])
	if port < 1 || port > 65535 {
		return "", ctlErr("gateway_invalid_address")
	}
	return "http://" + address, nil
}

var healthClient = &http.Client{
	Transport:     &http.Transport{Proxy: nil},
	Timeout:       2 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// live reports whether the gateway at url answers /healthz as itself with the given pid.
func live(url string, pid int64) bool {
	if pid <= 0 {
		return false
	}
	resp, err := healthClient.Get(url + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != 200 || media != "application/json" {
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, gatewayLimit+1))
	if err != nil || len(raw) > gatewayLimit {
		return false
	}
	page, err := strictJSON(raw)
	if err != nil {
		return false
	}
	m, _ := page.(map[string]any)
	got, isInt := host.Int(m["pid"])
	return m != nil && m["service"] == "arkism-gateway" && isInt && got == pid
}

var (
	digits12    = regexp.MustCompile(`^[0-9]{1,12}$`)
	digits24    = regexp.MustCompile(`^[0-9]{1,24}$`)
	invocation  = regexp.MustCompile(`^(?:[a-f0-9]{32}|)$`)
	unitStateRe = regexp.MustCompile(`^[a-z-]{1,40}$`)
)

func status() (map[string]any, error) {
	url, err := endpoint()
	if err != nil {
		return nil, err
	}
	rc, out, err := gatewaySystemctl("show", "--property="+strings.Join(gatewayProperties, ","))
	if err != nil {
		return nil, err
	}
	unavailable := ctlErr("gateway_status_unavailable")
	if rc != 0 || len(out) > 8192 || !utf8.Valid(out) {
		return nil, unavailable
	}
	fields := map[string]any{}
	for _, line := range pySplitlines(string(out)) {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, unavailable
		}
		fields[k] = v
	}
	if !keysAre(fields, gatewayProperties...) || fields["Id"] != gatewayUnit {
		return nil, unavailable
	}
	for _, v := range fields {
		s := v.(string)
		if host.Len(s) > 128 || strings.IndexFunc(s, func(r rune) bool { return r < 32 }) >= 0 {
			return nil, unavailable
		}
	}
	if !digits12.MatchString(fields["MainPID"].(string)) || !digits24.MatchString(fields["ExecMainStartTimestampMonotonic"].(string)) ||
		!invocation.MatchString(fields["InvocationID"].(string)) {
		return nil, unavailable
	}
	for _, k := range []string{"LoadState", "ActiveState", "SubState"} {
		if !unitStateRe.MatchString(fields[k].(string)) {
			return nil, unavailable
		}
	}
	withURL := map[string]any{"url": url}
	for k, v := range fields {
		withURL[k] = v
	}
	pid, _ := strconv.ParseInt(fields["MainPID"].(string), 10, 64)
	active := fields["LoadState"] == "loaded" && fields["ActiveState"] == "active"
	return map[string]any{"unit": gatewayUnit, "load": fields["LoadState"], "active": fields["ActiveState"], "sub": fields["SubState"],
		"revision": digest([]byte(mustCanonical(withURL))), "url": url, "pid": pid, "listening": active && gatewayLive(url, pid)}, nil
}

// privateDirectory reports whether path exists; it must be our own 0700-style directory, never a link.
func privateDirectory(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	st, _ := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || st == nil || int(st.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return false, ctlErr("gateway_state_permissions")
	}
	return true, nil
}

func readOwnJSON(path string) (any, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, _ := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || st == nil || int(st.Uid) != os.Getuid() || info.Size() > gatewayLimit {
		return nil, errValue
	}
	raw, err := io.ReadAll(io.LimitReader(f, gatewayLimit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > gatewayLimit {
		return nil, errValue
	}
	return strictJSON(raw)
}

var receiptResults = []map[string]any{{"status": "accepted"}, {"status": "failed", "error": "gateway_control_failed"},
	{"status": "uncertain", "error": "gateway_control_uncertain"}}

func readReceipt(folder string) (map[string]any, error) {
	ok, err := privateDirectory(folder)
	if err != nil || !ok {
		return nil, err
	}
	invalid := ctlErr("gateway_receipt_invalid")
	r, err := readOwnJSON(filepath.Join(folder, "request.json"))
	if err != nil || validate(r) != nil {
		return nil, invalid
	}
	request := r.(map[string]any)
	if request["operation"] == "status" || request["request_id"] != filepath.Base(folder) {
		return nil, invalid
	}
	result := map[string]any{"status": "uncertain", "error": "gateway_control_uncertain"}
	resultPath := filepath.Join(folder, "result.json")
	if exists(resultPath) {
		saved, err := readOwnJSON(resultPath)
		if err != nil {
			return nil, invalid
		}
		known := false
		for _, r := range receiptResults {
			known = known || same(saved, r)
		}
		if !known {
			return nil, invalid
		}
		result = saved.(map[string]any)
	}
	return withRequest(request, result), nil
}

func withRequest(request, result map[string]any) map[string]any {
	out := map[string]any{"request": request}
	for k, v := range result {
		out[k] = v
	}
	return out
}

func handleGateway(request any, state string) (map[string]any, error) {
	if err := validate(request); err != nil {
		return nil, err
	}
	r := request.(map[string]any)
	if _, err := privateDirectory(state); err != nil {
		return nil, err
	}
	folder := ""
	if id, ok := r["request_id"].(string); ok {
		folder = filepath.Join(state, id)
	}
	answer := func(receipt any) (map[string]any, error) {
		gateway, err := gatewayStatus()
		if err != nil {
			return nil, err
		}
		return map[string]any{"authority": gatewayAuthority, "gateway": gateway, "receipt": receipt}, nil
	}
	if r["operation"] == "status" {
		if folder == "" {
			return answer(nil)
		}
		saved, err := readReceipt(folder)
		if err != nil {
			return nil, err
		}
		if saved == nil {
			return answer(nil)
		}
		return answer(saved)
	}
	// All our changes share one lock; an external systemctl remains outside this lock.
	if err := os.MkdirAll(state, 0o700); err != nil {
		return nil, err
	}
	if _, err := privateDirectory(state); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(state, "control.lock"), syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	lock := os.NewFile(uintptr(fd), "control.lock")
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return nil, err
	}
	if st, _ := info.Sys().(*syscall.Stat_t); !info.Mode().IsRegular() || st == nil || int(st.Uid) != os.Getuid() || info.Mode().Perm()&0o022 != 0 {
		return nil, ctlErr("gateway_state_permissions")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ctlErr("gateway_control_busy")
		}
		return nil, err
	}
	saved, err := readReceipt(folder)
	if err != nil {
		return nil, err
	}
	if saved != nil {
		if !same(saved["request"], r) {
			return nil, ctlErr("gateway_receipt_conflict")
		}
		return answer(saved)
	}
	before, err := gatewayStatus()
	if err != nil {
		return nil, err
	}
	if before["revision"] != r["expected_revision"] {
		return nil, ctlErr("gateway_state_changed")
	}
	if before["load"] != "loaded" {
		return nil, ctlErr("gateway_not_installed")
	}
	prepared, err := os.MkdirTemp(state, ".prepare-")
	if err != nil {
		return nil, err
	}
	err = gatewaySaveJSON(filepath.Join(prepared, "request.json"), r)
	if err == nil {
		err = os.Rename(prepared, folder)
	}
	if exists(prepared) {
		_ = os.Remove(filepath.Join(prepared, "request.json"))
		_ = os.Remove(prepared)
	}
	if err != nil {
		return nil, err
	}
	dir, err := os.Open(state)
	if err != nil {
		return nil, err
	}
	err = dir.Sync()
	dir.Close()
	if err != nil {
		return nil, err
	}
	receipt := map[string]any{"status": "uncertain", "error": "gateway_control_uncertain"}
	if rc, _, err := gatewaySystemctl("--no-block", r["operation"].(string)); err == nil {
		receipt = map[string]any{"status": "accepted"}
		if rc != 0 {
			receipt = map[string]any{"status": "failed", "error": "gateway_control_failed"}
		}
	}
	if err := gatewaySaveJSON(filepath.Join(folder, "result.json"), receipt); err != nil {
		return nil, err
	}
	return answer(withRequest(r, receipt))
}

// ordered writes the answer in the Python key order (authority, gateway fields, receipt).
func orderedAnswer(a map[string]any) host.Obj {
	gateway := host.Obj{}
	g := a["gateway"].(map[string]any)
	for _, k := range []string{"unit", "load", "active", "sub", "revision", "url", "pid", "listening"} {
		if v, ok := g[k]; ok {
			gateway = append(gateway, host.KV{Key: k, Value: v})
		}
	}
	var receipt any
	if r, ok := a["receipt"].(map[string]any); ok {
		req := host.Obj{}
		for _, k := range []string{"operation", "request_id", "expected_revision"} {
			if v, ok := obj(r["request"])[k]; ok {
				req = append(req, host.KV{Key: k, Value: v})
			}
		}
		o := host.Obj{{Key: "request", Value: req}, {Key: "status", Value: r["status"]}}
		if e, ok := r["error"]; ok {
			o = append(o, host.KV{Key: "error", Value: e})
		}
		receipt = o
	}
	return host.Obj{{Key: "authority", Value: a["authority"]}, {Key: "gateway", Value: gateway}, {Key: "receipt", Value: receipt}}
}

func gatewayControlMain(argv []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	fail := func(err error) int {
		msg := "gateway_control_unavailable"
		var ce *controlError
		if errors.As(err, &ce) {
			msg = ce.code
		}
		fmt.Fprintln(stdout, host.MustDumps(map[string]any{"error": msg}, host.DumpOpts{}))
		return 1
	}
	defer func() {
		if r := recover(); r != nil {
			code = fail(nil)
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(stdin, gatewayLimit+1))
	if err != nil {
		return fail(err)
	}
	if len(raw) > gatewayLimit {
		return fail(ctlErr("gateway_invalid_request"))
	}
	request, err := strictJSON(raw)
	if err != nil {
		return fail(ctlErr("gateway_invalid_request"))
	}
	answer, err := handleGateway(request, gatewayStateFn())
	if err != nil {
		return fail(err)
	}
	fmt.Fprintln(stdout, host.MustDumps(orderedAnswer(answer), host.DumpOpts{NoASCII: true}))
	return 0
}

func init() { host.Register("gateway-control", gatewayControlMain) }
