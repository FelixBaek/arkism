package gateway

import (
	"bufio"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The fake Claude CLI is this test binary re-run with CLAUDE_FAKE_CLI=1. It speaks just
// enough stream-json to drive a run: FAKE_MODE picks the behaviour, every received line is
// appended to FAKE_TRACE, and FAKE_ARGS (when set) records how it was launched.
func TestMain(m *testing.M) {
	if os.Getenv("CLAUDE_FAKE_CLI") == "1" {
		claudeFakeCLI()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func claudeFakeCLI() {
	mode, trace := os.Getenv("FAKE_MODE"), os.Getenv("FAKE_TRACE")
	if path := os.Getenv("FAKE_ARGS"); path != "" {
		cwd, _ := os.Getwd()
		system, _ := os.ReadFile(filepath.Join(cwd, "system.txt"))
		info, _ := os.Stat(filepath.Join(cwd, "system.txt"))
		raw, _ := json.Marshal(map[string]any{"args": os.Args[1:], "cwd": cwd, "system": string(system),
			"mode": fmt.Sprintf("%o", info.Mode().Perm()), "tmpdir": os.Getenv("CLAUDE_CODE_TMPDIR")})
		os.WriteFile(path, raw, 0o600)
	}
	send := func(v any) {
		raw, _ := json.Marshal(v)
		os.Stdout.Write(append(raw, '\n'))
	}
	type m = map[string]any
	event := func(v m) { send(m{"type": "stream_event", "event": v, "parent_tool_use_id": nil}) }
	mcp := func(source string, index int, args m) {
		send(m{"type": "control_request", "request_id": fmt.Sprint("call-", index), "request": m{"subtype": "mcp_message",
			"server_name": "arkism", "message": m{"jsonrpc": "2.0", "id": index, "method": "tools/call",
				"params": m{"name": "echo", "arguments": args, "_meta": m{"claudecode/toolUseId": source}}}}})
	}
	reply := func(tools bool) {
		event(m{"type": "message_start", "message": m{"type": "message", "role": "assistant", "id": "msg-test", "model": "claude-test",
			"content": []any{}, "usage": m{"input_tokens": 11, "output_tokens": 0, "cache_read_input_tokens": 4}}})
		event(m{"type": "content_block_start", "index": 0, "content_block": m{"type": "text", "text": ""}})
		event(m{"type": "content_block_delta", "index": 0, "delta": m{"type": "text_delta", "text": "안녕"}})
		if mode == "slow" {
			time.Sleep(3 * time.Second)
		}
		event(m{"type": "content_block_stop", "index": 0})
		if mode == "truncated" {
			os.Exit(0)
		}
		if tools {
			for i := range 2 {
				event(m{"type": "content_block_start", "index": i + 1, "content_block": m{"type": "tool_use",
					"id": fmt.Sprint("source", i), "name": "mcp__arkism__echo", "input": m{}}})
				event(m{"type": "content_block_delta", "index": i + 1, "delta": m{"type": "input_json_delta",
					"partial_json": fmt.Sprintf(`{"value": %d}`, i)}})
				event(m{"type": "content_block_stop", "index": i + 1})
			}
			value := 0
			if mode == "bad-mcp" {
				value = 999
			}
			mcp("source0", 0, m{"value": value})
		}
		reason := "end_turn"
		if tools {
			reason = "tool_use"
		}
		event(m{"type": "message_delta", "delta": m{"stop_reason": reason, "stop_sequence": nil}, "usage": m{"output_tokens": 5}})
		event(m{"type": "message_stop"})
		if !tools {
			send(m{"type": "result", "subtype": "success", "is_error": false, "usage": m{"input_tokens": 11, "output_tokens": 5}})
		}
	}
	turns := 0
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return
		}
		f, _ := os.OpenFile(trace, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		f.WriteString(line)
		f.Close()
		var v struct {
			Type      string
			RequestID string `json:"request_id"`
			Response  struct {
				RequestID string `json:"request_id"`
			}
		}
		json.Unmarshal([]byte(line), &v)
		switch v.Type {
		case "control_request":
			send(m{"type": "control_response", "response": m{"subtype": "success", "request_id": v.RequestID, "response": m{}}})
			if mode == "no-read" {
				time.Sleep(30 * time.Second)
				return
			}
		case "user":
			turns++
			switch mode {
			case "hang":
				time.Sleep(30 * time.Second)
			case "bad-json":
				fmt.Println("{bad")
				return
			case "error":
				send(m{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": "PRIVATE-SECRET"})
			default:
				reply((mode == "tools" || mode == "bad-mcp") && turns == 1)
			}
		case "control_response":
			if v.Response.RequestID == "call-0" {
				send(m{"type": "tool_progress", "heartbeat": true})
				mcp("source1", 1, m{"value": 1})
			} else {
				reply(false)
			}
		}
	}
}

type claudeHarness struct {
	t        *testing.T
	bridge   *claudeBridge
	mode     string
	trace    string
	args     string // FAKE_ARGS, "" = off
	selected map[string]any
	mu       sync.Mutex
	profiles int
}

// claudeSetVar replaces *p for the test, like mock.patch.object.
func claudeSetVar[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func claudeNewHarness(t *testing.T) *claudeHarness {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	h := &claudeHarness{t: t, bridge: &claudeBridge{}, mode: "tools", trace: filepath.Join(root, "trace.jsonl"),
		selected: map[string]any{"cli": "claude", "id": "fixture", "config_dir": root}}
	t.Cleanup(h.bridge.close)
	claudeSetVar(t, &claudeProfile, func(model string) (map[string]any, string, error) {
		h.mu.Lock()
		h.profiles++
		h.mu.Unlock()
		return h.selected, exe, nil
	})
	claudeSetVar(t, &claudeEnvironment, func(map[string]any, string) (map[string]string, error) {
		env := map[string]string{"PATH": os.Getenv("PATH"), "CLAUDE_FAKE_CLI": "1", "FAKE_MODE": h.mode, "FAKE_TRACE": h.trace}
		if h.args != "" {
			env["FAKE_ARGS"] = h.args
		}
		return env, nil
	})
	return h
}

func (h *claudeHarness) profileCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.profiles
}

func (h *claudeHarness) runs() []*claudeRun {
	h.bridge.mu.Lock()
	defer h.bridge.mu.Unlock()
	return slices.Clone(h.bridge.runs)
}

func claudeParse(t *testing.T, text string) host.Obj {
	t.Helper()
	v, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return v.(host.Obj)
}

func claudeRequest(t *testing.T) host.Obj {
	return claudeParse(t, `{"model": "claude-test", "max_tokens": 128, "system": "지시",
		"messages": [{"role": "user", "content": "질문"}], "tools": [{"name": "echo", "input_schema":
		{"type": "object", "properties": {"value": {"type": "integer"}}}}]}`)
}

func claudeWith(body host.Obj, key string, value any) host.Obj {
	out := clone(body).(host.Obj)
	out.Set(key, value)
	return out
}

// claudeFollow answers every tool call of reply, in reverse order, the first as an error.
func claudeFollow(body, reply host.Obj) host.Obj {
	var calls []any
	for _, b := range get(reply, "content").([]any) {
		if get(b, "type") == "tool_use" {
			calls = append(calls, b)
		}
	}
	slices.Reverse(calls)
	results := []any{}
	for i, call := range calls {
		results = append(results, host.Obj{{Key: "type", Value: "tool_result"}, {Key: "tool_use_id", Value: get(call, "id")},
			{Key: "content", Value: fmt.Sprint("result ", i)}, {Key: "is_error", Value: i == 0}})
	}
	messages := append(slices.Clone(get(body, "messages").([]any)),
		host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: get(reply, "content")}},
		host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: results}})
	return claudeWith(body, "messages", messages)
}

