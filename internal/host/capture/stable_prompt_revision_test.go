package capture

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestPromptDeliversRevisedStandingMemory(t *testing.T) {
	t.Chdir(t.TempDir())
	standing := stableFake(t)
	hookText(t, `{"hook_event_name":"SessionStart","session_id":"revised-standing"}`)
	var revision int64 = 1
	search = func(_, _ string, _ int, _ float64) ([]any, bool, error) {
		return []any{map[string]any{
			"id": "rule-1", "revision": json.Number(strconv.FormatInt(revision, 10)),
			"kind": "user", "text": "synthetic revised standing instruction",
		}}, true, nil
	}
	part, _, err := promptParts("show the current standing instruction", "revised-standing", "")
	if err != nil || part.Full != "" {
		t.Fatal("same revision should remain deduplicated", err)
	}
	revision = 2
	(*standing)[0].(map[string]any)["revision"] = json.Number("2")
	part, _, err = promptParts("show the current standing instruction", "revised-standing", "")
	if err != nil || !strings.Contains(part.Full, "synthetic revised standing instruction") {
		t.Fatal("freshly authorized revised memory was suppressed by the old stable ID", err)
	}
}
