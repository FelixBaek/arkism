package capture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// Windows apps -> SSH -> central Arkism. No API tokens or LLMs on the PC.

var (
	winOrigins = []string{"codex", "claude_code", "zcode", "gemini", "grok", "deepseek", "opencode", "cursor"}
	// winState is the PC client's folder; tests replace it.
	winState = filepath.Join(host.Home, ".config", "arkism")
)

// winLocalConn is what the Arkism app writes while its built-in Arkism runs on this PC
// (runtime/agents.json in the app's data folder). With it the agents talk to that Arkism
// directly; without it they reach the server over SSH as before.
type winLocalConn struct {
	Mode      string `json:"mode"`
	URL       string `json:"url"`
	TokenFile string `json:"token_file"`
	// With mode "server": the SSH address the app connected with (설정에서 서버에 연결).
	SSHHost           string `json:"ssh_host"`
	SSHPort           int    `json:"ssh_port"`
	SSHKey            string `json:"ssh_key"`
	SSHKnownHosts     string `json:"ssh_known_hosts"`
	SSHHostAlias      string `json:"ssh_host_alias"`
	SSHHostAlgorithms string `json:"ssh_host_algorithms"`
}

// winLocalConnPath is the app's agents.json, in Tauri's local data folder for the app on each system.
func winLocalConnPath() string {
	var local string
	switch runtime.GOOS {
	case "windows":
		local = host.Env("LOCALAPPDATA", filepath.Join(host.Home, "AppData", "Local"))
	case "darwin":
		local = filepath.Join(host.Home, "Library", "Application Support")
	default:
		local = host.Env("XDG_DATA_HOME", filepath.Join(host.Home, ".local", "share"))
	}
	return host.Env("ARKISM_AGENT_CONNECTION", filepath.Join(local, "dev.arkism.desktop", "runtime", "agents.json"))
}

// winSSHFlag is `arkism windows --ssh-host`, which wins over the app's choice.
var winSSHFlag string

// winServerSSH is the SSH address and port the agents use: --ssh-host, else the one the app
// connected with, else ARKISM_SSH_HOST.
func winServerSSH() (string, int) {
	h, p, _ := winServerSSHKey()
	return h, p
}

// winServerSSHKey also gives the app's 인증 키 파일 (empty: ssh's own defaults).
func winServerSSHKey() (string, int, string) {
	if winSSHFlag != "" {
		return winSSHFlag, 0, ""
	}
	var c winLocalConn
	if data, err := os.ReadFile(winLocalConnPath()); err == nil && json.Unmarshal(data, &c) == nil && c.Mode == "server" && winSSHHostOK(c.SSHHost) {
		if c.SSHKey != "" && !(filepath.IsAbs(c.SSHKey) && exists(c.SSHKey)) {
			c.SSHKey = ""
		}
		return c.SSHHost, c.SSHPort, c.SSHKey
	}
	return host.Env("ARKISM_SSH_HOST", "arkism-server"), 0, ""
}

// winSSHHostOK accepts `host` or `user@host` and nothing ssh could read as an option.
func winSSHHostOK(h string) bool {
	if h == "" || len(h) > 253 || strings.HasPrefix(h, "@") || strings.Count(h, "@") > 1 {
		return false
	}
	for i, r := range h {
		ok := r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || i > 0 && strings.ContainsRune("._-@", r))
		if !ok {
			return false
		}
	}
	return true
}

func winLocalConnection() (winLocalConn, bool) {
	var c winLocalConn
	data, err := os.ReadFile(winLocalConnPath())
	if err != nil || json.Unmarshal(data, &c) != nil {
		return c, false
	}
	// Only the app's own loopback Arkism: a file that points elsewhere is ignored.
	ok := c.Mode == "local" && strings.HasPrefix(c.URL, "http://127.0.0.1:") && c.TokenFile != "" && exists(c.TokenFile)
	return c, ok
}

