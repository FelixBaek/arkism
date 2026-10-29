package memory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

// The host tools run as subcommands of the real arkism binary, built once.
var arkismBuild struct {
	once      sync.Once
	dir, path string
	output    []byte
	err       error
}

func TestMain(m *testing.M) {
	// The test binary doubles as a fake external program (see fakeHelper), chosen
	// by its link name (fakeTool) or, where the caller clears the env, by a variable.
	if helper := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe"); helper == "claude" || helper == "ssh" {
		os.Exit(fakeHelper(helper))
	}
	if helper := os.Getenv("ARKISM_TEST_HELPER"); helper != "" {
		os.Exit(fakeHelper(helper))
	}
	code := m.Run()
	if arkismBuild.dir != "" {
		os.RemoveAll(arkismBuild.dir)
	}
	os.Exit(code)
}

func arkismBinary(t *testing.T) string {
	t.Helper()
	arkismBuild.once.Do(func() {
		if arkismBuild.dir, arkismBuild.err = os.MkdirTemp("", "arkism-bin-"); arkismBuild.err != nil { //nolint:usetesting // one build shared by every test, removed in TestMain
			return
		}
		arkismBuild.path = filepath.Join(arkismBuild.dir, "arkism")
		if runtime.GOOS == "windows" {
			arkismBuild.path += ".exe"
		}
		arkismBuild.output, arkismBuild.err = exec.Command("go", "build", "-o", arkismBuild.path, "../../cmd/arkism").CombinedOutput()
	})
	if arkismBuild.err != nil {
		t.Fatalf("build arkism: %v %s", arkismBuild.err, arkismBuild.output)
	}
	return arkismBuild.path
}

// arkismEnv is a closed environment: HOME is a test folder and the API and token
// are explicit, so no default can reach the real server or the user's state.
func arkismEnv(home, url, tokenFile string, extra ...string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "ARKISM_URL=" + url, "ARKISM_TOKEN_FILE=" + tokenFile,
		"ARKISM_WORKSPACE=" + scopeA.WorkspaceID, "ARKISM_PROJECT=" + scopeA.ProjectID, "ARKISM_USER=" + scopeA.UserID, "ARKISM_AGENT=" + scopeA.AgentID,
		"ARKISM_CAPTURE_STATE=" + filepath.Join(home, "capture"), "ARKISM_CAPTURE_CLAUDE=" + filepath.Join(home, "missing-claude"), "TMPDIR=" + os.TempDir()}
	if runtime.GOOS == "windows" {
		env = append(env, "SystemRoot="+os.Getenv("SystemRoot"), "USERPROFILE="+home)
	}
	return append(env, extra...)
}

// arkismRun runs one subcommand and fails the test on a nonzero exit.
func arkismRun(t *testing.T, env []string, stdin string, args ...string) []byte {
	t.Helper()
	out, err := arkismExec(t, env, stdin, args...)
	if err != nil {
		t.Fatalf("arkism %s: %v %s", strings.Join(args, " "), err, out)
	}
	return out
}

func arkismExec(t *testing.T, env []string, stdin string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command(arkismBinary(t), args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return append(out, stderr.Bytes()...), err
	}
	return out, nil
}

type mcpCall struct {
	Name string
	Args map[string]any
}

// arkismMCP sends tool calls to one `arkism skill-router` process, so state such
// as loaded skill refs carries across calls, and returns one response per call.
func arkismMCP(t *testing.T, env []string, calls ...mcpCall) []map[string]json.RawMessage {
	t.Helper()
	var in strings.Builder
	for i, call := range calls {
		message, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": i + 1, "method": "tools/call", "params": map[string]any{"name": call.Name, "arguments": call.Args}})
		in.Write(append(message, '\n'))
	}
	out := arkismRun(t, env, in.String(), "skill-router")
	var responses []map[string]json.RawMessage
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(nil, 16<<20)
	for scanner.Scan() {
		var response map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			t.Fatalf("router output: %v %s", err, out)
		}
		responses = append(responses, response)
	}
	if len(responses) != len(calls) {
		t.Fatalf("router answered %d of %d calls: %s", len(responses), len(calls), out)
	}
	return responses
}

// mcpResult decodes a successful tool call's structuredContent.
func mcpResult(t *testing.T, response map[string]json.RawMessage, into any) {
	t.Helper()
	var result struct {
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if response["error"] != nil || json.Unmarshal(response["result"], &result) != nil || result.IsError || json.Unmarshal(result.StructuredContent, into) != nil {
		t.Fatalf("tool call failed: %s %s", response["error"], response["result"])
	}
}

// mcpError is the JSON-RPC error message, or "" for a successful call.
func mcpError(response map[string]json.RawMessage) string {
	var e struct {
		Message string `json:"message"`
	}
	if response["error"] == nil || json.Unmarshal(response["error"], &e) != nil {
		return ""
	}
	return e.Message
}

// arkismHook runs `arkism capture hook` with one hook event and returns the
// additionalContext it prints ("" when it prints nothing).
func arkismHook(t *testing.T, env []string, event map[string]any) string {
	t.Helper()
	payload, _ := json.Marshal(event)
	out := arkismRun(t, env, string(payload), "capture", "hook")
	if len(bytes.TrimSpace(out)) == 0 {
		return ""
	}
	var result struct {
		Hook struct {
			Context string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("hook output: %v %s", err, out)
	}
	return result.Hook.Context
}

// blockSpawnedDrain stops the detached `capture drain` that SessionStart, Stop and
// SessionEnd hooks start: drain.lock is a folder, so the child fails to open it and
// exits without touching the state that the test's temp folder cleanup removes.
func blockSpawnedDrain(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "capture", "drain.lock"), 0700); err != nil {
		t.Fatal(err)
	}
}

