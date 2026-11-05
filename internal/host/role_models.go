package host

import (
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// cliModelRe is a model name safe to hand `claude -p --model` (never a flag).
var cliModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,79}$`)

// RolePick is the owner's pick for role in 설정 › 역할별 모델 (GET /v1/providers of scope): its model, the
// engines switched off (skill_scan) and how it is billed (BillingSubscription, BillingAPI, or "" for a pick saved
// before the choice existed). Empty when nothing is picked or Arkism cannot be read; the caller keeps its default.
func RolePick(get func(path string, timeout time.Duration) (map[string]any, error), scope map[string]any, role string) (string, []string, string) {
	q := url.Values{}
	for k, v := range scope {
		if s, _ := v.(string); s != "" {
			q.Set(k, s)
		}
	}
	page, err := get("/v1/providers?"+q.Encode(), 15*time.Second)
	if err != nil {
		return "", nil, ""
	}
	providers, _ := page["providers"].(map[string]any)
	roles, _ := providers["roles"].(map[string]any)
	pick, _ := roles[role].(map[string]any)
	model, _ := pick["model"].(string)
	via, _ := pick["via"].(string)
	var off []string
	if list, ok := pick["off"].([]any); ok {
		for _, x := range list {
			if s, ok := x.(string); ok {
				off = append(off, s)
			}
		}
	}
	return model, off, via
}

// CLIRoleModel is a picked model as `claude -p --model` takes it, or fallback when model is empty or not a model name.
// A Codex or Gemini model (기억 추출 can run on those CLIs) stays as named.
func CLIRoleModel(model, fallback string) string {
	if model == "" || model == "basic" || !cliModelRe.MatchString(model) {
		return fallback
	}
	if cli := ModelCLI(model); cli == "codex" || cli == "gemini" {
		return model
	}
	return ClaudeModel(model)
}

// RoleModel is the Claude CLI model picked for role and its billing, or fallback (billing "": the CLI as set up).
func RoleModel(get func(path string, timeout time.Duration) (map[string]any, error), scope map[string]any, role, fallback string) (string, string) {
	model, _, via := RolePick(get, scope, role)
	return CLIRoleModel(model, fallback), via
}

// How a picked model is billed: the CLI's signed-in subscription, or the provider's stored API key.
const (
	BillingSubscription = "subscription"
	BillingAPI          = "api"
)

// BillingEnv sets env (a child CLI's environment) up for model's billing: BillingAPI puts the provider's stored key in
// (an error when none is stored: never a quiet switch to the subscription), BillingSubscription takes every key out so
// the CLI's own login answers, "" leaves env as it is. The key travels only in env, never in arguments or logs.
func BillingEnv(env map[string]string, model, via string) error {
	if via == "" {
		return nil
	}
	row, _ := CLIByName(ModelCLI(model))
	if row.KeyEnv == "" {
		return Crewf("%s는 구독·API 키를 고를 수 없는 모델이에요", model)
	}
	for _, k := range billingEnvs(row) {
		delete(env, k)
	}
	if via != BillingAPI {
		return nil
	}
	keys, err := LoadKeys()
	if err != nil {
		return err
	}
	if !ApplyStoredKey(env, row, keys) {
		return Crewf("%s API 키가 없어요. 설정 › 에이전트에서 키를 넣거나 구독으로 골라 주세요", row.Provider)
	}
	delete(env, "CLAUDE_CODE_OAUTH_TOKEN") // the key, not a login token, answers
	return nil
}

// billingEnvs are the variables that point a CLI at a key instead of its login.
func billingEnvs(row CLI) []string {
	out := []string{row.KeyEnv, row.BaseEnv}
	switch row.Name {
	case "claude":
		out = append(out, "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY_FILE_DESCRIPTOR")
	case "codex":
		out = append(out, "CODEX_API_KEY")
	}
	return out
}

// RoleEnviron is a role's child CLI environment as exec.Cmd.Env takes it: this process's, never re-triggering the
// capture hook, billed as via (BillingEnv).
func RoleEnviron(model, via string) ([]string, error) {
	env := EnvMap()
	env["ARKISM_CAPTURE_CHILD"] = "1"
	if err := BillingEnv(env, model, via); err != nil {
		return nil, err
	}
	return EnvList(env), nil
}

// EnvMap is this process's environment as a map.
func EnvMap() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

// EnvList is env as exec.Cmd.Env takes it.
func EnvList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// ApplyStoredKey puts the row's provider's first stored key, and its address, into a CLI's environment; false when none.
func ApplyStoredKey(env map[string]string, row CLI, keys []map[string]any) bool {
	if row.Provider == "zai" && !strings.HasPrefix(env[row.BaseEnv], "https://") { // nor an inherited http one
		delete(env, row.BaseEnv)
	}
	for _, k := range keys {
		key, _ := k["key"].(string)
		if k["provider"] != row.Provider || key == "" {
			continue
		}
		env[row.KeyEnv] = key
		if row.Name == "codex" {
			env["CODEX_API_KEY"] = key
		}
		if b, _ := k["base_url"].(string); b != "" && (row.Provider != "zai" || strings.HasPrefix(b, "https://")) { // a wrapper gets no pre-0.1.4 http Z.ai address (#170)
			env[row.BaseEnv] = b
		}
		return true
	}
	return false
}
