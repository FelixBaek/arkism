package subscription

// Host-only API auth for the Chat entrance.

import (
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/FelixBaek/arkism/internal/host"
)

var (
	loadKeys    = host.LoadKeys // test seam
	ipvFutureRe = regexp.MustCompile(`^v[a-fA-F0-9]+\..+$`)
	errURL      = errors.New("invalid_url")
)

// firstKey is the first row for provider whose key is set.
func firstKey(provider string) (map[string]any, error) {
	rows, err := loadKeys()
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row["provider"] == provider && truthy(row["key"]) {
			return row, nil
		}
	}
	return nil, nil
}

// ClaudeHeaders returns fresh provider headers and the secret needed for error redaction.
func ClaudeHeaders(model string) (map[string]string, string, error) {
	// Subscription requests need Claude Code's execution, not replayed OAuth tokens.
	if !host.APIModeOn("claude") {
		return nil, "", errors.New("claude_subscription_requires_cli")
	}
	// Provider helpers may include credential paths or secrets in their errors, so none pass through.
	row, err := firstKey("anthropic")
	secret, ok := Token(row["key"])
	if err != nil || !ok {
		return nil, "", errors.New("login_unavailable")
	}
	return map[string]string{"content-type": "application/json", "anthropic-version": "2023-06-01", "x-api-key": secret}, secret, nil
}

// ChatConnection uses the same explicit API mode and first matching host key as CLI execution.
// An empty base means none: API mode is off for the provider's CLI, or it has no CLI.
func ChatConnection(provider string) (base, key string, err error) {
	cli := map[string]string{"openai": "codex", "xai": "grok"}[provider]
	if cli == "" || !host.APIModeOn(cli) {
		return "", "", nil
	}
	refuse := &host.CrewError{Msg: "host_api_configuration_unavailable"}
	row, err := firstKey(provider)
	if err != nil || row == nil {
		return "", "", refuse
	}
	key, ok := Token(row["key"])
	if !ok {
		return "", "", refuse
	}
	raw, present := row["base_url"]
	if !present || raw == nil || raw == "" {
		raw = host.ModelsURL[provider]
	}
	s, isStr := raw.(string)
	if !isStr || checkURL(s) != nil {
		return "", "", refuse
	}
	return strings.TrimRight(s, "/"), key, nil
}

// checkURL accepts what Python's urlsplit checks did: http(s), a host, no user info,
// query or fragment, and a port in 1..65535 when one is given.
func checkURL(base string) error {
	if utf8.RuneCountInString(base) > 2048 || strings.ContainsFunc(base, func(r rune) bool { return unicode.IsSpace(r) || r < 32 || r == 127 }) ||
		strings.ContainsAny(base, "?#\\@") {
		return errURL
	}
	scheme, rest, _ := strings.Cut(base, ":")
	if s := strings.ToLower(scheme); s != "http" && s != "https" {
		return errURL
	}
	rest, ok := strings.CutPrefix(rest, "//")
	if !ok {
		return errURL
	}
	netloc, _, _ := strings.Cut(rest, "/")
	if strings.HasSuffix(netloc, ":") {
		return errURL
	}
	var hostname, port string
	if strings.ContainsAny(netloc, "[]") {
		inner, ok := strings.CutPrefix(netloc, "[")
		if !ok {
			return errURL
		}
		var after string
		if hostname, after, ok = strings.Cut(inner, "]"); !ok {
			return errURL
		}
		if port, ok = strings.CutPrefix(after, ":"); !ok && after != "" {
			return errURL
		}
		if ip, err := netip.ParseAddr(hostname); (err != nil || !ip.Is6()) && !ipvFutureRe.MatchString(hostname) {
			return errURL
		}
	} else {
		hostname, port, _ = strings.Cut(netloc, ":")
	}
	if hostname == "" {
		return errURL
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || strings.TrimLeft(port, "0123456789") != "" || n < 1 || n > 65535 {
			return errURL
		}
	}
	return nil
}
