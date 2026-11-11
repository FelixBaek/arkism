package memory

import "testing"

func TestReflectMixedEnvelopeDetectionPreservesPlainAndStrictReplies(t *testing.T) {
	strict := `{"tool_call":{"name":"recall","args":{"query":"bravo"}}}`
	for _, text := range []string{"A plain supported answer.", `The field "tool_call" names a tool.`, `{"answer":"ordinary JSON answer"}`, strict, "```json\n" + strict + "\n```"} {
		_, isTool := parseReflectToolCalls(text)
		if reflectReplyNeedsRepair(text, isTool) {
			t.Fatalf("valid plain or strict reply rejected: %q", text)
		}
	}
	if !reflectReplyNeedsRepair("Checking now. "+strict, false) {
		t.Fatal("mixed envelope not detected")
	}
}