func (h *claudeHarness) call(body host.Obj, owner string, cancelled func() bool) (host.Obj, *ClaudeError) {
	var out []byte
	for chunk, err := range h.bridge.iterMessages(body, owner, cancelled) {
		if err != nil {
			return nil, err.(*ClaudeError)
		}
		out = append(out, chunk...)
	}
	return claudeParse(h.t, string(out)), nil
}

func (h *claudeHarness) result(body host.Obj, owner string) host.Obj {
	h.t.Helper()
	out, err := h.call(body, owner, nil)
	if err != nil {
		h.t.Fatalf("unexpected %s", err.Code)
	}
	return out
}

func (h *claudeHarness) fails(body host.Obj, owner string) *ClaudeError {
	h.t.Helper()
	_, err := h.call(body, owner, nil)
	if err == nil {
		h.t.Fatal("expected a ClaudeError")
	}
	return err
}

func (h *claudeHarness) records() []host.Obj {
	raw, _ := os.ReadFile(h.trace)
	var out []host.Obj
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			out = append(out, claudeParse(h.t, line))
		}
	}
	return out
}

func claudeDump(v any) string { return host.MustDumps(v, host.DumpOpts{Compact: true, NoASCII: true}) }

func claudeClosed(c chan struct{}, wait time.Duration) bool {
	select {
	case <-c:
		return true
	default:
	}
	select {
	case <-c:
		return true
	case <-time.After(wait):
		return false
	}
}

