package capture

// The session trace's behavior evidence: what the agent did in the ledger's window,
// so a personal_compliance checker can judge delivered memories against it. Replies keep their text
// without code blocks; commits and pull requests keep the shell command that made them. The window
// ends sessionIdle after the last recall, which a claimed ledger has already passed, so a replay
// reads the same lines and posts the same body.

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	behaviorReplyRunes   = 4000
	behaviorCommandRunes = 2000
	behaviorMaxBytes     = 60 << 10 // the server's per-event payload limit is 64 KiB
)

// hookTranscript is the running hook's transcript_path; the recall ledger keeps it per row.
var hookTranscript string

var (
	codeFenceRE = regexp.MustCompile("(?s)```.*?(```|$)")
	// Command position only: a heredoc or test fixture that merely quotes "git commit" is no commit.
	commitCmdRE   = regexp.MustCompile(`(^|[;&|(]|\n)\s*git\s+(-C\s+\S+\s+)?commit\b`)
	prCmdRE       = regexp.MustCompile(`(^|[;&|(]|\n)\s*gh\s+pr\s+(create|edit)\b`)
	heredocRE     = regexp.MustCompile(`<<-?\s*['"]?(\w+)['"]?`)
	quotedRE      = regexp.MustCompile(`'[^']*'|"(?:[^"\\]|\\.)*"`) // a quoted "; git commit" is an argument, not a command
	detailAskedRE = regexp.MustCompile(`자세히|자세하게|상세히|길게|전부 설명|walk me through|explain`)
	// Work evidence (#356): a test run or a CI check in command position, judged by its exit status.
	testCmdRE = regexp.MustCompile(`(^|[;&|(]|\n)\s*(\w+=\S*\s+)*(go\s+test|cargo\s+test|pytest|python3?\s+(-m\s+(pytest|unittest)|\S*check_local\.py)|(npm|pnpm|yarn)\s+(run\s+)?test|npx\s+vitest|make\s+test)\b`)
	// `gh run watch|view` exits 0 on a failed run unless asked for its status.
	ciCmdRE    = regexp.MustCompile(`(^|[;&|(]|\n)\s*gh\s+(pr\s+checks\b|run\s+(watch|view)\b[^;&|\n]*--exit-status)`)
	exitCodeRE = regexp.MustCompile(`(?:exit_code\\?"?\s*:\s*|Process exited with code |Exit code:? )(\d+)`)
)

// editTools change files; a successful call is a diff the session produced.
var editTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true, "apply_patch": true}

// sessionWork is what the host saw the session's work end with: the last test
// and CI results win, so a red run fixed later in the session counts as passed.
type sessionWork struct {
	pending   map[string]string // tool call id -> "test", "ci" or "edit"
	diff      bool
	tests, ci string
}

func newSessionWork() *sessionWork {
	return &sessionWork{pending: map[string]string{}, tests: "none", ci: "none"}
}

// call notes a tool call whose result will tell a work outcome.
func (w *sessionWork) call(id, name, shell string) {
	switch {
	case id == "":
	case editTools[name]:
		w.pending[id] = "edit"
	case commitCmdRE.MatchString(shell):
		w.pending[id] = "edit" // a commit is a diff once it succeeds
	case ciCmdRE.MatchString(shell):
		w.pending[id] = "ci"
	case testCmdRE.MatchString(shell):
		w.pending[id] = "test"
	}
}

// result settles a pending call from its output. flagged says isError is the
// tool's own failure flag; without it only an exit code in the output decides.
func (w *sessionWork) result(id string, flagged, isError bool, output string) {
	kind, ok := w.pending[id]
	if !ok {
		return
	}
	delete(w.pending, id)
	failed := isError
	if m := exitCodeRE.FindStringSubmatch(output); m != nil {
		failed = m[1] != "0"
		if kind == "ci" && m[1] == "8" {
			return // gh pr checks: still pending, no result
		}
	} else if !flagged {
		return
	}
	verdict := map[bool]string{false: "passed", true: "failed"}[failed]
	switch kind {
	case "edit":
		w.diff = w.diff || !failed
	case "test":
		w.tests = verdict
	case "ci":
		w.ci = verdict
	}
}