// winLocalCommand runs mode in this same program against the built-in Arkism. The settings the
// server side reads from its environment are set here and inherited by the child.
func winLocalCommand(c winLocalConn, origin, mode string) ([]string, error) {
	set := map[string]string{"ARKISM_URL": c.URL, "ARKISM_TOKEN_FILE": c.TokenFile, "ARKISM_ORIGIN": origin, "ARKISM_CLIENT": "pc", "ARKISM_PC_LOCAL": "1", "ARKISM_REPO_TAGS": "1"}
	var args []string
	switch mode {
	case "hook":
		set["ARKISM_CAPTURE_ON_PROMPT"], set["ARKISM_CAPTURE_ACK"] = "1", "1"
		set["ARKISM_CAPTURE_STATE"] = filepath.Join(winState, "local-capture-"+origin)
		args = []string{"capture", "hook"}
	case "mcp":
		set["ARKISM_RUNTIME"] = origin
		args = []string{"skill-router"}
	default:
		// Work hand-off and archive retention belong to a server; the built-in Arkism has neither.
		return nil, valueErr("not available with the built-in Arkism")
	}
	for k, v := range set {
		os.Setenv(k, v)
	}
	return append([]string{host.Executable()}, args...), nil
}

// winSSHCommand is the argv that runs mode for origin: in this program against the built-in
// Arkism when the app runs one, otherwise over ssh on the server's Arkism binary.
func winSSHCommand(origin, mode string) ([]string, error) {
	if !slices.Contains(winOrigins, origin) || !slices.Contains([]string{"hook", "mcp", "work-event", "archive-retention"}, mode) {
		return nil, valueErr("unsupported origin or mode")
	}
	if c, ok := winLocalConnection(); ok {
		return winLocalCommand(c, origin, mode)
	}
	// Where the server keeps the Arkism binary and per-client capture state (paths on the server).
	bin := host.Env("ARKISM_SERVER_BIN", "/srv/arkism/bin/arkism")
	state := host.Env("ARKISM_SERVER_STATE", "$HOME/.local/state")
	var remote string
	switch mode {
	case "hook":
		remote = fmt.Sprintf("env ARKISM_ORIGIN=%s ARKISM_CAPTURE_ON_PROMPT=1 ARKISM_CAPTURE_ACK=1 ARKISM_CLIENT=pc "+
			"ARKISM_CAPTURE_STATE=%s/arkism-capture-pc-%s %s capture hook", origin, state, origin, bin)
	case "archive-retention":
		if origin != "zcode" {
			return nil, valueErr("unsupported retention origin")
		}
		remote = bin + " archive-retention"
	case "work-event":
		remote = fmt.Sprintf("env ARKISM_ORIGIN=%s ARKISM_CLIENT=pc ARKISM_CAPTURE_STATE=%s/arkism-capture-pc-%s %s work-pc-receiver",
			origin, state, origin, bin)
	default:
		// The server knows the call came from the PC.
		remote = fmt.Sprintf("env ARKISM_RUNTIME=%s ARKISM_CLIENT=pc %s skill-router", origin, bin)
	}
	// Windows OpenSSH can hang on piped stdin/stdout; Git's installed OpenSSH handles MCP pipes.
	ssh := "ssh"
	if git := filepath.Join(host.Env("ProgramFiles", "C:/Program Files"), "Git", "usr", "bin", "ssh.exe"); exists(git) {
		ssh = git
	}
	argv := []string{ssh, "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2"}
	sshHost, sshPort, sshKey := winServerSSHKey()
	if !winSSHHostOK(sshHost) {
		return nil, valueErr("invalid SSH host")
	}
	trust, err := winSSHTrust(sshHost)
	if err != nil {
		return nil, err
	}
	argv = append(argv, trust...)
	if sshPort > 0 && sshPort < 65536 {
		argv = append(argv, "-p", fmt.Sprint(sshPort))
	}
	if sshKey != "" {
		argv = append(argv, "-i", sshKey, "-o", "IdentitiesOnly=yes")
	}
	return append(argv, sshHost, remote), nil
}

// winRun is subprocess.run(argv, input=input, capture_output=True, timeout=timeout). Tests replace it.
var winRun = func(argv []string, input []byte, timeout time.Duration) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(input)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if ctx.Err() != nil {
		return 0, nil, errTimeoutExpired
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), stdout.Bytes(), nil
	}
	if err != nil {
		return 0, nil, err
	}
	return 0, stdout.Bytes(), nil
}

func winSendWorkEvent(origin string, data []byte) (any, error) {
	argv, err := winSSHCommand(origin, "work-event")
	if err != nil {
		return nil, err
	}
	code, stdout, err := winRun(argv, data, 18*time.Second)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, errors.New("work_event_ssh_failed")
	}
	return loadsBytes(stdout)
}

