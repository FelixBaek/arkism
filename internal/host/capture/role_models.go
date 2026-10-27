package capture

import (
	"strings"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// fallbackModel maps a model picked in 설정 › 역할별 모델 to the default it replaced; privateModel retries with it.
var fallbackModel = map[string]string{}

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
		if m := host.RoleModel(get, capScope(), r.role, *r.cur); m != *r.cur {
			fallbackModel[m] = *r.cur
			*r.cur = m
		}
	}
}

// claudeName is model as the generator records it: claude-<alias> or the full id as is.
func claudeName(model string) string {
	if strings.HasPrefix(model, "claude-") {
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
