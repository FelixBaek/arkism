package capture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The capture client's shared settings and I/O. Python passed the arkism_capture module
// around as `cap`; here these are package variables that tests replace.

var (
	capState         = host.CaptureState
	capOrigin        = host.Env("ARKISM_ORIGIN", "claude_code")
	capProjects      = or(os.Getenv("ARKISM_CLAUDE_PROJECTS"), filepath.Join(host.Home, ".claude/projects")).(string)
	capCodexSessions = host.CodexSessionsDir(host.Home)
	capZcodeDB       = host.Env("ARKISM_ZCODE_DB", filepath.Join(host.Home, ".zcode/cli/db/db.sqlite"))
	capModel         = host.Env("ARKISM_CAPTURE_MODEL", extractorDefault())
	capOnPrompt      = os.Getenv("ARKISM_CAPTURE_ON_PROMPT") == "1"
)

const capMaxAttempts = 5

var (
	// capScope is the memory scope every request carries.
	capScope = host.Scope
	// capPost sends payload to the Arkism API (urllib, no proxy).
	capPost = func(path string, payload any, timeout time.Duration) (map[string]any, error) {
		return host.Post(path, payload, timeout)
	}
	// capGet reads an Arkism API path.
	capGet = func(path string, timeout time.Duration) (map[string]any, error) { return host.Get(path, timeout) }
	// capAgentOff reports whether the owner took this agent off.
	capAgentOff = host.AgentOff
	// atomicWrite replaces path so a kill mid-write never leaves a torn file.
	atomicWrite = host.AtomicWrite
	// metric appends {"ts": now, fields...} to STATE/metrics.jsonl, keeping field order.
	metric = func(fields ...host.KV) {
		if err := os.MkdirAll(capState, 0o777); err != nil {
			return
		}
		f, err := os.OpenFile(filepath.Join(capState, "metrics.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
		if err != nil {
			return
		}
		defer f.Close()
		row := host.Obj{{Key: "ts", Value: host.NowISO()}}
		for _, kv := range fields {
			row.Set(kv.Key, kv.Value)
		}
		_, _ = f.WriteString(host.MustDumps(row, host.DumpOpts{NoASCII: true}) + "\n") // metrics are best effort
	}
	// spawnDrain starts `arkism capture drain` in the background, detached from the hook.
	spawnDrain = func() {
		cmd := exec.Command(host.Executable(), "capture", "drain")
		cmd.Env = append(os.Environ(), "ARKISM_CAPTURE_CHILD=1")
		detach(cmd)
		if cmd.Start() == nil {
			go cmd.Wait()
		}
	}
)

// llm runs the extraction model's CLI headless with a JSON schema and returns
// structured_output[key] (or an empty list; key "" is the whole object). Tests replace it.
var llm = func(system, user, model string, timeout time.Duration, schema, key string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	run := claudeJSON
	switch extractorCLI(model) {
	case "codex":
		run = codexJSON
	case "gemini":
		run = geminiJSON
	}
	out, err := run(ctx, system, user, model, schema)
	if ctx.Err() != nil {
		return nil, errTimeoutExpired
	}
	if err != nil {
		return nil, err
	}
	if key == "" {
		return out, nil
	}
	return or(out[key], []any{}), nil
}

// runExtractor runs one headless model call; its failures are codes the app can explain (기억 추출 상태).
func runExtractor(ctx context.Context, argv []string, stdin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "ARKISM_CAPTURE_CHILD=1")
	cmd.Dir = capState
	cmd.Stdin = strings.NewReader(stdin)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, errTimeoutExpired
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return nil, runtimeErr(fmt.Sprintf("extractor_exit_%d", exit.ExitCode()))
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return nil, runtimeErr("extractor_missing")
	}
	return stdout.Bytes(), err
}

