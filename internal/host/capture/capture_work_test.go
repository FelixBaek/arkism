package capture

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// hookContext runs the hook on event and returns hookSpecificOutput.
func hookContext(t *testing.T, event string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	if code := hookMain(strings.NewReader(event), &out); code != 0 {
		t.Fatal(code)
	}
	got, err := loadObj(out.Bytes())
	if err != nil {
		t.Fatalf("%q", out.String())
	}
	return got["hookSpecificOutput"].(map[string]any)
}

func hookEnv(t *testing.T) {
	capAgentOff = func(string) bool { return false }
	capOnPrompt = false
	t.Setenv("ARKISM_CAPTURE_CHILD", "")
	t.Setenv("ARKISM_CAPTURE_ACK", "")
	t.Setenv("ARKISM_CLIENT", "")
}

func TestWorkHookWorkAndMemoryFailIndependently(t *testing.T) {
	for _, memoryFailed := range []bool{true, false} {
		capFake(t)
		hookEnv(t)
		t.Setenv("ARKISM_WORK_ID", "task")
		capGet = func(string, time.Duration) (map[string]any, error) {
			if memoryFailed {
				return nil, errors.New("offline")
			}
			return mustLoad(t, `{"items": [{"id": "m", "kind": "user", "text": "regular memory"}]}`), nil
		}
		capPost = func(string, any, time.Duration) (map[string]any, error) {
			if !memoryFailed {
				return nil, errors.New("offline")
			}
			return mustLoad(t, `{"id": "task", "revision": 1, "package": {"goal": "selected goal"}}`), nil
		}
		text := str(hookContext(t, `{"hook_event_name": "SessionStart", "session_id": "s"}`)["additionalContext"])
		if memoryFailed != strings.Contains(text, "selected goal") || memoryFailed == strings.Contains(text, "regular memory") {
			t.Fatal(memoryFailed, text)
		}
	}
}

func TestWorkHookNoSelectionDoesNotGuessOrQuery(t *testing.T) {
	capFake(t) // any post fails the test
	t.Setenv("ARKISM_WORK_ID", "")
	t.Setenv("ARKISM_CLIENT", "")
	if got := workContext(map[string]any{"cwd": "/some/repository"}); got != "" {
		t.Fatal(got)
	}
}

func relatedSaved(t *testing.T) string {
	return `{"id": "task", "revision": 1, "authority": "data_only_untrusted",
		"package": {"goal": "계속할 작업", "source_refs": [{"collection": "sources", "id": "source", "revision": 1}]}}`
}

const relatedItem = `{"id": "memory", "revision": 1, "collection": "memories", "scope": {"workspace_id": "w", "project_id": "p"},
	"authority": "data_only_untrusted", "status": "active", "text": "확인된 근거", "conflict_pending": true,
	"supersedes": [{"collection": "memories", "id": "old", "revision": 2}]}`

func TestWorkRelatedSharedDeadlineDecreasesAndStopsRecheck(t *testing.T) {
	for _, c := range []struct {
		times    []float64
		expected []float64
		state    string
	}{{[]float64{10, 10.2, 11, 12}, []float64{2.8, 2, 1}, "ready"}, {[]float64{10, 10.2, 11, 13.1}, []float64{2.8, 2}, "work_unverified"}} {
		capFake(t)
		keepSel, keepDel, keepBind := selectedWorkID, delStatus, hoBind
		t.Cleanup(func() { selectedWorkID, delStatus, hoBind = keepSel, keepDel, keepBind })
		selectedWorkID = func(map[string]any) (string, error) { return "task", nil }
		delStatus = func(string) (any, error) { return map[string]any{"state": "no_pending_observed"}, nil }
		capScope = func() map[string]any { return map[string]any{"workspace_id": "w", "project_id": "p"} }
		t.Cleanup(func() { capScope = host.Scope })
		times := append([]float64{}, c.times...)
		monotonic = func() float64 { v := times[0]; times = times[1:]; return v }
		answers := []string{relatedSaved(t), `{"authority": "data_only_untrusted", "items": [` + relatedItem + `]}`, relatedSaved(t)}
		var timeouts []float64
		capPost = func(_ string, _ any, timeout time.Duration) (map[string]any, error) {
			timeouts = append(timeouts, timeout.Seconds())
			a := answers[0]
			answers = answers[1:]
			return mustLoad(t, a), nil
		}
		rendered := workContext(map[string]any{})
		value := mustLoad(t, strings.TrimPrefix(rendered, host.HookHeader))
		related := value["related_memories"].(map[string]any)
		if related["state"] != c.state || len(timeouts) != len(c.expected) {
			t.Fatal(rendered, timeouts)
		}
		for i, want := range c.expected {
			if math.Abs(timeouts[i]-want) > 1e-6 {
				t.Fatal(timeouts)
			}
		}
		if host.MustDumps(value["package"], host.DumpOpts{}) != host.MustDumps(mustLoad(t, relatedSaved(t))["package"], host.DumpOpts{}) {
			t.Fatal(value["package"])
		}
		if c.state != "ready" && len(related["items"].([]any)) != 0 {
			t.Fatal(related)
		}
	}
}