// winSplitLines is bytes.splitlines().
func winSplitLines(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.IndexAny(b, "\r\n")
		if i < 0 {
			out = append(out, b)
			break
		}
		out = append(out, b[:i])
		if b[i] == '\r' && i+1 < len(b) && b[i+1] == '\n' {
			i++
		}
		b = b[i+1:]
	}
	return out
}

// winSendHook relays one hook event; a prompt needs the server's storage receipt.
// Tests replace it.
var winSendHook = func(origin string, data []byte) ([]byte, error) {
	argv, err := winSSHCommand(origin, "hook")
	if err != nil {
		return nil, err
	}
	code, stdout, err := winRun(argv, data, 18*time.Second)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, errors.New("ssh_failed") // no stderr: remote errors can contain private data
	}
	v, err := loadsBytes(data)
	if err != nil {
		return nil, err
	}
	if obj(v)["hook_event_name"] != "UserPromptSubmit" {
		return stdout, nil
	}
	// Strip the transport receipt before returning the app's hook output.
	// An old server or a caught enqueue failure must leave the PC spool intact.
	var output [][]byte
	accepted := false
	for _, line := range winSplitLines(stdout) {
		record, err := loadsBytes(line)
		if err != nil {
			return nil, err
		}
		if m, ok := record.(map[string]any); ok {
			if d, has := m["arkismDelivery"]; has {
				dm, ok := d.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%w: arkismDelivery", errType)
				}
				accepted = dm["accepted"] == true
				continue
			}
		}
		output = append(output, line)
	}
	if !accepted {
		return nil, errors.New("capture_unacknowledged")
	}
	return bytes.Join(output, []byte("\n")), nil
}

// winLoadEvent decodes a hook payload keeping its key order (json.loads of bytes).
func winLoadEvent(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, errJSONShape
	}
	v, err := loadsOrdered(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	if err != nil {
		return nil, err
	}
	return pyNorm(v), nil
}

func winSpool(origin string) string { return filepath.Join(winState, "spool", origin) }

func winQueuePrompt(origin string, data []byte) (target string, err error) {
	folder := winSpool(origin)
	err = outboxLocked(folder, func(string) error {
		target = filepath.Join(folder, sha256Hex(data)+".json")
		if isSymlink(target) {
			return valueErr("capture_spool_alias")
		}
		if exists(target) {
			return nil
		}
		v, err := winLoadEvent(data)
		if err != nil {
			return err
		}
		event, ok := v.(host.Obj)
		if !ok {
			return fmt.Errorf("%w: event", errType)
		}
		event.Set(pcTime, timeNow())
		return outboxWrite(target, []byte(dumps(event)))
	})
	return target, err
}

func winCleanPrompts(origin string) (removed int, err error) {
	folder := winSpool(origin)
	if !exists(folder) {
		return 0, nil
	}
	err = outboxLocked(folder, func(string) error {
		removed, err = cleanupSpool(folder, pcTime, -1)
		return err
	})
	return removed, err
}

