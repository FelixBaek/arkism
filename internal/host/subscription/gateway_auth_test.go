package subscription

import (
	"errors"
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/internal/host"
)

const hostKey = "synthetic-host-secret"

var chatProviders = []struct{ provider, cli, base string }{
	{"openai", "codex", "https://api.openai.com/v1"},
	{"xai", "grok", "https://api.x.ai/v1"},
}

// keysFixture sets API mode and serves rows (or err) to the key loader, counting reads.
func keysFixture(t *testing.T, mode ...string) (set func([]map[string]any, error), reads *int) {
	oldMode, oldLoad := host.APIMode, loadKeys
	t.Cleanup(func() { host.APIMode, loadKeys = oldMode, oldLoad })
	host.APIMode = map[string]bool{}
	for _, m := range mode {
		host.APIMode[m] = true
	}
	var rows []map[string]any
	var fail error
	reads = new(int)
	loadKeys = func() ([]map[string]any, error) { *reads++; return rows, fail }
	return func(r []map[string]any, err error) { rows, fail = r, err }, reads
}

func refused(t *testing.T, provider string) {
	t.Helper()
	base, key, err := ChatConnection(provider)
	var crew *host.CrewError
	if !errors.As(err, &crew) || crew.Msg != "host_api_configuration_unavailable" || base != "" || key != "" {
		t.Errorf("%s: want refusal, got %q %q %v", provider, base, key, err)
	}
}

func connects(t *testing.T, provider, wantBase, wantKey string) {
	t.Helper()
	base, key, err := ChatConnection(provider)
	if err != nil || base != wantBase || key != wantKey {
		t.Errorf("%s: got %q %q %v, want %q %q", provider, base, key, err, wantBase, wantKey)
	}
}

func TestClaudeHeadersSelectsAPIKeyWithFreshHeaders(t *testing.T) {
	set, _ := keysFixture(t, "all")
	set([]map[string]any{{"provider": "custom", "key": "wrong"}, {"provider": "anthropic", "key": "host-key"}}, nil)
	headers, secret, err := ClaudeHeaders("claude-test")
	if err != nil || secret != "host-key" {
		t.Fatal(secret, err)
	}
	eq(t, headers, map[string]any{"content-type": "application/json", "anthropic-version": "2023-06-01", "x-api-key": "host-key"})
	headers["authorization"] = "caller-value"
	if again, _, _ := ClaudeHeaders("claude-test"); again["authorization"] != "" {
		t.Error("headers shared between calls")
	}
}

func TestClaudeHeadersInvalidOrMissingKeys(t *testing.T) {
	set, _ := keysFixture(t, "claude")
	for _, key := range []any{nil, "", "bad\r\nHeader: secret", "한글", 123, strings.Repeat("x", 65537)} {
		set([]map[string]any{{"provider": "anthropic", "key": key}}, nil)
		if _, _, err := ClaudeHeaders("claude-test"); err == nil || err.Error() != "login_unavailable" {
			t.Errorf("%T: %v", key, err)
		}
	}
	set(nil, errors.New("/private/auth secret"))
	if _, _, err := ClaudeHeaders("claude-test"); err == nil || err.Error() != "login_unavailable" {
		t.Error(err)
	}
}

func TestClaudeSubscriptionRequiresCLIAndReadsNoKeys(t *testing.T) {
	_, reads := keysFixture(t, "codex")
	if _, _, err := ClaudeHeaders("claude-test"); err == nil || err.Error() != "claude_subscription_requires_cli" {
		t.Error(err)
	}
	eq(t, *reads, 0)
}

func TestChatConnectionAPIModesSelectFirstNonEmptyKey(t *testing.T) {
	for _, mode := range [][]string{{"codex"}, {"grok"}, {"codex", "grok"}, {"all"}} {
		for _, p := range chatProviders {
			set, reads := keysFixture(t, mode...)
			set([]map[string]any{
				{"provider": "anthropic", "key": "synthetic-other-secret"},
				{"provider": p.provider, "key": ""},
				{"provider": p.provider, "key": hostKey},
				{"provider": p.provider, "key": "synthetic-later-secret", "base_url": "https://later.invalid/v1"},
			}, nil)
			if host.APIModeOn(p.cli) {
				connects(t, p.provider, p.base, hostKey)
			} else {
				connects(t, p.provider, "", "")
				eq(t, *reads, 0)
			}
		}
	}
}

