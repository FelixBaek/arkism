package crew

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// A handoff receives only explicitly selected provider authentication. Global
// settings, hooks, MCP servers and Arkism owner/runner credentials stay behind.
func isolatedHandoffEnv(original map[string]string, cli string) (map[string]string, func(), error) {
	if cli != "claude" && cli != "codex" {
		return nil, nil, crewErr("인계는 내장 Claude/Codex CLI만 지원해요")
	}
	for _, key := range []string{"OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		if original[key] != "" {
			return nil, nil, crewErr("인계 공급자 주소와 인증 경로가 재정의되어 있어요")
		}
	}
	root, err := os.MkdirTemp("", "arkism-handoff-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	env := map[string]string{"HOME": root, "ARKISM_CAPTURE_CHILD": "1", "TERM": "dumb", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_AUTOUPDATER": "1"}
	for _, key := range []string{"PATH", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value := original[key]; value != "" {
			env[key] = value
		}
	}
	var source, destination string
	if cli == "codex" {
		from := original["CODEX_HOME"]
		if from == "" {
			from = filepath.Join(host.Home, ".codex")
		}
		source = filepath.Join(from, "auth.json")
		env["CODEX_HOME"] = filepath.Join(root, ".codex")
		destination = filepath.Join(env["CODEX_HOME"], "auth.json")
		if value := original["OPENAI_API_KEY"]; value != "" {
			env["OPENAI_API_KEY"] = value
		}
	} else {
		from := original["CLAUDE_CONFIG_DIR"]
		if from == "" {
			from = filepath.Join(host.Home, ".claude")
		}
		source = filepath.Join(from, ".credentials.json")
		env["CLAUDE_CONFIG_DIR"] = filepath.Join(root, ".claude")
		destination = filepath.Join(env["CLAUDE_CONFIG_DIR"], ".credentials.json")
		for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"} {
			if value := original[key]; value != "" {
				env[key] = value
			}
		}
	}
	if err = os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		cleanup()
		return nil, nil, err
	}
	raw, err := os.ReadFile(source)
	if err == nil {
		if len(raw) > 1<<20 {
			cleanup()
			return nil, nil, crewErr("인증 파일 크기를 확인해 주세요")
		}
		err = os.WriteFile(destination, raw, 0600)
	}
	if err != nil && !os.IsNotExist(err) {
		cleanup()
		return nil, nil, err
	}
	return env, cleanup, nil
}

func isolatedHandoffCommand(argv []string, cli string) []string {
	if cli == "claude" {
		return append(argv, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--setting-sources", "")
	}
	// Disable cloud applications, hooks, plugins and nested agents. MCP
	// isolation is separately verified in the empty untrusted profile.
	flags := isolatedCodexFlags()
	return append(append(append([]string{}, argv[:len(argv)-1]...), flags...), argv[len(argv)-1])
}

func isolatedCodexFlags() []string {
	flags := []string{}
	for _, feature := range []string{"apps", "plugins", "hooks", "multi_agent", "remote_plugin", "skill_mcp_dependency_install"} {
		flags = append(flags, "-c", "features."+feature+"=false")
	}
	return flags
}

// An empty untrusted profile suppresses project configuration. Verify the
// effective MCP table using the same CLI, environment and checkout before run;
// table overrides alone do not remove servers merged from project config.
func verifyHandoffCodex(executable, cwd string, env map[string]string) error {
	config := "[projects." + strconv.Quote(cwd) + "]\ntrust_level = \"untrusted\"\n"
	if err := os.WriteFile(filepath.Join(env["CODEX_HOME"], "config.toml"), []byte(config), 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := append([]string{"mcp", "list", "--json"}, isolatedCodexFlags()...)
	command := exec.CommandContext(ctx, executable, args...)
	command.Dir = cwd
	command.Env = envList(env)
	raw, err := command.Output()
	if err != nil || len(raw) > 1<<20 {
		return crewErr("Codex 인계의 MCP 격리를 확인하지 못했어요")
	}
	var servers []struct {
		Enabled bool `json:"enabled"`
	}
	if json.Unmarshal(raw, &servers) != nil {
		return crewErr("Codex MCP 설정 응답을 확인하지 못했어요")
	}
	// Refuse even disabled entries: no host/project MCP configuration should be
	// present in this fresh profile, and a malformed response cannot imply denial.
	if len(servers) != 0 {
		return crewErr("Codex 인계에 외부 MCP 설정이 남아 있어 실행하지 않았어요")
	}
	return nil
}
