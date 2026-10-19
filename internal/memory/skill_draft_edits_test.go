package memory

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The provider proposes anchored changes, never a rewritten
// existing skill. Unchanged bytes (including CRLF and Korean) must survive.
func localizedDraftFixture() (SkillDraftInput, SkillDraft, map[string]any) {
	in, answer := draftFixture()
	in.BaselineContent = "# 검토\r\n\r\n## Steps\r\nCheck results.\r\n\r\n## Constraints\r\nNever publish.\r\n\r\n## Recovery\r\nKeep the original.\r\n"
	a := answer.Analysis
	a.SkillID, a.ContentHash = in.SkillID, ContentHash(in.BaselineContent)
	in.BaselineAnalysis = &a
	raw := map[string]any{"analysis": answer.Analysis, "edits": []any{
		map[string]any{"old_text": "Keep the original.", "new_text": "Keep the original and report the failed check.", "lesson_index": 1},
		map[string]any{"old_text": "Check results.", "new_text": "Check results before reporting completion.", "lesson_index": 0},
	}}
	return in, answer, raw
}

func TestChatSkillDraftLocalizedEdits(t *testing.T) {
	in, _, raw := localizedDraftFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Role, Content string }
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Messages) != 2 || req.Messages[0].Role != "system" || !strings.Contains(req.Messages[0].Content, "lesson_index") || !strings.Contains(req.Messages[0].Content, "old_text") {
			t.Error("localized edit contract missing")
		}
		_ = json.NewEncoder(w).Encode(draftResponse(structuredText(raw)))
	}))
	defer server.Close()
	d := &ChatSkillDrafter{Config: LLMConfig{Endpoint: server.URL, APIKey: "test", Model: "draft-model"}}
	out, usage, err := d.Draft(testCtx, in)
	want := strings.Replace(in.BaselineContent, "Check results.", "Check results before reporting completion.", 1)
	want = strings.Replace(want, "Keep the original.", "Keep the original and report the failed check.", 1)
	if err != nil || out.Content != want || out.Analysis.ContentHash != ContentHash(want) || usage.TotalTokens != 27 {
		t.Fatalf("localized changes lost baseline: %+v %+v %v", out, usage, err)
	}
}

func TestSkillDraftEditsUseOriginalLocationsAndSupportDeletion(t *testing.T) {
	in, _ := draftFixture()
	in.BaselineContent = "## Steps\nleft|right\nobsolete\n\n## Constraints\nPreserve this.\n"
	edits := []skillDraftEdit{
		{OldText: "right", NewText: "done", LessonIndex: 0},
		{OldText: "left", NewText: "right", LessonIndex: 1},
		{OldText: "obsolete\n", NewText: "", LessonIndex: 2},
	}
	got, err := applySkillDraftEdits(in, edits)
	if err != nil || got != "## Steps\nright|done\n\n## Constraints\nPreserve this.\n" {
		t.Fatal("edits retargeted generated text or lost the unchanged suffix", got, err)
	}
}

func TestChatSkillDraftRejectsInvalidEditsWithoutLosingUsage(t *testing.T) {
	for _, kind := range []string{"missing_anchor", "ambiguous", "overlapping_occurrences", "overlap", "chained", "blank_anchor", "no_change", "cancelled_changes", "no_edits", "too_many", "missing_lesson", "negative_lesson", "unknown_lesson", "fractional_lesson", "null_lesson", "null_old", "null_new", "unknown_key", "duplicate_key", "rewrite", "both", "new_skill", "capability", "oversized_result"} {
		t.Run(kind, func(t *testing.T) {
			in, answer, raw := localizedDraftFixture()
			edits := raw["edits"].([]any)
			e := edits[0].(map[string]any)
			switch kind {
			case "missing_anchor":
				e["old_text"] = "absent"
			case "ambiguous":
				in.BaselineContent += "Keep the original."
			case "overlapping_occurrences":
				in.BaselineContent += "aaa"
				e["old_text"] = "aa"
			case "overlap":
				edits[1] = map[string]any{"old_text": "the original.", "new_text": "it.", "lesson_index": 0}
			case "chained":
				edits[1] = map[string]any{"old_text": e["new_text"], "new_text": "replacement", "lesson_index": 0}
			case "blank_anchor":
				e["old_text"] = "\r\n"
			case "no_change":
				e["new_text"] = e["old_text"]
			case "cancelled_changes":
				// Adjacent insert/delete operations must not manufacture a candidate
				// whose final bytes are unchanged.
				in.BaselineContent = "prefix ab suffix"
				raw["edits"] = []any{map[string]any{"old_text": "a", "new_text": "ab", "lesson_index": 0}, map[string]any{"old_text": "b", "new_text": "", "lesson_index": 1}}
			case "no_edits":
				raw["edits"] = []any{}
			case "too_many":
				raw["edits"] = make([]any, 9)
			case "missing_lesson":
				delete(e, "lesson_index")
			case "negative_lesson":
				e["lesson_index"] = -1
			case "unknown_lesson":
				e["lesson_index"] = len(in.Lessons)
			case "fractional_lesson":
				e["lesson_index"] = 0.5
			case "null_lesson":
				e["lesson_index"] = nil
			case "null_old":
				e["old_text"] = nil
			case "null_new":
				e["new_text"] = nil
			case "unknown_key":
				e["extra"] = "ignored?"
			case "rewrite":
				raw = map[string]any{"content": answer.Content, "analysis": answer.Analysis}
			case "both":
				raw["content"] = answer.Content
			case "new_skill":
				in.BaselineContent, in.BaselineAnalysis = "", nil
			case "capability":
				a := answer.Analysis
				a.Writes = true
				raw["analysis"] = a
			case "oversized_result":
				// Serialized input and output fit separately, but their combined
				// candidate must still obey the canonical size limit.
				in.BaselineContent += strings.Repeat("z", 33000)
				e["new_text"] = strings.Repeat("q", 33000)
			}
			if in.BaselineAnalysis != nil {
				a := *in.BaselineAnalysis
				a.ContentHash = ContentHash(in.BaselineContent)
				in.BaselineAnalysis = &a
			}
			text := structuredText(raw)
			if kind == "duplicate_key" {
				text = strings.Replace(text, `"lesson_index":1`, `"lesson_index":2,"lesson_index":1`, 1)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(draftResponse(text)) }))
			defer server.Close()
			d := &ChatSkillDrafter{Config: LLMConfig{Endpoint: server.URL, APIKey: "test", Model: "draft-model"}}
			out, usage, err := d.Draft(testCtx, in)
			if err == nil || out.Content != "" || usage.TotalTokens != 27 {
				t.Fatalf("invalid edit accepted or usage lost: %+v %+v %v", out, usage, err)
			}
			if kind == "capability" && !errors.Is(err, ErrDenied) {
				t.Fatal("capability ceiling not enforced", err)
			}
		})
	}
}
