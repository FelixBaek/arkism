package host

import (
	"net/url"
	"regexp"
	"time"
)

// cliModelRe is a model name safe to hand `claude -p --model` (never a flag).
var cliModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]{0,79}$`)

// RolePick is the owner's pick for role in 설정 › 역할별 모델 (GET /v1/providers of scope): its model and the
// engines switched off (skill_scan). Empty when nothing is picked or Arkism cannot be read; the caller keeps its default.
func RolePick(get func(path string, timeout time.Duration) (map[string]any, error), scope map[string]any, role string) (string, []string) {
	q := url.Values{}
	for k, v := range scope {
		if s, _ := v.(string); s != "" {
			q.Set(k, s)
		}
	}
	page, err := get("/v1/providers?"+q.Encode(), 15*time.Second)
	if err != nil {
		return "", nil
	}
	providers, _ := page["providers"].(map[string]any)
	roles, _ := providers["roles"].(map[string]any)
	pick, _ := roles[role].(map[string]any)
	model, _ := pick["model"].(string)
	var off []string
	if list, ok := pick["off"].([]any); ok {
		for _, x := range list {
			if s, ok := x.(string); ok {
				off = append(off, s)
			}
		}
	}
	return model, off
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

// RoleModel is the Claude CLI model picked for role, or fallback.
func RoleModel(get func(path string, timeout time.Duration) (map[string]any, error), scope map[string]any, role, fallback string) string {
	model, _ := RolePick(get, scope, role)
	return CLIRoleModel(model, fallback)
}
