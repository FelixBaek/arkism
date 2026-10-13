package gateway

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// Claude CLI with caller-owned SDK MCP tools.
//
// Only the host's selected login is used. Runs live in memory, never in a session archive.
// Tool results resume their exact run; neither errors nor timeouts retry.

// Limits and timers; variables so tests can shrink them.
var (
	claudeMaxInput  = 8 << 20
	claudeMaxOutput = 16 << 20
	claudeMaxRuns   = 8
	claudeDeadline  = 600 * time.Second
	claudeIdle      = 300 * time.Second
)

const (
	claudePrefix    = "toolu_mem_"
	claudeMCPPrefix = "mcp__arkism__"
)

// ClaudeError is a fixed public code and status; Started means the run had begun.
type ClaudeError struct {
	Code    string
	Status  int
	Started bool
}

func (e *ClaudeError) Error() string { return e.Code }

func claudeErr(code string, status int) *ClaudeError { return &ClaudeError{Code: code, Status: status} }

// claudeFail raises inside a run; iterMessages turns it back into the returned error.
func claudeFail(code string, status int) { panic(claudeErr(code, status)) }

func claudeProtocol() { claudeFail("upstream_protocol", 502) }

// claudeJSON is _json: compact, non-ASCII, sorted keys; ok is false where Python raised ValueError.
func claudeJSON(v any) (string, bool) {
	if !validJSON(v, 0) {
		return "", false
	}
	s, err := host.Dumps(v, host.DumpOpts{Compact: true, NoASCII: true, SortKeys: true})
	return s, err == nil
}

// claudeMustJSON is _json where a failure is a protocol error.
func claudeMustJSON(v any) string {
	s, ok := claudeJSON(v)
	if !ok {
		claudeProtocol()
	}
	return s
}

// claudeHistory makes text strings and a single text block the same conversation to clients.
func claudeHistory(messages any) []any {
	out := []any{}
	for _, m := range messages.([]any) {
		content := get(m, "content")
		if s, ok := content.(string); ok {
			content = []any{host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: s}}}
		}
		out = append(out, host.Obj{{Key: "role", Value: get(m, "role")}, {Key: "content", Value: content}})
	}
	return out
}

// claudeOwnedCalls reports whether messages carry a gateway-issued tool id (owned_calls).
func claudeOwnedCalls(messages any) bool {
	list, _ := messages.([]any)
	for _, message := range list {
		blocks, _ := get(message, "content").([]any)
		for _, block := range blocks {
			o, ok := block.(host.Obj)
			if !ok {
				continue
			}
			id, ok := o.Get("tool_use_id")
			if !ok {
				id = getd(o, "id", "")
			}
			if s, ok := id.(string); ok && strings.HasPrefix(s, claudePrefix) {
				return true
			}
		}
	}
	return false
}

// claudeEqual is Python == on JSON values: object key order is ignored and numbers (and
// booleans) compare by value.
func claudeEqual(a, b any) bool {
	switch x := a.(type) {
	case host.Obj:
		y, ok := b.(host.Obj)
		if !ok || len(x) != len(y) {
			return false
		}
		for _, kv := range x {
			if v, ok := y.Get(kv.Key); !ok || !claudeEqual(kv.Value, v) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		return ok && slices.EqualFunc(x, y, claudeEqual)
	case string:
		y, ok := b.(string)
		return ok && x == y
	case nil:
		return b == nil
	}
	xn, xok := claudeNumber(a)
	yn, yok := claudeNumber(b)
	if !xok || !yok {
		return false
	}
	if isInt(xn) && isInt(yn) {
		return xn == yn
	}
	return number(xn) == number(yn)
}

func claudeNumber(v any) (json.Number, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return "1", true
		}
		return "0", true
	case json.Number:
		return x, true
	case int:
		return json.Number(strconv.Itoa(x)), true
	case int64:
		return json.Number(strconv.FormatInt(x, 10)), true
	}
	return "", false
}

func claudePrefixEqual(messages, history []any) bool {
	return len(messages) >= len(history) && claudeEqual(messages[:len(history)], history)
}

