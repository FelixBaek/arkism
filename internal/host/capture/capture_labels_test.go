package capture

import (
	"strings"
	"testing"
	"time"
)

// A fake extractor answers a Korean and an English session; the host keeps the well-formed
// labels (person kind, rule, decision status, temporary, entities, relations), drops the
// malformed ones and tells the extractor which language to write in.
func TestCaptureExtractionLabelsKoreanAndEnglish(t *testing.T) {
	capFake(t)
	keep := spaceConfig
	t.Cleanup(func() { spaceConfig = keep })
	spaceConfig = func() map[string]any { return map[string]any{"memory_language": "ko"} }
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{}, true, nil }
	ko := "민수는 결제팀 리드야. 배포는 항상 금요일 전에 끝내. 이번만 로그 레벨을 debug로 둬."
	en := "We decided to drop the legacy importer. Alice reports to Bob."
	var userMsg string
	llm = func(_ string, user string, _ string, _ time.Duration, _ string, _ string) (any, error) {
		userMsg = user
		return []any{
			map[string]any{"kind": "person", "subject": "민수", "text": "민수는 결제팀 리드다.", "quote": "민수는 결제팀 리드야.",
				"entities":  []any{map[string]any{"name": "민수", "type": "person"}, map[string]any{"name": "결제팀", "type": "organization"}, map[string]any{"name": "x", "type": "planet"}},
				"relations": []any{map[string]any{"source": "민수", "relation": "leads", "target": "결제팀"}, map[string]any{"source": "민수", "relation": "Bad Name", "target": "결제팀"}, map[string]any{"source": "민수", "relation": "knows", "target": "철수"}}},
			map[string]any{"kind": "procedural", "subject": "배포", "text": "배포는 항상 금요일 전에 끝낸다.", "quote": "배포는 항상 금요일 전에 끝내.", "rule": "always"},
			map[string]any{"kind": "user", "subject": "로그", "text": "이번만 로그 레벨을 debug로 둔다.", "temporary": true, "rule": "sometimes"},
			map[string]any{"kind": "decision", "subject": "importer", "text": "The legacy importer is dropped.", "quote": "We decided to drop the legacy importer.", "decision_status": "confirmed"},
			map[string]any{"kind": "person", "subject": "team", "text": "Alice reports to Bob.", "decision_status": "confirmed",
				"entities":  []any{map[string]any{"name": "Alice", "type": "person"}, map[string]any{"name": "Bob", "type": "person"}},
				"relations": []any{map[string]any{"source": "Alice", "relation": "reports_to", "target": "Bob"}, map[string]any{"source": "Alice", "relation": "self", "target": "alice"}}},
		}, nil
	}
	cued := []any{map[string]any{"text": ko, "at": "2026-11-06T00:00:00Z"}, map[string]any{"text": en, "at": "2026-11-06T00:01:00Z"}}
	job := map[string]any{"session_id": "labels", "event_id": "labels-event", "type": "turn_completed", "users": cued}
	prepared, err := prepareCapture(job, cued, "labels", "claude_code", func() error { return nil }, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(userMsg, "MEMORY_LANGUAGE: ko\n") {
		t.Fatalf("language line missing: %q", userMsg)
	}
	claims := prepared["claims"].([]any)
	if len(claims) != 5 {
		t.Fatalf("claims = %d", len(claims))
	}
	c := func(i int) map[string]any { return claims[i].(map[string]any) }
	person := c(0)
	if person["kind"] != "semantic" || len(person["entities"].([]any)) != 2 || dumps(person["relations"]) != dumps([]any{map[string]any{"source": "민수", "relation": "leads", "target": "결제팀"}}) {
		t.Fatalf("person claim = %s", dumps(person))
	}
	if c(1)["rule"] != "always" || c(1)["kind"] != "procedural" {
		t.Fatalf("rule claim = %s", dumps(c(1)))
	}
	if _, bad := c(2)["rule"]; bad || c(2)["temporary"] != true {
		t.Fatalf("temporary claim = %s", dumps(c(2)))
	}
	if c(3)["decision_status"] != "confirmed" || c(3)["basis"] != "explicit_user" {
		t.Fatalf("decision claim = %s", dumps(c(3)))
	}
	if _, bad := c(4)["decision_status"]; bad || c(4)["kind"] != "semantic" || dumps(c(4)["relations"]) != dumps([]any{map[string]any{"source": "Alice", "relation": "reports_to", "target": "Bob"}}) {
		t.Fatalf("status kept off a decision or self relation kept: %s", dumps(c(4)))
	}
}
