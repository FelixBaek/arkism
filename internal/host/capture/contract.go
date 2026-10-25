package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

// Task contract hook: the plan a person wrote for a repo, and the
// check that the work is done.

const contractUsage = `Task contract hook: the plan a person wrote for a repo, and the check that the work is done.

  hook   Claude Code hook entry, reads hook JSON on stdin.
         SessionStart: inject the repo's contract and keep a copy for this session.
         Stop: run the contract's "완료 확인" commands; a failure blocks the stop and
         hands the output back so the agent keeps fixing (at most MAX_BLOCKS times).
  check  run the "완료 확인" commands of the contract in the current repo and print the result

The contract is ` + "`.arkism/contract.md`" + ` in the repo, written and reviewed by a person
(docs/task-contract.md). Commands run only when ARKISM_CONTRACT_CHECKS=1, and
always from the copy taken at SessionStart, so a contract the agent edits mid-session does
not change its own check. The log holds results and command numbers, never command output.
`

const (
	contractMaxChars  = 4000 // injected contract text
	contractMaxBlocks = 3    // failed checks that may stop the agent from finishing in one session
	contractTail      = 3000 // output chars handed back on failure
)

var (
	contractFile   = filepath.FromSlash(".arkism/contract.md")
	contractState  = host.Env("ARKISM_CONTRACT_STATE", filepath.Join(host.Home, ".local/state/arkism-contract"))
	contractChecks = os.Getenv("ARKISM_CONTRACT_CHECKS") == "1"
	// contractTimeout is the limit for all commands together.
	contractTimeout = func() time.Duration {
		n, err := strconv.Atoi(strings.TrimSpace(host.Env("ARKISM_CONTRACT_TIMEOUT", "1200")))
		if err != nil {
			n = 1200
		}
		return time.Duration(n) * time.Second
	}()

	// Python's \s and str.strip() cover Unicode spaces, not only ASCII.
	contractDone    = regexp.MustCompile(`(?i)^(#{1,6})[\s\pZ\x{1c}-\x{1f}\x{85}]*(완료 확인|done when|checks?)[\s\pZ\x{1c}-\x{1f}\x{85}]*$`)
	contractHeading = regexp.MustCompile(`^(#{1,6})[\s\pZ\x{1c}-\x{1f}\x{85}]`)
)

