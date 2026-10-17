package capture

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// stableFake serves standing memories (changeable mid-session), an approved skill list and a
// per-prompt search.
func stableFake(t *testing.T) *[]any {
	t.Helper()
	capFake(t)
	hookEnv(t)
	standing := &[]any{map[string]any{"id": "rule-1", "kind": "user", "text": "보고는 짧게"}}
	capGet = func(path string, _ time.Duration) (map[string]any, error) {
		switch {
		case strings.HasPrefix(path, "/v1/memories?"):
			if strings.Contains(path, "kind=user") {
				return map[string]any{"items": *standing}, nil
			}
			return map[string]any{"items": []any{}}, nil
		case strings.HasPrefix(path, "/v1/skills?"):
			return map[string]any{"items": []any{
				map[string]any{"id": "agent/release", "version_id": "skill-" + strings.Repeat("a", 64), "name": "release", "description": "배포 절차"},
				map[string]any{"id": "bad", "name": "no version"},
			}}, nil
		case strings.HasPrefix(path, "/v1/digests/user/context?"):
			return map[string]any{"text": "", "authority": "data_only_untrusted"}, nil
		}
		t.Fatalf("unexpected get %s", path)
		return nil, nil
	}
	search = func(q, _ string, _ int, _ float64) ([]any, bool, error) {
		return []any{
			map[string]any{"id": "rule-1", "kind": "user", "text": "보고는 짧게"}, // already in the stable block
			map[string]any{"id": "hit-" + q[:3], "kind": "decision", "text": "턴별 기억 " + q},
		}, true, nil
	}
	return standing
}

func hookText(t *testing.T, event string) string {
	t.Helper()
	return str(hookContext(t, event)["additionalContext"])
}

// Two consecutive prompts leave the session's stable block byte-identical, the per-turn recall
// stays out of it, and a later SessionStart (compact) replays the same bytes.
func TestStableBlockIdenticalAcrossPrompts(t *testing.T) {
	t.Chdir(t.TempDir()) // the hook reads .arkism/contract.md from the cwd's repo; keep a local one out
	standing := stableFake(t)
	start := hookText(t, `{"hook_event_name": "SessionStart", "session_id": "s1", "source": "startup"}`)
	stable, ok := sessionStore().Stable("s1")
	if !ok || start != stable.Full || !strings.Contains(start, "보고는 짧게") || !strings.Contains(start, "agent/release") ||
		!strings.Contains(start, host.RecallGuide) || strings.Contains(start, "턴별 기억") {
		t.Fatal(ok, start)
	}
	if regexp.MustCompile(`\d{4}-\d{2}-\d{2}|\d{2}:\d{2}`).MatchString(start) {
		t.Fatal("timestamp in the stable block", start)
	}
	// The standing rules change mid-session; the session keeps its block.
	*standing = append(*standing, map[string]any{"id": "rule-2", "kind": "user", "text": "새 규칙"})
	first := hookText(t, `{"hook_event_name": "UserPromptSubmit", "session_id": "s1", "prompt": "첫 번째 질문을 합니다"}`)
	afterFirst, _ := sessionStore().Stable("s1")
	second := hookText(t, `{"hook_event_name": "UserPromptSubmit", "session_id": "s1", "prompt": "두 번째 질문을 합니다"}`)
	afterSecond, _ := sessionStore().Stable("s1")
	if afterFirst.Full != stable.Full || afterSecond.Full != stable.Full || afterFirst.Meta != stable.Meta {
		t.Fatal("stable block changed between prompts")
	}
	for _, turn := range []string{first, second} {
		if strings.Contains(turn, "보고는 짧게") || strings.Contains(turn, "agent/release") || !strings.Contains(turn, "턴별 기억") ||
			!strings.Contains(turn, host.RecallGuide) {
			t.Fatal("per-turn block", turn)
		}
	}
	if first == second {
		t.Fatal("per-turn recall did not change with the prompt")
	}
	again := hookText(t, `{"hook_event_name": "SessionStart", "session_id": "s1", "source": "compact"}`)
	if again != start || strings.Contains(again, "새 규칙") {
		t.Fatal("compact rebuilt the stable block", again)
	}
	// A new session sees the new rule.
	if fresh := hookText(t, `{"hook_event_name": "SessionStart", "session_id": "s2"}`); !strings.Contains(fresh, "새 규칙") {
		t.Fatal(fresh)
	}
}

// The SessionStart skill list pins each listed approved version for the session.
func TestSessionStartPinsListedSkills(t *testing.T) {
	stableFake(t)
	hookText(t, `{"hook_event_name": "SessionStart", "session_id": "s1"}`)
	pin, ok := sessionStore().SkillPin("s1", "agent/release")
	if !ok || pin.VersionID != "skill-"+strings.Repeat("a", 64) {
		t.Fatal(pin, ok)
	}
	if _, ok := sessionStore().SkillPin("s1", "bad"); ok {
		t.Fatal("a skill without a version was pinned")
	}
}

// A failed memory read is injected without being fixed for the session, so the next start retries.
func TestPartialStableBlockIsNotSaved(t *testing.T) {
	stableFake(t)
	skills := capGet
	capGet = func(path string, d time.Duration) (map[string]any, error) {
		if strings.HasPrefix(path, "/v1/memories?") {
			return nil, errTimeout
		}
		return skills(path, d)
	}
	text := hookText(t, `{"hook_event_name": "SessionStart", "session_id": "s1"}`)
	if !strings.Contains(text, "agent/release") {
		t.Fatal(text)
	}
	if _, ok := sessionStore().Stable("s1"); ok {
		t.Fatal("partial block saved")
	}
}

// The skill list keeps whole names and descriptions and degrades line by line to name + id.
func TestSkillSectionNeverCutsADescription(t *testing.T) {
	stableFake(t)
	long := strings.Repeat("설명", 150)
	capGet = func(string, time.Duration) (map[string]any, error) {
		return map[string]any{"items": []any{map[string]any{"id": "agent/long", "version_id": "v", "name": "long", "description": long}}}, nil
	}
	p := skillSection("s1", 800)
	if !strings.Contains(p.Full, "- long (id: agent/long)") || strings.Contains(p.Full, "설명") {
		t.Fatal(p.Full)
	}
}