// toolResults settles the Claude Code tool_result blocks of one user line.
func (w *sessionWork) toolResults(d, msg map[string]any) {
	if msg == nil || d["type"] != "user" {
		return
	}
	list, _ := msg["content"].([]any)
	for _, b := range list {
		if b, _ := b.(map[string]any); b != nil && b["type"] == "tool_result" {
			w.result(strField(b["tool_use_id"]), true, truthy(b["is_error"]), resultText(b["content"]))
		}
	}
}

func (w *sessionWork) payload() map[string]any {
	return map[string]any{"diff": w.diff, "tests": w.tests, "ci": w.ci}
}

type behaviorTurn struct {
	at, reply string
	detail    bool
}

// sessionBehavior reads the transcript lines stamped within [from, to]. No readable transcript gives
// nil: the trace then carries no behavior and every checker reports unobserved.
func sessionBehavior(transcript, from, to string) map[string]any {
	start, err1 := time.Parse(time.RFC3339Nano, from)
	end, err2 := time.Parse(time.RFC3339Nano, to)
	if transcript == "" || err1 != nil || err2 != nil {
		return nil
	}
	if _, err := os.Stat(transcript); err != nil {
		return nil
	}
	var turns []behaviorTurn
	commits, prs := []any{}, []any{}
	corrections := 0
	incomplete := false // a cut or dropped artifact: the checker must not call the rest a pass
	redact := func(text string) string {
		masked := redactPrivate(text)
		if masked != text {
			incomplete = true
		}
		if privatePayload(masked) {
			// An encoded/literal marker that cannot be safely delimited hides
			// this record rather than reaching behavior storage or a checker.
			incomplete = true
			return privateMark
		}
		return masked
	}
	reply := func(at, text string) {
		text = redact(text)
		if len(turns) == 0 {
			turns = append(turns, behaviorTurn{})
		}
		if text = strings.TrimSpace(text); text != "" {
			turns[len(turns)-1].at, turns[len(turns)-1].reply = at, text
		}
	}
	work := newSessionWork()
	command := func(id, name, cmd string) {
		work.call(id, name, shellText(cmd)) // before redaction: only the verdict is kept
		cmd = redact(cmd)
		shell := shellText(cmd) // judged whole; only the stored copy is cut
		commit, pr := commitCmdRE.MatchString(shell), prCmdRE.MatchString(shell)
		if (commit || pr) && len([]rune(cmd)) > behaviorCommandRunes {
			cmd, incomplete = headTail(cmd, behaviorCommandRunes), true
		}
		if commit {
			commits = append(commits, cmd)
		}
		if pr {
			prs = append(prs, cmd)
		}
	}
	_, err := eachLine(transcript, 0, func(raw []byte, _ int64) error {
		v, err := loadsBytes(raw)
		d, _ := v.(map[string]any)
		if err != nil || d == nil {
			incomplete = true
			return nil
		}
		at := str(d["timestamp"])
		if t, err := time.Parse(time.RFC3339Nano, at); err != nil || t.Before(start) || t.After(end) {
			return nil
		}
		user := func(text string) {
			text = redact(text)
			if text = clean(text); text != "" {
				turns = append(turns, behaviorTurn{detail: detailAskedRE.MatchString(text)})
				if correctionRE.MatchString(text) {
					corrections++
				}
			}
		}
		msg, _ := d["message"].(map[string]any)
		payload, _ := d["payload"].(map[string]any)
		work.toolResults(d, msg)
		switch {
		case msg != nil && humanTyped(d): // Claude Code
			if s, ok := msg["content"].(string); ok {
				user(s)
			} else if list, ok := msg["content"].([]any); ok {
				if s, err := textBlocks(list); err == nil {
					user(s)
				}
			}
		case msg != nil && d["type"] == "assistant":
			list, _ := msg["content"].([]any)
			if s, err := textBlocks(list); err == nil {
				reply(at, s)
			}
			for _, b := range list {
				if b, _ := b.(map[string]any); b != nil && b["type"] == "tool_use" {
					in, _ := b["input"].(map[string]any)
					command(strField(b["id"]), strField(b["name"]), str(in["command"]))
				}
			}
		case d["type"] == "response_item" && payload != nil: // Codex rollout
			behaviorCodex(payload, at, user, reply, command, work)
		}
		return nil
	})
	if err != nil {
		return nil
	}
	replies := []any{}
	for _, t := range turns {
		if t.reply == "" {
			continue
		}
		prose := strings.TrimSpace(codeFenceRE.ReplaceAllString(t.reply, ""))
		if len([]rune(prose)) > behaviorReplyRunes {
			prose, incomplete = headTail(prose, behaviorReplyRunes), true
		}
		replies = append(replies, map[string]any{"at": t.at, "text": prose,
			"lines": len(strings.Split(t.reply, "\n")), "code_blocks": len(codeFenceRE.FindAllString(t.reply, -1)), "detail_requested": t.detail})
	}
	out := map[string]any{"behavior": "agent_session", "replies": replies, "commits": commits, "pull_requests": prs, "user_corrections": corrections, "incomplete": incomplete, "work": work.payload()}
	// shortcut: oldest replies go first when the event would pass the server limit; one event per trace.
	for raw, _ := json.Marshal(out); len(raw) > behaviorMaxBytes && len(replies) > 0; raw, _ = json.Marshal(out) {
		replies = replies[1:]
		out["replies"], out["incomplete"] = replies, true
	}
	if raw, _ := json.Marshal(out); len(raw) > behaviorMaxBytes {
		return nil
	}
	return out
}

