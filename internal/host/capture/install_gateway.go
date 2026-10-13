package capture

// Claude Code's side of the gateway's local token. When ~/.claude/settings.json already points
// ANTHROPIC_BASE_URL at this host's gateway, the installer adds the token to ANTHROPIC_CUSTOM_HEADERS
// (newline-separated "Name: Value" lines; other lines are kept) and turns on Claude Code's gateway
// hint headers (CLAUDE_CODE_GATEWAY_HINT_HEADERS=1: request class, prompt id), which the gateway's
// side-call classifier and turn trace read. A settings file that does not use the gateway is left
// alone, and the token is never made for it.

import (
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/FelixBaek/arkism/internal/host"
)

// instGatewayLocal reports whether base is this host's gateway (loopback, the gateway's port).
func instGatewayLocal(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme != "http" {
		return false
	}
	_, port, err := net.SplitHostPort(host.Env("ARKISM_GATEWAY_LISTEN", "127.0.0.1:8099"))
	if err != nil {
		return false
	}
	h := u.Hostname()
	return (h == "127.0.0.1" || h == "localhost" || h == "::1") && u.Port() == port
}

// instGatewayHeaders replaces our header line in a custom headers value and keeps every other line.
func instGatewayHeaders(current, token string) string {
	lines := []string{host.GatewayTokenHeader + ": " + token}
	for _, line := range strings.Split(strings.ReplaceAll(current, "\r\n", "\n"), "\n") {
		name, _, _ := strings.Cut(line, ":")
		if strings.TrimSpace(line) == "" || strings.EqualFold(strings.TrimSpace(name), host.GatewayTokenHeader) {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// instGatewayToken is where this user's gateway keeps its token.
func instGatewayToken(userRoot string) string {
	if userRoot == host.Home {
		return host.GatewayTokenFile
	}
	return filepath.Join(userRoot, ".config", "arkism", "gateway.token")
}

// instGatewayEnv adds the token header to Claude Code settings that use the local gateway.
func instGatewayEnv(settings host.Obj, userRoot string) (host.Obj, error) {
	envValue, _ := settings.Get("env")
	env, ok := envValue.(host.Obj)
	if !ok {
		return settings, nil
	}
	base, _ := env.Get("ANTHROPIC_BASE_URL")
	if s, _ := base.(string); !instGatewayLocal(s) {
		return settings, nil
	}
	token, err := host.EnsureGatewayToken(instGatewayToken(userRoot))
	if err != nil {
		return nil, err
	}
	current, _ := env.Get("ANTHROPIC_CUSTOM_HEADERS")
	s, _ := current.(string)
	env.Set("ANTHROPIC_CUSTOM_HEADERS", instGatewayHeaders(s, token))
	if _, set := env.Get("CLAUDE_CODE_GATEWAY_HINT_HEADERS"); !set {
		env.Set("CLAUDE_CODE_GATEWAY_HINT_HEADERS", "1")
	}
	settings.Set("env", env)
	return settings, nil
}
