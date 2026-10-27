package host

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRolePickReadsTheScopeAndFallsBack(t *testing.T) {
	asked := ""
	get := func(path string, _ time.Duration) (map[string]any, error) {
		asked = path
		return map[string]any{"providers": map[string]any{"roles": map[string]any{
			"reflect":    map[string]any{"model": "sonnet 5.5", "provider": "anthropic"},
			"skill_scan": map[string]any{"off": []any{"cisco"}},
			"extract":    map[string]any{"model": "--danger"},
		}}}, nil
	}
	scope := map[string]any{"workspace_id": "w", "project_id": "p", "agent_id": ""}
	if m := RoleModel(get, scope, "reflect", "sonnet"); m != "claude-sonnet-5-5" {
		t.Fatal("picked", m)
	}
	if !strings.HasPrefix(asked, "/v1/providers?") || !strings.Contains(asked, "workspace_id=w") || strings.Contains(asked, "agent_id") {
		t.Fatal("query", asked)
	}
	if m := RoleModel(get, scope, "extract", "haiku"); m != "haiku" {
		t.Fatal("a flag-like name became a model", m)
	}
	if _, off := RolePick(get, scope, "skill_scan"); len(off) != 1 || off[0] != "cisco" {
		t.Fatal("off", off)
	}
	down := func(string, time.Duration) (map[string]any, error) { return nil, errors.New("down") }
	if m := RoleModel(down, scope, "reflect", "sonnet"); m != "sonnet" {
		t.Fatal("unreachable", m)
	}
}
