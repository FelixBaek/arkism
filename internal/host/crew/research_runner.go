package crew

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Research is a separate capability from ordinary read access. Filesystem
// read-only flags alone do not constrain MCP writes, hooks or delegated agents.
// This adapter exposes only Claude's built-in read/search tools; other runners
// fail closed until their equivalent restrictions have been verified.
const researchTools = "Read,Glob,Grep,WebSearch,WebFetch"

func researchUnsupported(runner string) error {
	return crewErr("research_runner_unsupported: " + runner + "의 조사 전용 도구 제한은 아직 지원하지 않아요. 읽기·검색 제한을 지원하는 최신 Claude Code와 Claude 모델을 선택해 주세요. 모델은 자동으로 바꾸지 않았어요")
}

func researchCommand(argv []string) []string {
	return append(argv, "--safe-mode", "--restricted", "--tools", researchTools, "--allowedTools", researchTools,
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "",
		"--settings", `{"disableAllHooks":true}`, "--disable-slash-commands", "--no-chrome", "--no-session-persistence")
}

// Copy only selected authentication into a temporary profile. Inherited host
// tokens, plugin/config paths, loader overrides and provider/model overrides
// must not reach the research process. Explicit cwd remains the read boundary;
// otherwise the CLI receives an empty workspace, not the engine's directory.
func researchEnvironment(original map[string]string, cwd string, explicitCWD bool) (map[string]string, string, func(), error) {
	selected := make(map[string]string, len(original))
	for key, value := range original {
		if key != "OPENAI_BASE_URL" { // another CLI's address cannot affect this native Claude run
			selected[key] = value
		}
	}
	env, cleanup, err := isolatedHandoffEnv(selected, "claude")
	if err != nil {
		return nil, "", nil, crewErr("research_isolation_failed: 조사 실행기의 인증·공급자 격리를 확인해 주세요 (" + err.Error() + ")")
	}
	env["CLAUDE_CODE_SAFE_MODE"] = "1"
	if !explicitCWD {
		cwd = filepath.Join(env["HOME"], "research")
		if err = os.Mkdir(cwd, 0700); err != nil {
			cleanup()
			return nil, "", nil, err
		}
	}
	return env, cwd, cleanup, nil
}

// Help is a local capability probe, never a model request. Requiring every
// isolation flag avoids silently degrading on an older CLI. An overridden
// executable remains operator-trusted, like every other local runner binary.
var researchCLIHelp = func(executable, cwd string, env map[string]string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "--safe-mode", "--restricted", "--help")
	cmd.Dir, cmd.Env = cwd, envList(env)
	raw, err := cmd.Output()
	if len(raw) > 1<<20 {
		return "", crewErr("research_cli_help_too_large")
	}
	return string(raw), err
}

func verifyResearchClaude(executable, cwd string, env map[string]string) error {
	help, err := researchCLIHelp(executable, cwd, env)
	if err != nil {
		return crewErr("research_cli_unavailable: Claude Code의 읽기·검색 제한을 확인하지 못했어요. CLI를 설치하거나 업데이트한 뒤 다시 실행해 주세요")
	}
	for _, flag := range []string{"--safe-mode", "--restricted", "--tools", "--allowedTools", "--strict-mcp-config", "--mcp-config", "--setting-sources", "--settings", "--disable-slash-commands", "--no-chrome", "--no-session-persistence", "--permission-mode"} {
		if !strings.Contains(help, flag) {
			return crewErr("research_cli_outdated: 조사 실행에 필요한 " + flag + " 옵션이 없어요. Claude Code를 업데이트해 주세요")
		}
	}
	return nil
}