// behaviorCodex routes one Codex rollout response item to the user, reply or command recorder.
func behaviorCodex(payload map[string]any, at string, user func(string), reply func(at, text string), command func(id, name, cmd string), work *sessionWork) {
	switch payload["type"] {
	case "message":
		text := codexText(payload["content"])
		if payload["role"] == "assistant" {
			reply(at, text)
		} else if payload["role"] == "user" && !strings.HasPrefix(strings.TrimSpace(text), "<") && !strings.HasPrefix(strings.TrimSpace(text), "#") {
			user(text) // skip injected environment and AGENTS.md blocks
		}
	case "custom_tool_call":
		work.call(strField(payload["call_id"]), strField(payload["name"]), shellText(str(payload["input"])))
		command("", "", str(payload["input"]))
	case "function_call":
		work.call(strField(payload["call_id"]), strField(payload["name"]), shellText(codexCommand(str(payload["arguments"]))))
		command("", "", str(payload["arguments"]))
	case "function_call_output", "custom_tool_call_output":
		work.result(strField(payload["call_id"]), false, false, resultText(payload["output"]))
	}
}

// codexCommand is the shell script of a Codex call's JSON arguments: the last
// element of an argv such as ["bash","-lc","go test ./..."], or its cmd string.
func codexCommand(arguments string) string {
	v, err := loadsMap(arguments)
	if err != nil {
		return arguments
	}
	if argv := anyList(v["command"]); len(argv) > 0 {
		return strField(argv[len(argv)-1])
	}
	return strField(or(v["command"], v["cmd"]))
}

// resultText is a tool result's text: a string, or the text of its content blocks.
func resultText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	var parts []string
	for _, b := range anyList(v) {
		if b, _ := b.(map[string]any); b != nil {
			parts = append(parts, strField(b["text"]))
		}
	}
	return strings.Join(parts, "\n")
}

// headTail keeps both ends of s within n runes: footers and closing summaries are what checkers judge.
func headTail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	h := n / 2
	return string(r[:h]) + "\n…\n" + string(r[len(r)-(n-h-3):])
}

func codexText(content any) string {
	var parts []string
	for _, b := range anyList(content) {
		if b, _ := b.(map[string]any); b != nil && (b["type"] == "output_text" || b["type"] == "input_text") {
			parts = append(parts, strField(b["text"]))
		}
	}
	return strings.Join(parts, " ")
}

// shellText is what the shell runs as commands: heredoc bodies and quoted arguments are data.
func shellText(cmd string) string {
	return quotedRE.ReplaceAllString(withoutHeredocs(cmd), "''")
}

// withoutHeredocs drops here-document bodies: they are data (a file being written), not commands.
// The whole command is still stored, so a commit message given through a heredoc is judged.
func withoutHeredocs(cmd string) string {
	var keep []string
	end := ""
	for _, line := range strings.Split(cmd, "\n") {
		if end != "" {
			if strings.TrimSpace(line) == end {
				end = ""
			}
			continue
		}
		keep = append(keep, line)
		if m := heredocRE.FindStringSubmatch(line); m != nil {
			end = m[1]
		}
	}
	return strings.Join(keep, "\n")
}