// hookWork decodes the work section that follows host.HookHeader in a hook context.
func hookWork(t *testing.T, context string, into any) {
	t.Helper()
	_, work, found := strings.Cut(context, host.HookHeader)
	if !found || json.Unmarshal([]byte(work), into) != nil {
		t.Fatalf("no work section: %s", context)
	}
}

// sameJSON compares two values by their JSON form.
func sameJSON(a, b any) bool {
	decode := func(v any) any {
		raw, ok := v.(json.RawMessage)
		if !ok {
			raw, _ = json.Marshal(v)
		}
		var out any
		json.Unmarshal(raw, &out)
		return out
	}
	return reflect.DeepEqual(decode(a), decode(b))
}

// fakeTool links the test binary under name in a new folder and returns the
// folder, to put first on PATH (or name directly) for the arkism binary.
func fakeTool(t *testing.T, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Symlink(self, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeHelper is a stand-in program run from the test binary:
// "claude" replays queued extractor answers (see fakeClaude);
// "ssh" runs the remote command line here, as the server would;
// "adapter" is the skill comparison adapter used by skill_comparison_runner_test.
func fakeHelper(kind string) int {
	switch kind {
	case "claude":
		return fakeClaude()
	case "ssh":
		return fakeSSH()
	case "adapter":
		return fakeAdapter()
	}
	return 2
}

// fakeSSH runs the last argument (the remote command line) with the caller's
// environment and pipes, so PC-side tools reach the server-side arkism command.
func fakeSSH() int {
	words := strings.Fields(os.Args[len(os.Args)-1])
	env := os.Environ()
	// The server's own environment (as sshd gives it), when the test separates PC and server.
	if path := os.Getenv("ARKISM_TEST_SSH_ENV"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(raw, &env) != nil {
			return 255
		}
	}
	if len(words) > 0 && words[0] == "env" {
		words = words[1:]
		for len(words) > 0 && strings.Contains(words[0], "=") {
			env = append(env, os.ExpandEnv(words[0])) // as the remote shell would
			words = words[1:]
		}
	}
	if len(words) == 0 {
		return 255
	}
	cmd := exec.Command(words[0], words[1:]...)
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		return 255
	}
	return 0
}

// fakeClaude prints the next queued reply (reply<N>.json) from ARKISM_TEST_CLAUDE_DIR and
// saves the prompt beside it, so tests can assert what extraction received.
func fakeClaude() int {
	dir := os.Getenv("ARKISM_TEST_CLAUDE_DIR")
	stdin, _ := io.ReadAll(os.Stdin)
	for i := 0; ; i++ {
		reply := filepath.Join(dir, "reply"+strconv.Itoa(i)+".json")
		used := reply + ".used"
		if _, err := os.Stat(used); err == nil {
			continue
		}
		raw, err := os.ReadFile(reply)
		if err != nil {
			os.Stderr.WriteString("fake claude: no reply queued\n")
			return 3
		}
		if os.WriteFile(used, stdin, 0600) != nil {
			return 4
		}
		os.Stdout.Write(raw)
		return 0
	}
}

// queueClaude drops replies fakeClaude has not used and queues the given claim lists
// (structured_output.claims) as the next answers.
func queueClaude(t *testing.T, dir string, claims ...[]map[string]any) {
	t.Helper()
	used := len(claudePrompts(t, dir))
	unused, _ := filepath.Glob(filepath.Join(dir, "reply*.json"))
	for _, path := range unused {
		if _, err := os.Stat(path + ".used"); os.IsNotExist(err) {
			os.Remove(path)
		}
	}
	for i, list := range claims {
		raw, _ := json.Marshal(map[string]any{"is_error": false, "structured_output": map[string]any{"claims": list}})
		if err := os.WriteFile(filepath.Join(dir, "reply"+strconv.Itoa(used+i)+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// claudePrompts are the prompts fakeClaude answered, in order.
func claudePrompts(t *testing.T, dir string) []string {
	t.Helper()
	var prompts []string
	for i := 0; ; i++ {
		raw, err := os.ReadFile(filepath.Join(dir, "reply"+strconv.Itoa(i)+".json.used"))
		if err != nil {
			return prompts
		}
		prompts = append(prompts, string(raw))
	}
}

func fakeAdapter() int {
	// A sentinel turns the adapter into a failure, like rewriting the old script.
	if _, err := os.Stat(os.Getenv("ARKISM_TEST_ADAPTER_BROKEN")); err == nil {
		os.Stderr.WriteString("adapter must not restart\n")
		return 1
	}
	var x map[string]any
	if json.NewDecoder(os.Stdin).Decode(&x) != nil || len(x) != 2 {
		return 1
	}
	input, ok1 := x["input"].(string)
	skill, ok2 := x["skill"].(string)
	if !ok1 || !ok2 {
		return 1
	}
	output := "wrong"
	if strings.HasPrefix(skill, "# Review") || input != "quality" {
		output = input
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"output": output, "cost": 0})
	return 0
}