func winSendPrompt(origin, path string) ([]byte, error) {
	// Keep the network outside the enqueue lock so another prompt can be saved.
	var raw []byte
	var identity fileStamp
	err := outboxLocked(filepath.Dir(path), func(folder string) error {
		if _, err := cleanupSpool(folder, pcTime, -1); err != nil {
			return err
		}
		if isSymlink(path) {
			return valueErr("capture_spool_alias")
		}
		if !exists(path) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		raw = b
		v, err := winLoadEvent(raw)
		if err != nil {
			return err
		}
		event, ok := v.(host.Obj)
		if !ok {
			return valueErr("capture_prompt_invalid")
		}
		at, has := event.Get(pcTime)
		if !has {
			info, err := os.Stat(path)
			if err != nil {
				return err
			}
			q, err := queuedAt(mtime(info), -1)
			if err != nil {
				return err
			}
			at = q
			event.Set(pcTime, at)
			raw = []byte(dumps(event))
			if err := outboxWrite(path, raw); err != nil {
				return err
			}
		}
		if _, err := queuedAt(at, -1); err != nil {
			return err
		}
		identity, err = pathStamp(path, true)
		return err
	})
	if err != nil || raw == nil {
		return []byte{}, err
	}
	output, err := winSendHook(origin, raw)
	if err != nil {
		return nil, err
	}
	err = outboxLocked(filepath.Dir(path), func(string) error {
		if !isFile(path) || isSymlink(path) {
			return nil
		}
		current, err := pathStamp(path, true)
		if err != nil {
			return err
		}
		now, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if current.Dev == identity.Dev && current.Ino == identity.Ino && current.MtimeNs == identity.MtimeNs && bytes.Equal(now, raw) {
			return os.Remove(path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return output, nil
}

func winWorkRoot(origin string) string {
	sshHost, _ := winServerSSH()
	key := sha256Hex([]byte(sshHost))
	return filepath.Join(winState, "work-events", key, origin)
}

func winWorkFiles(root string) []string {
	paths, _ := filepath.Glob(filepath.Join(globEscape(root), "*-pc-work-*.json"))
	return paths
}

// winDrainWork recovers half-written transitions and resends queued work events.
func winDrainWork(origin string) (host.Obj, error) {
	if origin != "codex" && origin != "claude_code" && origin != "zcode" {
		return nil, valueErr("unsupported work origin")
	}
	expiredPrompts, err := winCleanPrompts(origin)
	if err != nil {
		return nil, err
	}
	root := winWorkRoot(origin)
	if !exists(root) {
		return host.Obj{{Key: "acknowledged", Value: 0}, {Key: "expired", Value: 0}, {Key: "remaining", Value: 0},
			{Key: "failed", Value: false}, {Key: "expired_prompts", Value: expiredPrompts}}, nil
	}
	result := host.Obj{{Key: "expired_prompts", Value: expiredPrompts}}
	err = func() error {
		rec, err := pcHookRecover(root)
		if err != nil {
			return err
		}
		result = append(result, rec...)
		drained, err := outboxDrain(root, func(raw []byte) (any, error) { return winSendWorkEvent(origin, raw) }, 20)
		if err != nil {
			return err
		}
		for _, kv := range drained {
			result.Set(kv.Key, kv.Value)
		}
		return nil
	}()
	result.Set("failed", err != nil)
	result.Set("remaining", len(winWorkFiles(root)))
	return result, nil
}

// winSpawnExport starts `arkism export-zcode --execute` without waiting. Tests replace it.
var winSpawnExport = func() {
	cmd := exec.Command(host.Executable(), "export-zcode", "--execute")
	detach(cmd)
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

// winHook relays one app hook: records work transitions, persists prompts before
// sending, and uses a spare request to retry a queued work event or older prompt.
func winHook(origin string, data []byte, stderr io.Writer) ([]byte, error) {
	if _, err := winCleanPrompts(origin); err != nil {
		return nil, err
	}
	v, err := winLoadEvent(data)
	if err != nil {
		return nil, err
	}
	event, ok := v.(host.Obj)
	name, _ := event.Get("hook_event_name")
	if !ok || !slices.Contains([]any{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"}, name) {
		return []byte{}, nil
	}
	if cwd, _ := event.Get("cwd"); captureSkipped(str(or(cwd, ""))) {
		return []byte{}, nil // paused, or a folder the user excluded
	}
	given, _ := event.Get("arkism_work_id")
	explicitWork := truthy(given) || os.Getenv("ARKISM_WORK_ID") != ""
	workID := os.Getenv("ARKISM_WORK_ID")
	cwd, _ := event.Get("cwd")
	if workID == "" && !truthy(given) {
		workID = selectionSelected(cwd)
	}
	if workID != "" && !truthy(given) {
		event.Set("arkism_work_id", workID)
		data = []byte(dumps(event))
	}
	selected, _ := event.Get("arkism_work_id")
	selected = or(selected, "")
	queuedWork := winWorkRoot(origin)
	err = func() (err error) {
		defer recovered(&err)
		stamp := []any{"explicit", selected}
		if truthy(selected) && !explicitWork {
			_, path, err := selectionPath(cwd)
			if err != nil {
				return err
			}
			info, err := pathStamp(path, true)
			if err != nil {
				return err
			}
			stamp = []any{"folder", int64(info.Ino), info.MtimeNs}
		}
		m := map[string]any{}
		for _, kv := range event {
			m[kv.Key] = kv.Value
		}
		return pcHookRecord(queuedWork, origin, m, selected, stamp)
	}()
	if err != nil {
		// Zcode may invoke SessionStart before its SQLite session exists.
		fmt.Fprintln(stderr, "Arkism: 작업 인계 기록을 만들지 못했습니다. 다음 훅에서 다시 확인합니다.")
	}
	// Persist before sending: an interrupted hook must not lose the prompt.
	var output []byte
	if prompt, _ := event.Get("prompt"); name == "UserPromptSubmit" && truthy(prompt) {
		pending, err := winQueuePrompt(origin, data)
		if err != nil {
			return nil, err
		}
		if output, err = winSendPrompt(origin, pending); err != nil {
			return nil, err
		}
	} else if output, err = winSendHook(origin, data); err != nil {
		return nil, err
	}
	if origin == "zcode" {
		// The exporter uses a nonblocking OS lock; the hook never waits on DB work.
		winSpawnExport()
	}
	if err := winHookRetry(origin, queuedWork, stderr); err != nil {
		return nil, err
	}
	return output, nil
}

// winHookRetry spends a hook's spare request on a queued work event or else one older prompt.
func winHookRetry(origin, queuedWork string, stderr io.Writer) error {
	// Keep at most two SSH requests per hook; work retries take the second slot.
	if len(winWorkFiles(queuedWork)) > 0 {
		if _, err := outboxDrain(queuedWork, func(raw []byte) (any, error) { return winSendWorkEvent(origin, raw) }, 1); err != nil {
			fmt.Fprintln(stderr, "Arkism: 작업 인계 신호를 보관했습니다. 다음 훅에서 재전송합니다.")
		}
		return nil
	}
	// Retry one older prompt when no work signal is waiting.
	if _, err := winCleanPrompts(origin); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(globEscape(winSpool(origin)), "*.json"))
	if len(old) > 0 && !isSymlink(old[0]) {
		winSendPrompt(origin, old[0])
	}
	return nil
}

const winUsage = "usage: arkism windows [-h] [--ssh-host SSH_HOST] {hook,mcp,drain-work} {codex,claude_code,zcode,gemini,grok,deepseek}\n"

func winMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(msg string) int {
		fmt.Fprint(stderr, winUsage)
		fmt.Fprintf(stderr, "arkism windows: error: %s\n", msg)
		return 2
	}
	var pos []string
	sshHost := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-h" || a == "--help":
			fmt.Fprint(stdout, winUsage)
			return 0
		case a == "--ssh-host":
			if i+1 >= len(args) {
				return fail("argument --ssh-host: expected one argument")
			}
			i++
			sshHost = args[i]
		case strings.HasPrefix(a, "--ssh-host="):
			sshHost = strings.TrimPrefix(a, "--ssh-host=")
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) < 2 {
		return fail("the following arguments are required: mode, origin")
	}
	if len(pos) > 2 {
		return fail("unrecognized arguments: " + strings.Join(pos[2:], " "))
	}
	mode, origin := pos[0], pos[1]
	if !slices.Contains([]string{"hook", "mcp", "drain-work"}, mode) {
		return fail(fmt.Sprintf("argument mode: invalid choice: '%s' (choose from hook, mcp, drain-work)", mode))
	}
	if !slices.Contains(winOrigins, origin) {
		return fail(fmt.Sprintf("argument origin: invalid choice: '%s' (choose from %s)", origin, strings.Join(winOrigins, ", ")))
	}
	if sshHost != "" {
		os.Setenv("ARKISM_SSH_HOST", sshHost)
		winSSHFlag = sshHost
		defer func() { winSSHFlag = "" }()
	}
	switch mode {
	case "drain-work":
		if origin != "codex" && origin != "claude_code" && origin != "zcode" {
			return fail("drain-work supports codex, claude_code or zcode")
		}
		result, err := winDrainWork(origin)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, host.MustDumps(result, host.DumpOpts{}))
		if failed, _ := result.Get("failed"); failed == true {
			return 1
		}
		return 0
	case "mcp":
		return winMCPMain(origin, stdin, stdout, stderr)
	}
	return winHookMain(origin, stdin, stdout, stderr)
}

// winMCPMain relays the MCP stdio session over SSH and returns its exit code.
func winMCPMain(origin string, stdin io.Reader, stdout, stderr io.Writer) int {
	argv, err := winSSHCommand(origin, "mcp")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// winHookMain relays the hook event on stdin; it never fails the app's hook.
func winHookMain(origin string, stdin io.Reader, stdout, stderr io.Writer) int {
	data, err := io.ReadAll(stdin)
	var out []byte
	if err == nil {
		out, err = winHook(origin, data, stderr)
	}
	if err != nil {
		// SessionStart failures have no prompt to save; submit failures remain in the spool.
		fmt.Fprintln(stderr, "Arkism: 연결 실패. 전송 대기 기록은 다음 훅에서 재시도합니다.")
		return 0
	}
	stdout.Write(out)
	return 0
}

func init() { host.Register("windows", winMain) }
