package gateway

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Request bodies shaped like Claude Code's (content shortened; the structure is what counts).
const (
	ccTools   = `"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object"}},{"name":"Read","description":"read","input_schema":{"type":"object"}}]`
	ccSystem  = `"system":[{"type":"text","text":"You are Claude Code."},{"type":"text","text":"env","cache_control":{"type":"ephemeral"}}]`
	ccPrompt  = `{"role":"user","content":[{"type":"text","text":"<system-reminder>ctx</system-reminder>"},{"type":"text","text":"fix the bug"}]}`
	ccPrompt2 = `{"role":"user","content":[{"type":"text","text":"now add a test"}]}`
	ccToolUse = `{"role":"assistant","content":[{"type":"text","text":"Looking."},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"a.go"}}]}`
	ccAnswer  = `{"role":"assistant","content":[{"type":"text","text":"Fixed."}]}`
)

func ccMarked(text string) string {
	return `{"role":"user","content":[{"type":"text","text":"` + text + `","cache_control":{"type":"ephemeral"}}]}`
}

func ccResult(marked bool) string {
	marker := ""
	if marked {
		marker = `,"cache_control":{"type":"ephemeral"}`
	}
	return `{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"package a"},{"type":"text","text":"<system-reminder>r</system-reminder>"` + marker + `}]}`
}

func ccBody(t *testing.T, fields string, messages ...string) host.Obj {
	t.Helper()
	v, err := Parse(`{"model":"group/test","max_tokens":32000,` + fields + `,"messages":[` + strings.Join(messages, ",") + `]}`)
	if err != nil {
		t.Fatal(err)
	}
	return v.(host.Obj)
}

func TestClassifySeparatesClaudeCodeSideCallsFromTheMainConversation(t *testing.T) {
	main := ccSystem + `,` + ccTools + `,"thinking":{"type":"adaptive"},"stream":true`
	cases := []struct {
		name         string
		body         host.Obj
		header       string
		kind, reason string
	}{
		{"first turn", ccBody(t, main, ccMarked("fix the bug")), "", callMain, ""},
		{"single unmarked prompt with tools", ccBody(t, main, ccPrompt), "", callMain, ""},
		{"tool loop", ccBody(t, main, ccPrompt, ccToolUse, ccResult(true)), "", callMain, ""},
		{"later turn", ccBody(t, main, ccPrompt, ccAnswer, ccMarked("now add a test")), "", callMain, ""},
		// Compaction and summaries fork the conversation: the marker stays on the history's last message.
		{"compaction fork", ccBody(t, main, ccPrompt, ccToolUse, ccResult(true),
			`{"role":"user","content":[{"type":"text","text":"Your task is to create a detailed summary of the conversation so far."}]}`), "", callSide, sideFork},
		// The session title: no marker, no tools, thinking off.
		{"title", ccBody(t, `"system":[{"type":"text","text":"Generate a concise, sentence-case title"}],"tools":[],"thinking":{"type":"disabled"}`,
			`{"role":"user","content":"fix the bug in the gateway"}`), "", callSide, sideQuery},
		{"title without a tools field", ccBody(t, `"system":"title","thinking":{"type":"disabled"}`, ccPrompt), "", callSide, sideQuery},
		// A third-party endpoint with caching off still has tools: main.
		{"uncached main", ccBody(t, ccTools+`,"thinking":{"type":"disabled"}`, ccPrompt, ccToolUse, ccResult(false)), "", callMain, ""},
		{"tools off, thinking on", ccBody(t, `"tools":[],"thinking":{"type":"enabled","budget_tokens":1024}`, ccPrompt), "", callMain, ""},
		{"system-role message ignored", ccBody(t, main, `{"role":"system","content":"x"}`, ccMarked("hi")), "", callMain, ""},
		// Claude Code's own request class wins over the body.
		{"header compaction", ccBody(t, main, ccMarked("fix")), "compaction", callSide, "compaction"},
		{"header auxiliary", ccBody(t, main, ccMarked("fix")), "auxiliary", callSide, "auxiliary"},
		{"header side reply", ccBody(t, main, ccMarked("fix")), "side_reply", callSide, "side_reply"},
		{"header main over a fork-shaped body", ccBody(t, main, ccMarked("a"), ccPrompt2), "main", callMain, ""},
		{"header subagent", ccBody(t, `"tools":[],"thinking":{"type":"disabled"}`, ccPrompt), "subagent", callMain, ""},
		{"unknown header falls back to the body", ccBody(t, `"tools":[],"thinking":{"type":"disabled"}`, ccPrompt), "future", callSide, sideQuery},
		{"malformed messages", host.Obj{{Key: "messages", Value: "x"}}, "", callMain, ""},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.header != "" {
			h.Set("x-claude-code-request-class", c.header)
		}
		if kind, reason := classify(c.body, h); kind != c.kind || reason != c.reason {
			t.Errorf("%s: got %s/%s, want %s/%s", c.name, kind, reason, c.kind, c.reason)
		}
	}
}

func TestPromptsCountsOnlyRealUserPrompts(t *testing.T) {
	body := ccBody(t, ccTools, ccPrompt, ccToolUse, ccResult(false), ccAnswer, ccMarked("next"), `{"role":"user","content":""}`)
	if n := prompts(body); n != 2 {
		t.Fatal(n)
	}
}