func TestClaudeParallelResultsAndLaterUserTurnKeepProcessAndUsage(t *testing.T) {
	h := claudeNewHarness(t)
	body := claudeRequest(t)
	first := h.result(body, "owner")
	run := h.runs()[0]
	if usage := get(first, "usage"); !claudeEqual(usage, claudeParse(t, `{"input_tokens":11,"output_tokens":5,"cache_read_input_tokens":4}`)) {
		t.Fatal(claudeDump(usage))
	}
	var inputs []any
	for _, b := range get(first, "content").([]any) {
		if get(b, "type") == "tool_use" {
			inputs = append(inputs, get(b, "input"))
		}
	}
	if got := claudeDump(inputs); got != `[{"value":0},{"value":1}]` {
		t.Fatal(got)
	}
	next := claudeFollow(body, first)
	second := h.result(next, "owner")
	if get(second, "stop_reason") != "end_turn" {
		t.Fatal(claudeDump(second))
	}
	third := claudeWith(body, "messages", append(slices.Clone(get(next, "messages").([]any)),
		host.Obj{{Key: "role", Value: "assistant"}, {Key: "content", Value: get(second, "content")}},
		host.Obj{{Key: "role", Value: "user"}, {Key: "content", Value: "후속"}}))
	if got := get(get(h.result(third, "owner"), "content").([]any)[0], "text"); got != "안녕" {
		t.Fatal(got)
	}
	if h.runs()[0] != run || h.profileCalls() != 1 {
		t.Fatal("the run was replaced")
	}
	var delivered []any
	for _, r := range h.records() {
		if get(r, "type") == "control_response" {
			delivered = append(delivered, get(r, "response"))
		}
	}
	if len(delivered) != 2 || get(delivered[0], "request_id") != "call-0" || get(delivered[1], "request_id") != "call-1" {
		t.Fatal(claudeDump(delivered))
	}
	result := func(i int) any { return get(get(get(delivered[i], "response"), "mcp_response"), "result") }
	if text := get(get(result(0), "content").([]any)[0], "text"); text != "result 1" {
		t.Fatal(text)
	}
	if get(result(1), "isError") != true {
		t.Fatal(claudeDump(result(1)))
	}
}

func TestClaudeMismatchesDoNotGenerateOrDestroyWaitingRun(t *testing.T) {
	h := claudeNewHarness(t)
	body := claudeRequest(t)
	good := claudeFollow(body, h.result(body, "owner"))
	if got := h.bridge.continuationModel(good, "owner"); got != "claude-test" {
		t.Fatal(got)
	}
	if got := h.bridge.continuationModel(claudeWith(good, "max_tokens", json.Number("256")), "owner"); got != "" {
		t.Fatal(got)
	}
	if got := h.bridge.continuationModel(host.Obj{{Key: "messages", Value: "x"}}, "owner"); got != "" {
		t.Fatal(got)
	}
	run := h.runs()[0]
	type variant struct {
		body  host.Obj
		owner string
	}
	var variants []variant
	changed := clone(good).(host.Obj)
	last := get(changed, "messages").([]any)[2].(host.Obj)
	last.Set("content", get(last, "content").([]any)[:1])
	variants = append(variants, variant{changed, "owner"})
	variants = append(variants, variant{claudeWith(good, "system", "변경"), "owner"})
	changed = clone(good).(host.Obj)
	head := get(changed, "messages").([]any)[0].(host.Obj)
	head.Set("content", "다름")
	variants = append(variants, variant{changed, "owner"}, variant{good, "another"})
	for _, v := range variants {
		h.fails(v.body, v.owner)
		if run.isClosed() || h.profileCalls() != 1 {
			t.Fatalf("variant for %s destroyed the run or selected an account", v.owner)
		}
	}
	if get(h.result(good, "owner"), "stop_reason") != "end_turn" {
		t.Fatal("the waiting run did not resume")
	}
}

