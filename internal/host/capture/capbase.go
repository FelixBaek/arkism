package capture

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// The capture client's shared settings and I/O. Python passed the arkism_capture module
// around as `cap`; here these are package variables that tests replace.

var (
	capState         = host.CaptureState
	capOrigin        = host.Env("ARKISM_ORIGIN", "claude_code")
	capProjects      = host.Env("ARKISM_CLAUDE_PROJECTS", filepath.Join(host.Home, ".claude/projects"))
	capCodexSessions = host.Env("ARKISM_CODEX_SESSIONS", filepath.Join(host.Home, ".codex/sessions"))
	capZcodeDB       = host.Env("ARKISM_ZCODE_DB", filepath.Join(host.Home, ".zcode/cli/db/db.sqlite"))
	capModel         = host.Env("ARKISM_CAPTURE_MODEL", "haiku")
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
		f.WriteString(host.MustDumps(row, host.DumpOpts{NoASCII: true}) + "\n")
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

// llm runs the Claude Code CLI headless with a JSON schema and returns
// structured_output[key] (or an empty list). Tests replace it.
var llm = func(system, user, model string, timeout time.Duration, schema, key string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, host.ClaudeBin, "-p", "--model", model, "--output-format", "json", "--system-prompt", system,
		"--tools", "", "--setting-sources", "", "--strict-mcp-config", "--no-session-persistence", "--json-schema", schema)
	cmd.Env = append(os.Environ(), "ARKISM_CAPTURE_CHILD=1")
	cmd.Dir = capState
	cmd.Stdin = strings.NewReader(user)
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
	if err != nil {
		return nil, err
	}
	result, err := loadObj(stdout.Bytes())
	if err != nil {
		return nil, err
	}
	out, isMap := result["structured_output"].(map[string]any)
	if truthy(result["is_error"]) || !isMap {
		return nil, runtimeErr("extractor_no_json")
	}
	return or(out[key], []any{}), nil
}

// kv builds one metric field.
func kv(key string, value any) host.KV { return host.KV{Key: key, Value: value} }
