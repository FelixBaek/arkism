package capture

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func conversationSpace(t *testing.T) {
	t.Helper()
	keep := spaceConfig
	t.Cleanup(func() { spaceConfig = keep })
	spaceConfig = func() map[string]any { return map[string]any{"capture_density": "conversation"} }
}

func TestExtractionWindowsOverlapOnlyForConversation(t *testing.T) {
	turns := make([]string, 22)
	for i := range turns {
		turns[i] = fmt.Sprint(i)
	}
	if w := extractionWindows("balanced", turns); len(w) != 1 || len(w[0]) != 22 {
		t.Fatal("default profile split the batch", len(w))
	}
	w := extractionWindows(densityConversation, turns)
	if len(w) != 3 || w[0][0] != "0" || w[0][9] != "9" || w[1][0] != "8" || w[2][0] != "16" || w[2][len(w[2])-1] != "21" {
		t.Fatal("windows lost a turn or the overlap", w)
	}
	if w := extractionWindows(densityConversation, turns[:10]); len(w) != 1 {
		t.Fatal("a short batch was split", len(w))
	}
}

func TestExtractWindowsCapsEachCallAndDropsOverlapRepeats(t *testing.T) {
	turns := make([]string, 12) // two windows
	for i := range turns {
		turns[i] = fmt.Sprintf("turn %d", i)
	}
	calls := 0
	raw, err := extractWindows(densityConversation, turns, func(text string) (any, error) {
		calls++
		if calls == 2 && !strings.HasPrefix(text, "turn 8\n") {
			t.Fatal("second window did not start at the overlap", text)
		}
		out := []any{map[string]any{"text": "Shared fact."}, map[string]any{"text": "Shared fact."}}
		for i := 0; i < 20; i++ {
			out = append(out, map[string]any{"text": fmt.Sprintf("Fact %d of call %d.", i, calls)})
		}
		return out, nil
	})
	// call 1 keeps 16 (both copies of the shared fact: one call's duplicates are the server's to merge);
	// call 2 drops its two copies the first window already returned and keeps 14.
	if err != nil || calls != 2 || len(raw) != 30 {
		t.Fatal(err, calls, len(raw))
	}
}

func TestConversationPromptReplacesCodingRules(t *testing.T) {
	p := capturePrompt("", densityConversation)
	for _, gone := range []string{"Keep ONLY", "AI coding agent", "Only the user's words count"} {
		if strings.Contains(p, gone) {
			t.Fatalf("conversation prompt kept %q", gone)
		}
	}
	for _, kept := range []string{"Every speaker's words count", "Keep EVERY concrete fact", "Skip a claim only when an EXISTING MEMORY already states the same fact"} {
		if !strings.Contains(p, kept) {
			t.Fatalf("conversation prompt is missing %q", kept)
		}
	}
	if !strings.Contains(p, "reason: one short phrase in MEMORY_LANGUAGE") || !strings.Contains(capturePrompt("", ""), "reason: one short phrase in MEMORY_LANGUAGE") {
		t.Fatal("reason language rule missing")
	}
	if strings.Contains(capturePrompt("", ""), "Skip anything") || !strings.Contains(capturePrompt("", "balanced"), "Keep ONLY") {
		t.Fatal("default profile changed beyond the skip rule")
	}
}

func TestConversationChunksKeepDayAndSize(t *testing.T) {
	long := strings.Repeat("가", 700)
	chunks := ConversationChunks("2023-05-08", []string{"Mina: I adopted a cat named Luna.", "", "Joon: Nice!", "Mina: " + long})
	if len(chunks) != 2 || chunks[0] != "[2023-05-08] Mina: I adopted a cat named Luna.\n[2023-05-08] Joon: Nice!" {
		t.Fatal(chunks)
	}
	if utf8.RuneCountInString(chunks[1]) > turnChunkRunes || !strings.HasPrefix(chunks[1], "[2023-05-08] Mina: 가") {
		t.Fatal("long turn not cut to its own chunk", utf8.RuneCountInString(chunks[1]))
	}
}

