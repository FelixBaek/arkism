package crew

// Host runner for Arkism workflows: the model → CLI table and the argv each CLI takes.

import (
	"os"
	"regexp"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

var efforts = []string{"low", "medium", "high", "max"}

// accessModes: what a step may do, said to each CLI in its own words, so a workflow never inherits whatever the
// operator's own CLI settings happen to allow. read: look only. write: edit files inside the working folder (shell
// commands stay inside the CLI's sandbox, or are refused where it has none). full: the CLI's no-limits mode.
var accessModes = []string{"read", "write", "full"}

var claudeAccess = map[string][]string{"read": {"--permission-mode", "dontAsk", "--disallowedTools", "Bash,Edit,Write,NotebookEdit"},
	"write": {"--permission-mode", "acceptEdits"}, "full": {"--permission-mode", "bypassPermissions"}}

var accessFlags = map[string]map[string][]string{
	"claude": claudeAccess, "glm": claudeAccess, "custom": claudeAccess,
	"codex": {"read": {"-s", "read-only"}, "write": {"-s", "workspace-write"}, "full": {"--dangerously-bypass-approvals-and-sandbox"}},
	// grok's acceptEdits refuses even a new file in headless runs; auto inside its workspace sandbox does not.
	"grok": {"read": {"--permission-mode", "plan"}, "write": {"--permission-mode", "auto", "--sandbox", "workspace"},
		"full": {"--permission-mode", "bypassPermissions"}},
	"agy":    {"read": {"--mode", "plan"}, "write": {"--mode", "accept-edits"}, "full": {"--dangerously-skip-permissions"}},
	"gemini": {"read": {"--approval-mode", "default"}, "write": {"--approval-mode", "auto_edit"}, "full": {"--approval-mode", "yolo"}},
}

func in(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// cliFor is the CLI row for a model name. ARKISM_CREW_ROUTES="deepseek=glm,astra=claude" adds prefixes first.
// A model no CLI claims runs on a 직접 추가 provider when one of its keys matches (custom).
func cliFor(model string) (host.CLI, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	for _, pair := range strings.Split(os.Getenv("ARKISM_CREW_ROUTES"), ",") {
		prefix, cli, _ := strings.Cut(pair, "=")
		prefix = strings.TrimSpace(prefix)
		if prefix != "" && strings.HasPrefix(name, strings.ToLower(prefix)) {
			for _, row := range host.CLIS {
				if row.Name == strings.TrimSpace(cli) {
					return row, true
				}
			}
			return host.CLI{}, false
		}
	}
	for _, row := range host.CLIS {
		if row.Pattern.MatchString(name) {
			return row, true
		}
	}
	if name != "" {
		if key, err := customKeyFor(model, nil, ""); err == nil && key != nil {
			return host.CustomCLI, true
		}
	}
	return host.CLI{}, false
}

func codexEffort(effort string) string {
	if effort == "max" {
		return host.Env("ARKISM_CODEX_MAX_EFFORT", "xhigh")
	}
	return effort
}

func binOf(row host.CLI) string {
	if row.Name == "glm" && !glmWrapper() {
		return host.Bin(host.CustomCLI.BinEnv, host.CustomCLI.BinName)
	}
	return host.Bin(row.BinEnv, row.BinName)
}

// glmWrapper: a claude-glm wrapper is installed (or named by ARKISM_GLM_BIN). Without one, GLM models run in
// Claude Code against Z.ai's Anthropic-compatible address with the stored Z.ai key.
func glmWrapper() bool {
	if os.Getenv("ARKISM_GLM_BIN") != "" {
		return true
	}
	exe := host.Bin("ARKISM_GLM_BIN", "claude-glm")
	return exists(exe) || onPath(exe)
}

// buildCommand is argv and stdin for one run. Instructions go where the CLI keeps standing instructions; the task is
// the prompt. once (ai_once): one question, one answer; Claude Code gets --max-turns 1. access: one of accessModes.
func buildCommand(row host.CLI, model, effort, instructions, task, outFile string, once bool, access string) ([]string, string) {
	argv, stdin := command(row, model, effort, instructions, task, outFile, once)
	flags := accessFlags[row.Name][access]
	if row.Name == "codex" { // Codex reads its prompt from the trailing "-"; the flags go before it.
		out := append(append(append([]string{}, argv[:len(argv)-1]...), flags...), argv[len(argv)-1])
		return out, stdin
	}
	return append(argv, flags...), stdin
}

var effortSuffix = regexp.MustCompile(`-(minimal|low|medium|high|max)$`)

// command is the CLI's own argv and stdin, before accessFlags.
func command(row host.CLI, model, effort, instructions, task, outFile string, once bool) ([]string, string) {
	exe := binOf(row)
	maxTurns := []string{}
	if once {
		maxTurns = []string{"--max-turns", "1"}
	}
	appendPrompt := []string{}
	if instructions != "" {
		appendPrompt = []string{"--append-system-prompt", instructions}
	}
	switch row.Name {
	case "codex":
		argv := []string{exe, "exec", "-m", model, "-c", "model_reasoning_effort=" + codexEffort(effort), "--skip-git-repo-check"}
		if instructions != "" {
			argv = append(argv, "-c", "developer_instructions="+dumpLine(instructions))
		}
		if outFile != "" {
			argv = append(argv, "--output-last-message", outFile)
		}
		return append(argv, "-"), task
	case "claude":
		argv := append([]string{exe, "-p", "--model", host.ClaudeModel(model), "--effort", effort}, maxTurns...)
		return append(argv, appendPrompt...), task
	case "glm", "custom": // Claude Code too (the glm wrapper, or a 직접 추가 provider): ai_once answers in one turn
		argv := append([]string{exe, "--bare", "-p", "--model", model}, maxTurns...)
		return append(argv, appendPrompt...), task
	}
	// gemini and grok have no switch that adds to the system prompt: the role goes first, clearly apart from the task.
	prompt := task
	if instructions != "" {
		prompt = "# 역할 지시\n" + instructions + "\n\n# 작업\n" + task
	}
	switch row.Name {
	case "grok":
		e := effort
		if effort == "max" {
			e = "high"
		}
		return []string{exe, "-p", prompt, "-m", model, "--reasoning-effort", e}, ""
	case "agy":
		name := strings.TrimPrefix(model, "antigravity-")
		argv := []string{exe, "-p", prompt, "--model", name}
		// A model named with its effort (gemini-3.8-flash-high) is refused alongside --effort.
		if !effortSuffix.MatchString(name) {
			argv = append(argv, "--effort", effort)
		}
		return argv, ""
	}
	return []string{exe, "-m", model, "-p", prompt}, ""
}
