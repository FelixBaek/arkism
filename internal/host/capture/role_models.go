package capture

import (
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// fallbackModel maps a model picked in 설정 › 역할별 모델 to the default it replaced; privateModel retries with it.
// modelVia is how each picked model is billed (host.BillingEnv).
// shortcut: keyed by model name, so 기억 추출 and 반성·검토 picking one name under different billings share the last one;
// pass the role through llm if that pairing is ever needed.
var fallbackModel, modelVia = map[string]string{}, map[string]string{}

// useRoleModels swaps in the models picked in 설정 › 역할별 모델 (기억 추출, 반성·검토) for this run.
func useRoleModels() {
	var page map[string]any
	var err error
	read := false
	get := func(path string, timeout time.Duration) (map[string]any, error) { // one read for all roles
		if !read {
			read = true
			page, err = capGet(path, timeout)
		}
		return page, err
	}
	for _, r := range []struct {
		cur  *string
		role string
	}{{&capModel, "extract"}, {&reflectModel, "reflect"}, {&reviewModel, "reflect"}} {
		m, via := host.RoleModel(get, capScope(), r.role, *r.cur)
		if via != "" {
			modelVia[m] = via
		}
		if m != *r.cur {
			fallbackModel[m] = *r.cur
			*r.cur = m
		}
	}
}

// claudeName is model as the generator records it: claude-<alias>, or the full id (or another CLI's model) as is.
func claudeName(model string) string {
	if strings.HasPrefix(model, "claude-") || extractorCLI(model) != "claude" {
		return model
	}
	return "claude-" + model
}

// ReflectModel is the 반성·검토 model this host runs with nothing picked.
func ReflectModel() string { return reflectModel }

// useRoleModelsFor reads the picks for the commands that run a model.
func useRoleModelsFor(cmd string) {
	if cmd == "drain" || cmd == "reflect" || cmd == "review" {
		useRoleModels()
	}
}
