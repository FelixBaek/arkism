package capture

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// 설정 › 역할별 모델: 기억 추출 and 반성·검토 run the picked models, and the default answers when a pick cannot.
func TestRoleModelsArePickedAndFallBack(t *testing.T) {
	capFake(t)
	c, r, v, fb := capModel, reflectModel, reviewModel, fallbackModel
	t.Cleanup(func() { capModel, reflectModel, reviewModel, fallbackModel = c, r, v, fb })
	capModel, reflectModel, reviewModel, fallbackModel = "haiku", "sonnet", "sonnet", map[string]string{}
	reads := 0
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		reads++
		if !strings.HasPrefix(path, "/v1/providers?") {
			return nil, errors.New("unexpected " + path)
		}
		return map[string]any{"providers": map[string]any{"roles": map[string]any{"extract": map[string]any{"model": "haiku 4.5", "provider": "anthropic"}}}}, nil
	}
	useRoleModels()
	if capModel != "claude-haiku-4-5" || reflectModel != "sonnet" || reads != 1 || claudeName(capModel) != "claude-haiku-4-5" || claudeName("sonnet") != "claude-sonnet" {
		t.Fatal(capModel, reflectModel, reads)
	}
	tried := []string{}
	llm = func(_, _, model string, _ time.Duration, _, _ string) (any, error) {
		tried = append(tried, model)
		if model == "claude-haiku-4-5" {
			return nil, errors.New("no such model here")
		}
		return []any{}, nil
	}
	if _, err := privateModel("system", "user", capModel, time.Second, "{}", "claims"); err != nil || strings.Join(tried, ",") != "claude-haiku-4-5,haiku" {
		t.Fatal(tried, err)
	}
}
