package gateway

// What a Claude Code Messages call is, for the routing report only: routing never reads it.
//
// classify tells the main conversation from Claude Code's side calls (title, summary, compaction,
// suggestions). Claude Code says so itself in x-claude-code-request-class when its gateway hint
// headers are on (CLAUDE_CODE_GATEWAY_HINT_HEADERS=1, set by the installer). Without the header the
// body decides: a fork (summary, compaction, recap) reuses the cache with its cache_control marker
// moved to messages[n-2]; a side query (title) has no marker, no tools and thinking disabled.
// Anything unclear is main, so a wrong guess only ever merges a side call into the main counts.
//
// turns groups one agent turn's calls (its tool loop and side calls) under the session id and a
// sequence that only grows within this process. A turn starts at a new x-claude-code-prompt-id, or
// without that header when the count of real user prompts in the history changes.

import (
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/FelixBaek/arkism/internal/host"
)

// Call kinds a routing report's origin carries.
const (
	callMain = "gateway"
	callSide = "side"
)

// Side reasons: what made a call a side call.
const (
	sideFork  = "fork"  // cache marker at messages[n-2]: summary, compaction, recap
	sideQuery = "query" // no marker, tools=[], thinking disabled: title and the like
)

var requestClassSide = map[string]string{"compaction": "compaction", "auxiliary": "auxiliary", "side_reply": "side_reply"}

var requestClassMain = set("main", "subagent", "workflow")

// classify returns ("", "") for a main call, or ("side", reason).
func classify(body host.Obj, h http.Header) (kind, reason string) {
	switch class := h.Get("x-claude-code-request-class"); {
	case requestClassSide[class] != "":
		return callSide, requestClassSide[class]
	case requestClassMain[class]:
		return callMain, ""
	}
	var messages []any
	if list, ok := get(body, "messages").([]any); ok {
		for _, m := range list {
			if role := get(m, "role"); role == "user" || role == "assistant" {
				messages = append(messages, m)
			}
		}
	}
	n, marker := len(messages), lastCacheMarker(messages)
	if marker >= 0 {
		if n >= 2 && marker == n-2 {
			return callSide, sideFork
		}
		return callMain, ""
	}
	tools, _ := get(body, "tools").([]any)
	if len(tools) == 0 && get(get(body, "thinking"), "type") == "disabled" {
		return callSide, sideQuery
	}
	return callMain, ""
}

// lastCacheMarker is the index of the last message whose content blocks carry cache_control, or -1.
func lastCacheMarker(messages []any) int {
	for i := len(messages) - 1; i >= 0; i-- {
		blocks, _ := get(messages[i], "content").([]any)
		for _, b := range blocks {
			if o, ok := b.(host.Obj); ok && has(o, "cache_control") {
				return i
			}
		}
	}
	return -1
}

// prompts counts real user prompts: user messages with text and no tool_result.
func prompts(body host.Obj) int64 {
	list, _ := get(body, "messages").([]any)
	var n int64
	for _, m := range list {
		if get(m, "role") != "user" {
			continue
		}
		text, result := false, false
		switch content := get(m, "content").(type) {
		case string:
			text = content != ""
		case []any:
			for _, b := range content {
				switch get(b, "type") {
				case "tool_result":
					result = true
				case "text":
					text = true
				}
			}
		}
		if text && !result {
			n++
		}
	}
	return n
}

var traceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type turnState struct {
	seq     int64
	prompt  string // the last x-claude-code-prompt-id, "" when the client sends none
	derived int64  // prompts() at the last turn start
	seen    time.Time
}

type turnTracker struct {
	mu       sync.Mutex
	sessions map[string]*turnState
	max      int
	ttl      time.Duration
}

var turns = &turnTracker{sessions: map[string]*turnState{}, max: 4096, ttl: 24 * time.Hour}

// observe returns the call's turn sequence in session (0 when there is no usable session id). Only a
// main call of the session's own conversation can start a turn; side calls and subagents join the
// current one. A retried request (same prompt id, same history) keeps its sequence.
func (t *turnTracker) observe(session, prompt string, derived int64, starts bool, now time.Time) int64 {
	if !traceID.MatchString(session) {
		return 0
	}
	if !traceID.MatchString(prompt) {
		prompt = ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.sessions[session]
	if s == nil || now.Sub(s.seen) > t.ttl {
		t.evict(now)
		s = &turnState{seq: max(1, derived), prompt: prompt, derived: derived}
		t.sessions[session] = s
	} else if starts {
		changed := derived != s.derived
		if prompt != "" && s.prompt != "" {
			changed = prompt != s.prompt
		}
		if changed {
			s.seq = max(s.seq+1, derived)
			s.derived = derived
		}
		if prompt != "" {
			s.prompt = prompt
		}
	}
	s.seen = now
	return s.seq
}

// evict drops expired sessions and, at the cap, the one seen longest ago. Called with mu held.
func (t *turnTracker) evict(now time.Time) {
	for id, s := range t.sessions {
		if now.Sub(s.seen) > t.ttl {
			delete(t.sessions, id)
		}
	}
	for len(t.sessions) >= t.max {
		oldest, at := "", now
		for id, s := range t.sessions {
			if !s.seen.After(at) {
				oldest, at = id, s.seen
			}
		}
		delete(t.sessions, oldest)
	}
}

// traceOrigin is a Messages call's routing-report origin: its kind and, when Claude Code names its
// session, the session id and turn sequence. Ids only, never content.
func traceOrigin(body host.Obj, h http.Header, now time.Time) host.Obj {
	kind, reason := classify(body, h)
	subagent := h.Get("x-claude-code-agent-id") != "" || h.Get("x-claude-code-request-class") == "subagent" ||
		h.Get("x-claude-code-request-class") == "workflow"
	session := h.Get("X-Claude-Code-Session-Id")
	origin := host.Obj{{Key: "kind", Value: kind}}
	if reason != "" {
		origin = append(origin, host.KV{Key: "side", Value: reason})
	}
	if seq := turns.observe(session, h.Get("x-claude-code-prompt-id"), prompts(body), kind == callMain && !subagent, now); seq > 0 {
		origin = append(origin, host.KV{Key: "session_id", Value: session}, host.KV{Key: "turn_seq", Value: seq})
	}
	return origin
}