func TestClaudeExpiredResultsNeverLaunchNewProcess(t *testing.T) {
	h := claudeNewHarness(t)
	body := claudeRequest(t)
	first := h.result(body, "owner")
	run := h.runs()[0]
	h.bridge.discard(run, -1)
	if err := h.fails(claudeFollow(body, first), "owner"); err.Code != "conversation_mismatch" || err.Started {
		t.Fatal(err)
	}
	if h.profileCalls() != 1 || !claudeClosed(run.exited, 0) {
		t.Fatal("expired results launched or left a process")
	}
	if _, err := os.Stat(run.tmp); !os.IsNotExist(err) {
		t.Fatal("temporary folder kept")
	}
}

func TestClaudeStreamTextPrecedesCompletionAndCloseKillsChild(t *testing.T) {
	h := claudeNewHarness(t)
	h.mode = "slow"
	next, stop := iter.Pull2(h.bridge.iterMessages(claudeWith(claudeRequest(t), "stream", true), "owner", nil))
	began := time.Now()
	chunk, err, _ := next()
	if err != nil || !strings.Contains(string(chunk), "message_start") {
		t.Fatal(string(chunk), err)
	}
	run := h.runs()[0]
	var text []byte
	for range 2 {
		chunk, err, _ := next()
		if err != nil {
			t.Fatal(err)
		}
		text = append(text, chunk...)
	}
	if !strings.Contains(string(text), "안녕") || time.Since(began) > 2*time.Second {
		t.Fatalf("%q after %v", text, time.Since(began))
	}
	stop()
	if !run.isClosed() || !claudeClosed(run.exited, 0) || len(h.runs()) != 0 {
		t.Fatal("closing the stream left the run alive")
	}
}

func TestClaudeInvalidProtocolErrorsAndStopsNeverRetry(t *testing.T) {
	h := claudeNewHarness(t)
	for mode, code := range map[string]string{"bad-json": "upstream_protocol", "error": "upstream_error",
		"truncated": "upstream_truncated", "bad-mcp": "upstream_protocol"} {
		h.mode = mode
		before := h.profileCalls()
		err := h.fails(claudeRequest(t), "owner")
		if err.Code != code || err.Status != 502 || !err.Started || strings.Contains(err.Error(), "PRIVATE") {
			t.Fatalf("%s: %+v", mode, err)
		}
		if h.profileCalls() != before+1 || len(h.runs()) != 0 {
			t.Fatalf("%s retried or kept the run", mode)
		}
	}
}

func TestClaudeActiveDeadlineAndIdleExpiryReapProcess(t *testing.T) {
	h := claudeNewHarness(t)
	h.mode = "hang"
	claudeSetVar(t, &claudeDeadline, 200*time.Millisecond)
	began := time.Now()
	h.fails(claudeRequest(t), "owner")
	if time.Since(began) > 2*time.Second {
		t.Fatal("the active deadline did not stop the run")
	}
	claudeDeadline = 600 * time.Second
	h.mode = "tools"
	claudeSetVar(t, &claudeIdle, 150*time.Millisecond)
	h.result(claudeRequest(t), "owner")
	run := h.runs()[0]
	if !claudeClosed(run.closed, 2*time.Second) || !claudeClosed(run.exited, 2*time.Second) || len(h.runs()) != 0 {
		t.Fatal("the idle run was not reaped")
	}
}

func TestClaudeBusyContinuationAndCapacityFailBeforeGeneration(t *testing.T) {
	h := claudeNewHarness(t)
	body := claudeRequest(t)
	first := h.result(body, "owner")
	run := h.runs()[0]
	h.bridge.mu.Lock()
	run.busy = true
	h.bridge.mu.Unlock()
	if err := h.fails(claudeFollow(body, first), "owner"); err.Code != "conversation_busy" || err.Status != 409 || err.Started {
		t.Fatal(err)
	}
	h.bridge.mu.Lock()
	run.busy = false
	h.bridge.mu.Unlock()
	claudeSetVar(t, &claudeMaxRuns, 1)
	if err := h.fails(body, "other"); err.Code != "claude_capacity" || err.Status != 503 || err.Started {
		t.Fatal(err)
	}
	if h.profileCalls() != 1 {
		t.Fatal(h.profileCalls())
	}
}