func TestWorkRelatedBudgetOmitsWholeSupplementAndPreservesOmission(t *testing.T) {
	saved := mustLoad(t, relatedSaved(t))
	item := mustLoad(t, relatedItem)
	item["text"] = strings.Repeat("근거", 5000)
	saved["related_memories"] = map[string]any{"items": []any{item}}
	rendered, err := host.RenderHookContext(saved, 8000)
	if err != nil || host.Len(rendered) > 8000 {
		t.Fatal(err, host.Len(rendered))
	}
	for range 2 {
		value := mustLoad(t, strings.TrimPrefix(rendered, host.HookHeader))
		omitted, _ := value["omitted_fields"].([]any)
		if _, has := value["related_memories"]; has || !containsAny(omitted, "related_memories") {
			t.Fatal(value)
		}
		if rendered, err = host.RenderHookContext(value, 1000); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkRelatedCombinedBudgetDropsSupplementWhole(t *testing.T) {
	saved := mustLoad(t, relatedSaved(t))
	item := mustLoad(t, relatedItem)
	item["text"] = strings.Repeat("근거", 2200)
	saved["related_memories"] = map[string]any{"items": []any{item}}
	selected, err := host.RenderHookContext(saved, 8000)
	if err != nil || !strings.Contains(selected, `"related_memories"`) {
		t.Fatal(err, selected)
	}
	combined, err := host.Assemble(strings.Repeat("기억", 1950), selected, strings.Repeat("정의", 900), host.ContextMaxChars)
	if err != nil || host.Len(combined) > 9500 {
		t.Fatal(err, host.Len(combined))
	}
	_, after, _ := strings.Cut(combined, host.HookHeader)
	body := after[:strings.LastIndex(after, "\n\n")]
	value := mustLoad(t, body)
	omitted, _ := value["omitted_fields"].([]any)
	if _, has := value["related_memories"]; has || !containsAny(omitted, "related_memories") {
		t.Fatal(value)
	}
}

// From test_arkism_context: a long prompt-search memory keeps its retrieval id in the index.
func TestContextRequestMemoryIndexKeepsLongTextRetrievalID(t *testing.T) {
	capFake(t)
	search = func(string, string, int, float64) ([]any, bool, error) {
		return []any{map[string]any{"kind": "user", "id": "memory-id", "text": strings.Repeat("긴 기억 ", 4000)}}, true, nil
	}
	result, err := promptContext("현재 작업 관련 내용을 찾아줘", "s", "")
	if err != nil || !strings.Contains(result, "memory-id") || !strings.Contains(result, "recall") || host.Len(result) >= 1000 {
		t.Fatal(err, result)
	}
}

// From test_arkism_context: the real hook keeps memory, work and crew within the total budget.
func TestContextRealHookAppliesTotalBudgetAndKeepsFailuresIndependent(t *testing.T) {
	state := `{"id": "selected-work", "revision": 7, "package": {"title": "진행 작업", "goal": "프로그램 전환 후 이어가기",
		"constraints": ["현재 요청 확인"], "decisions": ["` + strings.Repeat("결정", 1500) + `"],
		"next_steps": ["` + strings.Repeat("다음 작업", 500) + `"], "checks": [{"command": "never execute", "outcome": "passed"}]}}`
	for _, event := range []string{"SessionStart", "UserPromptSubmit"} {
		capFake(t)
		hookEnv(t)
		t.Setenv("ARKISM_WORK_ID", "selected-work")
		long := `{"items": [{"id": "m", "kind": "user", "text": "` + strings.Repeat("기억", 1950) + `"}]}`
		capGet = func(string, time.Duration) (map[string]any, error) { return mustLoad(t, long), nil }
		capPost = func(path string, _ any, _ time.Duration) (map[string]any, error) {
			if path == "/v1/search" {
				return mustLoad(t, long), nil
			}
			return mustLoad(t, state), nil
		}
		keep := host.CrewHookContext
		t.Cleanup(func() { host.CrewHookContext = keep })
		host.CrewHookContext = func(string, string, time.Duration) (string, error) { return strings.Repeat("정의", 900), nil }
		data := hookContext(t, `{"hook_event_name": "`+event+`", "session_id": "s", "prompt": "$팀 현재 작업 관련 내용을 찾아줘"}`)
		ctx := str(data["additionalContext"])
		if data["hookEventName"] != event || host.Len(ctx) > 9500 || !strings.Contains(ctx, "selected-work") {
			t.Fatal(event, host.Len(ctx))
		}
	}
}