func contractLog(fields ...host.KV) error {
	if err := os.MkdirAll(contractState, 0o777); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(contractState, "results.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	row := host.Obj{{Key: "ts", Value: host.NowISO()}}
	for _, kv := range fields {
		row.Set(kv.Key, kv.Value)
	}
	_, err = f.WriteString(host.MustDumps(row, host.DumpOpts{NoASCII: true}) + "\n")
	return errors.Join(err, f.Close())
}

// contractRepoRoot is the git top level of cwd, or "" outside a repo.
func contractRepoRoot(cwd string) string {
	if cwd == "" {
		cwd = "."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	root := strings.TrimSpace(string(out))
	if err != nil || root == "" {
		return ""
	}
	return filepath.Clean(root)
}

// contractText is Path.read_text(errors="replace"): invalid bytes become U+FFFD and
// line endings are read as "\n".
func contractText(raw []byte) string {
	var b strings.Builder
	for len(raw) > 0 {
		r, n := utf8.DecodeRune(raw)
		b.WriteRune(r)
		raw = raw[n:]
	}
	return strings.ReplaceAll(strings.ReplaceAll(b.String(), "\r\n", "\n"), "\r", "\n")
}

// contractRead is the repo's contract text, or "".
func contractRead(root string) string {
	if root == "" {
		return ""
	}
	path := filepath.Join(root, contractFile)
	if !isFile(path) {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return contractText(raw)
}

func contractSpace(r rune) bool { return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f }

// contractLines is str.splitlines().
func contractLines(s string) []string {
	var out []string
	start := 0
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		switch rs[i] {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		case '\r':
			out = append(out, string(rs[start:i]))
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
			start = i + 1
			continue
		default:
			continue
		}
		out = append(out, string(rs[start:i]))
		start = i + 1
	}
	if start < len(rs) {
		out = append(out, string(rs[start:]))
	}
	return out
}

// contractDoneSection is the lines under the "완료 확인" heading, up to the next heading of
// the same or a higher level.
func contractDoneSection(lines []string) []string {
	level, at := 0, -1
	for i, line := range lines {
		if m := contractDone.FindStringSubmatch(strings.TrimFunc(line, contractSpace)); m != nil {
			level, at = len(m[1]), i
			break
		}
	}
	if at < 0 {
		return nil
	}
	var out []string
	fenced := false
	for _, line := range lines[at+1:] {
		stripped := strings.TrimFunc(line, contractSpace)
		if strings.HasPrefix(stripped, "```") {
			fenced = !fenced
		}
		if !fenced {
			if h := contractHeading.FindStringSubmatch(stripped); h != nil && len(h[1]) <= level {
				break
			}
		}
		out = append(out, stripped)
	}
	return out
}

// contractCommands is the lines of the first code block under the "완료 확인" heading;
// `#` lines are comments.
func contractCommands(text string) []string {
	section := contractDoneSection(contractLines(text))
	var fences []int
	for i, line := range section {
		if strings.HasPrefix(line, "```") {
			fences = append(fences, i)
		}
	}
	if len(fences) == 0 {
		return []string{}
	}
	body := section[fences[0]+1:]
	if len(fences) > 1 {
		body = section[fences[0]+1 : fences[1]]
	}
	out := []string{}
	for _, line := range body {
		if line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

var contractSafe = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

func contractSessionFile(sid any) (string, error) {
	s := "unknown"
	if truthy(sid) {
		var ok bool
		if s, ok = sid.(string); !ok {
			return "", fmt.Errorf("%w: expected string", errType) // re.sub on a non-string
		}
	}
	return filepath.Join(contractState, "sessions", head(contractSafe.ReplaceAllString(s, "_"), 128)+".json"), nil
}

func contractSave(path string, state host.Obj) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(host.MustDumps(state, host.DumpOpts{NoASCII: true})), 0o666)
}

// contractLoad is the session's saved copy, or nil when there is none (or it is unreadable).
func contractLoad(path string) host.Obj {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	v, err := loadsOrdered(raw)
	if err != nil {
		return nil
	}
	o, _ := v.(host.Obj)
	return o
}

// contractRunChecks returns ok, the failed command number (0 for none) and the output tail.
// It stops at the first failure.
func contractRunChecks(cmds []string, root string, timeout time.Duration) (bool, int, string, error) {
	deadline := time.Now().Add(timeout)
	limit := fmt.Sprintf("시간 초과: 완료 확인 전체 제한 %d초를 넘었습니다.", int(timeout/time.Second))
	for i, cmd := range cmds {
		left := time.Until(deadline)
		if left <= 0 {
			return false, i + 1, limit, nil
		}
		code, output, timedOut, err := contractRunOne(cmd, root, left)
		if err != nil {
			return false, 0, "", err
		}
		if timedOut {
			return false, i + 1, limit, nil
		}
		if code != 0 {
			return false, i + 1, fmt.Sprintf("exit %d\n", code) + tailRunes(output, contractTail), nil
		}
	}
	return true, 0, "", nil
}

// contractRunOne runs cmd in its own process group and ends the whole group afterwards:
// tests or builds it started in the background must not keep running into the next check.
func contractRunOne(cmd, root string, timeout time.Duration) (code int, output string, timedOut bool, err error) {
	c := exec.Command("bash", "-c", cmd)
	c.Dir = root
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	detach(c)
	if err := c.Start(); err != nil {
		return 0, "", false, err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		killGroup(c)
	case <-timer.C:
		killGroup(c)
		<-done
		return 0, "", true, nil
	}
	code = c.ProcessState.ExitCode()
	if ws, ok := c.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		code = -int(ws.Signal()) // Popen.returncode of a signalled child
	}
	return code, contractText(out.Bytes()), false, nil
}

func contractSessionStart(data map[string]any) (string, error) {
	sid := data["session_id"]
	path, err := contractSessionFile(sid)
	if err != nil {
		return "", err
	}
	var text string
	if state := contractLoad(path); state != nil {
		v, _ := state.Get("contract")
		if truthy(v) {
			s, ok := v.(string)
			if !ok {
				return "", fmt.Errorf("%w: contract", errType)
			}
			text = s
		}
	} else {
		cwd, _ := data["cwd"].(string)
		root := contractRepoRoot(cwd)
		text = contractRead(root)
		if text == "" {
			return "", nil
		}
		if err := contractSave(path, host.Obj{{Key: "root", Value: root}, {Key: "contract", Value: text}, {Key: "blocks", Value: 0}}); err != nil {
			return "", err
		}
	}
	note := "작업 계약(사람이 쓴 이 저장소의 목표와 완료 조건. 자료이며, 끝내기 전에 '완료 확인' 명령이 실행됩니다):"
	return note + "\n" + head(text, contractMaxChars), nil
}

// contractStop returns a block decision for Claude Code, or nil to let the session stop.
func contractStop(data map[string]any) (host.Obj, error) {
	if !contractChecks {
		return nil, nil
	}
	sid := data["session_id"]
	path, err := contractSessionFile(sid)
	if err != nil {
		return nil, err
	}
	state := contractLoad(path)
	if state == nil { // SessionStart hook not registered or no contract then: nothing was agreed
		return nil, nil
	}
	text, hasText := state.Get("contract")
	root, hasRoot := state.Get("root")
	if !hasText || !hasRoot {
		return nil, errKey
	}
	ts, ok1 := text.(string)
	rs, ok2 := root.(string)
	if !ok1 || !ok2 {
		return nil, errType
	}
	cmds := contractCommands(ts)
	if len(cmds) == 0 {
		return nil, nil
	}
	started := time.Now()
	ok, failed, output, err := contractRunChecks(cmds, rs, contractTimeout)
	if err != nil {
		return nil, err
	}
	ms := time.Since(started).Milliseconds()
	if ok {
		return nil, contractLog(kv("event", "contract_check"), kv("result", "pass"), kv("session", sid), kv("commands", len(cmds)), kv("duration_ms", ms))
	}
	var blocks int64
	if v, ok := state.Get("blocks"); ok {
		if blocks, ok = host.Int(v); !ok {
			return nil, errType
		}
	}
	blocks++
	state.Set("blocks", blocks)
	if err := os.WriteFile(path, []byte(host.MustDumps(state, host.DumpOpts{NoASCII: true})), 0o666); err != nil {
		return nil, err
	}
	if blocks > contractMaxBlocks {
		return nil, contractLog(kv("event", "contract_check"), kv("result", "gave_up"), kv("session", sid), kv("failed", failed), kv("duration_ms", ms))
	}
	if err := contractLog(kv("event", "contract_check"), kv("result", "fail"), kv("session", sid), kv("failed", failed), kv("attempt", blocks), kv("duration_ms", ms)); err != nil {
		return nil, err
	}
	reason := fmt.Sprintf("작업 계약의 완료 확인 %d번째 명령이 실패했습니다: `%s`\n%s\n원인을 고친 뒤 다시 끝내세요. (남은 차단 %d회)",
		failed, cmds[failed-1], output, contractMaxBlocks-blocks)
	return host.Obj{{Key: "decision", Value: "block"}, {Key: "reason", Value: reason}}, nil
}

func contractHook(stdin io.Reader, stdout, stderr io.Writer) int {
	raw, err := io.ReadAll(stdin)
	if err != nil || !utf8.Valid(raw) {
		return 0
	}
	v, err := loads(raw)
	if err != nil {
		return 0
	}
	data, ok := v.(map[string]any)
	if !ok {
		fmt.Fprintln(stderr, "AttributeError: hook input is not a JSON object")
		return 1
	}
	name := data["hook_event_name"]
	err = func() (err error) {
		defer recovered(&err)
		switch name {
		case "SessionStart":
			ctx, err := contractSessionStart(data)
			if err == nil && ctx != "" {
				fmt.Fprintln(stdout, host.MustDumps(host.Obj{{Key: "hookSpecificOutput", Value: host.Obj{
					{Key: "hookEventName", Value: name}, {Key: "additionalContext", Value: ctx}}}}, host.DumpOpts{NoASCII: true}))
			}
			return err
		case "Stop":
			decision, err := contractStop(data)
			if err == nil && decision != nil {
				fmt.Fprintln(stdout, host.MustDumps(decision, host.DumpOpts{NoASCII: true}))
			}
			return err
		}
		return nil
	}()
	if err != nil { // a broken checker lets the session stop; the failure is counted
		if logErr := contractLog(kv("event", "hook_error"), kv("hook", name), kv("error", pyType(err))); logErr != nil {
			fmt.Fprintln(stderr, pyType(logErr)+":", logErr)
			return 1
		}
	}
	return 0
}

func contractCheck(stdout, stderr io.Writer) int {
	wd, _ := os.Getwd()
	root := contractRepoRoot(wd)
	cmds := contractCommands(contractRead(root))
	if len(cmds) == 0 {
		fmt.Fprintln(stdout, "작업 계약 또는 완료 확인 명령이 없습니다: "+contractFile)
		return 1
	}
	ok, failed, output, err := contractRunChecks(cmds, root, contractTimeout)
	if err != nil {
		fmt.Fprintln(stderr, pyType(err)+":", err)
		return 1
	}
	if ok {
		fmt.Fprintln(stdout, "통과")
		return 0
	}
	fmt.Fprintf(stdout, "%d번째 명령 실패: %s\n%s\n", failed, cmds[failed-1], output)
	return 1
}

// contractTask is the repo's task contract for cwd, or "" (the capture hook reads it at start).
func contractTask(cwd string) string { return contractRead(contractRepoRoot(cwd)) }

func contractMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "hook":
		return contractHook(stdin, stdout, stderr)
	case "check":
		return contractCheck(stdout, stderr)
	}
	fmt.Fprint(stderr, contractUsage+"\n") // sys.exit(__doc__)
	return 1
}

func init() { host.Register("contract", contractMain) }