func TestClaudeUnsupportedOptionsNeverSelectAccount(t *testing.T) {
	h := claudeNewHarness(t)
	for _, extra := range []host.KV{{Key: "temperature", Value: json.Number("0.1")},
		{Key: "tool_choice", Value: host.Obj{{Key: "type", Value: "any"}}},
		{Key: "tool_choice", Value: host.Obj{{Key: "type", Value: "auto"}, {Key: "disable_parallel_tool_use", Value: true}}},
		{Key: "max_tokens", Value: true}} {
		if err := h.fails(claudeWith(claudeRequest(t), extra.Key, extra.Value), "owner"); err.Status != 400 || err.Started {
			t.Fatal(extra.Key, err)
		}
	}
	if h.profileCalls() != 0 {
		t.Fatal("an account was selected")
	}
}

func TestClaudeCancelledBeforeUserPromptCleansAndDoesNotRetry(t *testing.T) {
	h := claudeNewHarness(t)
	_, err := h.call(claudeRequest(t), "owner", func() bool { return true })
	if err == nil || err.Code != "client_disconnected" || err.Status != 499 || !err.Started {
		t.Fatal(err)
	}
	if len(h.runs()) != 0 || h.profileCalls() != 1 {
		t.Fatal("the cancelled run was kept or retried")
	}
	for _, r := range h.records() {
		if get(r, "type") == "user" {
			t.Fatal("the prompt was sent")
		}
	}
}

// Bridge-level parts of the gateway continuation tests: expired ids never fall back to a new
// process, and a CLI error is a started upstream_error.
func TestClaudeClosedBridgeRefusesOwnedIdsAndErrorsAreStarted(t *testing.T) {
	h := claudeNewHarness(t)
	body := claudeRequest(t)
	next := claudeFollow(body, h.result(body, "owner"))
	if !claudeOwnedCalls(get(next, "messages")) {
		t.Fatal("gateway tool ids not recognised")
	}
	h.bridge.close()
	if got := h.bridge.continuationModel(next, "owner"); got != "" {
		t.Fatal(got)
	}
	if err := h.fails(next, "owner"); err.Code != "conversation_mismatch" || err.Started || h.profileCalls() != 1 {
		t.Fatal(err)
	}
	h.mode = "error"
	if err := h.fails(body, "owner"); err.Code != "upstream_error" || err.Status != 502 || !err.Started {
		t.Fatal(err)
	}
}

func TestClaudeBufferedResponseWriteFailureCancelsOnlyItsRequest(t *testing.T) {
	h := claudeNewHarness(t)
	body := claudeRequest(t)
	first := h.result(body, "owner")
	run := h.runs()[0]
	h.bridge.abortRequest(body, "other")
	if run.isClosed() {
		t.Fatal("another owner aborted the run")
	}
	next := claudeFollow(body, first)
	h.result(next, "owner")
	h.bridge.abortRequest(body, "owner")
	if run.isClosed() {
		t.Fatal("an earlier request aborted the run")
	}
	h.bridge.abortRequest(claudeWith(body, "temperature", json.Number("0.1")), "owner") // prepare errors are ignored
	h.bridge.abortRequest(next, "owner")
	if !run.isClosed() || !claudeClosed(run.exited, 0) {
		t.Fatal("the request's run survived")
	}
}

func TestClaudeOldExpiryCannotKillReacquiredRun(t *testing.T) {
	h := claudeNewHarness(t)
	h.result(claudeRequest(t), "owner")
	run := h.runs()[0]
	h.bridge.mu.Lock()
	epoch := run.epoch
	h.bridge.arm(run, 10*time.Second)
	h.bridge.mu.Unlock()
	h.bridge.discard(run, epoch)
	if run.isClosed() {
		t.Fatal("a stale expiry closed the run")
	}
}

func TestClaudeBlockedStdinIsBoundedByActiveDeadline(t *testing.T) {
	h := claudeNewHarness(t)
	h.mode = "no-read"
	body := claudeRequest(t)
	head := get(body, "messages").([]any)[0].(host.Obj)
	head.Set("content", strings.Repeat("x", 1<<20))
	claudeSetVar(t, &claudeDeadline, 200*time.Millisecond)
	began := time.Now()
	h.fails(body, "owner")
	if time.Since(began) > 2*time.Second || len(h.runs()) != 0 {
		t.Fatal("a blocked write outlived the deadline")
	}
}

