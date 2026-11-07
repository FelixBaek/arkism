package gateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/host/subscription"
)

// cursor/<model>: the cursor subscription through its own CLI (`cursor-agent login` signs it in; there
// is no account API). The CLI takes one prompt and answers text, so only text conversations run
// here: tools, images and tool results are refused before the CLI starts. It runs in an empty
// folder with its own config that denies every agent tool, so a prompt cannot reach a file or a shell.

var (
	cursorBin = func() string { return host.Bin("ARKISM_CURSOR_BIN", "cursor-agent") }
	cursorRun = runCursor // test seam
	// cursorDenied is every tool the CLI's agent offers; its config takes them one by one.
	cursorDenied = []string{"Shell", "AwaitShell", "Read", "Write", "Delete", "StrReplace", "EditNotebook", "Grep", "Glob",
		"ReadLints", "WebSearch", "WebFetch", "Task", "TodoWrite", "GetDynamicTools", "CallDynamicTool", "FetchMcpResource",
		"CreateGoal", "UpdateGoal", "GenerateImage"}
	// cursorEnv is what the CLI may see of the gateway's environment: enough to find its login, no Arkism secrets.
	cursorEnv = []string{"PATH", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "SystemRoot", "TMPDIR", "TEMP", "TMP", "LANG",
		"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"}
)

// cursorTarget is the CLI's target when it is installed; its own login decides the rest.
func cursorTarget(name string) *where {
	if _, err := os.Stat(cursorBin()); err != nil {
		return nil
	}
	return &where{kind: "cursor", model: name}
}

// cursorPrompt flattens a text-only Messages body into the one prompt the CLI reads; ok is false
// for anything else (tools, images, tool calls or results).
func cursorPrompt(body host.Obj) (string, bool) {
	if get(body, "tools") != nil {
		return "", false
	}
	var parts []string
	system, ok := cursorText(get(body, "system"))
	if !ok {
		return "", false
	}
	if system != "" {
		parts = append(parts, system)
	}
	messages, _ := get(body, "messages").([]any)
	for _, m := range messages {
		text, ok := cursorText(get(m, "content"))
		if !ok {
			return "", false
		}
		if get(m, "role") == "assistant" {
			text = "[Previous assistant response: " + text + "]"
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n"), len(messages) > 0
}

// cursorText is a string or a list of text blocks as one string.
func cursorText(v any) (string, bool) {
	switch c := v.(type) {
	case nil:
		return "", true
	case string:
		return c, true
	case []any:
		var out []string
		for _, block := range c {
			text, isText := get(block, "text").(string)
			if get(block, "type") != "text" || !isText {
				return "", false
			}
			out = append(out, text)
		}
		return strings.Join(out, "\n"), true
	}
	return "", false
}

// cursor answers one text request through the CLI, as one Messages reply or its event stream.
func (x *exchange) cursor(body host.Obj, w *where) error {
	prompt, ok := cursorPrompt(body)
	if !ok {
		return x.refuse("unsupported_field")
	}
	ctx, cancel := context.WithTimeout(x.r.Context(), upstreamWait)
	defer cancel()
	text, in, out, err := cursorRun(ctx, w.model, prompt)
	if err != nil {
		x.attemptErr = err.Error()
		if rerr := x.reject(503); rerr != nil {
			return rerr
		}
		return x.sendBytes(anthropicError(503, err.Error()))
	}
	model, _ := get(body, "model").(string)
	usage := host.Obj{{Key: "input_tokens", Value: in}, {Key: "output_tokens", Value: out}, {Key: "cache_read_input_tokens", Value: 0}}
	block := host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: text}}
	id := responsesID("msg_")
	if !truthy(get(body, "stream")) {
		return x.sendBytes(200, mustPy(messagesMessage(id, model, []any{block}, "end_turn", usage)))
	}
	empty := host.Obj{{Key: "type", Value: "text"}, {Key: "text", Value: ""}}
	events := [][]byte{
		messagesSSE("message_start", host.KV{Key: "message", Value: messagesMessage(id, model, []any{}, nil,
			host.Obj{{Key: "input_tokens", Value: in}, {Key: "output_tokens", Value: 0}, {Key: "cache_read_input_tokens", Value: 0}})}),
		messagesSSE("content_block_start", host.KV{Key: "index", Value: 0}, host.KV{Key: "content_block", Value: empty}),
		messagesSSE("content_block_delta", host.KV{Key: "index", Value: 0},
			host.KV{Key: "delta", Value: host.Obj{{Key: "type", Value: "text_delta"}, {Key: "text", Value: text}}}),
		messagesSSE("content_block_stop", host.KV{Key: "index", Value: 0}),
		messagesSSE("message_delta", host.KV{Key: "delta", Value: host.Obj{{Key: "stop_reason", Value: "end_turn"}, {Key: "stop_sequence", Value: nil}}},
			host.KV{Key: "usage", Value: usage}),
		messagesSSE("message_stop"),
	}
	return x.send(200, once(bytes.Join(events, nil)), "text/event-stream", nil)
}

