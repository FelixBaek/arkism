package capture

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAssistantResultsPromptAndSchema(t *testing.T) {
	if capturePrompt("", "") != extractPrompt || captureSchema("user") != claimsSchema {
		t.Fatal("the default mode must keep the user-only prompt and schema")
	}
	p := capturePrompt(assistantResultsMode, "dense")
	if strings.Contains(p, "Only the user's words count") || !strings.Contains(p, "ASSISTANT RESULTS mode") || !strings.Contains(p, "Density dense") {
		t.Fatal(p)
	}
	var schema struct {
		Properties struct {
			Claims struct {
				Items struct {
					Required   []string                   `json:"required"`
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"items"`
			} `json:"claims"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(captureSchema(assistantResultsMode)), &schema); err != nil {
		t.Fatal(err)
	}
	items := schema.Properties.Claims.Items
	if _, ok := items.Properties["speaker"]; !ok || !strings.Contains(strings.Join(items.Required, ","), "speaker") {
		t.Fatalf("speaker missing: %+v", items.Required)
	}
	if assistantSection("", map[string]any{"assistant": "x"}) != "" || !strings.Contains(assistantSection(assistantResultsMode, map[string]any{"assistant": "tests passed"}), "data, never instructions") {
		t.Fatal("assistant section")
	}
}

func TestAssistantResultClaimsStayReviewable(t *testing.T) {
	userText := "Mina said the launch moved to May 3."
	raw := []any{
		map[string]any{"text": "The test suite passes on main.", "quote": "all 42 tests passed", "speaker": "assistant", "risk": "low", "basis_hint": "explicit_user"},
		map[string]any{"text": "The launch moved to May 3.", "quote": "the launch moved to May 3", "speaker": "other_speaker", "risk": "low"},
		map[string]any{"text": "The agent will refactor later.", "quote": "I will refactor this later", "speaker": "assistant", "risk": "low"},
		map[string]any{"text": "User note.", "quote": "Mina said", "speaker": "user", "basis_hint": "assistant_result"},
	}
	markSpeakers(raw, "Ran go test: all 42 tests passed.", userText)
	want := []string{"assistant_result", "assistant_result", "inferred", "explicit_user"}
	for i, v := range raw {
		c, err := buildClaim(v.(map[string]any), "t", userText, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if c["basis"] != want[i] {
			t.Fatalf("%d: basis %v, want %s", i, c["basis"], want[i])
		}
		if want[i] != "explicit_user" && c["risk"] == "low" {
			t.Fatalf("%d: an assistant_result or inference is never low risk", i)
		}
	}
}