var (
	claudeModelPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@\[\]-]{0,199}$`)
	claudeToolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

var claudeRequestKeys = set("model", "messages", "system", "tools", "tool_choice", "max_tokens", "stream", "metadata")

// claudePrepare validates body and returns an independent copy plus the settings text runs
// are matched by (prepare).
func claudePrepare(body host.Obj) (result host.Obj, settings string, err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(*ClaudeError); ok {
				err = e
				return
			}
			if _, ok := r.(runtime.Error); !ok {
				panic(r)
			}
			err = claudeErr("unsupported_or_invalid_request", 400)
		}
	}()
	text, ok := claudeJSON(body)
	if !ok {
		return nil, "", claudeErr("unsupported_or_invalid_request", 400)
	}
	if len(text) > claudeMaxInput {
		return nil, "", claudeErr("input_too_large", 413)
	}
	// Reuse the public Messages schema and tool-result checks.
	if _, err := MessagesToChat(body); err != nil {
		return nil, "", claudeErr("unsupported_or_invalid_request", 400)
	}
	unsupported := claudeErr("unsupported_request", 400)
	for _, kv := range body {
		if !claudeRequestKeys[kv.Key] {
			return nil, "", unsupported
		}
	}
	choice := get(body, "tool_choice")
	if !truthy(choice) {
		choice = host.Obj{{Key: "type", Value: "auto"}}
	}
	if kind := get(choice, "type"); (kind != "auto" && kind != "none") || truthy(get(choice, "disable_parallel_tool_use")) {
		return nil, "", unsupported
	}
	if tools, _ := get(body, "tools").([]any); len(tools) > 128 {
		return nil, "", unsupported
	}
	// The model and tool names become CLI argument values (no shell); keep them to plain
	// identifiers so none can read as a flag or widen the comma-joined --allowedTools list.
	if model, _ := get(body, "model").(string); !claudeModelPattern.MatchString(model) {
		return nil, "", claudeErr("unsupported_or_invalid_request", 400)
	}
	if tools, _ := get(body, "tools").([]any); tools != nil {
		for _, tool := range tools {
			if name, _ := get(tool, "name").(string); !claudeToolNamePattern.MatchString(name) {
				return nil, "", claudeErr("unsupported_or_invalid_request", 400)
			}
		}
	}
	result = clone(body).(host.Obj)
	result.Set("messages", claudeHistory(get(result, "messages")))
	messages := get(result, "messages").([]any)
	if get(messages[len(messages)-1], "role") != "user" {
		return nil, "", unsupported
	}
	for _, message := range messages {
		blocks := get(message, "content").([]any)
		for i, block := range blocks {
			b := block.(host.Obj)
			b.Del("cache_control")
			blocks[i] = b // Del shrinks the slice header it was called on
			if get(b, "type") == "image" && get(get(b, "source"), "type") != "base64" {
				return nil, "", unsupported
			}
		}
	}
	if get(choice, "type") == "none" {
		result.Set("tools", []any{})
	}
	view := host.Obj{}
	for _, kv := range result {
		if kv.Key != "messages" && kv.Key != "stream" && kv.Key != "metadata" {
			view = append(view, kv)
		}
	}
	return result, claudeMustJSON(view), nil
}

// claudeProfile selects the host login for model (profile). A variable so tests can fake it.
var claudeProfile = func(model string) (profile map[string]any, executable string, err error) {
	executable = host.Bin("ARKISM_CLAUDE_BIN", "claude")
	var reason string
	func() {
		defer func() {
			if recover() != nil {
				profile, reason = nil, "login_unavailable"
			}
		}()
		profile, reason = subscription.Choose("claude", model, executable, host.Home, host.AgentsState, nil)
	}()
	if profile == nil {
		return nil, "", claudeErr(reason, 503)
	}
	return profile, executable, nil
}

// claudeEnvironment is the child CLI's environment for the selected login (environment).
var claudeEnvironment = func(selected map[string]any, maxTokens string) (map[string]string, error) {
	row, _ := host.CLIByName("claude")
	base, err := host.ChildEnv(row, nil, "", nil)
	if err != nil {
		return nil, err
	}
	env := subscription.ChildEnvironment(base, selected)
	// A gateway request must not inherit another provider, capture context or SDK session.
	for name := range env {
		upper := strings.ToUpper(name)
		if strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_CODE_") ||
			strings.HasPrefix(name, "ARKISM_") || strings.HasPrefix(name, "OPENAI_") || strings.HasPrefix(name, "CODEX_") ||
			strings.HasPrefix(name, "GEMINI_") || strings.HasPrefix(name, "GOOGLE_") || strings.HasPrefix(name, "OTEL_") ||
			strings.HasPrefix(name, "LANGCHAIN_") || strings.HasPrefix(name, "LANGSMITH_") ||
			name == "CLAUDECODE" || name == "CLAUDE_SECURESTORAGE_CONFIG_DIR" ||
			strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET") ||
			strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "API_KEY") {
			delete(env, name)
		}
	}
	for k, v := range map[string]string{"ARKISM_CAPTURE_CHILD": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		"CLAUDE_CODE_MAX_OUTPUT_TOKENS": maxTokens, "MCP_TOOL_TIMEOUT": "900000",
		"ENABLE_CLAUDEAI_MCP_SERVERS": "false", "ENABLE_TOOL_SEARCH": "false",
		"MAX_MCP_OUTPUT_TOKENS": strconv.Itoa(claudeMaxInput), "CLAUDE_CODE_MAX_RETRIES": "0",
		"CLAUDE_CODE_NONSTREAMING_TIMEOUT_RETRIES": "0", "CLAUDE_CODE_DISABLE_NONSTREAMING_FALLBACK": "1"} {
		env[k] = v
	}
	return env, nil
}

// claudeArgs is the CLI command line for model with the given tool names.
func claudeArgs(executable, model, systemFile string, tools host.Obj) []string {
	args := []string{executable, "-p", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--include-partial-messages", "--model", model,
		"--tools", "", "--strict-mcp-config", "--mcp-config",
		claudeMustJSON(host.Obj{{Key: "mcpServers", Value: host.Obj{{Key: "arkism", Value: host.Obj{
			{Key: "type", Value: "sdk"}, {Key: "name", Value: "arkism"}}}}}}),
		"--setting-sources", "", "--settings", `{"disableAllHooks":true,"autoMemoryEnabled":false}`,
		"--no-session-persistence", "--disable-slash-commands", "--system-prompt-file", systemFile,
		"--permission-prompt-tool", "stdio"}
	if len(tools) > 0 {
		names := make([]string, len(tools))
		for i, kv := range tools {
			names[i] = claudeMCPPrefix + kv.Key
		}
		args = append(args, "--allowedTools", strings.Join(names, ","))
	}
	return args
}

// claudePrompt is the first user turn. A CLI accepts user turns, not an API transcript, so
// prior roles are preserved as data.
func claudePrompt(messages []any) any {
	if len(messages) == 1 && get(messages[0], "role") == "user" {
		return get(messages[0], "content")
	}
	blocks := []any{}
	for _, message := range messages {
		blocks = append(blocks, host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: "Conversation role: " + get(message, "role").(string)}})
		for _, block := range get(message, "content").([]any) {
			if kind := get(block, "type"); kind == "text" || kind == "image" {
				blocks = append(blocks, block)
			} else {
				blocks = append(blocks, host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: claudeMustJSON(block)}})
			}
		}
	}
	return blocks
}

func claudeToolResult(block host.Obj) host.Obj {
	content := getd(block, "content", "")
	if s, ok := content.(string); ok {
		content = []any{host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: s}}}
	}
	return host.Obj{{Key: "content", Value: content}, {Key: "isError", Value: getd(block, "is_error", false)}}
}

type claudeCall struct {
	source, public, name string
	index                any
	arguments            string
}

type claudeEarly struct {
	call   *claudeCall
	result host.Obj
}

// claudeStart anchors the monotonic clock run deadlines are measured on.
var claudeStart = time.Now()

func claudeClock() int64 { return int64(time.Since(claudeStart)) }

type claudeRun struct {
	owner, settings, model string
	selected               map[string]any
	tools                  host.Obj // name -> tool, in request order
	tag                    string

	// Guarded by the bridge lock.
	inputHistory, history []any // history nil until the first reply
	busy                  bool
	timer                 *time.Timer
	epoch                 int

	// Owned by the request currently holding the run (busy).
	pending  host.Obj // CLI tool-use id -> its waiting tools/call frame
	calls    []*claudeCall
	early    map[string]claudeEarly
	nextCall int

	deadline  atomic.Int64 // claudeClock() nanoseconds
	closed    chan struct{}
	closeOnce sync.Once
	events    chan any // host.Obj frames, or a *ClaudeError
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	exited    chan struct{} // reader finished and the process was reaped
	tmp       string
	lock      *sync.Mutex // the bridge lock
}

func claudeNewRun(lock *sync.Mutex, owner, settings string, body host.Obj, selected map[string]any, executable string) (r *claudeRun, err error) {
	r = &claudeRun{owner: owner, settings: settings, model: get(body, "model").(string), selected: selected,
		inputHistory: get(body, "messages").([]any), busy: true, pending: host.Obj{}, early: map[string]claudeEarly{},
		tag: newHex(), closed: make(chan struct{}), events: make(chan any, 32), exited: make(chan struct{}), lock: lock}
	r.deadline.Store(claudeClock() + int64(claudeDeadline))
	defer func() {
		if p := recover(); p != nil {
			err = errors.New("claude: start failed")
		}
		if err != nil {
			r.close()
			r, err = nil, claudeErr("cli_unavailable", 503)
		}
	}()
	tools, _ := getd(body, "tools", []any{}).([]any)
	for _, tool := range tools {
		r.tools.Set(get(tool, "name").(string), tool)
	}
	if r.tmp, err = os.MkdirTemp("", "arkism-claude-"); err != nil {
		return
	}
	path := filepath.Join(r.tmp, "system.txt")
	system := getd(body, "system", "")
	if parts, ok := system.([]any); ok {
		texts := make([]string, len(parts))
		for i, part := range parts {
			texts[i] = get(part, "text").(string)
		}
		system = strings.Join(texts, "\n\n")
	}
	if err = os.WriteFile(path, []byte(system.(string)), 0o600); err != nil {
		return
	}
	maxTokens, _ := host.Int(get(body, "max_tokens"))
	env, err := claudeEnvironment(selected, strconv.FormatInt(maxTokens, 10))
	if err != nil {
		return
	}
	env["CLAUDE_CODE_TMPDIR"] = r.tmp
	args := claudeArgs(executable, r.model, path, r.tools)
	r.cmd = exec.Command(args[0], args[1:]...)
	r.cmd.Dir = r.tmp
	for k, v := range env {
		r.cmd.Env = append(r.cmd.Env, k+"="+v)
	}
	claudeDetach(r.cmd)
	if r.stdin, err = r.cmd.StdinPipe(); err != nil {
		return
	}
	if r.stdout, err = r.cmd.StdoutPipe(); err != nil {
		return
	}
	if err = r.cmd.Start(); err != nil {
		r.cmd = nil
		return
	}
	go r.readLoop()
	return
}

func (r *claudeRun) put(v any) {
	select {
	case r.events <- v:
	case <-r.closed:
	}
}

// readLine is readline(claudeMaxInput + 1).
func (r *claudeRun) readLine(in *bufio.Reader) ([]byte, error) {
	var line []byte
	for len(line) <= claudeMaxInput {
		chunk, err := in.ReadSlice('\n')
		if room := claudeMaxInput + 1 - len(line); len(chunk) > room {
			// Leave the rest unread: the line is already too long.
			return append(line, chunk[:room]...), nil
		}
		line = append(line, chunk...)
		if err != bufio.ErrBufferFull {
			return line, err
		}
	}
	return line, nil
}

func (r *claudeRun) readLoop() {
	defer close(r.exited)
	defer r.cmd.Wait()
	defer r.put(claudeErr("upstream_truncated", 502))
	in := bufio.NewReaderSize(r.stdout, 64<<10)
	total := 0
	for {
		select {
		case <-r.closed:
			return
		default:
		}
		line, err := r.readLine(in)
		if len(line) == 0 {
			if err != nil && err != io.EOF {
				r.put(claudeErr("upstream_protocol", 502))
			}
			return
		}
		total += len(line)
		if line[len(line)-1] != '\n' || total > claudeMaxOutput {
			r.put(claudeErr("upstream_protocol", 502))
			return
		}
		value, err := Loads(string(line), "invalid_response")
		frame, ok := value.(host.Obj)
		if err != nil || !ok {
			r.put(claudeErr("upstream_protocol", 502))
			return
		}
		r.put(frame)
	}
}

func (r *claudeRun) reaped() bool {
	select {
	case <-r.exited:
		return true
	default:
		return false
	}
}

func (r *claudeRun) isClosed() bool {
	select {
	case <-r.closed:
		return true
	default:
		return false
	}
}

func (r *claudeRun) send(value any) {
	text, ok := claudeJSON(value)
	if !ok {
		claudeFail("upstream_interrupted", 502)
	}
	wire := text + "\n"
	if len(wire) > claudeMaxInput {
		claudeFail("input_too_large", 413)
	}
	if _, err := io.WriteString(r.stdin, wire); err != nil {
		claudeFail("upstream_interrupted", 502)
	}
}

func (r *claudeRun) read(cancelled func() bool) host.Obj {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if cancelled() {
			claudeFail("client_disconnected", 499)
		}
		if r.isClosed() || claudeClock() >= r.deadline.Load() {
			claudeFail("deadline_exceeded", 504)
		}
		select {
		case item := <-r.events:
			if e, ok := item.(*ClaudeError); ok {
				panic(claudeErr(e.Code, e.Status))
			}
			return item.(host.Obj)
		case <-tick.C:
		}
	}
}

func (r *claudeRun) controlReply(frame host.Obj, result any) {
	message := get(get(frame, "request"), "message").(host.Obj)
	rpc := host.Obj{{Key: "jsonrpc", Value: "2.0"}, {Key: "result", Value: result}}
	if id, ok := message.Get("id"); ok {
		rpc.Set("id", id)
	}
	r.send(host.Obj{{Key: "type", Value: "control_response"}, {Key: "response", Value: host.Obj{
		{Key: "subtype", Value: "success"}, {Key: "request_id", Value: get(frame, "request_id")},
		{Key: "response", Value: host.Obj{{Key: "mcp_response", Value: rpc}}}}}})
}

func (r *claudeRun) call(source string) *claudeCall {
	for _, c := range r.calls {
		if c.source == source {
			return c
		}
	}
	return nil
}

// claudeSameArguments reports whether a tools/call carries exactly the streamed arguments.
func claudeSameArguments(params host.Obj, call *claudeCall) bool {
	text := call.arguments
	if text == "" {
		text = "{}"
	}
	streamed, err := Loads(text, "invalid_response")
	if err != nil {
		claudeProtocol()
	}
	return get(params, "name") == call.name && claudeMustJSON(get(params, "arguments")) == claudeMustJSON(streamed)
}

func (r *claudeRun) control(frame host.Obj) {
	request, ok := get(frame, "request").(host.Obj)
	if _, isStr := get(frame, "request_id").(string); !ok || !isStr {
		claudeProtocol()
	}
	if get(request, "subtype") == "can_use_tool" {
		// Built-ins or unexpected permissions never execute on the gateway host.
		r.send(host.Obj{{Key: "type", Value: "control_response"}, {Key: "response", Value: host.Obj{
			{Key: "subtype", Value: "success"}, {Key: "request_id", Value: get(frame, "request_id")},
			{Key: "response", Value: host.Obj{{Key: "behavior", Value: "deny"}, {Key: "message", Value: "tool_unavailable"}}}}}})
		return
	}
	if get(request, "subtype") != "mcp_message" || get(request, "server_name") != "arkism" {
		claudeProtocol()
	}
	rpc, ok := get(request, "message").(host.Obj)
	if !ok || get(rpc, "jsonrpc") != "2.0" {
		claudeProtocol()
	}
	var result any
	switch get(rpc, "method") {
	case "initialize":
		result = host.Obj{{Key: "protocolVersion", Value: "2024-11-05"}, {Key: "capabilities", Value: host.Obj{{Key: "tools", Value: host.Obj{}}}},
			{Key: "serverInfo", Value: host.Obj{{Key: "name", Value: "arkism"}, {Key: "version", Value: "1"}}}}
	case "tools/list":
		tools := []any{}
		for _, kv := range r.tools {
			tools = append(tools, host.Obj{{Key: "name", Value: kv.Key}, {Key: "description", Value: getd(kv.Value.(host.Obj), "description", "")},
				{Key: "inputSchema", Value: get(kv.Value, "input_schema")}})
		}
		result = host.Obj{{Key: "tools", Value: tools}}
	case "notifications/initialized", "ping":
		result = host.Obj{}
	case "tools/call":
		params, ok := get(rpc, "params").(host.Obj)
		if !ok {
			claudeProtocol()
		}
		meta, ok := get(params, "_meta").(host.Obj)
		if !ok {
			claudeProtocol()
		}
		identity, ok := get(meta, "claudecode/toolUseId").(string)
		name, _ := get(params, "name").(string)
		_, waiting := r.pending.Get(identity)
		_, known := r.tools.Get(name)
		if _, isObj := get(params, "arguments").(host.Obj); !ok || waiting || !known || !isObj {
			claudeProtocol()
		}
		if early, ok := r.early[identity]; ok {
			delete(r.early, identity)
			if !claudeSameArguments(params, early.call) {
				claudeProtocol()
			}
			r.controlReply(frame, early.result)
		} else {
			r.pending.Set(identity, frame)
		}
		return
	default:
		claudeProtocol()
	}
	r.controlReply(frame, result)
}

func (r *claudeRun) initialize(body host.Obj, cancelled func() bool) {
	r.send(host.Obj{{Key: "type", Value: "control_request"}, {Key: "request_id", Value: "arkism-init"},
		{Key: "request", Value: host.Obj{{Key: "subtype", Value: "initialize"}, {Key: "hooks", Value: nil}, {Key: "skills", Value: []any{}}}}})
	for done := false; !done; {
		frame := r.read(cancelled)
		switch get(frame, "type") {
		case "control_request":
			r.control(frame)
		case "control_response":
			response, ok := getd(frame, "response", host.Obj{}).(host.Obj)
			if !ok || get(response, "request_id") != "arkism-init" || get(response, "subtype") != "success" {
				claudeProtocol()
			}
			done = true
		case "system":
		default:
			claudeProtocol()
		}
	}
	r.send(host.Obj{{Key: "type", Value: "user"}, {Key: "client_composed", Value: true},
		{Key: "message", Value: host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: claudePrompt(get(body, "messages").([]any))}}}})
}

// continuation checks body adds exactly one user turn answering every waiting call.
func (r *claudeRun) continuation(body host.Obj) (host.Obj, []host.Obj) {
	mismatch := claudeErr("conversation_mismatch", 409)
	messages := get(body, "messages").([]any)
	suffix := messages[min(len(r.history), len(messages)):]
	if len(suffix) != 1 || get(suffix[0], "role") != "user" {
		panic(mismatch)
	}
	blocks := get(suffix[0], "content").([]any)
	var results []host.Obj
	for _, b := range blocks {
		if get(b, "type") == "tool_result" {
			results = append(results, b.(host.Obj))
		}
	}
	if len(r.calls) > 0 {
		if len(results) != len(blocks) || len(results) != len(r.calls) {
			panic(mismatch)
		}
		seen := map[string]bool{}
		for _, b := range results {
			id, _ := get(b, "tool_use_id").(string)
			seen[id] = true
		}
		for _, c := range r.calls {
			if !seen[c.public] {
				panic(mismatch)
			}
		}
		if len(seen) != len(r.calls) {
			panic(mismatch)
		}
	} else if len(results) > 0 {
		panic(mismatch)
	}
	return suffix[0].(host.Obj), results
}

func (r *claudeRun) resume(body host.Obj) {
	message, results := r.continuation(body)
	if len(r.calls) > 0 {
		r.early = map[string]claudeEarly{}
		for _, b := range results {
			for _, c := range r.calls {
				if c.public == get(b, "tool_use_id") {
					r.early[c.source] = claudeEarly{c, claudeToolResult(b)}
				}
			}
		}
		pending := r.pending
		r.pending, r.calls = host.Obj{}, nil
		for _, kv := range pending {
			r.control(kv.Value.(host.Obj))
		}
	} else {
		r.send(host.Obj{{Key: "type", Value: "user"}, {Key: "client_composed", Value: true}, {Key: "message", Value: message}})
	}
	r.lock.Lock()
	r.inputHistory = get(body, "messages").([]any)
	r.lock.Unlock()
}

// source turns CLI frames into Messages SSE events, holding completion until every tool wait
// or the CLI result is confirmed. Failures panic with *ClaudeError.
func (r *claudeRun) source(cancelled func() bool) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		stopped, terminal := false, false
		var reason any
		for {
			if stopped && reason == "tool_use" && len(r.calls) > 0 {
				if len(r.early) > 0 {
					claudeProtocol()
				}
				for _, kv := range r.pending {
					c := r.call(kv.Key)
					if c == nil {
						claudeProtocol()
					}
					params := get(get(get(kv.Value, "request"), "message"), "params").(host.Obj)
					if !claudeSameArguments(params, c) {
						claudeProtocol()
					}
				}
				break
			}
			if stopped && terminal {
				if len(r.pending) > 0 || len(r.calls) > 0 || len(r.early) > 0 {
					claudeProtocol()
				}
				break
			}
			frame := r.read(cancelled)
			switch kind := get(frame, "type"); kind {
			case "control_request":
				r.control(frame)
			case "control_cancel_request":
				claudeFail("upstream_interrupted", 502)
			case "result":
				if isErr, ok := get(frame, "is_error").(bool); !ok || isErr || get(frame, "subtype") != "success" {
					claudeFail("upstream_error", 502)
				}
				if !stopped {
					claudeProtocol()
				}
				terminal = true
			case "stream_event":
				event, ok := get(frame, "event").(host.Obj)
				if stopped || get(frame, "parent_tool_use_id") != nil || !ok {
					claudeProtocol()
				}
				event = clone(event).(host.Obj)
				eventType := get(event, "type")
				part := func(key string) host.Obj {
					o, ok := getd(event, key, host.Obj{}).(host.Obj)
					if !ok {
						claudeProtocol()
					}
					return o
				}
				switch {
				case eventType == "content_block_start" && get(part("content_block"), "type") == "tool_use":
					block := part("content_block")
					sourceID, ok := get(block, "id").(string)
					fullName, isStr := get(block, "name").(string)
					if !ok || r.call(sourceID) != nil || !isStr || !strings.HasPrefix(fullName, claudeMCPPrefix) {
						claudeProtocol()
					}
					name := strings.TrimPrefix(fullName, claudeMCPPrefix)
					if _, ok := r.tools.Get(name); !ok {
						claudeProtocol()
					}
					public := claudePrefix + r.tag + "_" + strconv.Itoa(r.nextCall)
					r.nextCall++
					arguments := ""
					if input := get(block, "input"); truthy(input) {
						arguments = claudeMustJSON(input)
					}
					r.calls = append(r.calls, &claudeCall{source: sourceID, public: public, name: name,
						index: get(event, "index"), arguments: arguments})
					block.Set("id", public)
					block.Set("name", name)
				case eventType == "content_block_delta" && get(part("delta"), "type") == "input_json_delta":
					delta := part("delta")
					var matching []*claudeCall
					for _, c := range r.calls {
						if claudeEqual(c.index, get(event, "index")) {
							matching = append(matching, c)
						}
					}
					partial, ok := get(delta, "partial_json").(string)
					if len(matching) != 1 || !ok {
						claudeProtocol()
					}
					matching[0].arguments += partial
				case eventType == "message_delta":
					if value := get(part("delta"), "stop_reason"); truthy(value) {
						reason = value
					}
				case eventType == "message_stop":
					stopped = true
					continue
				}
				kindName, ok := eventType.(string)
				if !ok {
					claudeProtocol()
				}
				var fields []host.KV
				for _, kv := range event {
					if kv.Key != "type" {
						fields = append(fields, kv)
					}
				}
				if !yield(messagesSSE(kindName, fields...), nil) {
					return
				}
			case "system", "assistant", "user", "rate_limit_event", "tool_progress", "tool_use_summary":
			default:
				claudeProtocol()
			}
		}
		yield(messagesSSE("message_stop"), nil)
	}
}

// claudeErrStopped is the emit result when the consumer stopped pulling.
var claudeErrStopped = errors.New("claude: consumer stopped")

// generate translates the run's turn into Messages SSE frames, or one JSON body when the
// request is not streaming. emit returning false means the consumer stopped.
func (r *claudeRun) generate(body host.Obj, cancelled func() bool, emit func([]byte) bool) bool {
	var text []string
	type chatCall struct {
		id, name  any
		arguments string
	}
	var calls []*chatCall
	callIndex := map[string]*chatCall{}
	var usage, identity, finish any
	streaming := truthy(get(body, "stream"))
	started, textOpen := false, false
	err := CodecStream(r.source(cancelled), r.model, true, func(chunk []byte) error {
		if string(chunk) == "data: [DONE]\n\n" {
			return nil
		}
		value := loads(string(chunk[6:]), "invalid_response")
		identity = get(value, "id")
		if streaming && !started {
			started = true
			if !emit(messagesSSE("message_start", host.KV{Key: "message", Value: messagesMessage(identity, r.model, []any{}, nil,
				host.Obj{{Key: "input_tokens", Value: 0}, {Key: "output_tokens", Value: 0}})})) {
				return claudeErrStopped
			}
		}
		choices := get(value, "choices").([]any)
		if len(choices) == 0 {
			usage = get(value, "usage")
			return nil
		}
		delta := get(choices[0], "delta").(host.Obj)
		if content := get(delta, "content"); truthy(content) {
			text = append(text, content.(string))
			if streaming {
				if !textOpen {
					textOpen = true
					if !emit(messagesSSE("content_block_start", host.KV{Key: "index", Value: 0},
						host.KV{Key: "content_block", Value: host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: ""}}})) {
						return claudeErrStopped
					}
				}
				if !emit(messagesSSE("content_block_delta", host.KV{Key: "index", Value: 0},
					host.KV{Key: "delta", Value: host.Obj{{Key: "type", Value: "text_delta"}, {Key: "text", Value: content}}})) {
					return claudeErrStopped
				}
			}
		}
		toolCalls, _ := getd(delta, "tool_calls", []any{}).([]any)
		for _, call := range toolCalls {
			index := claudeMustJSON(get(call, "index"))
			function := get(call, "function").(host.Obj)
			c, ok := callIndex[index]
			if !ok {
				c = &chatCall{id: get(call, "id"), name: get(function, "name")}
				callIndex[index] = c
				calls = append(calls, c)
			}
			c.arguments += getd(function, "arguments", "").(string)
		}
		if reason := get(choices[0], "finish_reason"); truthy(reason) {
			finish = reason
		}
		return nil
	})
	if err == claudeErrStopped {
		return false
	}
	if err != nil {
		claudeProtocol()
	}
	var content any
	if len(text) > 0 {
		content = strings.Join(text, "")
	}
	toolCalls := []any{}
	for _, c := range calls {
		toolCalls = append(toolCalls, host.Obj{{Key: "id", Value: c.id}, {Key: "type", Value: "function"},
			{Key: "function", Value: host.Obj{{Key: "name", Value: c.name}, {Key: "arguments", Value: c.arguments}}}})
	}
	result, err := MessagesResponse(host.Obj{{Key: "id", Value: identity}, {Key: "object", Value: "chat.completion"},
		{Key: "created", Value: time.Now().Unix()}, {Key: "model", Value: r.model},
		{Key: "choices", Value: []any{host.Obj{{Key: "index", Value: 0}, {Key: "finish_reason", Value: finish},
			{Key: "message", Value: host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: content},
				{Key: "tool_calls", Value: toolCalls}}}}}},
		{Key: "usage", Value: usage}}, r.model)
	if err != nil {
		claudeProtocol()
	}
	history := append(slices.Clone(get(body, "messages").([]any)),
		host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: get(result, "content")}})
	r.lock.Lock()
	r.history = history
	r.lock.Unlock()
	if !streaming {
		return emit([]byte(claudeMustJSON(result)))
	}
	if textOpen && !emit(messagesSSE("content_block_stop", host.KV{Key: "index", Value: 0})) {
		return false
	}
	for i, c := range calls {
		index := i
		if textOpen {
			index++
		}
		if !emit(messagesSSE("content_block_start", host.KV{Key: "index", Value: index}, host.KV{Key: "content_block", Value: host.Obj{
			{Key: "type", Value: "tool_use"}, {Key: "id", Value: c.id}, {Key: "name", Value: c.name}, {Key: "input", Value: host.Obj{}}}})) ||
			!emit(messagesSSE("content_block_delta", host.KV{Key: "index", Value: index}, host.KV{Key: "delta", Value: host.Obj{
				{Key: "type", Value: "input_json_delta"}, {Key: "partial_json", Value: c.arguments}}})) ||
			!emit(messagesSSE("content_block_stop", host.KV{Key: "index", Value: index})) {
			return false
		}
	}
	return emit(messagesSSE("message_delta", host.KV{Key: "delta", Value: host.Obj{{Key: "stop_reason", Value: get(result, "stop_reason")},
		{Key: "stop_sequence", Value: nil}}}, host.KV{Key: "usage", Value: get(result, "usage")})) &&
		emit(messagesSSE("message_stop"))
}

// close kills the CLI's whole process group and removes its folder; it runs once.
func (r *claudeRun) close() {
	r.closeOnce.Do(func() {
		close(r.closed)
		if r.cmd != nil && r.cmd.Process != nil {
			// Windows reuses a reaped pid, so only an unreaped tree is killed there.
			if runtime.GOOS != "windows" || !r.reaped() {
				if claudeKill(r.cmd.Process) != nil {
					r.cmd.Process.Kill()
				}
			}
			select {
			case <-r.exited:
			case <-time.After(5 * time.Second):
			}
		}
		if r.stdin != nil {
			r.stdin.Close()
		}
		if r.stdout != nil {
			r.stdout.Close()
		}
		if r.tmp != "" {
			os.RemoveAll(r.tmp)
		}
	})
}

type claudeBridge struct {
	mu   sync.Mutex
	runs []*claudeRun
}

// claudeRuns is the process-wide Bridge (bridge). The gateway calls close at exit.
var claudeRuns = &claudeBridge{}

// detach removes run and claims it so an expiry cannot resume it; the caller holds b.mu and
// closes the run after unlocking. epoch < 0 means any.
func (b *claudeBridge) detach(run *claudeRun, epoch int) bool {
	if epoch >= 0 && epoch != run.epoch {
		return false
	}
	b.runs = slices.DeleteFunc(b.runs, func(x *claudeRun) bool { return x == run })
	run.busy = true
	if run.timer != nil {
		run.timer.Stop()
	}
	return true
}

func (b *claudeBridge) discard(run *claudeRun, epoch int) {
	b.mu.Lock()
	ok := b.detach(run, epoch)
	b.mu.Unlock()
	if ok {
		run.close()
	}
}

// arm restarts run's expiry; the caller holds b.mu.
func (b *claudeBridge) arm(run *claudeRun, after time.Duration) {
	if run.timer != nil {
		run.timer.Stop()
	}
	run.epoch++
	epoch := run.epoch
	run.timer = time.AfterFunc(after, func() { b.discard(run, epoch) })
}

// continuationModel is the model of the one live run body continues, "" when none.
func (b *claudeBridge) continuationModel(body host.Obj, owner string) (model string) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(runtime.Error); !ok {
				panic(r)
			}
			model = ""
		}
	}()
	messages := claudeHistory(get(body, "messages"))
	b.mu.Lock()
	defer b.mu.Unlock()
	var matches []*claudeRun
	for _, run := range b.runs {
		if run.owner != owner || run.history == nil || !claudePrefixEqual(messages, run.history) {
			continue
		}
		changed := clone(body).(host.Obj)
		changed.Set("model", run.model)
		if _, settings, err := claudePrepare(changed); err == nil && settings == run.settings {
			matches = append(matches, run)
		}
	}
	if len(matches) == 1 {
		return matches[0].model
	}
	return ""
}

// close kills every live run (atexit).
func (b *claudeBridge) close() {
	b.mu.Lock()
	runs := b.runs
	b.runs = nil
	for _, run := range runs {
		if run.timer != nil {
			run.timer.Stop()
		}
	}
	b.mu.Unlock()
	for _, run := range runs {
		run.close()
	}
}

// abortRequest discards runs waiting on exactly this request (abort_request).
func (b *claudeBridge) abortRequest(body host.Obj, owner string) {
	prepared, settings, err := claudePrepare(body)
	if err != nil {
		return
	}
	messages := get(prepared, "messages").([]any)
	b.mu.Lock()
	var matches []*claudeRun
	for _, run := range slices.Clone(b.runs) {
		if run.owner == owner && run.settings == settings && claudeEqual(run.inputHistory, messages) && b.detach(run, -1) {
			matches = append(matches, run)
		}
	}
	b.mu.Unlock()
	for _, run := range matches {
		run.close()
	}
}

// acquire claims the run body continues, or starts one; errors here mean nothing started.
func (b *claudeBridge) acquire(body host.Obj, settings, owner string) (*claudeRun, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	messages := get(body, "messages").([]any)
	var candidates []*claudeRun
	for _, r := range b.runs {
		if r.owner == owner && r.settings == settings && r.history != nil && claudePrefixEqual(messages, r.history) {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) > 1 {
		return nil, false, claudeErr("conversation_mismatch", 409)
	}
	if len(candidates) == 1 {
		run := candidates[0]
		if run.busy || run.isClosed() {
			return nil, false, claudeErr("conversation_busy", 409)
		}
		if err := claudeCatch(func() { run.continuation(body) }); err != nil {
			return nil, false, err
		}
		run.busy = true
		run.deadline.Store(claudeClock() + int64(claudeDeadline))
		b.arm(run, claudeDeadline)
		return run, false, nil
	}
	if claudeOwnedCalls(messages) {
		return nil, false, claudeErr("conversation_mismatch", 409)
	}
	for _, r := range b.runs {
		if r.owner == owner && r.settings == settings && claudeEqual(r.inputHistory, messages) && (r.busy || len(r.pending) > 0) {
			return nil, false, claudeErr("conversation_busy", 409)
		}
	}
	if len(b.runs) >= claudeMaxRuns {
		return nil, false, claudeErr("claude_capacity", 503)
	}
	selected, executable, err := claudeProfile(get(body, "model").(string))
	if err != nil {
		var ce *ClaudeError
		if !errors.As(err, &ce) {
			ce = claudeErr("login_unavailable", 503)
		}
		return nil, false, ce
	}
	run, err := claudeNewRun(&b.mu, owner, settings, body, selected, executable)
	if err != nil {
		return nil, false, err
	}
	b.runs = append(b.runs, run)
	b.arm(run, claudeDeadline)
	return run, true, nil
}

// claudeConsumerPanic wraps a panic from the consumer's loop body on its way out.
type claudeConsumerPanic struct{ value any }

// claudeCatch runs f, returning the *ClaudeError it raised; any other failure is a protocol
// error, as Python mapped ValueError, KeyError and TypeError.
func claudeCatch(f func()) (err *ClaudeError) {
	defer func() {
		if r := recover(); r != nil {
			switch x := r.(type) {
			case *ClaudeError:
				err = x
			case Code, runtime.Error:
				err = claudeErr("upstream_protocol", 502)
			default:
				panic(r)
			}
		}
	}()
	f()
	return nil
}

// iterMessages yields Messages SSE frames or one JSON body; a failure ends the sequence with
// (nil, *ClaudeError). Stopping early releases or discards the run (iter_messages).
func (b *claudeBridge) iterMessages(body host.Obj, owner string, cancelled func() bool) iter.Seq2[[]byte, error] {
	if cancelled == nil {
		cancelled = func() bool { return false }
	}
	return func(yield func([]byte, error) bool) {
		prepared, settings, err := claudePrepare(body)
		if err != nil {
			yield(nil, err)
			return
		}
		run, fresh, err := b.acquire(prepared, settings, owner)
		if err != nil {
			yield(nil, err)
			return
		}
		completed := false
		defer func() {
			// A panic or runtime.Goexit (t.Fatal) in the consumer still releases the run.
			if !completed {
				b.discard(run, -1)
				if p := recover(); p != nil {
					if c, ok := p.(claudeConsumerPanic); ok {
						p = c.value
					}
					panic(p)
				}
			}
		}()
		success := false
		failure := claudeCatch(func() {
			if fresh {
				run.initialize(prepared, cancelled)
			} else {
				run.resume(prepared)
			}
			success = run.generate(prepared, cancelled, func(chunk []byte) (ok bool) {
				// A panic raised by the consumer is not the run's failure: carry it out unchanged.
				done := false
				defer func() {
					if !done {
						if p := recover(); p != nil {
							panic(claudeConsumerPanic{p})
						}
					}
				}()
				ok = yield(chunk, nil)
				done = true
				return ok
			})
		})
		completed = true
		if success {
			b.mu.Lock()
			run.busy = false
			b.arm(run, claudeIdle)
			b.mu.Unlock()
			return
		}
		b.discard(run, -1)
		if failure != nil {
			failure.Started = true
			yield(nil, failure)
		}
	}
}