func TestClaudeProfileErrorsAreFixed(t *testing.T) {
	claudeSetVar(t, &host.Home, t.TempDir())
	claudeSetVar(t, &host.AgentsState, t.TempDir())
	_, _, err := claudeProfile("claude-test")
	if ce, ok := err.(*ClaudeError); !ok || ce.Code != "login_unavailable" || ce.Status != 503 {
		t.Fatal(err)
	}
}

func TestClaudeConsumerPanicOrGoexitReleasesRun(t *testing.T) {
	h := claudeNewHarness(t)
	consume := func(stopping func()) (recovered any) {
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer func() { recovered = recover() }()
			for range h.bridge.iterMessages(claudeWith(claudeRequest(t), "stream", true), "owner", nil) {
				stopping()
			}
		}()
		<-done
		return recovered
	}
	if got := consume(func() { panic("consumer") }); got != "consumer" || len(h.runs()) != 0 {
		t.Fatalf("panic %v, runs %d", got, len(h.runs()))
	}
	if got := consume(runtime.Goexit); got != nil || len(h.runs()) != 0 {
		t.Fatalf("goexit %v, runs %d", got, len(h.runs()))
	}
}

func TestClaudeOwnedCalls(t *testing.T) {
	for text, want := range map[string]bool{
		`[{"role":"user","content":"toolu_mem_x"}]`:                                                  false,
		`[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_mem_a_0"}]}]`:                true,
		`[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_mem_a_0"}]}]`:         true,
		`[{"role":"user","content":[{"type":"tool_result","tool_use_id":null,"id":"toolu_mem_a"}]}]`: false,
		`[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_other"}]}]`:           false,
	} {
		v, _ := Parse(text)
		if claudeOwnedCalls(v) != want {
			t.Error(text)
		}
	}
}

func TestClaudeEqualIsPythonEquality(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{`{"a":1,"b":[1.0,true]}`, `{"b":[1,1],"a":1.0}`, true}, {`{"a":1}`, `{"a":1,"b":2}`, false},
		{`["1"]`, `[1]`, false}, {`null`, `false`, false}, {`0`, `false`, true}, {`1.5`, `1.5`, true}} {
		a, _ := Parse(c.a)
		b, _ := Parse(c.b)
		if claudeEqual(a, b) != c.want {
			t.Error(c.a, c.b)
		}
	}
}

type claudeGolden struct {
	Prepare []struct {
		Name, Body, Result, Settings, Prompt string
		MaxInput                             int `json:"max_input"`
		Error                                []any
	}
	Environment struct {
		Child    map[string]string
		Selected map[string]any
		Env      map[string]string
	}
	Argv []struct {
		Name, Body string
		Args       []string
		Env        map[string]string
	}
}