func TestChatConnectionStoredOrDefaultBaseURL(t *testing.T) {
	set, _ := keysFixture(t, "all")
	for _, base := range []string{"https://host.invalid:443/v1///", "http://127.0.0.1:8099/v1/", "http://[::1]:8099/v1/",
		"https://host.invalid:1/v1", "https://host.invalid:65535/v1", "HTTPS://Host.invalid/v1", "https://[fe80::1%25eth0]/v1"} {
		for _, p := range chatProviders {
			set([]map[string]any{{"provider": p.provider, "key": hostKey, "base_url": base}}, nil)
			connects(t, p.provider, strings.TrimRight(base, "/"), hostKey)
		}
	}
	for _, p := range chatProviders {
		for _, extra := range []map[string]any{{}, {"base_url": nil}, {"base_url": ""}} {
			row := map[string]any{"provider": p.provider, "key": hostKey}
			for k, v := range extra {
				row[k] = v
			}
			set([]map[string]any{row}, nil)
			connects(t, p.provider, p.base, hostKey)
		}
	}
}

func TestChatConnectionModeOffOrOtherProviderReadsNoKeys(t *testing.T) {
	for _, c := range []struct {
		mode     []string
		provider string
	}{{nil, "openai"}, {nil, "xai"}, {[]string{"grok"}, "openai"}, {[]string{"codex"}, "xai"}, {[]string{"claude"}, "openai"},
		{[]string{"all"}, "anthropic"}, {[]string{"all"}, "custom"}, {[]string{"all"}, "unknown"}} {
		_, reads := keysFixture(t, c.mode...)
		connects(t, c.provider, "", "")
		eq(t, *reads, 0)
	}
}

func TestChatConnectionRefusals(t *testing.T) {
	set, _ := keysFixture(t, "all")
	for _, p := range chatProviders {
		other := "openai"
		if p.provider == "openai" {
			other = "xai"
		}
		// Missing keys, other providers' keys, and load failures.
		for _, rows := range [][]map[string]any{nil, {nil}, {{"provider": "custom", "key": hostKey}}, {{"provider": other, "key": hostKey}},
			{{"provider": p.provider, "key": ""}}, {{"provider": p.provider, "key": nil}}} {
			set(rows, nil)
			refused(t, p.provider)
		}
		set(nil, host.Crewf("/synthetic/private/provider-keys.json "+hostKey))
		refused(t, p.provider)
		// An invalid first token never falls through to the next key.
		for _, token := range []any{"bad token", "bad\tkey", "bad\r\nHeader: injected", "bad\x00key", "bad\x1fkey", "bad\x7fkey", "한글",
			123, true, []any{"key"}, map[string]any{"key": "value"}, strings.Repeat("x", 65537)} {
			set([]map[string]any{{"provider": p.provider, "key": token}, {"provider": p.provider, "key": hostKey}}, nil)
			refused(t, p.provider)
		}
		for _, token := range []string{"k", strings.Repeat("k", 65536)} {
			set([]map[string]any{{"provider": p.provider, "key": token}}, nil)
			connects(t, p.provider, p.base, token)
		}
		// An invalid first URL never falls through either.
		for _, base := range []any{"file:///synthetic/private/provider-keys.json", "ftp://host.invalid/v1", "/v1", "https:///v1",
			"https://host.invalid:0/v1", "https://host.invalid:-1/v1", "https://host.invalid:65536/v1",
			"https://host.invalid:port/v1", "https://host.invalid:/v1", "https://[::1/v1", "https://[::1]x/v1", "https://[1.2.3.4]/v1",
			"https://host.invalid]/v1", "https://user@host.invalid/v1", "https://user:password@host.invalid/v1", "https://@host.invalid/v1",
			"https://host.invalid/v1?key=" + hostKey, "https://host.invalid/v1?", "https://host.invalid/v1#" + hostKey, "https://host.invalid/v1#",
			"https://host.invalid/v 1", "https://host.invalid/\t/v1", "https://host.invalid/\r\n/v1", "https://host.invalid/\x00/v1",
			"https://host.invalid/\x1f/v1", "https://host.invalid/\x7f/v1", "https://host.invalid/ /v1", "https://host.invalid\\other/v1",
			"https://host.invalid:+1/v1", "https:host.invalid/v1", "https://host.invalid/" + strings.Repeat("x", 2048),
			123, true, false, 0, []any{}, map[string]any{}, []any{"https://host.invalid/v1"}} {
			set([]map[string]any{{"provider": p.provider, "key": hostKey, "base_url": base}, {"provider": p.provider, "key": "synthetic-later-secret"}}, nil)
			refused(t, p.provider)
		}
	}
	set([]map[string]any{{"provider": "openai", "key": hostKey, "base_url": "https://[::1]:00080/v1"}}, nil)
	connects(t, "openai", "https://[::1]:00080/v1", hostKey)
}
