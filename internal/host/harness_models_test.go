package host

import (
	"strings"
	"testing"

	"github.com/FelixBaek/arkism/config"
)

func TestHarnessPlanRolesAndAliases(t *testing.T) {
	plan := parseHarnessPlan(config.HarnessModels)
	want := map[string]string{"session_processor": "haiku", "consolidator": "sonnet", "evaluator": "sonnet",
		"skill_drafter": "sonnet", "conflict_analyst": "opus", "retrieval_planner": "haiku"}
	for role, alias := range want {
		r, ok := plan[role]
		if !ok || CLIModelAlias(r.Model) != alias || r.MaxTokens <= 0 || r.Effort == "" || !strings.Contains(r.Prompt, "\n\n") {
			t.Fatal(role, r, ok)
		}
	}
	if CLIModelAlias("gpt-6") != "gpt-6" || HarnessCLIModel("no_such_role", "sonnet") != "sonnet" {
		t.Fatal("fallback")
	}
	if len(parseHarnessPlan([]byte("not json"))) != 0 {
		t.Fatal("bad config must configure nothing")
	}
}
