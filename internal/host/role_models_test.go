package host

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRolePickReadsTheScopeAndFallsBack(t *testing.T) {
	asked := ""
	get := func(path string, _ time.Duration) (map[string]any, error) {
		asked = path
		return map[string]any{"providers": map[string]any{"roles": map[string]any{
			"reflect":    map[string]any{"model": "sonnet 5.5", "provider": "anthropic", "via": "api"},
			"skill_scan": map[string]any{"off": []any{"cisco"}},
			"extract":    map[string]any{"model": "--danger"},
		}}}, nil
	}
	scope := map[string]any{"workspace_id": "w", "project_id": "p", "agent_id": ""}
	if m, via := RoleModel(get, scope, "reflect", "sonnet"); m != "claude-sonnet-5-5" || via != BillingAPI {
		t.Fatal("picked", m, via)
	}
	if !strings.HasPrefix(asked, "/v1/providers?") || !strings.Contains(asked, "workspace_id=w") || strings.Contains(asked, "agent_id") {
		t.Fatal("query", asked)
	}
	if m, _ := RoleModel(get, scope, "extract", "haiku"); m != "haiku" {
		t.Fatal("a flag-like name became a model", m)
	}
	if _, off, _ := RolePick(get, scope, "skill_scan"); len(off) != 1 || off[0] != "cisco" {
		t.Fatal("off", off)
	}
	down := func(string, time.Duration) (map[string]any, error) { return nil, errors.New("down") }
	if m, via := RoleModel(down, scope, "reflect", "sonnet"); m != "sonnet" || via != "" {
		t.Fatal("unreachable", m)
	}
}

func TestBillingEnvPicksKeyOrLogin(t *testing.T) {
	dir := t.TempDir()
	old := KeysFile
	t.Cleanup(func() { KeysFile = old })
	KeysFile = filepath.Join(dir, "keys.json")
	inherited := func() map[string]string {
		return map[string]string{"ANTHROPIC_API_KEY": "sk-env-inherited", "ANTHROPIC_AUTH_TOKEN": "tok", "CLAUDE_CODE_OAUTH_TOKEN": "oauth", "PATH": "/bin"}
	}
	env := inherited()
	if err := BillingEnv(env, "sonnet", ""); err != nil || env["ANTHROPIC_API_KEY"] != "sk-env-inherited" {
		t.Fatal("an old pick keeps the environment", env, err)
	}
	env = inherited()
	if err := BillingEnv(env, "sonnet", BillingSubscription); err != nil || env["ANTHROPIC_API_KEY"] != "" || env["ANTHROPIC_AUTH_TOKEN"] != "" || env["CLAUDE_CODE_OAUTH_TOKEN"] != "oauth" || env["PATH"] != "/bin" {
		t.Fatal("subscription must drop every key", env, err)
	}
	env = inherited()
	err := BillingEnv(env, "sonnet", BillingAPI)
	if err == nil || strings.Contains(err.Error(), "sk-") {
		t.Fatal("no stored key must fail, never fall back", err)
	}
	if err := os.WriteFile(KeysFile, []byte(`{"keys":[{"id":"k1","provider":"openai","key":"sk-openai-1234"},{"id":"k2","provider":"anthropic","key":"sk-ant-stored-5678"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env = inherited()
	if err := BillingEnv(env, "sonnet", BillingAPI); err != nil || env["ANTHROPIC_API_KEY"] != "sk-ant-stored-5678" || env["ANTHROPIC_AUTH_TOKEN"] != "" || env["CLAUDE_CODE_OAUTH_TOKEN"] != "" {
		t.Fatal("api must use the stored key only", env, err)
	}
	env = map[string]string{}
	if err := BillingEnv(env, "gpt-5.5", BillingAPI); err != nil || env["OPENAI_API_KEY"] != "sk-openai-1234" || env["CODEX_API_KEY"] != "sk-openai-1234" {
		t.Fatal("codex key", env, err)
	}
	list, err := RoleEnviron("sonnet", BillingAPI)
	if err != nil || !slices.Contains(list, "ARKISM_CAPTURE_CHILD=1") || !slices.Contains(list, "ANTHROPIC_API_KEY=sk-ant-stored-5678") {
		t.Fatal("environ", err)
	}
}