func claudeLoadGolden(t *testing.T) claudeGolden {
	raw, err := os.ReadFile("testdata/claude/golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g claudeGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestClaudePrepareGolden(t *testing.T) {
	for _, c := range claudeLoadGolden(t).Prepare {
		parsed, _ := Parse(c.Body)
		body, ok := parsed.(host.Obj)
		if !ok {
			continue // the gateway only hands objects to the bridge
		}
		if c.MaxInput > 0 {
			claudeSetVar(t, &claudeMaxInput, c.MaxInput)
		}
		result, settings, err := claudePrepare(body)
		claudeMaxInput = 8 << 20
		if c.Error != nil {
			ce, ok := err.(*ClaudeError)
			if !ok || ce.Code != c.Error[0] || float64(ce.Status) != c.Error[1].(float64) {
				t.Errorf("%s: %v, want %v", c.Name, err, c.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		if got := claudeDump(result); got != c.Result {
			t.Errorf("%s result:\n%s\n%s", c.Name, got, c.Result)
		}
		if settings != c.Settings {
			t.Errorf("%s settings:\n%s\n%s", c.Name, settings, c.Settings)
		}
		if got := claudeDump(claudePrompt(get(result, "messages").([]any))); got != c.Prompt {
			t.Errorf("%s prompt:\n%s\n%s", c.Name, got, c.Prompt)
		}
		if claudeDump(body) != claudeDump(claudeParse(t, c.Body)) {
			t.Errorf("%s: prepare changed its input", c.Name)
		}
	}
}

func TestClaudeEnvironmentGolden(t *testing.T) {
	g := claudeLoadGolden(t).Environment
	claudeSetVar(t, &host.ChildEnv, func(row host.CLI, keys []map[string]any, model string, key map[string]any) (map[string]string, error) {
		if row.Name != "claude" || keys != nil || model != "" || key != nil {
			t.Fatalf("child_env(%v)", row.Name)
		}
		out := map[string]string{}
		for k, v := range g.Child {
			out[k] = v
		}
		return out, nil
	})
	env, err := claudeEnvironment(g.Selected, "128")
	if err != nil {
		t.Fatal(err)
	}
	if claudeDump(env) != claudeDump(g.Env) {
		t.Fatalf("\n%s\n%s", claudeDump(env), claudeDump(g.Env))
	}
}

func TestClaudeArgvGolden(t *testing.T) {
	for _, c := range claudeLoadGolden(t).Argv {
		h := claudeNewHarness(t)
		h.args = filepath.Join(t.TempDir(), "args.json")
		body, settings, err := claudePrepare(claudeParse(t, c.Body))
		if err != nil {
			t.Fatal(err)
		}
		tools := host.Obj{}
		for _, tool := range getd(body, "tools", []any{}).([]any) {
			tools.Set(get(tool, "name").(string), tool)
		}
		if got := claudeArgs("/x/claude", get(body, "model").(string), "<tmp>/system.txt", tools); !slices.Equal(got, c.Args) {
			t.Fatalf("%s:\n%q\n%q", c.Name, got, c.Args)
		}
		// The launched process sees the same command line, its own folder and the system prompt.
		run, err := claudeNewRun(&h.bridge.mu, "owner", settings, body, h.selected, "/x/claude")
		if err == nil || err.(*ClaudeError).Code != "cli_unavailable" {
			t.Fatalf("a missing CLI started: %v", err)
		}
		exe, _ := os.Executable()
		run, err = claudeNewRun(&h.bridge.mu, "owner", settings, body, h.selected, exe)
		if err != nil {
			t.Fatal(err)
		}
		var seen struct {
			Args                      []string
			Cwd, System, Mode, Tmpdir string
		}
		for began := time.Now(); time.Since(began) < 5*time.Second; time.Sleep(20 * time.Millisecond) {
			if raw, err := os.ReadFile(h.args); err == nil && json.Unmarshal(raw, &seen) == nil {
				break
			}
		}
		run.close()
		cwd, _ := filepath.EvalSymlinks(run.tmp)
		if seen.Cwd != run.tmp && seen.Cwd != cwd {
			t.Fatalf("cwd %s, folder %s", seen.Cwd, run.tmp)
		}
		args := append([]string{"/x/claude"}, seen.Args...)
		for i := range args {
			args[i] = strings.ReplaceAll(args[i], run.tmp, "<tmp>")
		}
		if !slices.Equal(args, c.Args) || seen.Tmpdir != run.tmp || seen.Mode != "600" ||
			!strings.HasPrefix(filepath.Base(run.tmp), "arkism-claude-") {
			t.Fatalf("%s: %+v", c.Name, seen)
		}
		if seen.System != getd(body, "system", "") {
			t.Fatalf("system %q", seen.System)
		}
		if c.Env["CLAUDE_CODE_TMPDIR"] != "<tmp>" {
			t.Fatal("golden env changed")
		}
	}
}

func TestClaudePrepareRejectsArgumentShapedNames(t *testing.T) {
	msgs := `"messages":[{"role":"user","content":"hi"}],"max_tokens":8`
	for _, body := range []string{
		`{"model":"--settings",` + msgs + `}`,
		`{"model":"",` + msgs + `}`,
		`{"model":"claude-opus-4-1",` + msgs + `,"tools":[{"name":"a,Bash","input_schema":{"type":"object"}}]}`,
	} {
		if _, _, err := claudePrepare(claudeParse(t, body)); err == nil {
			t.Errorf("%s: accepted", body)
		}
	}
	ok := `{"model":"claude-opus-4-1[1m]",` + msgs + `,"tools":[{"name":"get_weather","input_schema":{"type":"object"}}]}`
	if _, _, err := claudePrepare(claudeParse(t, ok)); err != nil {
		t.Errorf("valid body rejected: %v", err)
	}
}
