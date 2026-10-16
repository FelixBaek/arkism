package capture

// The session trace's behavior evidence (ST-623, FR-012): what the agent did in the ledger's window,
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
	detailAskedRE = regexp.MustCompile(`자세히|자세하게|상세히|길게|전부 설명|walk me through|explain`)
)

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
	reply := func(at, text string) {
		if len(turns) == 0 {
			turns = append(turns, behaviorTurn{})
		}
		if text = strings.TrimSpace(text); text != "" {
			turns[len(turns)-1].at, turns[len(turns)-1].reply = at, text
		}
	}
	command := func(cmd string) {
		cmd = head(cmd, behaviorCommandRunes)
		shell := withoutHeredocs(cmd)
		if commitCmdRE.MatchString(shell) {
			commits = append(commits, cmd)
		}
		if prCmdRE.MatchString(shell) {
			prs = append(prs, cmd)
		}
	}
	_, err := eachLine(transcript, 0, func(raw []byte, _ int64) error {
		v, err := loadsBytes(raw)
		d, _ := v.(map[string]any)
		if err != nil || d == nil {
			return nil
		}
		at := str(d["timestamp"])
		if t, err := time.Parse(time.RFC3339Nano, at); err != nil || t.Before(start) || t.After(end) {
			return nil
		}
		user := func(text string) {
			if text = clean(text); text != "" {
				turns = append(turns, behaviorTurn{detail: detailAskedRE.MatchString(text)})
				if correctionRE.MatchString(text) {
					corrections++
				}
			}
		}
		msg, _ := d["message"].(map[string]any)
		payload, _ := d["payload"].(map[string]any)
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
					command(str(in["command"]))
				}
			}
		case d["type"] == "response_item" && payload != nil: // Codex rollout
			switch payload["type"] {
			case "message":
				text := codexText(payload["content"])
				if payload["role"] == "assistant" {
					reply(at, text)
				} else if payload["role"] == "user" && !strings.HasPrefix(strings.TrimSpace(text), "<") && !strings.HasPrefix(strings.TrimSpace(text), "#") {
					user(text) // skip injected environment and AGENTS.md blocks
				}
			case "custom_tool_call":
				command(str(payload["input"]))
			case "function_call":
				command(str(payload["arguments"]))
			}
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
		prose := codeFenceRE.ReplaceAllString(t.reply, "")
		replies = append(replies, map[string]any{"at": t.at, "text": head(strings.TrimSpace(prose), behaviorReplyRunes),
			"lines": len(strings.Split(t.reply, "\n")), "code_blocks": len(codeFenceRE.FindAllString(t.reply, -1)), "detail_requested": t.detail})
	}
	out := map[string]any{"behavior": "agent_session", "replies": replies, "commits": commits, "pull_requests": prs, "user_corrections": corrections}
	// shortcut: oldest replies go first when the event would pass the server limit; one event per trace.
	for raw, _ := json.Marshal(out); len(raw) > behaviorMaxBytes && len(replies) > 0; raw, _ = json.Marshal(out) {
		replies = replies[1:]
		out["replies"] = replies
	}
	if raw, _ := json.Marshal(out); len(raw) > behaviorMaxBytes {
		return nil
	}
	return out
}

func codexText(content any) string {
	var parts []string
	for _, b := range anyList(content) {
		if b, _ := b.(map[string]any); b != nil && (b["type"] == "output_text" || b["type"] == "input_text") {
			parts = append(parts, str(b["text"]))
		}
	}
	return strings.Join(parts, " ")
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