func newTurns() *turnTracker {
	return &turnTracker{sessions: map[string]*turnState{}, max: 3, ttl: time.Hour}
}

func TestTurnSequenceIsMonotonicAndGroupsOneTurnsToolLoop(t *testing.T) {
	tr, now := newTurns(), time.Unix(1_800_000_000, 0)
	step := func(prompt string, derived int64, starts bool, want int64) {
		t.Helper()
		now = now.Add(time.Second)
		if got := tr.observe("sess-1", prompt, derived, starts, now); got != want {
			t.Fatalf("prompt %q derived %d starts %v: got %d, want %d", prompt, derived, starts, got, want)
		}
	}
	// Without prompt ids: the history's prompt count decides.
	step("", 1, true, 1)  // turn 1
	step("", 1, true, 1)  // its tool loop
	step("", 1, true, 1)  // a retry of the same request
	step("", 1, false, 1) // a side call (title) joins the turn
	step("", 2, true, 2)  // turn 2
	step("", 2, false, 2) // compaction fork
	step("", 1, true, 3)  // after compaction the history is short again: still forward
	step("", 1, true, 3)
	step("", 2, true, 4)
	// With prompt ids (CLAUDE_CODE_GATEWAY_HINT_HEADERS): a new id is a new turn, the same id is not.
	step("p-a", 2, true, 4) // first id seen: the same history, the same turn
	step("p-b", 3, true, 5)
	step("p-b", 3, true, 5)
	step("", 3, true, 5) // a call without the header keeps to the history count
	step("p-c", 3, true, 6)
	step("p-c", 9, false, 6) // a subagent or side call never moves the turn
	// Other sessions have their own sequence.
	if got := tr.observe("sess-2", "", 4, true, now); got != 4 {
		t.Fatal(got)
	}
	if got := tr.observe("sess-1", "p-c", 3, true, now); got != 6 {
		t.Fatal(got)
	}
}

func TestTurnTrackerIgnoresBadSessionIdsAndStaysBounded(t *testing.T) {
	tr, now := newTurns(), time.Unix(1_800_000_000, 0)
	for _, bad := range []string{"", "a b", "../x", strings.Repeat("a", 129), "x\n"} {
		if got := tr.observe(bad, "", 1, true, now); got != 0 {
			t.Fatalf("%q: %d", bad, got)
		}
	}
	for i, id := range []string{"s1", "s2", "s3", "s4", "s5"} {
		tr.observe(id, "", 1, true, now.Add(time.Duration(i)*time.Second))
	}
	if len(tr.sessions) > tr.max || tr.sessions["s1"] != nil || tr.sessions["s5"] == nil {
		t.Fatal(len(tr.sessions))
	}
	// An expired session starts over from its history.
	tr.observe("s5", "", 3, true, now.Add(10*time.Second))
	if got := tr.observe("s5", "", 1, true, now.Add(3*time.Hour)); got != 1 {
		t.Fatal(got)
	}
}

func TestGroupReportsCarryTheTurnTraceAndSideKind(t *testing.T) {
	f := newFixture(t)
	swap(t, &turns, &turnTracker{sessions: map[string]*turnState{}, max: 16, ttl: time.Hour})
	url := f.gw.URL + "/v1/messages?beta=true"
	main := ccSystem + `,` + ccTools
	post := func(body string, headers ...string) {
		t.Helper()
		reply := send(t, "POST", url, []byte(body), append([]string{"content-type", "application/json", "X-Claude-Code-Session-Id", "0f0e-session"}, headers...)...)
		if reply.status != 200 {
			t.Fatalf("%d %s", reply.status, reply.body)
		}
	}
	wrap := func(fields string, messages ...string) string {
		return `{"model":"group/test","max_tokens":64,` + fields + `,"messages":[` + strings.Join(messages, ",") + `]}`
	}
	post(wrap(main, ccMarked("fix the bug")))
	post(wrap(main, ccPrompt, ccToolUse, ccResult(true)))
	post(wrap(`"tools":[],"thinking":{"type":"disabled"}`, `{"role":"user","content":"fix the bug"}`))
	post(wrap(main, ccPrompt, ccAnswer, ccMarked("now add a test")))
	post(wrap(main, ccPrompt, ccAnswer, ccMarked("x")), "x-claude-code-request-class", "compaction")
	want := []string{
		`{"kind":"gateway","session_id":"0f0e-session","turn_seq":1}`,
		`{"kind":"gateway","session_id":"0f0e-session","turn_seq":1}`,
		`{"kind":"side","side":"query","session_id":"0f0e-session","turn_seq":1}`,
		`{"kind":"gateway","session_id":"0f0e-session","turn_seq":2}`,
		`{"kind":"side","side":"compaction","session_id":"0f0e-session","turn_seq":2}`,
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reports) != len(want) {
		t.Fatal(f.reports)
	}
	for i, r := range f.reports {
		if got := string(mustPy(r["origin"])); got != strings.ReplaceAll(strings.ReplaceAll(want[i], ":", ": "), ",", ", ") {
			t.Errorf("report %d: %s", i, got)
		}
		if r["model"] != "custom-test/first" || r["ok"] != true {
			t.Errorf("report %d routing changed: %v", i, r)
		}
	}
	if f.decisions != len(want) {
		t.Fatal("every call is still routed the same way", f.decisions)
	}
}
