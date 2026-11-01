package crew

import (
	"bufio"
	"encoding/json"
	"strings"

	"github.com/FelixBaek/arkism/internal/crewrun"
)

const toolCallCap = crewrun.ToolCallLimit

func cliEventError(counted bool, calls toolUsage, failed bool, code int) string {
	if !counted {
		return ""
	}
	if calls.Exceeded {
		return "tool_call_limit_exceeded"
	}
	if !calls.Complete {
		return "tool_call_count_unknown"
	}
	if failed && code == 0 {
		return "cli_run_failed"
	}
	return ""
}

// Counts are CLI-reported invocations, not model turns or hidden provider work.
// The owner allows post-run enforcement when a CLI cannot cap individual calls.
// Raw events and tool arguments never become run metadata.
type toolUsage struct {
	Protocol        string `json:"protocol"`
	Limit           int    `json:"limit"`
	ObservedCalls   int    `json:"observed_calls"`
	Complete        bool   `json:"complete"`
	Exceeded        bool   `json:"exceeded"`
	Enforcement     string `json:"enforcement"`
	responseStarted bool
}

func supportsToolEvents(cli string) bool {
	return in([]string{"codex", "claude", "glm", "custom"}, cli)
}

func toolEventCommand(argv []string, cli string) ([]string, bool) {
	args := append([]string(nil), argv...)
	switch cli {
	case "codex":
		// Keep the stdin prompt sentinel last.
		return append(append(args[:len(args)-1:len(args)-1], "--json"), args[len(args)-1]), true
	case "claude", "glm", "custom":
		return append(args, "--output-format", "stream-json", "--verbose"), true
	default:
		return args, false
	}
}

type toolEventReader struct {
	usage    toolUsage
	seen     map[string]bool
	terminal bool
	invalid  bool
	answer   string
	failure  bool
}

func readToolEvents(raw, cli string) (string, toolUsage, bool) {
	r := toolEventReader{usage: toolUsage{Protocol: "crew-tool-calls-v1", Limit: toolCallCap, Enforcement: "post_run"}, seen: map[string]bool{}}
	s := bufio.NewScanner(strings.NewReader(raw))
	s.Buffer(make([]byte, 4096), 8<<20)
	for s.Scan() {
		if strings.TrimSpace(s.Text()) == "" {
			continue
		}
		var event map[string]any
		if json.Unmarshal(s.Bytes(), &event) != nil {
			r.invalid = true
			continue
		}
		if cli == "codex" {
			r.codex(event)
		} else {
			r.claude(event)
		}
	}
	r.usage.Complete = r.terminal && !r.invalid && s.Err() == nil
	r.usage.Exceeded = r.usage.ObservedCalls > r.usage.Limit
	return r.answer, r.usage, r.failure
}

func (r *toolEventReader) call(id string) {
	if id == "" {
		r.invalid = true
		return
	}
	if !r.seen[id] {
		r.seen[id] = true
		r.usage.ObservedCalls++
	}
}

func (r *toolEventReader) claude(event map[string]any) {
	switch event["type"] {
	case "assistant":
		contents, ok := obj(event["message"])["content"].([]any)
		if !ok {
			r.invalid = true
		}
		for _, content := range contents {
			block := obj(content)
			switch block["type"] {
			case "tool_use", "server_tool_use":
				id := sOr(block["id"])
				if id == "" {
					r.invalid = true
					continue
				}
				r.call(sOr(event["parent_tool_use_id"]) + "/" + id)
			case "text", "thinking", "redacted_thinking":
				if block["type"] == "text" && sOr(block["text"]) != "" {
					r.usage.responseStarted = true
				}
			default:
				r.invalid = true
			}
		}
	case "result":
		r.terminal = true
		r.answer = sOr(event["result"])
		r.usage.responseStarted = r.usage.responseStarted || r.answer != ""
		r.failure = event["is_error"] == true || event["subtype"] != "success"
	case "system", "user", "rate_limit_event":
		// Usage/permission diagnostics do not contain assistant invocations.
	default:
		// A new event format needs review before claiming a complete count.
		r.invalid = true
	}
}

type toolChainBudget struct {
	observed int
	complete bool
	seen     bool
}

// A fallback belongs to the same step, so it does not get a fresh tool budget.
// This is post-run accounting; a dispatched CLI may already have crossed it.
func (b *toolChainBudget) consume(res map[string]any) {
	u, ok := res["tool_usage"].(toolUsage)
	if !ok {
		return
	}
	b.observed += u.ObservedCalls
	b.complete = (!b.seen || b.complete) && u.Complete
	b.seen = true
	u.ObservedCalls, u.Complete = b.observed, b.complete
	u.Exceeded = b.observed > u.Limit
	res["tool_usage"] = u
	if u.Exceeded {
		res["ok"], res["error"] = false, "tool_call_limit_exceeded"
	}
}

func (r *toolEventReader) codex(event map[string]any) {
	switch event["type"] {
	case "item.started", "item.updated", "item.completed":
		item := obj(event["item"])
		switch item["type"] {
		case "command_execution", "file_change", "mcp_tool_call", "web_search":
			r.call(sOr(item["id"]))
		case "agent_message":
			r.usage.responseStarted = r.usage.responseStarted || sOr(item["text"]) != ""
			if event["type"] == "item.completed" {
				r.answer = sOr(item["text"])
			}
		case "reasoning", "todo_list", "error":
		default:
			r.invalid = true
		}
	case "turn.completed", "turn.failed":
		r.terminal = true
		r.failure = event["type"] == "turn.failed"
	case "thread.started", "turn.started", "error":
	default:
		r.invalid = true
	}
}

func toolBudgetStop(res map[string]any) string {
	for _, field := range []string{"error", "stopped"} {
		switch res[field] {
		case "tool_call_limit_exceeded", "tool_call_limit_reached", "tool_call_count_unknown":
			return sOr(res[field])
		}
	}
	return ""
}

func (b *toolChainBudget) exhausted(res map[string]any) bool {
	if !b.seen || b.observed < toolCallCap {
		return false
	}
	res["ok"], res["error"], res["stopped"] = false, "tool_call_limit_reached", "tool_call_limit_reached"
	return true
}

func runnerTerminal(spec, res map[string]any, free bool) string {
	if res["ok"] == true {
		return ""
	}
	if stop := toolBudgetStop(res); stop != "" {
		return stop
	}
	if usage, ok := res["tool_usage"].(toolUsage); ok && usage.responseStarted {
		return "response_started"
	}
	if usage, ok := res["tool_usage"].(toolUsage); ok && usage.Enforcement == "post_run" && !usage.Complete && res["error"] != "cli_not_found" {
		return "tool_call_count_unknown"
	}
	if !free && mayHaveChanged(spec, res) {
		return "effect_unknown"
	}
	return ""
}