// runCursor runs one headless CLI turn. Errors are fixed codes: the CLI's own output can quote the prompt.
func runCursor(ctx context.Context, model, prompt string) (text string, in, out int64, err error) {
	work, err := os.MkdirTemp("", "arkism-cursor-")
	if err != nil {
		return "", 0, 0, errors.New("upstream_unavailable")
	}
	defer os.RemoveAll(work)
	deny := make([]string, len(cursorDenied))
	for i, tool := range cursorDenied {
		deny[i] = tool + "(*)"
	}
	config, _ := json.Marshal(map[string]any{"version": 1, "permissions": map[string]any{"allow": []string{}, "deny": deny}})
	if err := os.WriteFile(filepath.Join(work, "cli-config.json"), config, 0o600); err != nil {
		return "", 0, 0, errors.New("upstream_unavailable")
	}
	space := filepath.Join(work, "workspace")
	if err := os.Mkdir(space, 0o700); err != nil {
		return "", 0, 0, errors.New("upstream_unavailable")
	}
	args := []string{"-p", "--output-format", "json", "--trust", "--mode", "ask"}
	if model != "" && model != "auto" {
		args = append(args, "--model", model)
	}
	cmd := exec.CommandContext(ctx, cursorBin(), args...)
	cmd.Dir, cmd.Stdin = space, strings.NewReader(prompt)
	cmd.Env = []string{"NO_COLOR=1", "CURSOR_CONFIG_DIR=" + work}
	for _, name := range cursorEnv {
		if v, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+v)
		}
	}
	var stdout bytes.Buffer
	cmd.Stdout = &limitWriter{&stdout, subscription.MaxOutput}
	runErr := cmd.Run()
	return cursorResult(stdout.Bytes(), runErr)
}

// cursorResult reads the CLI's one {"type":"result"} line.
func cursorResult(stdout []byte, runErr error) (string, int64, int64, error) {
	scanner := bufio.NewScanner(bytes.NewReader(stdout))
	scanner.Buffer(make([]byte, 0, 64<<10), subscription.MaxOutput)
	for scanner.Scan() {
		var r struct {
			Type    string `json:"type"`
			Result  string `json:"result"`
			IsError bool   `json:"is_error"`
			Usage   struct {
				In  int64 `json:"inputTokens"`
				Out int64 `json:"outputTokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(scanner.Bytes(), &r) != nil || r.Type != "result" {
			continue
		}
		if r.IsError {
			return "", 0, 0, errors.New("upstream_error")
		}
		return r.Result, r.Usage.In, r.Usage.Out, nil
	}
	if runErr != nil {
		return "", 0, 0, errors.New("login_unavailable") // signed out, missing or killed: nothing was answered
	}
	return "", 0, 0, errors.New("upstream_protocol")
}

// limitWriter keeps at most n bytes and drops the rest (the run fails to parse, not the gateway).
type limitWriter struct {
	b *bytes.Buffer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if room := l.n - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