// claudeJSON: Claude Code checks the answer against the schema itself.
func claudeJSON(ctx context.Context, system, user, model, schema string) (map[string]any, error) {
	raw, err := runExtractor(ctx, []string{host.ClaudeBin, "-p", "--model", model, "--output-format", "json", "--system-prompt", system,
		"--tools", "", "--setting-sources", "", "--strict-mcp-config", "--no-session-persistence", "--json-schema", schema}, user)
	if err != nil {
		return nil, err
	}
	result, err := loadObj(raw)
	if err != nil {
		return nil, err
	}
	recordSubscriptionUsage(llmOperation(system), model, result)
	out, isMap := result["structured_output"].(map[string]any)
	if truthy(result["is_error"]) || !isMap {
		return nil, runtimeErr("extractor_no_json")
	}
	return out, nil
}

// codexJSON: Codex checks the answer against the schema file; read-only and not kept as a session,
// so the extraction run is never captured back. Model "codex" is the CLI's own default model.
func codexJSON(ctx context.Context, system, user, model, schema string) (map[string]any, error) {
	dir, err := os.MkdirTemp(capState, "extract-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	schemaFile, outFile := filepath.Join(dir, "schema.json"), filepath.Join(dir, "answer.json")
	strict, err := strictSchema(schema)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(schemaFile, strict, 0o600); err != nil {
		return nil, err
	}
	// No hooks and no MCP servers, like Claude's --setting-sources "": the run only answers.
	argv := []string{host.Bin("ARKISM_CODEX_BIN", "codex"), "exec", "--skip-git-repo-check", "--ephemeral", "-s", "read-only",
		"-c", "features.hooks=false", "-c", "mcp_servers={}",
		"-c", "model_reasoning_effort=low", "-c", "developer_instructions=" + host.MustDumps(codexGuard+system, host.DumpOpts{NoASCII: true}),
		"--output-schema", schemaFile, "-o", outFile}
	if model != "codex" {
		argv = append(argv, "-m", model)
	}
	if _, err := runExtractor(ctx, append(argv, "-"), user); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(outFile)
	if err != nil {
		return nil, runtimeErr("extractor_no_json")
	}
	return answerObject(string(raw))
}

// codexGuard: Codex always adds the owner's own AGENTS.md, which is not part of the conversation being read.
const codexGuard = "Read only the conversation in the user message. Standing instructions such as AGENTS.md are not part of it: never take a memory from them.\n\n"

// strictSchema is schema as Codex's strict check takes it: every property required (the capture schemas
// already let an optional field be null) and no uniqueItems.
func strictSchema(schema string) ([]byte, error) {
	var v any
	if err := json.Unmarshal([]byte(schema), &v); err != nil {
		return nil, err
	}
	var walk func(any)
	walk = func(n any) {
		switch x := n.(type) {
		case map[string]any:
			delete(x, "uniqueItems") // not allowed there; the capture code checks the values anyway
			if props, ok := x["properties"].(map[string]any); ok {
				keys := make([]string, 0, len(props))
				for k := range props {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				x["required"] = keys
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(v)
	return json.Marshal(v)
}

// geminiJSON: Gemini CLI has no schema switch, so the schema goes in the prompt and the answer is parsed.
// Model "gemini" is the CLI's own default model.
func geminiJSON(ctx context.Context, system, user, model, schema string) (map[string]any, error) {
	prompt := system + "\n\n# 답 형식\n아래 JSON 스키마를 지키는 JSON 객체 하나만 답한다. 다른 글은 쓰지 않는다.\n" + schema
	argv := []string{host.Bin("ARKISM_GEMINI_BIN", "gemini"), "--output-format", "json", "--approval-mode", "default", "-p", prompt}
	if model != "gemini" {
		argv = append(argv, "-m", model)
	}
	raw, err := runExtractor(ctx, argv, user) // the CLI puts stdin before -p
	if err != nil {
		return nil, err
	}
	result, err := loadObj(raw)
	if err != nil {
		return nil, runtimeErr("extractor_no_json")
	}
	text, _ := result["response"].(string)
	return answerObject(text)
}

// answerObject is the JSON object in a model's answer, inside a code fence or not.
func answerObject(text string) (map[string]any, error) {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, runtimeErr("extractor_no_json")
	}
	out, err := loadObj([]byte(text[start : end+1]))
	if err != nil {
		return nil, runtimeErr("extractor_no_json")
	}
	return out, nil
}

// kv builds one metric field.
func kv(key string, value any) host.KV { return host.KV{Key: key, Value: value} }