func TestProcessConversationWindowsSplitsCaptureAndStoresTurns(t *testing.T) {
	capFake(t)
	conversationSpace(t)
	search = func(string, string, int, float64) ([]any, bool, error) { return []any{}, true, nil }
	users := make([]any, 22) // three windows
	for i := range users {
		users[i] = map[string]any{"text": fmt.Sprintf("Mina: fact number %d is true.", i), "at": "2023-05-08T10:00:00Z"}
	}
	models := 0
	llm = func(_, user, _ string, _ time.Duration, _, _ string) (any, error) {
		models++
		var out []any
		for _, line := range strings.Split(user, "\n") {
			if strings.HasPrefix(line, "Mina: ") {
				quote := strings.TrimPrefix(line, "Mina: ")
				out = append(out, map[string]any{"text": "Mina says " + quote, "quote": quote, "risk": "low", "kind": "person"},
					map[string]any{"text": "Also " + quote, "quote": quote, "risk": "low", "kind": "person"})
			}
		}
		return out, nil
	}
	var captures []int
	var sources []map[string]any
	capPost = func(path string, payload any, _ time.Duration) (map[string]any, error) {
		switch path {
		case "/v1/capture":
			n := len(field(payload, "claims").([]any))
			captures = append(captures, n)
			items := make([]any, n)
			for i := range items {
				items[i] = map[string]any{"status": "candidate"}
			}
			return map[string]any{"items": items}, nil
		case "/v1/sources":
			sources = append(sources, mustLoad(t, dumps(payload)))
			return map[string]any{"id": "src"}, nil
		}
		t.Fatal("unexpected post", path)
		return nil, nil
	}
	job := map[string]any{"session_id": "conv", "event_id": "conv-event", "type": "turn_completed", "users": users}
	checkpoints := 0
	if err := process(job, func() error { checkpoints++; return nil }); err != nil {
		t.Fatal(err)
	}
	if models != 3 || len(captures) != 2 || captures[0] != captureClaimsMax || captures[0]+captures[1] != 16+16+12 /* two capped windows and a 6-turn tail */ {
		t.Fatal("windows or capture split wrong", models, captures)
	}
	if len(sources) == 0 || job["turns_stored"] != true {
		t.Fatal("turns were not stored", len(sources))
	}
	checkConversationSources(t, sources)
	stored := len(sources)
	if err := storeTurns(job, users, "claude-code", func() error { return nil }); err != nil || len(sources) != stored {
		t.Fatal("turns stored twice")
	}
}

// checkConversationSources checks each stored turns source and its explicit capture order.
func checkConversationSources(t *testing.T, sources []map[string]any) {
	t.Helper()
	for i, s := range sources {
		order, ok := s["conversation_order"].(map[string]any)
		index, indexOK := num(order["index"])
		total, totalOK := num(order["total"])
		if !ok || !indexOK || !totalOK || order["epoch"] != sha256Hex([]byte("conv-event")) || index != float64(i+1) || total != float64(len(sources)) {
			t.Fatal("missing explicit capture order", order)
		}
		if s["kind"] != "conversation" || s["on_secret"] != "redact" || !strings.HasPrefix(s["text"].(string), "[2023-05-08] user: Mina: fact") ||
			!strings.HasPrefix(s["idempotency_key"].(string), "turns-") || len(s["idempotency_key"].(string)) > 128 {
			t.Fatal("bad source", s)
		}
	}
}

func TestStoreTurnsSkipsOtherProfilesAndPrivateText(t *testing.T) {
	capFake(t)
	posts := 0
	capPost = func(string, any, time.Duration) (map[string]any, error) { posts++; return map[string]any{}, nil }
	users := []any{map[string]any{"text": "Mina: hello", "at": "2023-05-08T10:00:00Z"}}
	if err := storeTurns(map[string]any{}, users, "claude-code", func() error { return nil }); err != nil || posts != 0 {
		t.Fatal("default profile stored turns", err, posts)
	}
	conversationSpace(t)
	private := []any{map[string]any{"text": "<private>secret plan</private>", "at": "2023-05-08T10:00:00Z"}}
	if err := storeTurns(map[string]any{}, private, "claude-code", func() error { return nil }); errorCode(err) != "capture_private_content" || posts != 0 {
		t.Fatal("private text was stored", err, posts)
	}
}

func TestBenchExtractClaimsReadsConversationInWindows(t *testing.T) {
	capFake(t)
	calls := 0
	llm = func(string, string, string, time.Duration, string, string) (any, error) {
		calls++
		return []any{map[string]any{"text": fmt.Sprintf("Fact %d.", calls), "quote": "Mina", "risk": "low"}}, nil
	}
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = "Mina: line " + fmt.Sprint(i)
	}
	if _, err := ExtractClaims(strings.Join(lines, "\n"), "2023-05-08", nil, "", "conversation", "bench"); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	calls = 0
	if _, err := ExtractClaims(strings.Join(lines, "\n"), "2023-05-08", nil, "", "", "bench"); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
}
