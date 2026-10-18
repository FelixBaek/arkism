package capture

import (
	"strings"
	"testing"
)

// A short instruction must not hide the existence of its applicability metadata.
func TestIndexPointsToCompleteMemoryWhenContextIsPresent(t *testing.T) {
	for name, value := range map[string]any{
		"domains": []any{"work"}, "applicability": "업무 발표 때만",
		"uncertainty": "현재 적용 가능한지 다시 확인", "valid_from": "다음 발표부터",
		"observed_at": "2026-10-18T01:00:00Z", "evidence_kind": "inference", "semantic_kind": "preference",
	} {
		t.Run(name, func(t *testing.T) {
			line := indexLine(map[string]any{
				"id": "scoped-rule", "kind": "procedural", "text": "짧게 작성해요",
				"memory_context": map[string]any{name: value},
			}, t.TempDir())
			if !strings.Contains(line, "맥락") || !strings.Contains(line, "… (id: scoped-rule)") {
				t.Fatal("short context-dependent instruction lost its full-memory pointer", line)
			}
		})
	}
}
