package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
	"github.com/FelixBaek/arkism/internal/memory"
)

const terraTextModel = "gpt-6.1-terra"
const terraTextSchema = `{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`

// terraText uses the local Codex sign-in for the memory service's text jobs.
type terraText struct {
	executable string
	vision     bool
}

func (terraText) Generator() memory.Generator {
	return memory.Generator{Model: terraTextModel, PromptVersion: "terra-text-v1", PipelineVersion: "codex-exec-v1"}
}

func (m terraText) Complete(ctx context.Context, system, user string, maxTokens int) (string, memory.ProviderUsage, error) {
	return m.complete(ctx, system, user, maxTokens, "", nil)
}

func (m terraText) ImageInput() bool { return m.vision }

func (m terraText) CompleteImage(ctx context.Context, system, user, mime string, image []byte, maxTokens int) (string, memory.ProviderUsage, error) {
	if !m.vision {
		return "", memory.ProviderUsage{}, memory.ErrModelNotConfigured
	}
	var name string
	switch mime {
	case "image/png":
		name = "input.png"
	case "image/jpeg":
		name = "input.jpg"
	case "image/webp":
		name = "input.webp"
	default:
		return "", memory.ProviderUsage{}, memory.ErrInvalid
	}
	if len(image) > 8<<20 || http.DetectContentType(image) != mime {
		return "", memory.ProviderUsage{}, memory.ErrInvalid
	}
	return m.complete(ctx, system, user, maxTokens, name, image)
}

func (m terraText) complete(ctx context.Context, system, user string, maxTokens int, imageName string, image []byte) (string, memory.ProviderUsage, error) {
	var usage memory.ProviderUsage
	if len(system)+len(user) > 8<<20 || maxTokens <= 0 {
		return "", usage, memory.ErrInvalid
	}
	dir, err := os.MkdirTemp("", "arkism-terra-text-")
	if err != nil {
		return "", usage, err
	}
	defer os.RemoveAll(dir)
	schema, answer := filepath.Join(dir, "schema.json"), filepath.Join(dir, "answer.json")
	if err := os.WriteFile(schema, []byte(terraTextSchema), 0600); err != nil {
		return "", usage, err
	}
	if imageName != "" {
		if err := os.WriteFile(filepath.Join(dir, imageName), image, 0600); err != nil {
			return "", usage, err
		}
	}
	instructions, err := json.Marshal(system + "\n\nUse only the supplied data. Do not use tools. Put the complete answer inside the required text field. Limit the answer to " +
		strconv.Itoa(maxTokens) + " tokens.")
	if err != nil {
		return "", usage, err
	}
	ctx, cancel := context.WithTimeout(ctx, 110*time.Second)
	defer cancel()
	args := []string{"exec", "--skip-git-repo-check", "--ephemeral", "--ignore-user-config", "--ignore-rules",
		"-s", "read-only", "--disable", "shell_tool", "--disable", "multi_agent", "--disable", "multi_agent_v2",
		"--disable", "plugins", "--disable", "remote_plugin", "--disable", "apps", "--disable", "request_permissions_tool",
		"-c", "features.hooks=false", "-c", "mcp_servers={}", "-c", `approval_policy="never"`,
		"-c", `web_search="disabled"`, "-c", `model_provider="openai"`, "-c", "model_reasoning_effort=low",
		"-c", "developer_instructions=" + string(instructions), "--json", "--output-schema", schema, "-o", answer, "-m", terraTextModel}
	if imageName != "" {
		args = append(args, "--image", imageName)
	}
	cmd := exec.CommandContext(ctx, m.executable, append(args, "--", "-")...)
	cmd.Dir, cmd.Stdin, cmd.WaitDelay = dir, strings.NewReader(user), time.Second
	for _, name := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "SystemRoot", "TEMP", "TMP", "CODEX_HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "XDG_CACHE_HOME"} {
		if value, ok := os.LookupEnv(name); ok {
			cmd.Env = append(cmd.Env, name+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, "ARKISM_CAPTURE_CHILD=1")
	var events terraEventLog
	cmd.Stdout = &events
	runErr := runLocalModel(cmd)
	usage, found := readTerraTextUsage(&events.Buffer)
	if runErr != nil || !found {
		return "", usage, memory.ErrUnavailable
	}
	if usage.OutputTokens > int64(maxTokens) {
		return "", usage, memory.ErrUnavailable
	}
	file, err := os.Open(answer)
	if err != nil {
		return "", usage, memory.ErrUnavailable
	}
	defer file.Close()
	const limit = 512 << 10 // JSON quotes, escapes and wrapper are separate from answer text.
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	var result struct {
		Text string `json:"text"`
	}
	if err != nil || len(raw) > limit || json.Unmarshal(raw, &result) != nil || strings.TrimSpace(result.Text) == "" {
		return "", usage, memory.ErrUnavailable
	}
	return result.Text, usage, nil
}

type terraEventLog struct{ bytes.Buffer }

func (b *terraEventLog) Write(p []byte) (int, error) {
	if len(p) > (4<<20)-b.Len() {
		return 0, memory.ErrUnavailable
	}
	return b.Buffer.Write(p)
}

func readTerraTextUsage(out io.Reader) (memory.ProviderUsage, bool) {
	var totals memory.ProviderUsage
	found := false
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var event struct {
			Type  string `json:"type"`
			Usage struct {
				Input  int64 `json:"input_tokens"`
				Output int64 `json:"output_tokens"`
				Cached int64 `json:"cached_input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Type == "turn.completed" {
			u := event.Usage
			if u.Input > 0 && u.Input <= 1<<30 && u.Output > 0 && u.Output <= 1<<30 && u.Cached >= 0 && u.Cached <= u.Input {
				totals = memory.ProviderUsage{Model: terraTextModel, Requests: 1, InputTokens: u.Input, OutputTokens: u.Output,
					TotalTokens: u.Input + u.Output, CachedInputTokens: u.Cached}
				found = true
			}
		}
	}
	if scanner.Err() != nil {
		return totals, false
	}
	return totals, found
}

func configureTerraText(svc *memory.Service, config memory.LLMConfig, recovery bool) error {
	raw := os.Getenv("MEMORY_LOCAL_TERRA_TEXT")
	skills := os.Getenv("MEMORY_LOCAL_TERRA_SKILLS")
	if recovery {
		return nil
	}
	if raw != "" && raw != "0" && raw != "1" || skills != "" && skills != "0" && skills != "1" {
		return memory.ErrInvalid
	}
	if raw != "1" && skills != "1" {
		return nil
	}
	if os.Getenv("MEMORY_LOCAL_STANDALONE") != "true" || config.Configured() {
		return memory.ErrInvalid
	}
	bin, err := exec.LookPath(host.Bin("ARKISM_CODEX_BIN", "codex"))
	if err != nil {
		return memory.ErrModelNotConfigured
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return err
	}
	model := terraText{executable: bin, vision: os.Getenv("MEMORY_FILE_VISION") == "1"}
	if raw == "1" {
		svc.Wiki, svc.ReflectReread = model, model
	}
	if skills == "1" {
		svc.Analyzer = &memory.ChatSkillAnalyzer{TextModel: model}
		svc.Drafter = &memory.ChatSkillDrafter{TextModel: model}
	}
	return nil
}
