package host

// Harness role router (ST-620, FR-012): config/harness-models.json names the model, output limit,
// effort and prompt components of each memory-harness role. ARKISM_HARNESS_MODELS points at an
// operator copy; otherwise the copy built into the binary is used.

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/FelixBaek/arkism/config"
)

// HarnessRole is one role's runtime plan. Model is the API model id.
type HarnessRole struct {
	Model     string
	MaxTokens int
	Effort    string
	Prompt    string // the role's prompt components joined in order
}

var harnessPlan = sync.OnceValue(func() map[string]HarnessRole {
	raw := config.HarnessModels
	if path := os.Getenv("ARKISM_HARNESS_MODELS"); path != "" {
		// An unusable operator copy falls back to the built-in plan, but never silently.
		if b, err := os.ReadFile(path); err != nil {
			fmt.Fprintln(os.Stderr, "harness_models_unreadable: using built-in plan")
		} else if !json.Valid(b) {
			fmt.Fprintln(os.Stderr, "harness_models_invalid: using built-in plan")
		} else {
			raw = b
		}
	}
	return parseHarnessPlan(raw)
})

func parseHarnessPlan(raw []byte) map[string]HarnessRole {
	var doc struct {
		Prompts map[string]string `json:"prompts"`
		Roles   map[string]struct {
			API struct {
				Model     string `json:"model"`
				MaxTokens int    `json:"max_tokens"`
				Output    struct {
					Effort string `json:"effort"`
				} `json:"output_config"`
			} `json:"api_parameters"`
			Components []string `json:"prompt_components"`
		} `json:"roles"`
	}
	out := map[string]HarnessRole{}
	if json.Unmarshal(raw, &doc) != nil {
		return out
	}
	for name, r := range doc.Roles {
		if r.API.Model == "" {
			continue
		}
		parts := []string{}
		for _, c := range r.Components {
			if p := doc.Prompts[c]; p != "" {
				parts = append(parts, p)
			}
		}
		out[name] = HarnessRole{Model: r.API.Model, MaxTokens: r.API.MaxTokens, Effort: r.API.Output.Effort, Prompt: strings.Join(parts, "\n\n")}
	}
	return out
}

// HarnessRoleFor is the configured plan for role.
func HarnessRoleFor(role string) (HarnessRole, bool) {
	r, ok := harnessPlan()[role]
	return r, ok
}

var claudeFamily = regexp.MustCompile(`^claude-(haiku|sonnet|opus)-\d`)

// CLIModelAlias turns an API model id into the `claude -p --model` alias
// (claude-haiku-5-5 → haiku); anything else passes through unchanged.
// shortcut: the alias follows the CLI's newest model of that family; pin full ids if versions must match.
func CLIModelAlias(model string) string {
	if m := claudeFamily.FindStringSubmatch(model); m != nil {
		return m[1]
	}
	return model
}

// HarnessCLIModel is the CLI alias configured for role, or fallback when the role is not configured.
func HarnessCLIModel(role, fallback string) string {
	if r, ok := HarnessRoleFor(role); ok {
		return CLIModelAlias(r.Model)
	}
	return fallback
}
